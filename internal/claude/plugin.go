package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DiscoverSuperpowersPluginDir returns the newest installed Superpowers plugin
// directory under <home>/.claude/plugins/cache/claude-plugins-official/superpowers/<version>/,
// or an error if none is found. Used when claude.plugin_dir is unset. It reads
// the real ~/.claude — the per-run config-dir relocation happens later, in Run.
func DiscoverSuperpowersPluginDir(home string) (string, error) {
	base := filepath.Join(home, ".claude", "plugins", "cache", "claude-plugins-official", "superpowers")
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", fmt.Errorf("read superpowers cache %s: %w", base, err)
	}
	best := ""
	var bestV [3]int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		v := parseSemver(e.Name())
		if best == "" || lessSemver(bestV, v) {
			best, bestV = e.Name(), v
		}
	}
	if best == "" {
		return "", fmt.Errorf("no superpowers plugin version found under %s", base)
	}
	return filepath.Join(base, best), nil
}

// parseSemver turns "5.10.0" into [5,10,0]; non-numeric parts become 0.
func parseSemver(s string) [3]int {
	var v [3]int
	for i, part := range strings.SplitN(s, ".", 3) {
		n, _ := strconv.Atoi(part)
		v[i] = n
	}
	return v
}

func lessSemver(a, b [3]int) bool {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
