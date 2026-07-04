package github

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/google/go-github/v66/github"

	"github.com/EmadMokhtar/wazir/internal/board"
	"github.com/EmadMokhtar/wazir/internal/store"
)

// botMarker stamps bot-authored content so ParseEvent can flag self-events.
const botMarker = "<!-- wazir -->"

// ErrNotProvisioned means the configured board has no cached identity yet.
var ErrNotProvisioned = errors.New("board/github: board not provisioned")

// ErrColumnsOccupied means prune was asked to delete Status columns that still
// hold cards. Move the cards or re-run with Force.
var ErrColumnsOccupied = errors.New("board/github: refusing to delete Status columns that still hold cards")

// boardReloadable is the hot-reloadable subset of the board config.
type boardReloadable struct {
	repos         []string
	botLogin      string
	webhookSecret string
}

// GitHubBoard implements board.Board against GitHub Projects v2.
type GitHubBoard struct {
	api   projectsAPI
	rest  *github.Client
	store store.Store

	owner         string
	ownerType     string
	projectNumber int
	boardName     string
	projectNodeID string
	reworkCommand string                          // phase-2 PR-comment trigger token (e.g. "@wazir fix"); static config, not hot-reloaded
	reloadable    atomic.Pointer[boardReloadable] // repos/bot_login/webhook_secret — hot-reloadable

	cached *store.BoardRecord // lazily loaded board identity
}

// snap returns the current reloadable settings, never nil.
func (b *GitHubBoard) snap() *boardReloadable {
	if r := b.reloadable.Load(); r != nil {
		return r
	}
	return &boardReloadable{}
}

// Reload swaps the hot-reloadable subset (allow-list, bot login, webhook secret).
func (b *GitHubBoard) Reload(repos []string, botLogin, webhookSecret string) {
	b.reloadable.Store(&boardReloadable{repos: slices.Clone(repos), botLogin: botLogin, webhookSecret: webhookSecret})
}

// EnsureProvisioned creates (if spec.Create) and/or reconciles the board's
// Status columns additively, then caches the resulting ids.
func (b *GitHubBoard) EnsureProvisioned(ctx context.Context, spec board.BoardSpec) error {
	info, found, err := b.api.GetProject(ctx, b.ownerType, b.owner, b.projectNumber)
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}
	if !found {
		if !spec.Create {
			return fmt.Errorf("%w: project %d not found for %s", ErrNotProvisioned, b.projectNumber, b.owner)
		}
		ownerID, err := b.api.OwnerID(ctx, b.ownerType, b.owner)
		if err != nil {
			return fmt.Errorf("owner id: %w", err)
		}
		info, err = b.api.CreateProject(ctx, ownerID, spec.Name)
		if err != nil {
			return fmt.Errorf("create project: %w", err)
		}
	}

	var (
		merged  []optionInput
		changed bool
	)
	if spec.Prune {
		var deleted []statusOption
		merged, deleted, changed = pruneStatusOptions(info.Options, spec.Columns)
		if len(deleted) > 0 && !spec.Force {
			if err := b.guardOccupied(ctx, info.ProjectID, deleted); err != nil {
				return err
			}
		}
	} else {
		merged, changed = mergeStatusOptions(info.Options, spec.Columns)
	}
	if changed {
		if err := b.api.UpdateStatusOptions(ctx, info.StatusFieldID, merged); err != nil {
			return fmt.Errorf("update status options: %w", err)
		}
		info, err = b.api.GetProjectByID(ctx, info.ProjectID)
		if err != nil {
			return fmt.Errorf("re-read project: %w", err)
		}
	}

	rec := store.BoardRecord{
		ProjectNumber: info.Number,
		ProjectNodeID: info.ProjectID,
		StatusFieldID: info.StatusFieldID,
		Options:       map[string]string{},
		Owner:         b.owner,
		OwnerType:     b.ownerType,
	}
	byName := map[string]string{}
	for _, o := range info.Options {
		byName[o.Name] = o.ID
	}
	for _, p := range spec.Columns {
		if id, ok := byName[columnName(p)]; ok {
			rec.Options[string(p)] = id
		}
	}
	if err := b.store.PutBoard(info.ProjectID, rec); err != nil {
		return fmt.Errorf("cache board: %w", err)
	}
	b.cached = &rec
	b.projectNodeID = rec.ProjectNodeID
	return nil
}

// guardOccupied returns ErrColumnsOccupied if any of the to-be-deleted options
// still holds cards (so prune can't silently orphan them).
func (b *GitHubBoard) guardOccupied(ctx context.Context, projectID string, deleted []statusOption) error {
	counts, err := b.api.StatusOptionItemCounts(ctx, projectID)
	if err != nil {
		return fmt.Errorf("check column occupancy: %w", err)
	}
	var occupied []string
	for _, d := range deleted {
		if n := counts[d.ID]; n > 0 {
			occupied = append(occupied, fmt.Sprintf("%q (%d)", d.Name, n))
		}
	}
	if len(occupied) > 0 {
		return fmt.Errorf("%w: %s — move the cards or re-run with --force", ErrColumnsOccupied, strings.Join(occupied, ", "))
	}
	return nil
}

