package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// treeMod provides the snapshot of the real two-epic tracker export.
type treeMod struct{ *modtest.Fake }

func (treeMod) Snapshot(context.Context) (core.TaskSnapshot, error) {
	b, err := os.ReadFile(filepath.Join("..", "taskview", "testdata", "export-tree.jsonl"))
	if err != nil {
		return core.TaskSnapshot{}, err
	}
	return taskview.Parse(bytes.NewReader(b), time.Date(2026, 11, 23, 4, 20, 0, 0, time.UTC), "tracker")
}

func treeMods() []core.Module {
	return []core.Module{treeMod{&modtest.Fake{IDStr: "bd", Det: core.Detection{State: core.Ready, Version: "1"}}}}
}

func TestTasksListTreeText(t *testing.T) {
	code, out, errOut := runTasks(t, treeMods(), "tasks", "list", "--tree", "--preset", "all")
	if code != 0 || errOut != "" {
		t.Fatalf("code %d err %q", code, errOut)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if !strings.HasPrefix(lines[0], "Task tree  14 of 14  (all, sort: priority ↑)") || len(lines) != 15 {
		t.Fatalf("header and 14 rows:\n%s", out)
	}
	for _, want := range []string{"▾ Tasks workspace", "├── Tree view of epics", "│ ├── Wire the lease heartbeat", "└── Document the write gate", "1/3 done"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	_, out, _ = runTasks(t, treeMods(), "tasks", "list", "--tree", "-q", "heartbeat")
	if !strings.Contains(out, "Task tree  1 of 14") || strings.Count(out, "(context)") != 2 || strings.Contains(out, "Kanban") {
		t.Errorf("a match keeps the two parents above it, as context:\n%s", out)
	}
}

func TestTasksListTreeJSONContract(t *testing.T) {
	_, out, _ := runTasks(t, treeMods(), "tasks", "list", "--tree", "--json", "--preset", "all")
	checkGoldenFile(t, "tasks-tree.golden.json", out)
	_, flat, _ := runTasks(t, treeMods(), "tasks", "list", "--json", "--preset", "all")
	if strings.Contains(flat, `"depth"`) || strings.Contains(flat, `"has_children"`) {
		t.Error("the flat list's JSON is unchanged")
	}
}

func TestTasksListTreeIsReadOnlyAndNeedsTheListVerb(t *testing.T) {
	if code, _, errOut := runTasks(t, treeMods(), "tasks", "show", "tr-jbt", "--tree"); code == 0 || errOut == "" {
		t.Errorf("--tree is a list flag: code %d err %q", code, errOut)
	}
}
