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
		owner:         cfg.ProjectOwner,
		ownerType:     cfg.OwnerType,
		projectNumber: cfg.ProjectNumber,
		boardName:     cfg.BoardName,
		botLogin:      cfg.BotLogin,
		repos:         cfg.Repos,
		webhookSecret: cfg.WebhookSecret,
	}
}
