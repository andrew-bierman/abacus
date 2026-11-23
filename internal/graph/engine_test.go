package graph_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bv is imported by exactly one file, and neither core nor any other package reaches it or
// this package's internals: the rest of abacus never names a bv type.
func TestBvIsImportedByOneFileAndCoreKnowsNothingOfIt(t *testing.T) {
	const bvPath = "github.com/Dicklesworthstone/beads_viewer"
	var importers []string
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") {
			return err
		}
		af, perr := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		for _, imp := range af.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if strings.HasPrefix(path, bvPath) {
				importers = append(importers, rel)
			}
			if strings.HasPrefix(rel, "internal/core/") && (strings.HasPrefix(path, bvPath) || strings.HasSuffix(path, "/internal/graph")) {
				t.Errorf("%s imports %s: core must not know bv or the graph package", rel, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range importers {
		seen[f] = true
	}
	if len(seen) != 1 || !seen["internal/graph/bvanalyzer.go"] {
		t.Fatalf("only internal/graph/bvanalyzer.go may import bv; importers: %v", importers)
	}
}
