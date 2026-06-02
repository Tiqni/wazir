package orchestrator_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The orchestrator core may import internal/board and internal/forge (ports)
// but NEVER a provider package. This test fails loudly if that rule is broken.
func TestNoProviderImportsInOrchestrator(t *testing.T) {
	dir := "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	banned := []string{"internal/board/github", "internal/forge/github"}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			for _, b := range banned {
				if strings.Contains(p, b) {
					t.Errorf("%s imports banned provider package %s", e.Name(), p)
				}
			}
		}
	}
}
