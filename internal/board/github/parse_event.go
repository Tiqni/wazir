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
		item := e.GetProjectV2Item()
		if item == nil || item.GetProjectNodeID() != b.projectNodeID {
			return board.Event{Kind: board.EventIgnore}, nil
		}
		return board.Event{
			Kind:   board.EventPhaseChanged,
			CardID: item.GetContentNodeID(),
			Dedup:  delivery,
		}, nil

	default:
		return board.Event{Kind: board.EventIgnore}, nil
	}
}
