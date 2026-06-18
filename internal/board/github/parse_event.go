package github

import (
	"fmt"
	"strings"

	"github.com/google/go-github/v66/github"

	"github.com/EmadMokhtar/wazir/internal/board"
)

func headerGet(h map[string]string, key string) string {
	for k, v := range h {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

func (b *GitHubBoard) repoAllowed(full string) bool {
	if len(b.repos) == 0 {
		return true // no allow-list configured = allow all
	}
	for _, r := range b.repos {
		if r == full {
			return true
		}
	}
	return false
}

// lookupPRIndex resolves a PR number to its card's issue node id via the store
// reverse index. Returns ("", false) on a cold index or a missing store.
func (b *GitHubBoard) lookupPRIndex(repo string, prNumber int) (string, bool) {
	if b.store == nil {
		return "", false
	}
	id, ok, err := b.store.GetPRIndex(repo, prNumber)
	if err != nil || !ok {
		return "", false
	}
	return id, true
}

// ParseEvent validates and normalizes a raw GitHub webhook into a domain Event.
func (b *GitHubBoard) ParseEvent(headers map[string]string, payload []byte) (board.Event, error) {
	sig := headerGet(headers, "X-Hub-Signature-256")
	if err := github.ValidateSignature(sig, payload, []byte(b.webhookSecret)); err != nil {
		return board.Event{}, fmt.Errorf("validate signature: %w", err)
	}
	eventType := headerGet(headers, "X-GitHub-Event")
	delivery := headerGet(headers, "X-GitHub-Delivery")

	raw, err := github.ParseWebHook(eventType, payload)
	if err != nil {
		return board.Event{}, fmt.Errorf("parse webhook: %w", err)
	}

	switch e := raw.(type) {
	case *github.IssuesEvent:
		repo := e.GetRepo().GetFullName()
		if !b.repoAllowed(repo) {
			return board.Event{Kind: board.EventIgnore}, nil
		}
		ev := board.Event{
			CardID: e.GetIssue().GetNodeID(),
			Repo:   repo,
			Dedup:  delivery,
		}
		if e.GetAction() == "opened" {
			ev.Kind = board.EventCardCreated
		} else {
			ev.Kind = board.EventIgnore
		}
		return ev, nil

	case *github.IssueCommentEvent:
		// A conversation comment on a PR arrives as issue_comment, but its issue
		// node id is the PR's, not the card's. Ignore here (phase-2 rework will
		// route these via the PR-index).
		if e.GetIssue().IsPullRequest() {
			return board.Event{Kind: board.EventIgnore}, nil
		}
		repo := e.GetRepo().GetFullName()
		if !b.repoAllowed(repo) {
			return board.Event{Kind: board.EventIgnore}, nil
		}
		if e.GetAction() != "created" {
			return board.Event{Kind: board.EventIgnore}, nil
		}
		author := e.GetComment().GetUser().GetLogin()
		body := e.GetComment().GetBody()
		return board.Event{
			Kind:   board.EventCommentAdded,
			CardID: e.GetIssue().GetNodeID(),
			Repo:   repo,
			Dedup:  delivery,
			Comment: &board.Comment{
				ID:      fmt.Sprintf("%d", e.GetComment().GetID()),
				Author:  author,
				IsBot:   author == b.botLogin || strings.Contains(body, botMarker),
				Body:    body,
				Created: e.GetComment().GetCreatedAt().Time,
			},
		}, nil

	case *github.ProjectV2ItemEvent:
		// Loop prevention: the bot's own MoveTo mutations emit projects_v2_item
		// events. Drop them so a move never re-triggers the worker. Requires
		// bot_login to be configured (guarded so an empty login never matches).
		if b.botLogin != "" && e.GetSender().GetLogin() == b.botLogin {
			return board.Event{Kind: board.EventIgnore}, nil
		}
		item := e.GetProjectV2Item()
		if item == nil || item.GetProjectNodeID() != b.projectNodeID {
			return board.Event{Kind: board.EventIgnore}, nil
		}
		cardID := item.GetContentNodeID()
		ev := board.Event{Kind: board.EventPhaseChanged, CardID: cardID, Dedup: delivery}
		// Best-effort: the payload carries no repo, but if we already know the
		// card's repo, populate it and drop events for repos outside the
		// allow-list. A cold cache can't filter here — the resolver enforces it
		// later when the card's repo is looked up.
		if b.store != nil {
			if rec, ok, err := b.store.GetCard(cardID); err == nil && ok && rec.Repo != "" {
				if !b.repoAllowed(rec.Repo) {
					return board.Event{Kind: board.EventIgnore}, nil
				}
				ev.Repo = rec.Repo
			}
		}
		return ev, nil

	case *github.PullRequestReviewEvent:
		if e.GetAction() != "submitted" {
			return board.Event{Kind: board.EventIgnore}, nil
		}
		// Decision-grade only: ignore "commented"/"dismissed" reviews.
		if s := e.GetReview().GetState(); s != "approved" && s != "changes_requested" {
			return board.Event{Kind: board.EventIgnore}, nil
		}
		repo := e.GetRepo().GetFullName()
		if !b.repoAllowed(repo) {
			return board.Event{Kind: board.EventIgnore}, nil
		}
		if b.botLogin != "" && e.GetSender().GetLogin() == b.botLogin {
			return board.Event{Kind: board.EventIgnore}, nil
		}
		cardID, ok := b.lookupPRIndex(repo, e.GetPullRequest().GetNumber())
		if !ok {
			return board.Event{Kind: board.EventIgnore}, nil
		}
		return board.Event{Kind: board.EventReviewSubmitted, CardID: cardID, Repo: repo, Dedup: delivery}, nil

	case *github.CheckSuiteEvent:
		if e.GetAction() != "completed" {
			return board.Event{Kind: board.EventIgnore}, nil
		}
		repo := e.GetRepo().GetFullName()
		if !b.repoAllowed(repo) {
			return board.Event{Kind: board.EventIgnore}, nil
		}
		if b.botLogin != "" && e.GetSender().GetLogin() == b.botLogin {
			return board.Event{Kind: board.EventIgnore}, nil
		}
		prs := e.GetCheckSuite().PullRequests
		if len(prs) == 0 {
			return board.Event{Kind: board.EventIgnore}, nil
		}
		// Wazir opens exactly one PR per card off its own branch, so the first
		// (typically only) PR in the suite is the card's PR.
		cardID, ok := b.lookupPRIndex(repo, prs[0].GetNumber())
		if !ok {
			return board.Event{Kind: board.EventIgnore}, nil
		}
		return board.Event{Kind: board.EventChecksCompleted, CardID: cardID, Repo: repo, Dedup: delivery}, nil

	default:
		return board.Event{Kind: board.EventIgnore}, nil
	}
}
