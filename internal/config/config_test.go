package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// clearGitHubEnv removes any ambient WAZIR_GITHUB_* vars for the duration of the
// test (restoring them after), so a developer who exports real App credentials to
// run the daemon doesn't clobber the file/default/absence values these tests assert.
func clearGitHubEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"WAZIR_GITHUB_APP_ID", "WAZIR_GITHUB_INSTALLATION_ID", "WAZIR_GITHUB_PRIVATE_KEY",
		"WAZIR_GITHUB_OWNER_TYPE", "WAZIR_GITHUB_WEBHOOK_SECRET",
	} {
		if v, ok := os.LookupEnv(k); ok {
			os.Unsetenv(k)
			t.Cleanup(func() { os.Setenv(k, v) })
		}
	}
}

// setAppEnv sets the GitHub App auth env so Load() passes validation, after
// clearing any ambient WAZIR_GITHUB_* so the operator's real env can't interfere.
// owner_type defaults to org, so it is not set here.
func setAppEnv(t *testing.T) {
	t.Helper()
	clearGitHubEnv(t)
	t.Setenv("WAZIR_GITHUB_APP_ID", "111")
	t.Setenv("WAZIR_GITHUB_INSTALLATION_ID", "222")
	t.Setenv("WAZIR_GITHUB_PRIVATE_KEY", "/tmp/wazir-test-key.pem")
}

