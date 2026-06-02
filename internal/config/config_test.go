package config

import (
	"os"
	"path/filepath"
	"testing"
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
