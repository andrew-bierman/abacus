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
	for _, args := range [][]string{{"tasks", "graph", "nope"}, {"tasks", "graph", "--depth", "9"}, {"tasks", "graph", "gr-ko8", "--whole"}, {"tasks", "graph", "a", "b"}} {
		if code, _, _ := runTasks(t, graphMods(), args...); code == core.ExitOK {
			t.Errorf("%v should fail", args)
		}
	}
}
