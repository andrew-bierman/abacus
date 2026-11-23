package graph_test

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/graph"
)

func TestParseEngine(t *testing.T) {
	for _, c := range []struct {
		in    string
		want  graph.Engine
		known bool
	}{
		{"", graph.Subprocess, true},
		{"subprocess", graph.Subprocess, true},
		{"library", graph.Library, true},
		{" Library\n", graph.Library, true},
		{"LIBRARY", graph.Library, true},
		{"libary", graph.Subprocess, false},
		{"bv", graph.Subprocess, false},
		{"1", graph.Subprocess, false},
	} {
		if got, known := graph.ParseEngine(c.in); got != c.want || known != c.known {
			t.Errorf("ParseEngine(%q) = %q, %v; want %q, %v", c.in, got, known, c.want, c.known)
		}
	}
}

func TestEngineCheckSaysWhichEngineIsActiveAndWarnsOnATypo(t *testing.T) {
	doctor := func(raw string) core.Check {
		ch := graph.NewEngineCheck(raw).Doctor(context.Background())
		if len(ch) != 1 {
			t.Fatalf("%q: %+v", raw, ch)
		}
		return ch[0]
	}
	if c := doctor(""); c.Status != core.StatusOK || !strings.Contains(c.Detail, "subprocess") {
		t.Errorf("default: %+v", c)
	}
	if c := doctor("library"); c.Status != core.StatusOK || !strings.Contains(c.Detail, "library") {
		t.Errorf("library: %+v", c)
	}
	c := doctor("libary")
	if c.Status != core.StatusWarn || c.Detail != "ABACUS_GRAPH_ENGINE: unknown value, using the default" {
		t.Errorf("unknown value: %+v", c)
	}
	// The value is never echoed: a typo can hold anything, and Doctor text is shared.
	if strings.Contains(doctor("libary\x1b[2J").Detail, "libary") {
		t.Error("the warning must not repeat the value")
	}
	if !graph.NewEngineCheck("").Quiet() || graph.NewEngineCheck("").Detect(context.Background()).State != core.Ready {
		t.Error("the engine check is a quiet, always-ready module")
	}
}

// bv is imported by exactly one file, and neither core nor any other package reaches it or
// this package's internals: removing bv is one file plus its constructor call.
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
