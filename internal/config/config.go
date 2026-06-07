// Package config loads Wazir's runtime configuration from a YAML file
// (with environment-variable overrides) using kkyr/fig.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kkyr/fig"
)

// envPrefix is prepended to every environment variable fig reads, e.g. the
// github.pat field is overridden by WAZIR_GITHUB_PAT.
const envPrefix = "WAZIR"

// Config is the full configuration surface (init-plan §11).
// Sections map to wazir.yaml; env overrides follow WAZIR_<SECTION>_<FIELD>.
type Config struct {
	GitHub   GitHubConfig  `fig:"github"`
	Project  ProjectConfig `fig:"project"`
	Repos    []string      `fig:"repos"`
	BotLogin string        `fig:"bot_login"`
	Store    StoreConfig   `fig:"store"`
	Claude   ClaudeConfig  `fig:"claude"`
	Forge    ForgeConfig   `fig:"forge"`
}

// ClaudeConfig configures the headless claude-CLI brain (M2; init-plan §8.4/§11).
type ClaudeConfig struct {
	Bin                 string        `fig:"bin" default:"claude"`             // WAZIR_CLAUDE_BIN
	Model               string        `fig:"model"`                            // WAZIR_CLAUDE_MODEL ("" = CLI default)
	Timeout             time.Duration `fig:"timeout" default:"5m"`             // WAZIR_CLAUDE_TIMEOUT
	MaxBrainstormTurns  int           `fig:"max_brainstorm_turns" default:"8"` // WAZIR_CLAUDE_MAX_BRAINSTORM_TURNS
	PlanTimeout         time.Duration `fig:"plan_timeout" default:"10m"`       // WAZIR_CLAUDE_PLAN_TIMEOUT
	ExecuteTimeout      time.Duration `fig:"execute_timeout" default:"30m"`    // WAZIR_CLAUDE_EXECUTE_TIMEOUT
	ExecuteAllowedTools string        `fig:"execute_allowed_tools" default:"Read,Edit,Write,Bash(git:*),Bash(go:*),Bash(gofmt:*),Bash(ls:*),Bash(cat:*)"`
	PluginDir           string        `fig:"plugin_dir"`      // WAZIR_CLAUDE_PLUGIN_DIR ("" = serve auto-discovers)
	SettingSources      string        `fig:"setting_sources"` // WAZIR_CLAUDE_SETTING_SOURCES (spike-pinned; "" = flag omitted)
}

// ForgeConfig configures the local git clone + worktree layout (M4; init-plan §8.6).
type ForgeConfig struct {
	GitBin       string `fig:"git_bin" default:"git"`                      // WAZIR_FORGE_GIT_BIN
	CloneRoot    string `fig:"clone_root" default:"~/.wazir/clones"`       // WAZIR_FORGE_CLONE_ROOT
	WorktreeRoot string `fig:"worktree_root" default:"~/.wazir/worktrees"` // WAZIR_FORGE_WORKTREE_ROOT
	BaseBranch   string `fig:"base_branch" default:"main"`                 // WAZIR_FORGE_BASE_BRANCH
}

// GitHubConfig holds auth + GitHub-side identity. Secrets (pat, webhook_secret,
// private_key) are normally supplied via env, not the file.
type GitHubConfig struct {
	Auth           string `fig:"auth" default:"pat"`        // pat | app
	PAT            string `fig:"pat"`                       // WAZIR_GITHUB_PAT
	OwnerType      string `fig:"owner_type" default:"user"` // user | org
	WebhookSecret  string `fig:"webhook_secret"`            // WAZIR_GITHUB_WEBHOOK_SECRET
	AppID          int64  `fig:"app_id"`
	PrivateKey     string `fig:"private_key"`
	InstallationID int64  `fig:"installation_id"`
}

// ProjectConfig identifies the Projects v2 board.
type ProjectConfig struct {
	Owner     string `fig:"owner"`
	Number    int    `fig:"number"`
	BoardName string `fig:"board_name" default:"Wazir"`
}

// StoreConfig configures persistence.
type StoreConfig struct {
	DBPath string `fig:"db_path" default:"wazir.db"`
}

// Load reads configuration. If path is non-empty it must exist. If path is
// empty, ./wazir.yaml (then $HOME/.config/wazir/wazir.yaml) is used when
// present; otherwise config comes from environment + defaults only.
func Load(path string) (Config, error) {
	var c Config
	opts := []fig.Option{fig.UseEnv(envPrefix)}

	switch {
	case path != "":
		if !fileExists(path) {
			return Config{}, fmt.Errorf("config file %q not found", path)
		}
		opts = append(opts, fig.File(filepath.Base(path)), fig.Dirs(filepath.Dir(path)))
	default:
		if dir, name, ok := defaultConfigFile(); ok {
			opts = append(opts, fig.File(name), fig.Dirs(dir))
		} else {
			opts = append(opts, fig.IgnoreFile())
		}
	}

	if err := fig.Load(&c, opts...); err != nil {
		return Config{}, fmt.Errorf("load config: %w", err)
	}
	c.Forge.CloneRoot = expandHome(c.Forge.CloneRoot)
	c.Forge.WorktreeRoot = expandHome(c.Forge.WorktreeRoot)
	if err := c.validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) validate() error {
	switch c.GitHub.Auth {
	case "pat":
		if c.GitHub.PAT == "" {
			return fmt.Errorf("github.auth=pat requires github.pat (set WAZIR_GITHUB_PAT)")
		}
	case "app":
		if c.GitHub.AppID == 0 || c.GitHub.PrivateKey == "" {
			return fmt.Errorf("github.auth=app requires github.app_id and github.private_key")
		}
	default:
		return fmt.Errorf("github.auth must be pat or app, got %q", c.GitHub.Auth)
	}
	if c.GitHub.OwnerType != "user" && c.GitHub.OwnerType != "org" {
		return fmt.Errorf("github.owner_type must be user or org, got %q", c.GitHub.OwnerType)
	}
	if c.Project.Owner == "" {
		return fmt.Errorf("project.owner is required")
	}
	if c.Project.Number <= 0 {
		return fmt.Errorf("project.number must be set and > 0 (got %d)", c.Project.Number)
	}
	return nil
}

// expandHome turns a leading ~ into $HOME. Leaves other paths untouched.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	return p
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// defaultConfigFile returns the (dir, name) of the first existing default
// config file, or ok=false if none is found.
func defaultConfigFile() (dir, name string, ok bool) {
	candidates := []string{"wazir.yaml"}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".config", "wazir", "wazir.yaml"))
	}
	for _, p := range candidates {
		if fileExists(p) {
			return filepath.Dir(p), filepath.Base(p), true
		}
	}
	return "", "", false
}
