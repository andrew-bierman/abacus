package graph_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	// Without a tracker directory the lookup walks up from the directory, as the tracker does.
	for _, src := range []graph.FileSource{{Dir: root}, {Dir: sub}, {Ref: core.NewTrackerRef(root)}} {
		issues, err := src.Issues(context.Background())
		if err != nil || len(issues) != 3 {
			t.Fatalf("%+v: %v %v", src, issues, err)
		}
	}
	// No export: a plain reason, never an empty tracker.
	none := graph.FileSource{Dir: t.TempDir()}
	if _, err := none.Issues(context.Background()); err == nil || !strings.Contains(err.Error(), "issues.jsonl") {
		t.Fatalf("missing export: %v", err)
	}
	// A cancelled read does not touch the file.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (graph.FileSource{Dir: root}).Issues(ctx); err == nil {
		t.Fatal("a cancelled context must stop the read")
	}
}

// writeExport makes dir/.beads/issues.jsonl with the chain fixture.
func writeExport(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join("testdata", "chain.jsonl"))
	p := filepath.Join(dir, ".beads", "issues.jsonl")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func touch(t *testing.T, p string, mod time.Time) {
	t.Helper()
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
}

// With a tracker directory set the lookup stays inside it: an export in a parent belongs to
// another project and is never read.
func TestFileSourceDoesNotWalkAboveTheTrackerDir(t *testing.T) {
	root := t.TempDir()
	writeExport(t, root)
	sub := filepath.Join(root, "child")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	src := graph.FileSource{Ref: core.NewTrackerRef(sub)}
	if p := src.Path(); p != "" {
		t.Fatalf("Path walked above the tracker dir: %q", p)
	}
	if _, err := src.Issues(context.Background()); err == nil || !strings.Contains(err.Error(), "issues.jsonl") {
		t.Fatalf("a parent's export must not be read: %v", err)
	}
	// The same directory through Dir (no tracker set) still walks up.
	if is, err := (graph.FileSource{Dir: sub}).Issues(context.Background()); err != nil || len(is) != 3 {
		t.Fatalf("Dir walks up: %v %v", is, err)
	}
	// A Ref with no directory means the working directory, and that walks up like Dir.
	t.Chdir(sub)
	if is, err := (graph.FileSource{Ref: core.NewTrackerRef("")}).Issues(context.Background()); err != nil || len(is) != 3 {
		t.Fatalf("empty Ref walks up from the working directory: %v %v", is, err)
	}
	// A tracker directory holding its own export reads it.
	p := writeExport(t, sub)
	if got := (graph.FileSource{Ref: core.NewTrackerRef(sub)}).Path(); got != p {
		t.Fatalf("Path = %q, want %q", got, p)
	}
}

