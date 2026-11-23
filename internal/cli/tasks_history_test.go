package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// histMod serves the history of the captured real event log.
type histMod struct {
	*modtest.Fake
	err   error
	asked *[]string
}

func (m histMod) History(_ context.Context, id string, limit int) (core.TaskHistoryReport, error) {
	if m.asked != nil {
		*m.asked = append(*m.asked, id)
	}
	if m.err != nil {
		return core.TaskHistoryReport{}, m.err
	}
	b, err := os.ReadFile(filepath.Join("..", "taskview", "testdata", "history-events.json"))
	if err != nil {
		return core.TaskHistoryReport{}, err
	}
	return taskview.ParseHistory(b, id, limit, time.Date(2026, 11, 23, 7, 0, 0, 0, time.UTC))
}

func hist(err error, asked *[]string) []core.Module {
	return []core.Module{histMod{Fake: &modtest.Fake{IDStr: "bd", Det: core.Detection{State: core.Ready, Version: "1"}}, err: err, asked: asked}}
}

func TestTasksHistoryText(t *testing.T) {
	code, out, errOut := runTasks(t, hist(nil, nil), "tasks", "history", "hs-yqf", "--limit", "3")
	if code != 0 || errOut != "" {
		t.Fatalf("code %d err %q", code, errOut)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if lines[0] != "hs-yqf: 3 events, newest first" || !strings.Contains(lines[1], "reopened") || !strings.Contains(lines[2], "closed") || !strings.Contains(lines[2], "shipped") || !strings.Contains(lines[3], "dependency_added") || !strings.Contains(out, "raise --limit") {
		t.Errorf("text:\n%s", out)
	}
}

func TestTasksHistoryJSON(t *testing.T) {
	code, out, _ := runTasks(t, hist(nil, nil), "tasks", "history", "hs-yqf", "--json")
	if code != 0 {
		t.Fatal(code)
	}
	var rep struct {
		SchemaVersion int `json:"schema_version"`
		ID            string
		Events        []struct{ Kind, Actor, Detail string }
		Truncated     bool
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if rep.SchemaVersion != 1 || rep.ID != "hs-yqf" || len(rep.Events) != 8 || rep.Events[7].Kind != "created" || rep.Truncated {
		t.Errorf("%+v", rep)
	}
}

func TestTasksHistoryErrors(t *testing.T) {
	var asked []string
	for _, c := range []struct {
		args []string
		code int
		sub  string
	}{
		{[]string{"tasks", "history"}, core.ExitUsage, "needs a task id"},
		{[]string{"tasks", "history", "--json"}, core.ExitUsage, "needs a task id"},
		{[]string{"tasks", "history", "not an id"}, core.ExitUsage, "not a task id"},
		{[]string{"tasks", "history", "hs-1", "--limit", "0"}, core.ExitUsage, "--limit must be"},
		{[]string{"tasks", "history", "hs-1", "--limit", "999"}, core.ExitUsage, "--limit must be"},
		{[]string{"tasks", "history", "hs-1", "extra"}, core.ExitUsage, "unexpected argument"},
	} {
		code, _, errOut := runTasks(t, hist(nil, &asked), c.args...)
		if code != c.code || !strings.Contains(errOut, c.sub) {
			t.Errorf("%v: code %d err %q", c.args, code, errOut)
		}
	}
	if len(asked) != 0 {
		t.Errorf("a bad request reached the provider: %v", asked)
	}
	code, _, errOut := runTasks(t, hist(errors.New("bd history exited 1"), nil), "tasks", "history", "hs-1")
	if code != core.ExitFail || !strings.Contains(errOut, "bd history exited 1") {
		t.Errorf("provider failure: %d %q", code, errOut)
	}
	if code, _, errOut = runTasks(t, snap(), "tasks", "history", "hs-1"); code != core.ExitFail || !strings.Contains(errOut, "not connected") {
		t.Errorf("no history provider: %d %q", code, errOut)
	}
}
