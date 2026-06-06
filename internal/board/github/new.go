package github

import (
	"net/http"

	"github.com/google/go-github/v66/github"
	"github.com/shurcooL/githubv4"

	"github.com/EmadMokhtar/wazir/internal/config"
	"github.com/EmadMokhtar/wazir/internal/store"
)

// New wires a GitHubBoard from an authenticated HTTP client, config, and store.
func New(httpClient *http.Client, cfg config.Config, st store.Store) *GitHubBoard {
	return &GitHubBoard{
		api:           &ghProjects{gql: githubv4.NewClient(httpClient)},
		rest:          github.NewClient(httpClient),
		store:         st,
		owner:         cfg.Project.Owner,
		ownerType:     cfg.GitHub.OwnerType,
		projectNumber: cfg.Project.Number,
		boardName:     cfg.Project.BoardName,
		botLogin:      cfg.BotLogin,
		repos:         cfg.Repos,
		webhookSecret: cfg.GitHub.WebhookSecret,
	}
}
