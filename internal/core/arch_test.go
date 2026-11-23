package core_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Front-ends sit on core, never the reverse: core, the tool adapters and the CLI must not
// import a UI library or a front-end package, so another GUI can reuse them unchanged.
func TestLogicPackagesImportNoUI(t *testing.T) {
	banned := []string{"charmbracelet", "internal/tui"}
	for _, dir := range []string{".", "../lambe", "../beadslease", "../cli", "../demo", "../rex"} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			if _, err := os.Stat(f); err != nil {
				continue
			}
			af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range af.Imports {
				for _, b := range banned {
					if strings.Contains(imp.Path.Value, b) {
						t.Errorf("%s imports %s", f, imp.Path.Value)
					}
				}
			}
		}
	}
}
