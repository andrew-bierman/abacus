package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/andrew-bierman/abacus/internal/cli"
	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/graph"
)

// engineEntries builds the registry with only fake tools on PATH (no real bd or lambe).
func engineEntries(t *testing.T, vars map[string]string) []core.Entry {
	t.Helper()
	bin := fakeBinDir(t, "x")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin:/bin")
	old := core.SidecarDirs
	core.SidecarDirs = func() []string { return nil }
	t.Cleanup(func() { core.SidecarDirs = old })
	return Build(cli.Options{}, env(t, vars)).Detect(context.Background())
}

func ids(entries []core.Entry) map[string]bool {
	out := map[string]bool{}
	for _, e := range entries {
		out[e.Module.ID()] = true
	}
	return out
}

// The in-process engine is the only graph-insights path: it is always registered, no bv tool
// and no engine choice exist, whatever the environment holds (the old flag and bv override are
// ignored), and the shared tracker is what it reads.
func TestTheLibraryEngineIsTheOnlyInsightsProviderAndReadsTheSharedTracker(t *testing.T) {
	tracker := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tracker, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("..", "graph", "testdata", "chain.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tracker, ".beads", "issues.jsonl"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"", "subprocess", "library", "bv"} {
		entries := engineEntries(t, map[string]string{"ABACUS_GRAPH_ENGINE": flag, "ABACUS_BV_BIN": "/nonexistent/bv", "ABACUS_BD_DIR": tracker})
		got := ids(entries)
		if got["bv"] || got["graph-engine"] || !got["graph"] {
			t.Fatalf("%q: modules %v, want the graph engine and neither bv nor an engine check", flag, got)
		}
		p := core.ResolveProviders(entries)
		if _, ok := p.Insights.(*graph.Module); !ok {
			t.Fatalf("%q: the insights provider is %T", flag, p.Insights)
		}
		r := core.ReadInsights(context.Background(), p.Insights, "")
		if r.State != core.Connected || r.TotalActionable != 1 || r.TotalBlocked != 2 || len(r.TopPicks) == 0 || r.TopPicks[0].ID != "ch-1" {
			t.Fatalf("%q: %+v", flag, r)
		}
	}
	// An unreadable export is a plain failed read: Tasks shows insights as unavailable and
	// Doctor keeps the reason.
	entries := engineEntries(t, map[string]string{"ABACUS_BD_DIR": tracker})
	p := core.ResolveProviders(entries)
	if err := os.Remove(filepath.Join(tracker, ".beads", "issues.jsonl")); err != nil {
		t.Fatal(err)
	}
	if r := core.ReadInsights(context.Background(), p.Insights, ""); r.State != core.Degraded || r.Reason != core.InsightsUnavailable {
		t.Fatalf("%+v", r.Header)
	}
}

// --demo never uses the library engine, whatever the environment says.
func TestDemoRegistersNoGraphEngine(t *testing.T) {
	reg := Build(cli.Options{Demo: true}, Env{Getenv: func(string) string { return "" }})
	got := ids(reg.Detect(context.Background()))
	if got["graph"] || got["bv"] {
		t.Fatalf("a demo registers only demo modules: %v", got)
	}
}
