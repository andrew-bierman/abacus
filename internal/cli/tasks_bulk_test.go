package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/cli"
	"github.com/andrew-bierman/abacus/internal/core"
)

type batchAudit struct{ lines []core.AuditEntry }

func (a *batchAudit) Record(e core.AuditEntry) error { a.lines = append(a.lines, e); return nil }

func runBulk(t *testing.T, stdin string, m *editMod, args ...string) (int, string, string, *batchAudit) {
	t.Helper()
	var o, e bytes.Buffer
	au := &batchAudit{}
	code := cli.Run(append([]string{"tasks", "bulk"}, args...), cli.Deps{Out: &o, Err: &e, In: strings.NewReader(stdin), Audit: au, Version: "t",
		Build: func(cli.Options) *core.Registry { return core.NewRegistry(m) }})
	return code, o.String(), e.String(), au
}

func TestBulkDryRunListsEveryTaskAndCommandAndRunsNothing(t *testing.T) {
	m := newEditMod()
	code, out, errOut, au := runBulk(t, "", m, "--ids", "t-1,t-2,t-3", "--op", "priority", "--value", "0", "--dry-run")
	if code != 0 || len(m.ran) != 0 || len(au.lines) != 0 {
		t.Fatalf("code=%d ran=%v audit=%d err=%s", code, m.ran, len(au.lines), errOut)
	}
	for _, want := range []string{"set the priority to 0 on 3 tasks", "t-1  one", "priority 2 -> 0", "command: bd priority t-1 P0", "command: bd priority t-3 P0"} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
}

