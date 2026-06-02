package github

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/go-github/v66/github"

	"github.com/EmadMokhtar/wazir/internal/board"
	"github.com/EmadMokhtar/wazir/internal/store"
)

// botMarker stamps bot-authored content so ParseEvent can flag self-events.
const botMarker = "<!-- wazir -->"

// ErrNotProvisioned means the configured board has no cached identity yet.
var ErrNotProvisioned = errors.New("board/github: board not provisioned")

// GitHubBoard implements board.Board against GitHub Projects v2.
type GitHubBoard struct {
	api   projectsAPI
	rest  *github.Client
	store store.Store

	owner         string
	ownerType     string
	projectNumber int
	boardName     string
	botLogin      string
	repos         []string // allow-list ("owner/name")

	cached *store.BoardRecord // lazily loaded board identity
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

	merged, changed := mergeStatusOptions(info.Options, spec.Columns)
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
	return nil
}

// board lazily loads the cached board identity (single board, v1).
func (b *GitHubBoard) board(ctx context.Context) (store.BoardRecord, error) {
	if b.cached != nil {
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
	return rec, nil
}

// resolveCard returns a card's forge coordinates, using the cache then a
// GraphQL node lookup; the result is cached for next time.
func (b *GitHubBoard) resolveCard(ctx context.Context, cardID string) (issueRef, error) {
	if rec, ok, err := b.store.GetCard(cardID); err != nil {
		return issueRef{}, err
	} else if ok && rec.Repo != "" {
		return issueRef{Repo: rec.Repo, Number: rec.IssueNumber}, nil
	}
	ref, err := b.api.ResolveIssue(ctx, cardID)
	if err != nil {
		return issueRef{}, fmt.Errorf("resolve issue %s: %w", cardID, err)
	}
	_ = b.store.PutCard(cardID, store.CardRecord{Repo: ref.Repo, IssueNumber: ref.Number})
	return ref, nil
}

func splitRepo(full string) (owner, name string, err error) {
	parts := strings.SplitN(full, "/", 2)
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
		newBody = markdown + "\n\n<details>\n<summary>Original idea</summary>\n\n" + orig + "\n\n</details>\n"
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
	cardRec, _, _ := b.store.GetCard(cardID)
	cardRec.ProjectItemID = itemID
	_ = b.store.PutCard(cardID, cardRec)
	return nil
}

// GetCard returns the card's issue title/body/comments and repo.
// Phase resolution from the item's status is an M1 concern (left empty here).
func (b *GitHubBoard) GetCard(ctx context.Context, cardID string) (board.Card, error) {
	ref, err := b.resolveCard(ctx, cardID)
	if err != nil {
		return board.Card{}, err
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
			IsBot:   author == b.botLogin || strings.Contains(body, botMarker),
			Body:    body,
			Created: c.GetCreatedAt().Time,
		})
	}
	return card, nil
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
