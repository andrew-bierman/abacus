package graph_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/graph"
)

func load(t *testing.T, name string) []graph.Issue {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	issues, err := graph.ParseIssues(f)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return issues
}

func parse(t *testing.T, s string) []graph.Issue {
	t.Helper()
	issues, err := graph.ParseIssues(strings.NewReader(s))
	if err != nil {
		t.Fatalf("%q: %v", s, err)
	}
	return issues
}

func TestParseIssuesReadsTheExportShape(t *testing.T) {
	issues := load(t, "fx_cyclic")
	if len(issues) != 7 {
		t.Fatalf("%d issues", len(issues))
	}
	i := issues[1]
	if i.ID != "fx-2" || i.Title != "API layer" || i.Status != graph.StatusOpen || i.Priority != 2 || i.Type != "task" ||
		len(i.DependsOn) != 1 || i.DependsOn[0] != (graph.Dependency{ID: "fx-1", Type: graph.DepBlocks}) || i.UpdatedAt.IsZero() {
		t.Fatalf("%+v", i)
	}
	if issues[3].Status != graph.StatusInProgress || issues[6].Status != graph.StatusClosed {
		t.Fatalf("statuses %q %q", issues[3].Status, issues[6].Status)
	}
	if got := load(t, "empty"); len(got) != 0 {
		t.Fatalf("empty export: %+v", got)
	}
}

func TestParseIssuesAcceptsAJSONArrayAndBlocksAndOtherSpellings(t *testing.T) {
	got := parse(t, `[
	  {"id":"a","title":"A","status":"OPEN ","blocks":["b"]},
	  {"id":"b","title":"B","status":"tombstone","dependencies":["a",{"id":"c","dependency_type":"related"},{"depends_on_id":"d"},{}]}
	]`)
	if len(got) != 2 || got[0].Status != graph.StatusOpen || len(got[0].Blocks) != 1 || got[0].Blocks[0] != "b" {
		t.Fatalf("%+v", got)
	}
	want := []graph.Dependency{{ID: "a", Type: graph.DepBlocks}, {ID: "c", Type: graph.DepRelated}, {ID: "d", Type: graph.DepBlocks}}
	if got[1].Status != graph.StatusClosed || len(got[1].DependsOn) != 3 {
		t.Fatalf("%+v", got[1])
	}
	for n, w := range want {
		if got[1].DependsOn[n] != w {
			t.Errorf("dep %d = %+v, want %+v", n, got[1].DependsOn[n], w)
		}
	}
}

func TestParseIssuesDuplicatesBlankLinesAndBadInput(t *testing.T) {
	got := parse(t, "\n{\"id\":\"a\",\"title\":\"first\"}\n\n{\"id\":\"b\"}\n{\"title\":\"no id\"}\n{\"id\":\"a\",\"title\":\"second\",\"status\":\"closed\"}\n")
	if len(got) != 2 || got[0].ID != "a" || got[0].Title != "second" || got[0].Status != graph.StatusClosed || got[1].ID != "b" {
		t.Fatalf("last record wins in the first one's place: %+v", got)
	}
	if got[1].Status != graph.StatusOpen {
		t.Fatalf("a missing status reads as open: %q", got[1].Status)
	}
	for _, bad := range []string{"{\"id\":\"a\"}\nnot json\n", "[{\"id\":", "{\"id\":\"a\"}{"} {
		if is, err := graph.ParseIssues(strings.NewReader(bad)); err == nil {
			t.Errorf("%q must be an error, never a partial graph; got %+v", bad, is)
		}
	}
	if is, err := graph.ParseIssues(strings.NewReader("  \n\n")); err != nil || len(is) != 0 {
		t.Fatalf("blank input is an empty tracker: %+v %v", is, err)
	}
}

func TestParseStatusAndDepType(t *testing.T) {
	for in, want := range map[string]graph.Status{"": graph.StatusOpen, "Open": graph.StatusOpen, " in_progress": graph.StatusInProgress,
		"blocked": graph.StatusBlocked, "deferred": graph.StatusDeferred, "closed": graph.StatusClosed, "tombstone": graph.StatusClosed, "review": "review"} {
		if got := graph.ParseStatus(in); got != want {
			t.Errorf("ParseStatus(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]graph.DepType{"": graph.DepBlocks, "blocks": graph.DepBlocks, "Related": graph.DepRelated,
		"parent-child": graph.DepParentChild, "waits-for": graph.DepWaitsFor, "weird": "weird"} {
		if got := graph.ParseDepType(in); got != want {
			t.Errorf("ParseDepType(%q) = %q, want %q", in, got, want)
		}
	}
}

// "a blocks b" and "b depends on a" are one edge, however the source spells it.
func TestEdgesMergeBothDirectionsAndDropDanglingLinks(t *testing.T) {
	issues := []graph.Issue{
		{ID: "a", Blocks: []string{"b", "ghost"}},
		{ID: "b", DependsOn: []graph.Dependency{{ID: "a", Type: graph.DepBlocks}, {ID: "ghost", Type: graph.DepBlocks}}},
		{ID: "c", DependsOn: []graph.Dependency{{ID: "b", Type: graph.DepRelated}}},
	}
	got := graph.Edges(issues)
	want := []graph.Edge{{Dependent: "b", DependsOn: "a", Type: graph.DepBlocks}, {Dependent: "c", DependsOn: "b", Type: graph.DepRelated}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("edges %+v, want %+v", got, want)
	}
	// Only a Blocks link: the dependent is the issue named in it.
	only := graph.Edges([]graph.Issue{{ID: "x", Blocks: []string{"y"}}, {ID: "y"}})
	if len(only) != 1 || only[0] != (graph.Edge{Dependent: "y", DependsOn: "x", Type: graph.DepBlocks}) {
		t.Fatalf("blocks-only %+v", only)
	}
	// A cycle is two edges, not one: opposite directions are different edges.
	cyc := graph.Edges([]graph.Issue{{ID: "p", DependsOn: []graph.Dependency{{ID: "q"}}}, {ID: "q", DependsOn: []graph.Dependency{{ID: "p"}}}})
	if len(cyc) != 2 {
		t.Fatalf("cycle edges %+v", cyc)
	}
}

func TestFileSourceReadsTheExportFromTheTrackerOrAParent(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join("testdata", "chain.jsonl"))
	if err := os.WriteFile(filepath.Join(root, ".beads", "issues.jsonl"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "deep", "er")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, src := range []graph.FileSource{{Dir: root}, {Dir: sub}, {Ref: core.NewTrackerRef(sub)}} {
		issues, err := src.Issues(context.Background())
		if err != nil || len(issues) != 3 {
			t.Fatalf("%+v: %v %v", src, issues, err)
		}
	}
	// No export: a plain reason that names the way back, never an empty tracker.
	none := graph.FileSource{Dir: t.TempDir()}
	if _, err := none.Issues(context.Background()); err == nil || !strings.Contains(err.Error(), "default engine") {
		t.Fatalf("missing export: %v", err)
	}
	// A cancelled read does not touch the file.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (graph.FileSource{Dir: root}).Issues(ctx); err == nil {
		t.Fatal("a cancelled context must stop the read")
	}
}
