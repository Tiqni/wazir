package config

import "testing"

func TestLoadRejectsMissingProjectNumber(t *testing.T) {
	t.Chdir(t.TempDir())
	setAppEnv(t)
	t.Setenv("WAZIR_PROJECT_OWNER", "octocat")
	t.Setenv("WAZIR_PROJECT_NUMBER", "0") // explicit zero — must be rejected

	if _, err := Load(""); err == nil {
		t.Fatal("expected error when project.number is 0")
	}
}
