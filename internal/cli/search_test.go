package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/cli"
	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
)

// searchFleet is one module that provides every domain from fixtures, so a search has something in
// each. Nothing here runs a tool.
type searchFleet struct {
	snapMod
	leasesErr error
}

func (m searchFleet) Leases(context.Context) (core.LeasesReport, error) {
	if m.leasesErr != nil {
		return core.LeasesReport{}, m.leasesErr
	}
	return core.LeasesReport{Leases: []core.Lease{{Resource: "tracker-sync", State: core.LeaseHeld, Holder: "ada", ExpiresAt: "12m"}, {Resource: "lease-export", State: core.LeaseStale, Holder: "mei"}}}, nil
}
func (m searchFleet) Board(context.Context) (core.BoardReport, error) {
	return core.BoardReport{Threads: []core.BoardThread{{Root: core.BoardMessage{ID: "15", Channel: "dev", Author: "ada", At: "2026-11-23T04:00:00Z", Text: "lease heartbeat keeps missing"}}},
		Tasks: []core.BoardTask{{ID: "16", Channel: "work", Text: "review the heartbeat fix", State: "ready"}}}, nil
}
func (m searchFleet) Queues(context.Context) (core.QueuesReport, error) {
	return core.QueuesReport{Runs: []core.QueueRun{{Issue: "tk-tbr", State: core.RunRunning, Phase: "build", Machine: "m2"}}}, nil
}

func searchMods(leasesErr error) []core.Module {
	return []core.Module{searchFleet{snapMod: snapMod{Fake: &modtest.Fake{IDStr: "fleet", Det: core.Detection{State: core.Ready, Version: "1"}}}, leasesErr: leasesErr}}
}

func runSearch(t *testing.T, mods []core.Module, args ...string) (int, string, string) {
	t.Helper()
	var o, e bytes.Buffer
	code := cli.Run(append([]string{"search"}, args...), cli.Deps{Out: &o, Err: &e, Version: "t", Now: func() time.Time { return time.Date(2026, 11, 23, 4, 20, 0, 0, time.UTC) },
		Build: func(cli.Options) *core.Registry { return core.NewRegistry(mods...) }})
	return code, o.String(), e.String()
}

func TestSearchTextGroupsEveryDomain(t *testing.T) {
	code, out, errOut := runSearch(t, searchMods(nil), "heartbeat")
	if code != 0 || errOut != "" {
		t.Fatalf("code %d err %q\n%s", code, errOut, out)
	}
	for _, want := range []string{"Tasks (1)", "tk-tbr", "Lease heartbeat bug", "Board (2)", "#15", "#16"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if _, q, _ := runSearch(t, searchMods(nil), "build"); !strings.Contains(q, "Queues (1)") || !strings.Contains(q, "tk-tbr") {
		t.Errorf("queues:\n%s", q)
	}
	// the query may be given unquoted, as several words
	if _, out2, _ := runSearch(t, searchMods(nil), "lease", "heartbeat", "in:board"); !strings.Contains(out2, "Board (1)") || strings.Contains(out2, "Tasks") {
		t.Errorf("unquoted words and in::\n%s", out2)
	}
}

func TestSearchFlagsDomainLimitAndPartialDomains(t *testing.T) {
	_, out, _ := runSearch(t, searchMods(nil), "lease", "--domain", "leases,board")
	if strings.Contains(out, "Tasks (") || !strings.Contains(out, "Leases (1)") || !strings.Contains(out, "Board (1)") {
		t.Errorf("--domain:\n%s", out)
	}
	_, out, _ = runSearch(t, searchMods(nil), "--domain", "leases", "--domain", "tasks", "--limit", "1", "e")
	if !strings.Contains(out, "Leases (2)") || !strings.Contains(out, "+1 more") {
		t.Errorf("--limit and a repeated --domain:\n%s", out)
	}
	// one domain that cannot be read is named, not a failure
	code, out, _ := runSearch(t, searchMods(&core.NotConnectedError{Reason: "no hub", HowToConnect: "install the thing"}), "heartbeat")
	if code != 0 || !strings.Contains(out, "Tasks (1)") || !strings.Contains(out, "Leases: not connected") || strings.Contains(out, "install the thing") {
		t.Errorf("a missing domain is named, with no install instruction (code %d):\n%s", code, out)
	}
}

func TestSearchExitCodesAndSilentStdoutOnFailure(t *testing.T) {
	for _, c := range []struct {
		name string
		mods []core.Module
		args []string
		code int
		sub  string
	}{
		{"no query", searchMods(nil), nil, core.ExitUsage, "needs a query"},
		{"blank query", searchMods(nil), []string{"   "}, core.ExitUsage, "needs a query"},
		{"bad query", searchMods(nil), []string{"nosuch:x"}, core.ExitUsage, `unknown field "nosuch" (column 1)`},
		{"too long", searchMods(nil), []string{strings.Repeat("a", 300)}, core.ExitUsage, "longer than 256"},
		{"bad domain", searchMods(nil), []string{"x", "--domain", "tools"}, core.ExitUsage, "unknown domain"},
		{"limit zero", searchMods(nil), []string{"x", "--limit", "0"}, core.ExitUsage, "--limit"},
		{"limit huge", searchMods(nil), []string{"x", "--limit", "9999"}, core.ExitUsage, "--limit"},
		{"unknown flag", searchMods(nil), []string{"x", "--nope"}, core.ExitUsage, ""},
		{"nothing connected", nil, []string{"x"}, core.ExitFail, "no domain could be searched"},
	} {
		for _, js := range []bool{false, true} {
			args := c.args
			if js {
				args = append(append([]string(nil), c.args...), "--json")
			}
			code, out, errOut := runSearch(t, c.mods, args...)
			if code != c.code || !strings.Contains(errOut, c.sub) {
				t.Errorf("%s json=%v: code %d err %q, want %d %q", c.name, js, code, errOut, c.code, c.sub)
			}
			if js && out != "" {
				t.Errorf("%s: stdout must be empty on failure under --json, got %q", c.name, out)
			}
		}
	}
	// no match is an answer, not a failure
	if code, out, _ := runSearch(t, searchMods(nil), "zzzzzzzz"); code != 0 || !strings.Contains(out, "no matches") {
		t.Errorf("no match: %d %q", code, out)
	}
	// a dash starts a negation after --
	if code, out, _ := runSearch(t, searchMods(nil), "--", "heartbeat", "-in:tasks"); code != 0 || strings.Contains(out, "Tasks (") {
		t.Errorf("-- terminator: %d\n%s", code, out)
	}
}

// The JSON is a contract: schema_version 1, additive keys only.
func TestSearchJSONContract(t *testing.T) {
	code, out, _ := runSearch(t, searchMods(nil), "heartbeat", "--json")
	if code != 0 {
		t.Fatal(code)
	}
	checkSearchGolden(t, "search.golden.json", out)
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &m); err != nil || string(m["schema_version"]) != "1" {
		t.Fatalf("schema_version: %v %s", err, m["schema_version"])
	}
	for _, k := range []string{"query", "total", "groups", "domains"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing %q", k)
		}
	}
	// the JSON carries what the text shows
	_, text, _ := runSearch(t, searchMods(nil), "heartbeat")
	var rep core.SearchReport
	_ = json.Unmarshal([]byte(out), &rep)
	for _, g := range rep.Groups {
		for _, r := range g.Results {
			if !strings.Contains(text, r.Label) {
				t.Errorf("text lacks %s", r.Label)
			}
		}
	}
}

func checkSearchGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (go test ./internal/cli -update-golden creates it)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs (-update-golden accepts):\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}
