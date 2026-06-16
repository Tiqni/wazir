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
	repos := b.snap().repos
	if len(repos) == 0 {
		return true // no allow-list configured = allow all
	}
	for _, r := range repos {
		if r == full {
			return true
		}
	}
	return false
}

// ParseEvent validates and normalizes a raw GitHub webhook into a domain Event.
func (b *GitHubBoard) ParseEvent(headers map[string]string, payload []byte) (board.Event, error) {
	sig := headerGet(headers, "X-Hub-Signature-256")
	rl := b.snap()
	if err := github.ValidateSignature(sig, payload, []byte(rl.webhookSecret)); err != nil {
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
				IsBot:   author == rl.botLogin || strings.Contains(body, botMarker),
				Body:    body,
				Created: e.GetComment().GetCreatedAt().Time,
			},
		}, nil

	case *github.ProjectV2ItemEvent:
		// Loop prevention: the bot's own MoveTo mutations emit projects_v2_item
		// events. Drop them so a move never re-triggers the worker. Requires
		// bot_login to be configured (guarded so an empty login never matches).
		if rl.botLogin != "" && e.GetSender().GetLogin() == rl.botLogin {
			return board.Event{Kind: board.EventIgnore}, nil
		}
		item := e.GetProjectV2Item()
		if item == nil || item.GetProjectNodeID() != b.projectNodeID {
			return board.Event{Kind: board.EventIgnore}, nil
		}
		cardID := item.GetContentNodeID()
		ev := board.Event{Kind: board.EventPhaseChanged, CardID: cardID, Dedup: delivery}
		// Best-effort routing hint: the payload carries no repo, so populate it
		// from the cache *only when the cached repo is still allowed*. We do NOT
		// drop on a cached repo that looks disallowed — a repo rename/transfer can
		// make the cache stale, and there's no repo in the payload to re-check
		// here. The authoritative, self-refreshing allow-list check happens in
		// resolveCard when the worker looks the card up.
		if b.store != nil {
			if rec, ok, err := b.store.GetCard(cardID); err == nil && ok && rec.Repo != "" && b.repoAllowed(rec.Repo) {
				ev.Repo = rec.Repo
			}
		}
		return ev, nil

	default:
		return board.Event{Kind: board.EventIgnore}, nil
	}
}
