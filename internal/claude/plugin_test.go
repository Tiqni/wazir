package claude

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverSuperpowersPluginDir(t *testing.T) {
	home := t.TempDir()
	base := filepath.Join(home, ".claude", "plugins", "cache", "claude-plugins-official", "superpowers")
	for _, v := range []string{"5.1.0", "5.10.0", "5.2.0"} {
		if err := os.MkdirAll(filepath.Join(base, v), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", v, err)
		}
	}
	got, err := DiscoverSuperpowersPluginDir(home)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if filepath.Base(got) != "5.10.0" {
		t.Errorf("got %q, want the newest version 5.10.0", got)
	}
}

func TestDiscoverSuperpowersPluginDirMissing(t *testing.T) {
	if _, err := DiscoverSuperpowersPluginDir(t.TempDir()); err == nil {
		t.Fatal("expected an error when no superpowers cache exists")
	}
}