// Hydrate loads the board's cached identity (project node id, status field, and
// option→phase map) into memory once, at startup. `serve` MUST call this before
// starting the queue/HTTP server: ParseEvent filters projects_v2_item events by
// b.projectNodeID, which is otherwise empty until some other call warms it — so
// without Hydrate every column-move webhook is silently dropped. Returns
// ErrNotProvisioned if the board hasn't been provisioned/bootstrapped yet.
// After Hydrate, b.cached and b.projectNodeID are set once and only read, so the
// concurrent ParseEvent (HTTP) and worker (queue) goroutines need no lock.
func (b *GitHubBoard) Hydrate(ctx context.Context) error {
	_, err := b.board(ctx)
	return err
}

// board lazily loads the cached board identity (single board, v1).
func (b *GitHubBoard) board(ctx context.Context) (store.BoardRecord, error) {
	if b.cached != nil {
		// projectNodeID was set when b.cached was first populated (below or in
		// EnsureProvisioned); don't re-write it here — that would be a data race
		// with ParseEvent reading it on the HTTP goroutine.
		return *b.cached, nil
	}
	info, found, err := b.api.GetProject(ctx, b.ownerType, b.owner, b.projectNumber)
	if err != nil {
		return store.BoardRecord{}, err
	}
	if !found {
		return store.BoardRecord{}, ErrNotProvisioned
	}
	rec, ok, err := b.store.GetBoard(info.ProjectID)
	if err != nil {
		return store.BoardRecord{}, err
	}
	if !ok {
		return store.BoardRecord{}, ErrNotProvisioned
	}
	b.cached = &rec
	b.projectNodeID = rec.ProjectNodeID
	return rec, nil
}

// ErrRepoNotAllowed means a card targets a repo outside the configured allow-list.
var ErrRepoNotAllowed = errors.New("board/github: repo not in allow-list")

// resolveCard returns a card's forge coordinates, using the cache then a
// GraphQL node lookup; the result is cached for next time. It refuses cards
// whose repo is not in the allow-list (multi-repo containment, spec §6).
func (b *GitHubBoard) resolveCard(ctx context.Context, cardID string) (issueRef, error) {
	rec, ok, err := b.store.GetCard(cardID)
	if err != nil {
		return issueRef{}, err
	}
	// Fast path: trust the cached repo only while it is still in the allow-list.
	// A repo rename/transfer can leave the cached owner/name stale (the issue
	// moved accounts); rather than reject forever on the stale value, fall through
	// to a fresh node lookup and refresh the cache below. resolveCard is thus the
	// single, self-healing allow-list gate.
	rl := b.snap() // one snapshot for this resolve op
	if ok && rec.Repo != "" && b.repoAllowed(rl, rec.Repo) {
		return issueRef{Repo: rec.Repo, Number: rec.IssueNumber}, nil
	}
	ref, err := b.api.ResolveIssue(ctx, cardID)
	if err != nil {
		return issueRef{}, fmt.Errorf("resolve issue %s: %w", cardID, err)
	}
	if !b.repoAllowed(rl, ref.Repo) {
		return issueRef{}, fmt.Errorf("%w: %s", ErrRepoNotAllowed, ref.Repo)
	}
	// Merge into any existing record so a previously-cached ProjectItemID
	// (e.g. set by MoveTo) is preserved, and a stale repo/number is refreshed.
	// Caching is best-effort: resolution already succeeded and this package is
	// logger-free by design.
	rec.Repo = ref.Repo
	rec.IssueNumber = ref.Number
	_ = b.store.PutCard(cardID, rec)
	return ref, nil
}

func splitRepo(full string) (owner, name string, err error) {
	parts := strings.Split(full, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo %q (want owner/name)", full)
	}
	return parts[0], parts[1], nil
}

// PostComment posts a marker-stamped comment on the card's issue.
func (b *GitHubBoard) PostComment(ctx context.Context, cardID, body string) error {
	ref, err := b.resolveCard(ctx, cardID)
	if err != nil {
		return err
	}
	owner, name, err := splitRepo(ref.Repo)
	if err != nil {
		return err
	}
	stamped := body + "\n\n" + botMarker
	_, _, err = b.rest.Issues.CreateComment(ctx, owner, name, ref.Number,
		&github.IssueComment{Body: &stamped})
	if err != nil {
		return fmt.Errorf("create comment: %w", err)
	}
	return nil
}

