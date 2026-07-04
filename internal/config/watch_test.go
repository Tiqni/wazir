package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// clearWazirEnv unsets ambient WAZIR_* vars for the test so file/default values
// (e.g. project.number=0) aren't overridden by the developer's running-daemon env.
func clearWazirEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"WAZIR_PROJECT_NUMBER", "WAZIR_PROJECT_OWNER", "WAZIR_GITHUB_OWNER_TYPE",
		"WAZIR_GITHUB_PAT", "WAZIR_GITHUB_APP_ID", "WAZIR_GITHUB_INSTALLATION_ID",
		"WAZIR_GITHUB_PRIVATE_KEY", "WAZIR_GITHUB_WEBHOOK_SECRET",
	} {
		if v, ok := os.LookupEnv(k); ok {
			os.Unsetenv(k)
			t.Cleanup(func() { os.Setenv(k, v) })
		}
	}
}

func TestResolvePath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "wazir.yaml")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := ResolvePath(p); !ok || got != p {
		t.Errorf("explicit flag: got %q ok=%v", got, ok)
	}
	if _, ok := ResolvePath(filepath.Join(dir, "missing.yaml")); ok {
		t.Error("missing explicit file should be ok=false")
	}
	t.Chdir(t.TempDir()) // no ./wazir.yaml
	t.Setenv("HOME", t.TempDir())
	if _, ok := ResolvePath(""); ok {
		t.Error("env-only run (no file) should be ok=false")
	}
}

func TestWatchReloadsOnChangeAndRejectsInvalid(t *testing.T) {
	clearWazirEnv(t) // file values (incl. project.number=0) must win over ambient WAZIR_*
	dir := t.TempDir()
	path := filepath.Join(dir, "wazir.yaml")
	const valid = "github:\n  app_id: 1\n  installation_id: 2\n  private_key: k\nproject:\n  owner: octocat\n  number: 1\n"
	if err := os.WriteFile(path, []byte(valid), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reloaded := make(chan Config, 1)
	errs := make(chan error, 1)
	go Watch(ctx, path, func(c Config) { reloaded <- c }, func(e error) { errs <- e })
	time.Sleep(300 * time.Millisecond) // let the watcher register (w.Add) before the first write

	// Valid edit → onReload with the new value.
	if err := os.WriteFile(path, []byte(valid+"repos:\n  - octocat/added\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-reloaded:
		if len(c.Repos) != 1 || c.Repos[0] != "octocat/added" {
			t.Errorf("reloaded repos = %v", c.Repos)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for reload")
	}

	// Invalid edit (number 0) → onError, not onReload.
	if err := os.WriteFile(path, []byte("github:\n  app_id: 1\n  installation_id: 2\n  private_key: k\nproject:\n  owner: octocat\n  number: 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-errs:
	case c := <-reloaded:
		t.Fatalf("invalid config should not reload; got %+v", c)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for onError")
	}
}
