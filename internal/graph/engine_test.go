package graph_test

import (
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
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

// Tripwire: nothing that runs the bv program or reads the removed switches may come back. It
// scans the string literals and identifiers of non-test Go files (comments are skipped, so the
// analyzer's notes may still say "bv --robot-insights") and the code lines of scripts/.
func TestNoLeftoversOfTheRemovedBvSubprocessOrEngineSwitch(t *testing.T) {
	bad := regexp.MustCompile(`ABACUS_BV_BIN|ABACUS_GRAPH_ENGINE|--robot-|LookPath\s*\(\s*"bv"|exec\s*\.\s*Command(Context)?\s*\([^)]*"bv"`)
	root := filepath.Join("..", "..")
	var hits []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch d.Name() {
			case ".git", "testdata", "docs", ".cache", "dist", "bin":
				return filepath.SkipDir
			}
			return nil
		}
		isGo := strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go")
		isScript := strings.HasPrefix(rel, "scripts/")
		if !isGo && !isScript {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var code []string
		if isGo {
			var sc scanner.Scanner
			fset := token.NewFileSet()
			sc.Init(fset.AddFile(p, fset.Base(), len(src)), src, nil, 0) // mode 0: comments are dropped
			var toks []string
			for {
				_, tok, lit := sc.Scan()
				if tok == token.EOF {
					break
				}
				if lit == "" {
					lit = tok.String()
				}
				toks = append(toks, lit)
			}
			code = []string{strings.Join(toks, " ")}
		} else {
			for _, l := range strings.Split(string(src), "\n") {
				if !strings.HasPrefix(strings.TrimSpace(l), "#") {
					code = append(code, l)
				}
			}
		}
		for _, c := range code {
			if m := bad.FindString(c); m != "" {
				hits = append(hits, rel+": "+m)
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("leftovers of the removed bv subprocess or engine switch: %v", hits)
	}
}
