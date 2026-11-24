package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/demo"
)

func runAgentsCmd(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var o, e bytes.Buffer
	code := Run(append([]string{"agents"}, args...), Deps{Out: &o, Err: &e, Version: "t",
		Build: func(Options) *core.Registry { return core.NewRegistry(demo.NewAgents()) }})
	return code, o.String(), e.String()
}

func TestAgentsPlainReportMarksGuessesAndFilters(t *testing.T) {
	agentsNow = func() time.Time { return time.Now() }
	code, out, _ := runAgentsCmd(t, "--state", "running")
	if code != 0 || !strings.Contains(out, "Agents  connected") || !strings.Contains(out, "orchard@feat/pear-counter") || strings.Contains(out, "mill@main") {
		t.Fatalf("%d\n%s", code, out)
	}
	_, all, _ := runAgentsCmd(t)
	for _, want := range []string{"idle    ~", "ctx 72%", "(monitor)", "limits Codex  5h 88%", "orphan port 4000"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in\n%s", want, all)
		}
	}
	if _, o, _ := runAgentsCmd(t, "--harness", "codex", "wheat"); !strings.Contains(o, "Package the wheat module") || strings.Contains(o, "pruning") {
		t.Fatalf("%s", o)
	}
	if code, _, e := runAgentsCmd(t, "--state", "bogus"); code != core.ExitUsage || !strings.Contains(e, "unknown state") {
		t.Fatalf("%d %s", code, e)
	}
}

func TestAgentsJSONIsTheTypedVersionedReport(t *testing.T) {
	code, out, _ := runAgentsCmd(t, "--json")
	var r struct {
		SchemaVersion int `json:"schema_version"`
		Domain        string
		State         string
		Agents        []struct {
			ID, Harness, State, Source string
			ContextPct                 *float64 `json:"context_pct"`
			Tokens                     *int64   `json:"tokens"`
		}
		RateLimits []struct{ Harness string } `json:"rate_limits"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil || code != 0 {
		t.Fatalf("%v\n%s", err, out)
	}
	if r.SchemaVersion != 1 || r.Domain != "agents" || r.State != "connected" || len(r.Agents) != 6 || len(r.RateLimits) != 2 {
		t.Fatalf("%+v", r)
	}
	for _, a := range r.Agents {
		if a.Harness == "opencode" && (a.ContextPct != nil || a.Tokens != nil || a.Source != "process") {
			t.Fatalf("an unknown value was written as a value: %+v", a)
		}
	}
}

func TestAgentsNotConnectedWithNoFeeds(t *testing.T) {
	var o bytes.Buffer
	code := Run([]string{"agents"}, Deps{Out: &o, Err: &bytes.Buffer{}, Version: "t", Build: func(Options) *core.Registry { return core.NewRegistry() }})
	if code != 0 || !strings.Contains(o.String(), "not connected") {
		t.Fatalf("%d %s", code, o.String())
	}
}