func TestBulkDryRunJSONIsTyped(t *testing.T) {
	_, out, _, _ := runBulk(t, "", newEditMod(), "--ids", "t-1,t-2", "--op", "close", "--dry-run", "--json")
	var d struct {
		SchemaVersion int  `json:"schema_version"`
		DryRun        bool `json:"dry_run"`
		Items         []struct {
			ID   string   `json:"id"`
			Argv []string `json:"argv"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &d); err != nil || d.SchemaVersion != 1 || !d.DryRun || len(d.Items) != 2 || d.Items[1].ID != "t-2" || len(d.Items[1].Argv) == 0 {
		t.Fatalf("%v %s", err, out)
	}
}

func TestBulkYesAppliesEveryTaskAuditedUnderOneBatch(t *testing.T) {
	m := newEditMod()
	code, out, errOut, au := runBulk(t, "", m, "--ids", "t-1, t-2,t-3", "--op", "label-add", "--value", "bug", "--yes")
	if code != 0 || len(m.ran) != 3 || len(au.lines) != 6 || !strings.Contains(out, "3 of 3 applied") {
		t.Fatalf("code=%d ran=%d audit=%d out=%s err=%s", code, len(m.ran), len(au.lines), out, errOut)
	}
	b := au.lines[0].Batch
	for _, l := range au.lines {
		if b == "" || l.Batch != b {
			t.Errorf("every line carries the batch id: %+v", l)
		}
	}
	if !strings.Contains(out, b) {
		t.Errorf("the batch id is printed: %s", out)
	}
}

func TestBulkASkippedTaskIsReportedAndExitsNonZero(t *testing.T) {
	m := newEditMod()
	m.changeAfter = 4 // three reads to plan, then t-1 is read again; from the next read on a task has moved
	m.again = core.Task{Title: "one", Status: "in_progress", Priority: 2}
	code, out, _, _ := runBulk(t, "", m, "--ids", "t-1,t-2,t-3", "--op", "close", "--yes")
	if code != 1 || len(m.ran) != 1 || !strings.Contains(out, "skipped") || !strings.Contains(out, "1 of 3 applied, 2 skipped") {
		t.Fatalf("code=%d ran=%d\n%s", code, len(m.ran), out)
	}
}

func TestBulkAsksFirstAndOnlyYesProceeds(t *testing.T) {
	for stdin, want := range map[string]int{"y\n": 3, "n\n": 0, "": 0} {
		m := newEditMod()
		code, _, errOut, _ := runBulk(t, stdin, m, "--ids", "t-1,t-2,t-3", "--op", "close")
		if len(m.ran) != want || (want == 0 && code == 0) || !strings.Contains(errOut, "[y/N]") {
			t.Errorf("stdin %q: ran=%d code=%d err=%q", stdin, len(m.ran), code, errOut)
		}
	}
}

func TestBulkUsageErrorsExitTwoAndRunNothing(t *testing.T) {
	var many []string
	for i := 0; i < core.MaxBulk+1; i++ {
		many = append(many, "t-"+string(rune('a'+i)))
	}
	bad := map[string][]string{
		"no ids":         {"--op", "close"},
		"no op":          {"--ids", "t-1"},
		"bad op":         {"--ids", "t-1", "--op", "delete"},
		"too many":       {"--ids", strings.Join(many, ","), "--op", "close", "--yes"},
		"bad id":         {"--ids", "t-1,--force", "--op", "close", "--yes"},
		"priority text":  {"--ids", "t-1", "--op", "priority", "--value", "high"},
		"priority 9":     {"--ids", "t-1", "--op", "priority", "--value", "9"},
		"status closed":  {"--ids", "t-1", "--op", "status", "--value", "closed"},
		"label space":    {"--ids", "t-1", "--op", "label-add", "--value", "a b"},
		"assign nothing": {"--ids", "t-1", "--op", "assign"},
		"assign both":    {"--ids", "t-1", "--op", "assign", "--value", "ada", "--clear"},
		"close value":    {"--ids", "t-1", "--op", "close", "--value", "x"},
		"json alone":     {"--ids", "t-1", "--op", "close", "--json"},
		"stray arg":      {"--ids", "t-1", "--op", "close", "extra"},
	}
	for name, args := range bad {
		m := newEditMod()
		code, _, _, au := runBulk(t, "", m, args...)
		if code != 2 || len(m.ran) != 0 || len(au.lines) != 0 || m.reads != 0 {
			t.Errorf("%s: code=%d ran=%d reads=%d", name, code, len(m.ran), m.reads)
		}
	}
}

func TestBulkRefusesWhenATaskCannotBeReadFirst(t *testing.T) {
	m := newEditMod()
	m.readErr = errors.New("gone")
	code, _, errOut, _ := runBulk(t, "", m, "--ids", "t-1", "--op", "close", "--yes")
	if code != 1 || len(m.ran) != 0 || !strings.Contains(errOut, "could not read") {
		t.Fatalf("code=%d err=%s", code, errOut)
	}
}

func TestBulkYesJSONPrintsTheTypedReport(t *testing.T) {
	m := newEditMod()
	m.changeAfter = 4 // t-1 is applied, the other two moved after the plan
	m.again = core.Task{Title: "one", Status: "in_progress", Priority: 2}
	code, out, _, _ := runBulk(t, "", m, "--ids", "t-1,t-2,t-3", "--op", "close", "--yes", "--json")
	var r struct {
		SchemaVersion int    `json:"schema_version"`
		BatchID       string `json:"batch_id"`
		Op            string `json:"op"`
		Applied       int    `json:"applied"`
		Skipped       int    `json:"skipped"`
		Items         []struct {
			ID      string `json:"id"`
			Outcome string `json:"outcome"`
			Detail  string `json:"detail"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if code != 1 || r.SchemaVersion != 1 || !strings.HasPrefix(r.BatchID, "batch-") || r.Op != "close" || r.Applied != 1 || r.Skipped != 2 || len(r.Items) != 3 ||
		r.Items[0].Outcome != "applied" || r.Items[1].Outcome != "skipped" || r.Items[1].Detail == "" {
		t.Errorf("code=%d %+v", code, r)
	}
}

// probeMod can answer a re-read with another id or another priority.
type probeMod struct {
	*editMod
	alias  map[string]string // requested id -> the id the tracker resolves it to
	lowPri map[string]bool
}

func (m *probeMod) ReadTask(ctx context.Context, id string) (core.Task, error) {
	t, err := m.editMod.ReadTask(ctx, id)
	if a, ok := m.alias[id]; ok {
		t.ID = a
	}
	if m.lowPri[id] {
		t.Priority = 0
	}
	return t, err
}

func runProbe(t *testing.T, stdin string, m *probeMod, args ...string) (int, string, string, *batchAudit) {
	t.Helper()
	var o, e bytes.Buffer
	au := &batchAudit{}
	code := cli.Run(append([]string{"tasks", "bulk"}, args...), cli.Deps{Out: &o, Err: &e, In: strings.NewReader(stdin), Audit: au, Version: "t",
		Build: func(cli.Options) *core.Registry { return core.NewRegistry(m) }})
	return code, o.String(), e.String(), au
}

func TestBulkRefusesAPrefixOrAliasThatResolvesToAnotherId(t *testing.T) {
	m := &probeMod{editMod: newEditMod(), alias: map[string]string{"t-": "t-1"}}
	code, _, errOut, au := runProbe(t, "", m, "--ids", "t-,t-1", "--op", "close", "--yes")
	if code != 2 || len(m.ran) != 0 || len(au.lines) != 0 || !strings.Contains(errOut, "not a whole task id") {
		t.Errorf("code=%d ran=%d err=%s", code, len(m.ran), errOut)
	}
}

func TestBulkPlanTimeSkipsAreInTheReportAndTheExitCode(t *testing.T) {
	m := &probeMod{editMod: newEditMod(), lowPri: map[string]bool{"t-2": true}}
	code, out, _, _ := runProbe(t, "", m, "--ids", "t-1,t-2,t-3", "--op", "priority", "--value", "0", "--yes", "--json")
	var r struct {
		Applied int `json:"applied"`
		Skipped int `json:"skipped"`
		Items   []struct {
			ID, Outcome string
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil || code != 1 || r.Applied != 2 || r.Skipped != 1 || len(r.Items) != 3 || r.Items[2].ID != "t-2" || r.Items[2].Outcome != "skipped" {
		t.Errorf("code=%d %v %+v\n%s", code, err, r, out)
	}
}
