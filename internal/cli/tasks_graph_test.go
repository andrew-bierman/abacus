package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// graphMod provides the snapshot of the third captured export (diamond, chain, fan-out) with the
// hand-added cycle, self-dependency and missing target.
type graphMod struct{ *modtest.Fake }

func (graphMod) Snapshot(context.Context) (core.TaskSnapshot, error) {
	b, err := os.ReadFile(filepath.Join("..", "taskview", "testdata", "graph-export-edges.jsonl"))
	if err != nil {
		return core.TaskSnapshot{}, err
	}
	return taskview.Parse(bytes.NewReader(b), time.Date(2026, 11, 23, 4, 20, 0, 0, time.UTC), "gr")
}

func graphMods() []core.Module {
	return []core.Module{graphMod{&modtest.Fake{IDStr: "bd", Det: core.Detection{State: core.Ready, Version: "1"}}}}
}

func TestTasksGraphJSONAndText(t *testing.T) {
	code, out, errOut := runTasks(t, graphMods(), "tasks", "graph", "gr-ko8", "--json")
	if code != core.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var v struct {
		SchemaVersion int      `json:"schema_version"`
		Mode          string   `json:"mode"`
		Focus         string   `json:"focus"`
		Chain         []string `json:"chain"`
		Nodes         []struct {
			ID string `json:"id"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	if v.SchemaVersion != core.SchemaVersion || v.Mode != "neighborhood" || v.Focus != "gr-ko8" || len(v.Nodes) != 8 || strings.Join(v.Chain, ",") != "gr-bac,gr-ko8,gr-67z,gr-au4" {
		t.Errorf("graph json: %+v", v)
	}
	_, out, _ = runTasks(t, graphMods(), "tasks", "graph", "--whole")
	for _, want := range []string{"layer 0", "cycle: gr-syt -> gr-tse -> gr-vgy -> gr-syt", "depends on itself: gr-1fm", "longest chain (6): gr-q7z"} {
		if !strings.Contains(out, want) {
			t.Errorf("whole text lacks %q:\n%s", want, out)
		}
	}
	_, out, _ = runTasks(t, graphMods(), "tasks", "graph", "gr-ko8")
	if !strings.Contains(out, "blocked by gr-bac") || !strings.Contains(out, "blocks gr-67z") {
		t.Errorf("neighborhood text:\n%s", out)
	}
}

// Failures say why and print nothing: an unknown id is a failure, a malformed command is usage.
func TestTasksGraphFailures(t *testing.T) {
	for _, c := range []struct {
		args    []string
		code    int
		wantErr string
	}{
		{[]string{"tasks", "graph", "nope"}, core.ExitFail, `no task "nope"`},
		{[]string{"tasks", "graph", "--depth", "9"}, core.ExitUsage, "usage: abacus tasks graph"},
		{[]string{"tasks", "graph", "--depth", "0"}, core.ExitUsage, "usage: abacus tasks graph"},
		{[]string{"tasks", "graph", "gr-ko8", "--whole"}, core.ExitUsage, "usage: abacus tasks graph"},
		{[]string{"tasks", "graph", "a", "b"}, core.ExitUsage, "usage: abacus tasks graph"},
		{[]string{"tasks", "graph", "--bogus"}, core.ExitUsage, "flag provided but not defined"},
	} {
		code, out, errOut := runTasks(t, graphMods(), c.args...)
		if code != c.code || out != "" || !strings.Contains(errOut, c.wantErr) {
			t.Errorf("%v: code %d (want %d), stdout %q, stderr %q (want %q)", c.args, code, c.code, out, errOut, c.wantErr)
		}
	}
	bad := []core.Module{snapMod{Fake: &modtest.Fake{IDStr: "bd", Det: core.Detection{State: core.Ready, Version: "1"}}, err: context.DeadlineExceeded}}
	if code, out, errOut := runTasks(t, bad, "tasks", "graph", "--whole"); code != core.ExitFail || out != "" || errOut == "" {
		t.Errorf("a failed read: %d %q %q", code, out, errOut)
	}
}

// The JSON is a contract like the list's: schema_version 1, the graph keys, additive only.
func TestTasksGraphJSONContract(t *testing.T) {
	_, near, _ := runTasks(t, graphMods(), "tasks", "graph", "gr-ko8", "--json")
	_, whole, _ := runTasks(t, graphMods(), "tasks", "graph", "--whole", "--json")
	checkGoldenFile(t, "tasks-graph.golden.json", near)
	checkGoldenFile(t, "tasks-graph-whole.golden.json", whole)
}

// memMod serves an in-memory snapshot.
type memMod struct {
	*modtest.Fake
	snap core.TaskSnapshot
}

func (m memMod) Snapshot(context.Context) (core.TaskSnapshot, error) { return m.snap, nil }

// A focus with more dependents than the boxes hold: every outline row of the text is a whole line
// (id, status, title), none of them blank, and the JSON rows carry the same nodes.
func TestTasksGraphOutlineReachesPastTheBoxes(t *testing.T) {
	is := []core.TaskIssue{{ID: "f-0", Title: "the focus", Status: "open", Priority: 2}}
	for i := 1; i <= 7; i++ {
		is = append(is, core.TaskIssue{ID: fmt.Sprintf("d-%d", i), Title: fmt.Sprintf("dependent %d", i), Status: "open", Priority: 2, Deps: []core.TaskDep{{DependsOn: "f-0", Type: "blocks"}}})
	}
	mods := []core.Module{memMod{&modtest.Fake{IDStr: "bd", Det: core.Detection{State: core.Ready, Version: "1"}}, core.TaskSnapshot{Issues: is}}}
	_, out, _ := runTasks(t, mods, "tasks", "graph", "f-0", "--depth", "1")
	for i := 1; i <= 7; i++ {
		if want := fmt.Sprintf("d-%d", i); !regexp.MustCompile(`blocks\s+` + want + `\s+open\s+dependent ` + fmt.Sprint(i)).MatchString(out) {
			t.Errorf("row for %s is missing or blank:\n%s", want, out)
		}
	}
	_, js, _ := runTasks(t, mods, "tasks", "graph", "f-0", "--depth", "1", "--json")
	var v struct {
		Nodes   []struct{ ID string }
		Outline []struct {
			ID   string
			More int
			Node *struct{ ID, Title, Status string }
		}
	}
	if err := json.Unmarshal([]byte(js), &v); err != nil {
		t.Fatal(err)
	}
	rows := 0
	for _, r := range v.Outline {
		if r.ID != "" {
			rows++
			if r.Node == nil || r.Node.ID != r.ID || r.Node.Title == "" {
				t.Errorf("outline row %s without its node", r.ID)
			}
		}
	}
	if rows != 8 || len(v.Nodes) >= 8 { // the focus and seven dependents, past the five the boxes hold
		t.Errorf("outline rows %d, boxes %d", rows, len(v.Nodes))
	}
}