// SetBody replaces the issue body with markdown, preserving the original
// idea in a collapsed <details> block (init-plan §8.5).
func (b *GitHubBoard) SetBody(ctx context.Context, cardID, markdown string) error {
	ref, err := b.resolveCard(ctx, cardID)
	if err != nil {
		return err
	}
	owner, name, err := splitRepo(ref.Repo)
	if err != nil {
		return err
	}
	cur, _, err := b.rest.Issues.Get(ctx, owner, name, ref.Number)
	if err != nil {
		return fmt.Errorf("get issue: %w", err)
	}
	newBody := markdown
	if orig := cur.GetBody(); orig != "" {
		// Keep the original idea in a collapsed block at the top (init-plan §8.5).
		newBody = "<details>\n<summary>Original idea</summary>\n\n" + orig + "\n\n</details>\n\n" + markdown
	}
	_, _, err = b.rest.Issues.Edit(ctx, owner, name, ref.Number,
		&github.IssueRequest{Body: &newBody})
	if err != nil {
		return fmt.Errorf("edit issue: %w", err)
	}
	return nil
}

// MoveTo sets the card's Status to the option for phase.
func (b *GitHubBoard) MoveTo(ctx context.Context, cardID string, phase board.Phase) error {
	rec, err := b.board(ctx)
	if err != nil {
		return err
	}
	optionID, ok := rec.Options[string(phase)]
	if !ok {
		return fmt.Errorf("no cached option for phase %s", phase)
	}
	itemID, found, err := b.api.FindItem(ctx, rec.ProjectNodeID, cardID)
	if err != nil {
		return fmt.Errorf("find item: %w", err)
	}
	if !found {
		return fmt.Errorf("card %s is not an item on the board", cardID)
	}
	if err := b.api.SetItemStatus(ctx, rec.ProjectNodeID, itemID, rec.StatusFieldID, optionID); err != nil {
		return fmt.Errorf("set status: %w", err)
	}
	cardRec, _, err := b.store.GetCard(cardID)
	if err != nil {
		return fmt.Errorf("read card cache: %w", err)
	}
	cardRec.ProjectItemID = itemID // preserves any cached Repo/IssueNumber
	_ = b.store.PutCard(cardID, cardRec)
	return nil
}

// GetCard returns the card's issue title/body/comments, repo, and current Phase
// (resolved from the project item's Status single-select value).
func (b *GitHubBoard) GetCard(ctx context.Context, cardID string) (board.Card, error) {
	ref, err := b.resolveCard(ctx, cardID)
	if err != nil {
		return board.Card{}, err
	}
	// Load the cached board first (project id + option→phase map) so an
	// unprovisioned board fails before the REST round-trips, matching the
	// board()-first pattern in MoveTo/ListCards.
	rec, err := b.board(ctx)
	if err != nil {
		return board.Card{}, fmt.Errorf("load board: %w", err)
	}
	owner, name, err := splitRepo(ref.Repo)
	if err != nil {
		return board.Card{}, err
	}
	iss, _, err := b.rest.Issues.Get(ctx, owner, name, ref.Number)
	if err != nil {
		return board.Card{}, fmt.Errorf("get issue: %w", err)
	}
	card := board.Card{ID: cardID, Repo: ref.Repo, Title: iss.GetTitle(), Body: iss.GetBody()}

	comments, _, err := b.rest.Issues.ListComments(ctx, owner, name, ref.Number, nil)
	if err != nil {
		return board.Card{}, fmt.Errorf("list comments: %w", err)
	}
	for _, c := range comments {
		body := c.GetBody()
		author := c.GetUser().GetLogin()
		card.Comments = append(card.Comments, board.Comment{
			ID:      fmt.Sprintf("%d", c.GetID()),
			Author:  author,
			IsBot:   author == b.snap().botLogin || strings.Contains(body, botMarker),
			Body:    body,
			Created: c.GetCreatedAt().Time,
		})
	}

	// Phase from the item's current Status option id (M2).
	optID, found, err := b.api.ItemStatus(ctx, rec.ProjectNodeID, cardID)
	if err != nil {
		return board.Card{}, fmt.Errorf("item status: %w", err)
	}
	if found {
		card.Phase = phaseFromOption(rec.Options, optID)
	}
	return card, nil
}

// phaseFromOption reverse-maps a Status option id to its domain Phase using the
// cached option map (phase token -> option id). Returns "" when unknown.
func phaseFromOption(options map[string]string, optionID string) board.Phase {
	for phaseTok, id := range options {
		if id == optionID {
			return board.Phase(phaseTok)
		}
	}
	return ""
}

// ListCards returns the cards currently in phase (first 100 items, v1).
func (b *GitHubBoard) ListCards(ctx context.Context, phase board.Phase) ([]board.Card, error) {
	rec, err := b.board(ctx)
	if err != nil {
		return nil, err
	}
	optionID, ok := rec.Options[string(phase)]
	if !ok {
		return nil, fmt.Errorf("no cached option for phase %s", phase)
	}
	items, err := b.api.ListItems(ctx, rec.ProjectNodeID, rec.StatusFieldID, optionID)
	if err != nil {
		return nil, fmt.Errorf("list items: %w", err)
	}
	var cards []board.Card
	for _, it := range items {
		cards = append(cards, board.Card{
			ID: it.IssueNodeID, Repo: it.Repo, Title: it.Title, Body: it.Body, Phase: phase,
		})
	}
	return cards, nil
}
