// Package config loads Wazir's runtime configuration from the environment.
package config

import (
	"fmt"

	"github.com/caarlos0/env/v11"
)

// Config is the full M0 configuration surface (init-plan §11, spec §8).
type Config struct {
	GitHubAuth              string `env:"GITHUB_AUTH" envDefault:"pat"` // pat | app
	GitHubPAT               string `env:"GITHUB_PAT"`
	GitHubAppID             int64  `env:"GITHUB_APP_ID"`
	GitHubPrivateKey        string `env:"GITHUB_PRIVATE_KEY"`
	GitHubAppInstallationID int64  `env:"GITHUB_APP_INSTALLATION_ID"`

	OwnerType     string   `env:"OWNER_TYPE" envDefault:"user"` // user | org
	ProjectOwner  string   `env:"PROJECT_OWNER"`
	ProjectNumber int      `env:"PROJECT_NUMBER"`
	BoardName     string   `env:"BOARD_NAME" envDefault:"Wazir"`
	Repos         []string `env:"REPOS" envSeparator:","`

	BotLogin      string `env:"BOT_LOGIN"`
	WebhookSecret string `env:"GITHUB_WEBHOOK_SECRET"`
	DBPath        string `env:"WAZIR_DB" envDefault:"wazir.db"`
}

// Load reads the environment and validates it.
func Load() (Config, error) {
	var c Config
	if err := env.Parse(&c); err != nil {
		return Config{}, fmt.Errorf("parse env: %w", err)
	}
	if err := c.validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) validate() error {
	switch c.GitHubAuth {
	case "pat":
		if c.GitHubPAT == "" {
			return fmt.Errorf("GITHUB_AUTH=pat requires GITHUB_PAT")
		}
	case "app":
		if c.GitHubAppID == 0 || c.GitHubPrivateKey == "" {
			return fmt.Errorf("GITHUB_AUTH=app requires GITHUB_APP_ID and GITHUB_PRIVATE_KEY")
		}
	default:
		return fmt.Errorf("GITHUB_AUTH must be pat or app, got %q", c.GitHubAuth)
	}
	if c.OwnerType != "user" && c.OwnerType != "org" {
		return fmt.Errorf("OWNER_TYPE must be user or org, got %q", c.OwnerType)
	}
	if c.ProjectOwner == "" {
		return fmt.Errorf("PROJECT_OWNER is required")
	}
	return nil
}
