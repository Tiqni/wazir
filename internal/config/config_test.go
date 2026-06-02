package config

import "testing"

func TestLoadDefaultsAndParsing(t *testing.T) {
	t.Setenv("GITHUB_PAT", "tok")
	t.Setenv("PROJECT_OWNER", "octocat")
	t.Setenv("PROJECT_NUMBER", "7")
	t.Setenv("REPOS", "octocat/a,octocat/b")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.GitHubAuth != "pat" {
		t.Errorf("GitHubAuth default = %q, want pat", c.GitHubAuth)
	}
	if c.OwnerType != "user" {
		t.Errorf("OwnerType default = %q, want user", c.OwnerType)
	}
	if c.ProjectNumber != 7 {
		t.Errorf("ProjectNumber = %d, want 7", c.ProjectNumber)
	}
	if len(c.Repos) != 2 || c.Repos[0] != "octocat/a" {
		t.Errorf("Repos = %v, want [octocat/a octocat/b]", c.Repos)
	}
}

func TestLoadRejectsPATAuthWithoutToken(t *testing.T) {
	t.Setenv("GITHUB_AUTH", "pat")
	t.Setenv("GITHUB_PAT", "")
	t.Setenv("PROJECT_OWNER", "octocat")
	t.Setenv("PROJECT_NUMBER", "7")
	if _, err := Load(); err == nil {
		t.Fatal("expected error when GITHUB_AUTH=pat but GITHUB_PAT empty")
	}
}
