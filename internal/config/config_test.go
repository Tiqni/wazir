package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const sampleYAML = `
github:
  auth: pat
  pat: filetoken
  owner_type: user
project:
  owner: octocat
  number: 7
  board_name: MyBoard
repos:
  - octocat/a
  - octocat/b
bot_login: bot
store:
  db_path: /tmp/x.db
`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "wazir.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadFromFile(t *testing.T) {
	c, err := Load(writeConfig(t, sampleYAML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.GitHub.Auth != "pat" || c.GitHub.PAT != "filetoken" {
		t.Errorf("github = %+v", c.GitHub)
	}
	if c.Project.Owner != "octocat" || c.Project.Number != 7 || c.Project.BoardName != "MyBoard" {
		t.Errorf("project = %+v", c.Project)
	}
	if len(c.Repos) != 2 || c.Repos[0] != "octocat/a" {
		t.Errorf("repos = %v", c.Repos)
	}
	if c.Store.DBPath != "/tmp/x.db" {
		t.Errorf("db_path = %q", c.Store.DBPath)
	}
}

func TestEnvOverridesFile(t *testing.T) {
	t.Setenv("WAZIR_GITHUB_PAT", "envtoken")
	t.Setenv("WAZIR_PROJECT_NUMBER", "99")

	c, err := Load(writeConfig(t, sampleYAML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.GitHub.PAT != "envtoken" {
		t.Errorf("env should override file pat, got %q", c.GitHub.PAT)
	}
	if c.Project.Number != 99 {
		t.Errorf("env should override file number, got %d", c.Project.Number)
	}
}

func TestLoadEnvOnlyWithDefaults(t *testing.T) {
	t.Chdir(t.TempDir()) // ensure no ./wazir.yaml is discovered
	t.Setenv("WAZIR_GITHUB_PAT", "tok")
	t.Setenv("WAZIR_PROJECT_OWNER", "octocat")
	t.Setenv("WAZIR_PROJECT_NUMBER", "5")

	c, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.GitHub.Auth != "pat" {
		t.Errorf("auth default = %q, want pat", c.GitHub.Auth)
	}
	if c.GitHub.OwnerType != "user" {
		t.Errorf("owner_type default = %q, want user", c.GitHub.OwnerType)
	}
	if c.Project.BoardName != "Wazir" {
		t.Errorf("board_name default = %q, want Wazir", c.Project.BoardName)
	}
	if c.Store.DBPath != "wazir.db" {
		t.Errorf("db_path default = %q, want wazir.db", c.Store.DBPath)
	}
	if c.Project.Owner != "octocat" || c.Project.Number != 5 {
		t.Errorf("project from env = %+v", c.Project)
	}
}

func TestLoadRejectsPATAuthWithoutToken(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("WAZIR_PROJECT_OWNER", "octocat")
	// no WAZIR_GITHUB_PAT
	if _, err := Load(""); err == nil {
		t.Fatal("expected error when auth=pat but pat empty")
	}
}

func TestLoadExplicitMissingFileErrors(t *testing.T) {
	if _, err := Load("/no/such/wazir.yaml"); err == nil {
		t.Fatal("expected error for an explicit but missing config file")
	}
}

func TestClaudeDefaults(t *testing.T) {
	t.Chdir(t.TempDir()) // isolate from any local wazir.yaml so defaults are exercised
	t.Setenv("WAZIR_GITHUB_PAT", "x")
	t.Setenv("WAZIR_PROJECT_OWNER", "octocat")
	t.Setenv("WAZIR_PROJECT_NUMBER", "7")
	c, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Claude.Bin != "claude" {
		t.Errorf("Bin = %q, want claude", c.Claude.Bin)
	}
	if c.Claude.Timeout != 5*time.Minute {
		t.Errorf("Timeout = %s, want 5m", c.Claude.Timeout)
	}
	if c.Claude.MaxBrainstormTurns != 8 {
		t.Errorf("MaxBrainstormTurns = %d, want 8", c.Claude.MaxBrainstormTurns)
	}
}

func TestClaudeEnvOverrides(t *testing.T) {
	t.Setenv("WAZIR_GITHUB_PAT", "x")
	t.Setenv("WAZIR_PROJECT_OWNER", "octocat")
	t.Setenv("WAZIR_PROJECT_NUMBER", "7")
	t.Setenv("WAZIR_CLAUDE_BIN", "/usr/local/bin/claude")
	t.Setenv("WAZIR_CLAUDE_MODEL", "opus")
	t.Setenv("WAZIR_CLAUDE_TIMEOUT", "90s")
	t.Setenv("WAZIR_CLAUDE_MAX_BRAINSTORM_TURNS", "3")
	c, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Claude.Bin != "/usr/local/bin/claude" || c.Claude.Model != "opus" {
		t.Errorf("claude = %+v", c.Claude)
	}
	if c.Claude.Timeout != 90*time.Second {
		t.Errorf("Timeout = %s, want 90s", c.Claude.Timeout)
	}
	if c.Claude.MaxBrainstormTurns != 3 {
		t.Errorf("MaxBrainstormTurns = %d, want 3", c.Claude.MaxBrainstormTurns)
	}
}

func TestForgeAndClaudeM4Defaults(t *testing.T) {
	t.Setenv("WAZIR_GITHUB_PAT", "x")
	t.Setenv("WAZIR_PROJECT_OWNER", "octocat")
	t.Setenv("WAZIR_PROJECT_NUMBER", "7")
	c, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Forge.GitBin != "git" {
		t.Errorf("GitBin = %q, want git", c.Forge.GitBin)
	}
	if c.Forge.BaseBranch != "main" {
		t.Errorf("BaseBranch = %q, want main", c.Forge.BaseBranch)
	}
	if c.Forge.CloneRoot == "" || c.Forge.WorktreeRoot == "" {
		t.Errorf("clone/worktree roots must default non-empty: %+v", c.Forge)
	}
	if c.Claude.PlanTimeout != 10*time.Minute {
		t.Errorf("PlanTimeout = %s, want 10m", c.Claude.PlanTimeout)
	}
	if c.Claude.ExecuteTimeout != 30*time.Minute {
		t.Errorf("ExecuteTimeout = %s, want 30m", c.Claude.ExecuteTimeout)
	}
	if c.Claude.ExecuteAllowedTools == "" {
		t.Error("ExecuteAllowedTools must default non-empty")
	}
}

func TestForgeEnvOverrides(t *testing.T) {
	t.Setenv("WAZIR_GITHUB_PAT", "x")
	t.Setenv("WAZIR_PROJECT_OWNER", "octocat")
	t.Setenv("WAZIR_PROJECT_NUMBER", "7")
	t.Setenv("WAZIR_FORGE_GIT_BIN", "/usr/bin/git")
	t.Setenv("WAZIR_FORGE_CLONE_ROOT", "/srv/clones")
	t.Setenv("WAZIR_FORGE_WORKTREE_ROOT", "/srv/wt")
	t.Setenv("WAZIR_FORGE_BASE_BRANCH", "trunk")
	t.Setenv("WAZIR_CLAUDE_EXECUTE_TIMEOUT", "12m")
	c, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Forge.GitBin != "/usr/bin/git" || c.Forge.CloneRoot != "/srv/clones" ||
		c.Forge.WorktreeRoot != "/srv/wt" || c.Forge.BaseBranch != "trunk" {
		t.Errorf("forge overrides not applied: %+v", c.Forge)
	}
	if c.Claude.ExecuteTimeout != 12*time.Minute {
		t.Errorf("ExecuteTimeout = %s, want 12m", c.Claude.ExecuteTimeout)
	}
}
