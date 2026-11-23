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
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// snapMod is a module that provides the tracker snapshot from the captured real export.
type snapMod struct {
	*modtest.Fake
	err error
}

func (m snapMod) Snapshot(context.Context) (core.TaskSnapshot, error) {
	if m.err != nil {
		return core.TaskSnapshot{}, m.err
	}
	b, err := os.ReadFile(filepath.Join("..", "taskview", "testdata", "export.jsonl"))
	if err != nil {
		return core.TaskSnapshot{}, err
	}
	return taskview.Parse(bytes.NewReader(b), time.Date(2026, 11, 23, 4, 20, 0, 0, time.UTC), "tracker")
}

func snap() []core.Module {
	return []core.Module{snapMod{Fake: &modtest.Fake{IDStr: "bd", Det: core.Detection{State: core.Ready, Version: "1"}}}}
}

func runTasks(t *testing.T, mods []core.Module, args ...string) (int, string, string) {
	t.Helper()
	var o, e bytes.Buffer
	code := cli.Run(args, cli.Deps{Out: &o, Err: &e, Version: "t", Now: func() time.Time { return time.Date(2026, 11, 23, 4, 20, 0, 0, time.UTC) },
		Build: func(cli.Options) *core.Registry { return core.NewRegistry(mods...) }})
	return code, o.String(), e.String()
}

func TestTasksListTextAndFilters(t *testing.T) {
	code, out, errOut := runTasks(t, snap(), "tasks", "list")
	if code != 0 || errOut != "" {
		t.Fatalf("code %d err %q", code, errOut)
	}
	if !strings.Contains(out, "8 of 9  (open, sort: priority ↑)") || !strings.Contains(out, "Lease heartbeat bug") || strings.Contains(out, "Snapshot reader") {
		t.Errorf("default list is the open tasks by priority:\n%s", out)
	}
	if first := strings.Split(out, "\n")[1]; !strings.HasPrefix(first, "tk-tbr") {
		t.Errorf("P0 first: %q", first)
	}
	_, out, _ = runTasks(t, snap(), "tasks", "list", "--preset", "all", "-q", "label:ui !status:open", "--sort", "title")
	if !strings.Contains(out, "1 of 9") || !strings.Contains(out, "Snapshot reader") || !strings.Contains(out, "query: label:ui !status:open") {
		t.Errorf("query and preset:\n%s", out)
	}
	_, out, _ = runTasks(t, snap(), "tasks", "list", "--preset", "ready", "--sort", "title", "--reverse")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if !strings.Contains(lines[0], "sort: title ↓") || !strings.HasPrefix(lines[1], "tk-0at ") || !strings.HasPrefix(lines[len(lines)-1], "tk-1rz") {
		t.Errorf("reverse title:\n%s", out)
	}
}

func TestTasksListUsageErrors(t *testing.T) {
	for _, c := range []struct {
		args []string
		sub  string
	}{
		{[]string{"-q", "nosuch:x"}, `unknown field "nosuch" (column 1)`},
		{[]string{"-q", "status:open priority:9"}, "column 22"},
		{[]string{"--sort", "color"}, "unknown sort"},
		{[]string{"--preset", "soon"}, "unknown preset"},
		{[]string{"extra"}, "unexpected argument"},
	} {
		code, out, errOut := runTasks(t, snap(), append([]string{"tasks", "list"}, c.args...)...)
		if code != core.ExitUsage || out != "" || !strings.Contains(errOut, c.sub) {
			t.Errorf("%v: code %d out %q err %q", c.args, code, out, errOut)
		}
	}
}

func TestTasksShow(t *testing.T) {
	code, out, _ := runTasks(t, snap(), "tasks", "show", "tk-0at.2")
	if code != 0 {
		t.Fatal(code)
	}
	for _, want := range []string{"tk-0at.2  feature", "List view", "labels ui", "parent tk-0at Tasks workspace", "Blocked by (1)", "tk-0at.1", "Snapshot reader", "Blocks (1)", "Render list.", "Comments (2)", "Agreed, after tk-0at.1"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, c := range []struct {
		args []string
		code int
		sub  string
	}{
		{[]string{"tasks", "show"}, core.ExitUsage, "needs a task id"},
		{[]string{"tasks", "show", "--json"}, core.ExitUsage, "needs a task id"},
		{[]string{"tasks", "show", "tk-nope"}, core.ExitFail, `no task "tk-nope"`},
		{[]string{"tasks", "show", "tk-0at", "extra"}, core.ExitUsage, "unexpected argument"},
	} {
		code, _, errOut := runTasks(t, snap(), c.args...)
		if code != c.code || !strings.Contains(errOut, c.sub) {
			t.Errorf("%v: code %d err %q", c.args, code, errOut)
		}
	}
}

func TestTasksViewNotConnectedAndReadFailure(t *testing.T) {
	for _, verb := range []string{"list", "show tk-1"} {
		code, out, errOut := runTasks(t, nil, append([]string{"tasks"}, strings.Fields(verb)...)...)
		if code != core.ExitFail || out != "" || !strings.Contains(errOut, "not connected") {
			t.Errorf("%s: %d %q %q", verb, code, out, errOut)
		}
	}
	bad := []core.Module{snapMod{Fake: &modtest.Fake{IDStr: "bd", Det: core.Detection{State: core.Ready, Version: "1"}}, err: &core.NotConnectedError{Reason: "no tracker", HowToConnect: "set a directory"}}}
	if code, _, errOut := runTasks(t, bad, "tasks", "list"); code != core.ExitFail || !strings.Contains(errOut, "no tracker") {
		t.Errorf("%d %q", code, errOut)
	}
}

// The JSON of both commands is a contract: schema_version 1 first-class, additive keys only.
func TestTasksViewJSONContract(t *testing.T) {
	_, out, _ := runTasks(t, snap(), "tasks", "list", "--json", "--preset", "all")
	checkGoldenFile(t, "tasks-list.golden.json", out)
	_, out2, _ := runTasks(t, snap(), "tasks", "show", "tk-0at.2", "--json")
	checkGoldenFile(t, "tasks-show.golden.json", out2)
	for _, js := range []string{out, out2} {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(js), &m); err != nil || string(m["schema_version"]) != "1" {
			t.Errorf("schema_version: %v %s", err, m["schema_version"])
		}
	}
}

func checkGoldenFile(t *testing.T, name, got string) {
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
