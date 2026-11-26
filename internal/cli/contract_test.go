package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/cli"
	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/demo"
	"github.com/andrew-bierman/abacus/internal/lambe"
)

// The contract goldens: the exact --json bytes of every command a client reads or writes through,
// produced from the demo providers (invented data, nothing contacted) at a fixed clock. A client
// vendors testdata/contract and decodes it; a change to any key, type or ordering fails here first.
// `go test ./internal/cli -update-golden` rewrites them (and refuses a document that is not a
// schema_version 1 object).

var contractNow = time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)

// fixedAgents is the demo Agents feed at a fixed moment (the demo's own ages are relative to now).
type fixedAgents struct{ demo.AgentsFeed }

func (fixedAgents) Agents(context.Context) ([]core.Agent, core.AgentsExtra, error) {
	return demo.AgentsAt(contractNow)
}

// fixedTasks is the demo Tasks provider whose history is read at the fixed moment too.
type fixedTasks struct{ demoTasks } // not embedded as demo.Tasks: its field name would hide the Tasks method

type demoTasks = demo.Tasks

func (fixedTasks) History(_ context.Context, id string, limit int) (core.TaskHistoryReport, error) {
	return demo.TaskHistoryAt(contractNow, id, limit)
}

func contractMods() []core.Module {
	return []core.Module{lambe.NewDemo(), demo.NewLeases(), fixedTasks{demo.NewTasks()}, newBoardMod(), fixedAgents{demo.NewAgents()}}
}

// contractRun runs one command over the demo providers with isolated config, audit and clock.
func contractRun(t *testing.T, mods []core.Module, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	code = cli.Run(args, cli.Deps{Out: &o, Err: &e, Version: "0.0.0-contract", Audit: &memAudit{},
		Config: core.ConfigStore{Path: filepath.Join(string(filepath.Separator), "contract", "abacus", "config.json")},
		Now:    func() time.Time { return contractNow },
		Build:  func(cli.Options) *core.Registry { return core.NewRegistry(mods...) }})
	return code, o.String(), e.String()
}

// checkContract compares out with testdata/contract/NAME.golden.json (or rewrites it with -update-golden).
func checkContract(t *testing.T, name, out string) {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &m); err != nil || string(m["schema_version"]) != "1" {
		t.Fatalf("%s: not a schema_version 1 object (%v):\n%s", name, err, out)
	}
	usedMu.Lock()
	usedGolden[name] = true
	usedMu.Unlock()
	path := filepath.Join("testdata", "contract", name+".golden.json")
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (go test ./internal/cli -update-golden creates it)", err)
	}
	if out != string(want) {
		t.Errorf("%s differs from its golden (-update-golden accepts):\n--- want\n%s\n--- got\n%s", name, want, out)
	}
}

func TestContractReadGoldens(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
	}{
		{"version", []string{"version", "--json"}},
		{"doctor", []string{"doctor", "--json"}},
		{"doctor-demo", []string{"doctor", "--demo", "--json"}},
		{"status", []string{"status", "--json"}},
		{"board", []string{"board", "--json"}},
		{"tasks", []string{"tasks", "--json"}},
		{"leases", []string{"leases", "--json"}},
		{"queues", []string{"queues", "--json"}},
		{"agents", []string{"agents", "--json"}},
		{"search", []string{"search", "lease", "--json"}},
		{"tasks-history", []string{"tasks", "history", "ac-12", "--json"}},
		{"config-show", []string{"config", "show", "--json"}},
	} {
		code, out, errOut := contractRun(t, contractMods(), c.args...)
		if code != 0 || errOut != "" {
			t.Fatalf("%s: exit %d, stderr %q", c.name, code, errOut)
		}
		checkContract(t, c.name, out)
	}
}

func TestContractSetupGoldens(t *testing.T) {
	r := runSetupCmd(t, "", "setup", "--json")
	checkContract(t, "setup", r.out)
	r = runSetupCmd(t, "", "setup", "--yes", "--json")
	checkContract(t, "setup-applied", r.out)
}

func TestContractWriteGoldens(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
	}{
		{"dry-run-create", []string{"tasks", "create", "--title", "Add a pear counter", "--priority", "2", "--type", "feature", "--dry-run", "--json"}},
		{"dry-run-update", []string{"tasks", "update", "ac-12", "--status", "blocked", "--dry-run", "--json"}},
		{"dry-run-close", []string{"tasks", "close", "ac-12", "--reason", "done", "--dry-run", "--json"}},
		{"dry-run-reopen", []string{"tasks", "reopen", "ac-03", "--reason", "regressed", "--dry-run", "--json"}},
		{"dry-run-label", []string{"tasks", "label", "add", "ac-12", "ui", "--dry-run", "--json"}},
		{"dry-run-assign", []string{"tasks", "assign", "ac-12", "mei", "--dry-run", "--json"}},
		{"dry-run-priority", []string{"tasks", "priority", "ac-12", "3", "--dry-run", "--json"}},
		{"dry-run-dep", []string{"tasks", "dep", "add", "ac-15", "--blocked-by", "ac-30", "--dry-run", "--json"}},
		{"dry-run-comment", []string{"tasks", "comment", "ac-12", "Reproduced on the laptop.", "--dry-run", "--json"}},
		{"dry-run-bulk", []string{"tasks", "bulk", "--ids", "ac-12,ac-15,ac-30", "--op", "label-add", "--value", "sprint", "--dry-run", "--json"}},
		{"dry-run-board-post", []string{"board", "post", "dev", "deploys are green again", "--dry-run", "--json"}},
		{"dry-run-board-reply", []string{"board", "reply", "15", "rolled back", "--dry-run", "--json"}},
		{"applied-update", []string{"tasks", "update", "ac-12", "--priority", "1", "--yes", "--json"}},
		{"applied-bulk", []string{"tasks", "bulk", "--ids", "ac-12,ac-15", "--op", "label-add", "--value", "sprint", "--yes", "--json"}},
		{"applied-board-post", []string{"board", "post", "dev", "deploys are green again", "--yes", "--json"}},
	} {
		restore := core.SetBoardClockForTest(func() time.Time { return contractNow })
		code, out, errOut := contractRun(t, contractMods(), c.args...)
		restore()
		if code != 0 || errOut != "" {
			t.Fatalf("%s: exit %d, stderr %q, stdout %s", c.name, code, errOut, out)
		}
		checkContract(t, c.name, batchID.ReplaceAllString(out, "batch-000000000000"))
	}
}

// batch_id is a random per-run value, opaque to a client; the golden pins its shape, not its digits.
var batchID = regexp.MustCompile(`batch-[0-9a-f]{12}`)