// An export, or the .beads directory, that is a symlink leaving the tracker directory is
// refused: a planted link must not make abacus read another project's file.
func TestFileSourceRefusesASymlinkLeavingTheTrackerDir(t *testing.T) {
	outside := t.TempDir()
	outExport := writeExport(t, outside)

	check := func(name string, src graph.FileSource) {
		t.Helper()
		if p := src.Path(); p != "" {
			t.Errorf("%s: Path = %q, want none", name, p)
		}
		_, err := src.Issues(context.Background())
		if err == nil || !strings.Contains(err.Error(), "outside the tracker directory") {
			t.Errorf("%s: %v", name, err)
		}
	}

	fileLink := t.TempDir()
	if err := os.MkdirAll(filepath.Join(fileLink, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outExport, filepath.Join(fileLink, ".beads", "issues.jsonl")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	check("issues.jsonl link", graph.FileSource{Ref: core.NewTrackerRef(fileLink)})
	check("issues.jsonl link via Dir", graph.FileSource{Dir: fileLink})

	dirLink := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, ".beads"), filepath.Join(dirLink, ".beads")); err != nil {
		t.Fatal(err)
	}
	check(".beads link", graph.FileSource{Ref: core.NewTrackerRef(dirLink)})

	// A link that stays inside the tracker directory is fine, and so is a tracker directory
	// that is itself reached through a link.
	inside := t.TempDir()
	real := writeExport(t, inside)
	alias := filepath.Join(inside, ".beads", "alias.jsonl")
	if err := os.Rename(real, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(alias, real); err != nil {
		t.Fatal(err)
	}
	top := filepath.Join(t.TempDir(), "via-link")
	if err := os.Symlink(inside, top); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{inside, top} {
		if is, err := (graph.FileSource{Ref: core.NewTrackerRef(d)}).Issues(context.Background()); err != nil || len(is) != 3 {
			t.Errorf("%s: %v %v", d, is, err)
		}
	}
}

func TestFileSourceFreshnessComparesTheExportToTheTrackerData(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	dir := t.TempDir()
	exp := writeExport(t, dir)
	touch(t, exp, now.Add(-10*time.Minute))
	src := graph.FileSource{Ref: core.NewTrackerRef(dir)}

	f, ok := src.Freshness()
	if !ok || !f.Export.Equal(now.Add(-10*time.Minute)) || !f.Data.IsZero() || f.Stale() {
		t.Fatalf("no tracker data: %+v %v", f, ok)
	}

	// A database newer than the export: stale, by the difference.
	db := filepath.Join(dir, ".beads", "beads.db")
	if err := os.WriteFile(db, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, db, now.Add(-4*time.Minute))
	f, _ = src.Freshness()
	if !f.Stale() || f.Behind() != 6*time.Minute || !f.Data.Equal(now.Add(-4*time.Minute)) {
		t.Fatalf("db newer: %+v behind %v", f, f.Behind())
	}

	// Within the slack the export is current (it is written moments after the data).
	touch(t, db, now.Add(-10*time.Minute).Add(time.Second))
	if f, _ = src.Freshness(); f.Stale() {
		t.Fatalf("a one second lag is not stale: %+v", f)
	}
	// An older database: not stale.
	touch(t, db, now.Add(-30*time.Minute))
	if f, _ = src.Freshness(); f.Stale() || f.Behind() != 0 {
		t.Fatalf("old db: %+v", f)
	}

	// So does a write-ahead log beside the database.
	wal := filepath.Join(dir, ".beads", "beads.db-wal")
	if err := os.WriteFile(wal, []byte("w"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, wal, now.Add(-2*time.Minute))
	if f, _ = src.Freshness(); f.Behind() != 8*time.Minute {
		t.Fatalf("wal newer: %+v behind %v", f, f.Behind())
	}
	touch(t, wal, now.Add(-30*time.Minute))

	// A dolt directory counts too, by the newest file inside it.
	dolt := filepath.Join(dir, ".beads", "dolt", "noms")
	if err := os.MkdirAll(dolt, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dolt, "manifest")
	if err := os.WriteFile(manifest, []byte("m"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, manifest, now.Add(-1*time.Minute))
	touch(t, dolt, now.Add(-2*time.Hour))
	touch(t, filepath.Join(dir, ".beads", "dolt"), now.Add(-2*time.Hour))
	f, _ = src.Freshness()
	if !f.Stale() || f.Behind() != 9*time.Minute {
		t.Fatalf("dolt newer: %+v behind %v", f, f.Behind())
	}

	// No export: nothing to report.
	if _, ok := (graph.FileSource{Dir: t.TempDir()}).Freshness(); ok {
		t.Fatal("no export, no freshness")
	}
}

func TestOversizedExportIsAClearError(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, ".beads", "issues.jsonl")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	line := `{"id":"a","title":"` + strings.Repeat("x", 1<<20-32) + `"}` + "\n"
	for written := 0; written <= 64<<20; written += len(line) {
		if _, err := f.WriteString(line); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	check := func(name string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "export larger than 64 MiB") || strings.Contains(err.Error(), "unreadable") {
			t.Errorf("%s: %v", name, err)
		}
	}
	_, err = (graph.FileSource{Ref: core.NewTrackerRef(dir)}).Issues(context.Background())
	check("file", err)

	// The stream path (no size known up front) says the same, for JSONL and for a JSON array.
	rf, _ := os.Open(p)
	defer rf.Close()
	_, err = graph.ParseIssues(rf)
	check("jsonl stream", err)
	arr := strings.NewReader("[" + strings.Repeat(`{"id":"a","title":"`+strings.Repeat("x", 1<<20-32)+`"},`, 66) + `{"id":"z"}]`)
	_, err = graph.ParseIssues(arr)
	check("array stream", err)
}

// A default tracker keeps no .beads/issues.jsonl, so the source falls back to the tracker read it
// is given (the bd export); a file, when there is one, wins and the fallback is not called.
func TestFileSourceFallsBackToTheTrackerReadOnlyWithoutAnExportFile(t *testing.T) {
	const jsonl = `{"id":"a","title":"A","status":"open"}` + "\n" + `{"id":"b","title":"B","status":"open","dependencies":[{"depends_on_id":"a","type":"blocks"}]}` + "\n"
	calls := 0
	src := func(dir string, fallback func(context.Context) ([]byte, error)) graph.FileSource {
		return graph.FileSource{Ref: core.NewTrackerRef(dir), Fallback: fallback}
	}
	live := func(context.Context) ([]byte, error) { calls++; return []byte(jsonl), nil }

	issues, err := src(t.TempDir(), live).Issues(context.Background())
	if err != nil || len(issues) != 2 || issues[1].DependsOn[0].ID != "a" || calls != 1 {
		t.Fatalf("%+v %v calls=%d", issues, err, calls)
	}
	if _, ok := src(t.TempDir(), live).Freshness(); ok {
		t.Error("a live read has no export file to be stale")
	}

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".beads", "issues.jsonl"), []byte(`{"id":"only","title":"file","status":"open"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls = 0
	if issues, err := src(dir, live).Issues(context.Background()); err != nil || len(issues) != 1 || issues[0].ID != "only" || calls != 0 {
		t.Fatalf("the export file wins: %+v %v calls=%d", issues, err, calls)
	}

	failing := func(context.Context) ([]byte, error) { return nil, errors.New("bd export exited 1") }
	if _, err := src(t.TempDir(), failing).Issues(context.Background()); err == nil || !strings.Contains(err.Error(), "exited 1") {
		t.Fatalf("a failing fallback is an error: %v", err)
	}
	if _, err := src(t.TempDir(), nil).Issues(context.Background()); err == nil || !strings.Contains(err.Error(), "issues.jsonl") {
		t.Fatalf("no file and no fallback is the plain no-export error: %v", err)
	}
	garbled := func(context.Context) ([]byte, error) { return []byte("not json"), nil }
	if _, err := src(t.TempDir(), garbled).Issues(context.Background()); err == nil {
		t.Fatal("an unreadable fallback is an error, never a partial graph")
	}
}

func TestParseIssuesSkipsRecordsThatAreNotIssues(t *testing.T) {
	got := parse(t, `{"_type":"issue","id":"a","title":"A","status":"open"}
{"_type":"memory","id":"m-1","title":"a note","status":"open"}
`)
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("%+v", got)
	}
}

func TestFallbackOverTheCapIsTheExistingTooLargeErrorAndEmptyOutputIsEmpty(t *testing.T) {
	src := func(f func(context.Context) ([]byte, error)) graph.FileSource {
		return graph.FileSource{Ref: core.NewTrackerRef(t.TempDir()), Fallback: f}
	}
	big := func(context.Context) ([]byte, error) { return nil, fmt.Errorf("bd export: %w", core.ErrOutputTooLarge) }
	if _, err := src(big).Issues(context.Background()); err == nil || !strings.Contains(err.Error(), "export larger than 64 MiB") {
		t.Fatalf("%v", err)
	}
	over := func(context.Context) ([]byte, error) { return make([]byte, 64<<20+1), nil }
	if _, err := src(over).Issues(context.Background()); err == nil || !strings.Contains(err.Error(), "export larger than 64 MiB") {
		t.Fatalf("%v", err)
	}
	empty := func(context.Context) ([]byte, error) { return nil, nil }
	m := graph.NewModule(graph.Options{Source: src(empty), Analyzer: graph.NewBVAnalyzer()})
	r, err := m.Insights(context.Background())
	if err != nil || !r.Empty {
		t.Fatalf("empty stdout is the empty report: %+v %v", r, err)
	}
}

// The real `bd --readonly export` output (testdata/bd/README.md) goes through the fallback and the
// parser: the shape the decoder assumes is the shape bd writes.
func TestTheRealBdExportDecodes(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "bd", "export-real.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	src := graph.FileSource{Ref: core.NewTrackerRef(t.TempDir()), Fallback: func(context.Context) ([]byte, error) { return raw, nil }}
	issues, err := src.Issues(context.Background())
	if err != nil || len(issues) != 4 {
		t.Fatalf("%d issues, %v", len(issues), err)
	}
	by := map[string]graph.Issue{}
	for _, i := range issues {
		by[i.Title] = i
	}
	design, build, docs, ship := by["Design the schema"], by["Build the importer"], by["Write the docs"], by["Ship the first cut"]
	if ship.Status != graph.StatusClosed || ship.Priority != 0 || design.Status != graph.StatusOpen || design.Type != "task" || design.UpdatedAt.IsZero() {
		t.Fatalf("%+v %+v", ship, design)
	}
	if len(build.DependsOn) != 1 || build.DependsOn[0] != (graph.Dependency{ID: design.ID, Type: graph.DepBlocks}) ||
		len(docs.DependsOn) != 1 || docs.DependsOn[0].ID != build.ID || len(design.DependsOn) != 0 {
		t.Fatalf("dependencies: %+v %+v %+v", design.DependsOn, build.DependsOn, docs.DependsOn)
	}
	if fmt.Sprint(design.Labels) != "[backend]" || fmt.Sprint(docs.Labels) != "[docs]" || len(build.Labels) != 0 {
		t.Fatalf("labels: %v %v %v", design.Labels, docs.Labels, build.Labels)
	}
	r, err := graph.NewBVAnalyzer().Analyze(context.Background(), issues)
	if err != nil || r.TotalActionable != 1 || r.TotalBlocked != 2 || len(r.TopPicks) == 0 || r.TopPicks[0].ID != design.ID {
		t.Fatalf("%+v %v", r, err)
	}
}