const sampleYAML = `
github:
  app_id: 111
  installation_id: 222
  private_key: filekey
  owner_type: org
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
	clearGitHubEnv(t) // file values must win over any ambient WAZIR_GITHUB_*
	c, err := Load(writeConfig(t, sampleYAML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.GitHub.AppID != 111 || c.GitHub.InstallationID != 222 || c.GitHub.PrivateKey != "filekey" {
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
	t.Chdir(t.TempDir()) // isolate from any ambient ./wazir.yaml
	clearGitHubEnv(t)     // file's owner_type: org must win over any ambient WAZIR_GITHUB_OWNER_TYPE
	t.Setenv("WAZIR_GITHUB_PRIVATE_KEY", "envkey")
	t.Setenv("WAZIR_PROJECT_NUMBER", "99")

	c, err := Load(writeConfig(t, sampleYAML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.GitHub.PrivateKey != "envkey" {
		t.Errorf("env should override file private_key, got %q", c.GitHub.PrivateKey)
	}
	if c.Project.Number != 99 {
		t.Errorf("env should override file number, got %d", c.Project.Number)
	}
}

func TestLoadEnvOnlyWithDefaults(t *testing.T) {
	t.Chdir(t.TempDir()) // ensure no ./wazir.yaml is discovered
	setAppEnv(t)
	t.Setenv("WAZIR_PROJECT_OWNER", "octocat")
	t.Setenv("WAZIR_PROJECT_NUMBER", "5")

	c, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.GitHub.OwnerType != "org" {
		t.Errorf("owner_type default = %q, want org", c.GitHub.OwnerType)
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

func TestLoadRejectsMissingAppFields(t *testing.T) {
	t.Chdir(t.TempDir())
	clearGitHubEnv(t) // the App fields must be genuinely absent, not supplied by the operator's shell
	t.Setenv("WAZIR_PROJECT_OWNER", "octocat")
	t.Setenv("WAZIR_PROJECT_NUMBER", "5")
	// no app_id/installation_id/private_key
	_, err := Load("")
	if err == nil {
		t.Fatal("expected error when the App auth fields are missing")
	}
	if !strings.Contains(err.Error(), "WAZIR_GITHUB_APP_ID") {
		t.Errorf("error should name the missing env vars, got: %v", err)
	}
}

func TestLoadRejectsUserOwnerType(t *testing.T) {
	t.Chdir(t.TempDir())
	setAppEnv(t)
	t.Setenv("WAZIR_GITHUB_OWNER_TYPE", "user")
	t.Setenv("WAZIR_PROJECT_OWNER", "octocat")
	t.Setenv("WAZIR_PROJECT_NUMBER", "5")
	if _, err := Load(""); err == nil {
		t.Fatal("expected error: an App can't drive a user-owned board")
	}
}

func TestLoadExplicitMissingFileErrors(t *testing.T) {
	if _, err := Load("/no/such/wazir.yaml"); err == nil {
		t.Fatal("expected error for an explicit but missing config file")
	}
}

func TestClaudeDefaults(t *testing.T) {
	t.Chdir(t.TempDir()) // isolate from any local wazir.yaml so defaults are exercised
	setAppEnv(t)
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
	setAppEnv(t)
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
	t.Chdir(t.TempDir()) // ensure no ./wazir.yaml is discovered
	setAppEnv(t)
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
	if !strings.HasSuffix(c.Forge.CloneRoot, ".wazir/clones") {
		t.Errorf("CloneRoot = %q, want it to expand and end with .wazir/clones", c.Forge.CloneRoot)
	}
	if !strings.HasSuffix(c.Forge.WorktreeRoot, ".wazir/worktrees") {
		t.Errorf("WorktreeRoot = %q, want it to expand and end with .wazir/worktrees", c.Forge.WorktreeRoot)
	}
	if strings.HasPrefix(c.Forge.CloneRoot, "~") {
		t.Errorf("CloneRoot not expanded: %q", c.Forge.CloneRoot)
	}
	if c.Claude.PlanTimeout != 45*time.Minute {
		t.Errorf("PlanTimeout = %s, want 45m", c.Claude.PlanTimeout)
	}
	if c.Claude.ExecuteTimeout != 60*time.Minute {
		t.Errorf("ExecuteTimeout = %s, want 60m", c.Claude.ExecuteTimeout)
	}
	if c.Claude.ExecuteAllowedTools == "" {
		t.Error("ExecuteAllowedTools must default non-empty")
	}
}

func TestForgeEnvOverrides(t *testing.T) {
	t.Chdir(t.TempDir()) // ensure no ./wazir.yaml is discovered
	setAppEnv(t)
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

func TestClaudeIsolationConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	setAppEnv(t)
	t.Setenv("WAZIR_PROJECT_OWNER", "octocat")
	t.Setenv("WAZIR_PROJECT_NUMBER", "7")

	c, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.HasSuffix(c.Claude.PluginsDir, ".claude/plugins") || strings.HasPrefix(c.Claude.PluginsDir, "~") {
		t.Errorf("plugins_dir default = %q, want an expanded path ending in .claude/plugins", c.Claude.PluginsDir)
	}
	if c.Claude.PluginID != "superpowers@claude-plugins-official" {
		t.Errorf("plugin_id default = %q", c.Claude.PluginID)
	}
	if c.Claude.SettingSources != "user" {
		t.Errorf("setting_sources default = %q, want user", c.Claude.SettingSources)
	}

	t.Setenv("WAZIR_CLAUDE_PLUGINS_DIR", "/opt/plugins")
	t.Setenv("WAZIR_CLAUDE_PLUGIN_ID", "custom@mp")
	t.Setenv("WAZIR_CLAUDE_SETTING_SOURCES", "user,local")
	c2, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c2.Claude.PluginsDir != "/opt/plugins" || c2.Claude.PluginID != "custom@mp" || c2.Claude.SettingSources != "user,local" {
		t.Errorf("env overrides not applied: %+v", c2.Claude)
	}
}

func TestReworkDefaultsAndNormalizer(t *testing.T) {
	t.Chdir(t.TempDir()) // no ./wazir.yaml
	setAppEnv(t)
	t.Setenv("WAZIR_PROJECT_OWNER", "octocat")
	t.Setenv("WAZIR_PROJECT_NUMBER", "7")
	c, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Claude.MaxReworkRounds != 3 {
		t.Errorf("MaxReworkRounds = %d, want 3", c.Claude.MaxReworkRounds)
	}
	if c.Claude.ReworkCommand != "@wazir fix" {
		t.Errorf("ReworkCommand = %q, want @wazir fix", c.Claude.ReworkCommand)
	}
	// Unset rework_timeout / rework_allowed_tools fall back to the execute values.
	if c.Claude.ReworkTimeout != c.Claude.ExecuteTimeout {
		t.Errorf("ReworkTimeout = %s, want = ExecuteTimeout %s", c.Claude.ReworkTimeout, c.Claude.ExecuteTimeout)
	}
	if c.Claude.ReworkAllowedTools != c.Claude.ExecuteAllowedTools {
		t.Errorf("ReworkAllowedTools = %q, want = ExecuteAllowedTools", c.Claude.ReworkAllowedTools)
	}
}

func TestReworkExplicitOverridesPreserved(t *testing.T) {
	t.Chdir(t.TempDir())
	setAppEnv(t)
	t.Setenv("WAZIR_PROJECT_OWNER", "octocat")
	t.Setenv("WAZIR_PROJECT_NUMBER", "7")
	t.Setenv("WAZIR_CLAUDE_REWORK_TIMEOUT", "30m")
	t.Setenv("WAZIR_CLAUDE_REWORK_ALLOWED_TOOLS", "Read,Edit")
	c, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// The normalizer must NOT clobber an explicitly-set rework value with the execute default.
	if c.Claude.ReworkTimeout != 30*time.Minute {
		t.Errorf("ReworkTimeout = %s, want 30m (explicit override preserved)", c.Claude.ReworkTimeout)
	}
	if c.Claude.ReworkAllowedTools != "Read,Edit" {
		t.Errorf("ReworkAllowedTools = %q, want Read,Edit (explicit override preserved)", c.Claude.ReworkAllowedTools)
	}
}
