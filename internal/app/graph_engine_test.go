package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/cli"
	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/graph"
)

// engineEntries builds the registry with only fake tools on PATH (no real bd, bv or lambe).
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

func engineCheck(t *testing.T, entries []core.Entry) core.Check {
	t.Helper()
	for _, m := range core.DoctorReport(context.Background(), "test", entries).Modules {
		if m.ID == "graph-engine" {
			if len(m.Checks) != 1 {
				t.Fatalf("%+v", m.Checks)
			}
			return m.Checks[0]
		}
	}
	t.Fatal("Doctor has no graph engine line")
	return core.Check{}
}

// Unset, "subprocess" and any value that is not "library" keep the bv subprocess, and nothing
// about the default changes: bv stays registered and the library engine is absent.
func TestTheDefaultEngineIsTheSubprocessAndAnyOtherValueKeepsIt(t *testing.T) {
	for _, v := range []string{"", "subprocess", "libary", "bv", "0"} {
		entries := engineEntries(t, map[string]string{"ABACUS_GRAPH_ENGINE": v})
		got := ids(entries)
		if !got["bv"] || got["graph"] {
			t.Errorf("%q: modules %v, want bv and no graph engine", v, got)
		}
		if _, ok := core.ResolveProviders(entries).Insights.(*graph.Module); ok {
			t.Errorf("%q: the library module must not be the insights provider", v)
		}
		c := engineCheck(t, entries)
		switch v {
		case "libary", "bv", "0":
			if c.Status != core.StatusWarn || c.Detail != "ABACUS_GRAPH_ENGINE: unknown value, using the default" {
				t.Errorf("%q: %+v", v, c)
			}
		default:
			if c.Status != core.StatusOK || !strings.Contains(c.Detail, "subprocess") {
				t.Errorf("%q: %+v", v, c)
			}
		}
	}
}

func TestTheLibraryEngineReplacesBvAndReadsTheSharedTracker(t *testing.T) {
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
	entries := engineEntries(t, map[string]string{"ABACUS_GRAPH_ENGINE": "library", "ABACUS_BD_DIR": tracker})
	got := ids(entries)
	if got["bv"] || !got["graph"] {
		t.Fatalf("modules %v: the library engine takes bv's place", got)
	}
	p := core.ResolveProviders(entries)
	if _, ok := p.Insights.(*graph.Module); !ok {
		t.Fatalf("the insights provider is %T", p.Insights)
	}
	r := core.ReadInsights(context.Background(), p.Insights, "")
	if r.State != core.Connected || r.TotalActionable != 1 || r.TotalBlocked != 2 || len(r.TopPicks) == 0 || r.TopPicks[0].ID != "ch-1" {
		t.Fatalf("%+v", r)
	}
	if c := engineCheck(t, entries); c.Status != core.StatusOK || !strings.Contains(c.Detail, "library") {
		t.Fatalf("Doctor must say the library engine is active: %+v", c)
	}
	// An unreadable export is a plain failed read: Tasks shows insights as unavailable and
	// Doctor keeps the reason.
	if err := os.Remove(filepath.Join(tracker, ".beads", "issues.jsonl")); err != nil {
		t.Fatal(err)
	}
	if r := core.ReadInsights(context.Background(), p.Insights, ""); r.State != core.Degraded || r.Reason != core.InsightsUnavailable {
		t.Fatalf("%+v", r.Header)
	}
}

// --demo never uses the library engine, whatever the environment says.
func TestDemoIgnoresTheGraphEngine(t *testing.T) {
	reg := Build(cli.Options{Demo: true}, Env{Getenv: func(k string) string {
		if k == "ABACUS_GRAPH_ENGINE" {
			return "library"
		}
		return ""
	}})
	got := ids(reg.Detect(context.Background()))
	if got["graph"] || got["graph-engine"] || got["bv"] {
		t.Fatalf("a demo registers only demo modules: %v", got)
	}
}
