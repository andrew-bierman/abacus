package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
)

// editMod is a Tasks writer, reader and snapshot source for the edit verbs: t-1 waits for t-2, t-3 is free.
type editMod struct {
	*taskMod
	cur     core.Task // what a re-read returns
	reads   int
	readErr error
	// after the Nth read the task changes to again (a change by someone else between confirm and write)
	snaps, cycleAfter int
	changeAfter       int
	again             core.Task
}

func (m *editMod) BuildTaskArgv(ch core.TaskChange) ([]string, error) {
	a := []string{"bd", string(ch.Op), ch.ID}
	for _, v := range []string{ch.Label, ch.Blocker, ch.Text} {
		if v != "" {
			a = append(a, v)
		}
	}
	if ch.Assignee != nil {
		a = append(a, "assignee="+*ch.Assignee+" if="+ch.IfAssignee)
	}
	if ch.Priority != nil {
		a = append(a, "P"+string(rune('0'+*ch.Priority)))
	}
	return a, nil
}

func (m *editMod) ReadTask(_ context.Context, id string) (core.Task, error) {
	m.reads++
	if m.readErr != nil {
		return core.Task{}, m.readErr
	}
	if m.changeAfter > 0 && m.reads > m.changeAfter {
		return m.again, nil
	}
	c := m.cur
	c.ID = id
	if id != "t-1" { // only t-1 waits for t-2
		c.DependsOn = nil
	}
	return c, nil
}

func (m *editMod) Snapshot(context.Context) (core.TaskSnapshot, error) {
	m.snaps++
	if m.cycleAfter > 0 && m.snaps > m.cycleAfter { // someone made t-2 wait for t-3 in between
		return core.TaskSnapshot{Issues: []core.TaskIssue{
			{ID: "t-1", Title: "one", Status: "open", Deps: []core.TaskDep{{DependsOn: "t-2", Type: "blocks"}}},
			{ID: "t-2", Title: "two", Status: "open", Deps: []core.TaskDep{{DependsOn: "t-3", Type: "blocks"}}},
			{ID: "t-3", Title: "three", Status: "open"},
		}}, nil
	}
	return core.TaskSnapshot{Issues: []core.TaskIssue{
		{ID: "t-1", Title: "one", Status: "open", Deps: []core.TaskDep{{DependsOn: "t-2", Type: "blocks"}}},
		{ID: "t-2", Title: "two", Status: "open"},
		{ID: "t-3", Title: "three", Status: "open"},
	}}, nil
}

func newEditMod() *editMod {
	m := &editMod{taskMod: newTaskMod()}
	m.cur = core.Task{Title: "one", Status: "open", Priority: 2, Assignee: "bob", Labels: []string{"ui"}, DependsOn: []string{"t-2"}}
	return m
}

var _ = modtest.Fake{}

func TestEditVerbsPlanTheExactChangeAndDryRunRunsNothing(t *testing.T) {
	cases := map[string]struct {
		args []string
		want []string
	}{
		"label add":    {[]string{"label", "add", "t-1", "bug"}, []string{"add a label to task t-1", `label: "bug"`, "command: bd label-add t-1 bug"}},
		"label remove": {[]string{"label", "remove", "t-1", "ui"}, []string{"remove a label from task t-1", "command: bd label-remove t-1 ui"}},
		"assign":       {[]string{"assign", "t-1", "ada"}, []string{"assign task t-1", `assignee: "bob" -> "ada"`, `if=bob`}},
		"unassign":     {[]string{"assign", "t-1", "--clear"}, []string{"unassign task t-1", `assignee: "bob" -> "nobody"`}},
		"priority":     {[]string{"priority", "t-1", "0"}, []string{"set the priority of task t-1", `priority: "2" -> "0"`}},
		"dep blocked":  {[]string{"dep", "add", "t-2", "--blocked-by", "t-3"}, []string{"make task t-2 wait for t-3", `blocker: "t-3 three"`}},
		"dep blocks":   {[]string{"dep", "add", "t-3", "--blocks", "t-2"}, []string{"make task t-2 wait for t-3"}},
		"dep remove":   {[]string{"dep", "remove", "t-1", "--blocked-by", "t-2"}, []string{"stop task t-1 waiting for t-2"}},
		"comment text": {[]string{"comment", "t-1", "--text", "-x looks odd"}, []string{"add a comment to task t-1", `comment: "-x looks odd"`}},
	}
	for name, c := range cases {
		m := newEditMod()
		code, out, errOut, au := runIn(t, "", []core.Module{m}, append(append([]string{"tasks"}, c.args...), "--dry-run")...)
		if code != 0 || len(m.ran) != 0 || au.n != 0 {
			t.Errorf("%s: code=%d ran=%v audit=%d err=%s", name, code, m.ran, au.n, errOut)
		}
		for _, w := range c.want {
			if !strings.Contains(out, w) {
				t.Errorf("%s: output lacks %q:\n%s", name, w, out)
			}
		}
	}
}

func TestEditVerbsApplyWithYesAndAreAudited(t *testing.T) {
	for name, args := range map[string][]string{
		"label":    {"label", "add", "t-1", "bug"},
		"assign":   {"assign", "t-1", "ada"},
		"priority": {"priority", "t-1", "0"},
		"dep":      {"dep", "add", "t-3", "--blocked-by", "t-2"},
		"comment":  {"comment", "t-1", "hello there"},
	} {
		m := newEditMod()
		code, out, errOut, au := runIn(t, "", []core.Module{m}, append(append([]string{"tasks"}, args...), "--yes")...)
		if code != 0 || len(m.ran) != 1 || au.n != 2 || !strings.Contains(out, "done:") {
			t.Errorf("%s: code=%d ran=%v audit=%d out=%q err=%q", name, code, m.ran, au.n, out, errOut)
		}
		if m.reads < 2 {
			t.Errorf("%s: the task is read at plan time and again right before the write (reads=%d)", name, m.reads)
		}
	}
}

func TestEditDoneLinesSayWhatHappened(t *testing.T) {
	m := newEditMod()
	_, out, _, _ := runIn(t, "", []core.Module{m}, "tasks", "dep", "add", "t-3", "--blocked-by", "t-2", "--yes")
	if !strings.Contains(out, "t-9 now waits for t-2") && !strings.Contains(out, "now waits for t-2") {
		t.Errorf("%q", out)
	}
	_, out, _, _ = runIn(t, "", []core.Module{newEditMod()}, "tasks", "comment", "t-1", "hi", "--yes")
	if !strings.Contains(out, "comment added to") {
		t.Errorf("%q", out)
	}
}

func TestEditAsksFirstAndOnlyYesProceeds(t *testing.T) {
	for stdin, want := range map[string]int{"y\n": 1, "n\n": 0, "": 0, "maybe\n": 0} {
		m := newEditMod()
		code, _, errOut, _ := runIn(t, stdin, []core.Module{m}, "tasks", "label", "add", "t-1", "bug")
		if len(m.ran) != want || (want == 0 && code == 0) || !strings.Contains(errOut, "[y/N]") {
			t.Errorf("stdin %q: ran=%d code=%d err=%q", stdin, len(m.ran), code, errOut)
		}
	}
}

func TestEditJSONNeedsYesOrDryRunAndPrintsTypedResults(t *testing.T) {
	code, _, errOut, _ := runIn(t, "", []core.Module{newEditMod()}, "tasks", "label", "add", "t-1", "bug", "--json")
	if code != 2 || !strings.Contains(errOut, "--json needs --yes or --dry-run") {
		t.Fatalf("code=%d %q", code, errOut)
	}
	_, out, _, _ := runIn(t, "", []core.Module{newEditMod()}, "tasks", "priority", "t-1", "1", "--json", "--dry-run")
	var d struct {
		DryRun bool     `json:"dry_run"`
		Argv   []string `json:"argv"`
	}
	if err := json.Unmarshal([]byte(out), &d); err != nil || !d.DryRun || len(d.Argv) == 0 {
		t.Fatalf("%v %s", err, out)
	}
	_, out, _, _ = runIn(t, "", []core.Module{newEditMod()}, "tasks", "priority", "t-1", "1", "--json", "--yes")
	var r struct {
		Applied bool            `json:"applied"`
		Result  core.TaskResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil || !r.Applied || r.Result.ID == "" {
		t.Fatalf("%v %s", err, out)
	}
}

func TestEditUsageErrorsExitTwoAndRunNothing(t *testing.T) {
	bad := map[string][]string{
		"label no verb":        {"label", "t-1", "bug"},
		"label bad verb":       {"label", "set", "t-1", "bug"},
		"label missing label":  {"label", "add", "t-1"},
		"label comma":          {"label", "add", "t-1", "a,b"},
		"label space":          {"label", "add", "t-1", "a b"},
		"label flag-like":      {"label", "add", "t-1", "--force"},
		"assign none":          {"assign"},
		"assign no name":       {"assign", "t-1"},
		"assign name and none": {"assign", "t-1", "ada", "--clear"},
		"assign blank":         {"assign", "t-1", " "},
		"assign same":          {"assign", "t-1", "bob"},
		"unassign unassigned":  {"assign", "t-3", "--clear"}, // t-3 reads as bob too: see below
		"priority missing":     {"priority", "t-1"},
		"priority text":        {"priority", "t-1", "high"},
		"priority 9":           {"priority", "t-1", "9"},
		"priority same":        {"priority", "t-1", "2"},
		"dep no flag":          {"dep", "add", "t-1"},
		"dep both flags":       {"dep", "add", "t-1", "--blocks", "t-2", "--blocked-by", "t-3"},
		"dep bad verb":         {"dep", "rm", "t-1", "--blocked-by", "t-2"},
		"dep self":             {"dep", "add", "t-1", "--blocked-by", "t-1"},
		"dep unknown target":   {"dep", "add", "t-1", "--blocked-by", "t-99"},
		"dep cycle":            {"dep", "add", "t-2", "--blocked-by", "t-1"},
		"dep duplicate":        {"dep", "add", "t-1", "--blocked-by", "t-2"},
		"dep remove missing":   {"dep", "remove", "t-3", "--blocked-by", "t-2"},
		"dep flag-like id":     {"dep", "add", "t-1", "--blocked-by", "--force"},
		"comment empty":        {"comment", "t-1", " "},
		"comment no text":      {"comment", "t-1"},
		"comment dash text":    {"comment", "t-1", "x", "-y"},
		"extra word":           {"priority", "t-1", "1", "2"},
		"flag-like id":         {"comment", "--force", "hi"},
	}
	for name, args := range bad {
		m := newEditMod()
		if name == "unassign unassigned" {
			m.cur.Assignee = ""
		}
		code, _, errOut, au := runIn(t, "", []core.Module{m}, append(append([]string{"tasks"}, args...), "--yes")...)
		if code != 2 || len(m.ran) != 0 || au.n != 0 {
			t.Errorf("%s: code=%d ran=%v audit=%d err=%q", name, code, m.ran, au.n, errOut)
		}
	}
}

func TestEditCycleMessageNamesTheChainAndWritesNothing(t *testing.T) {
	m := newEditMod()
	code, _, errOut, au := runIn(t, "", []core.Module{m}, "tasks", "dep", "add", "t-2", "--blocked-by", "t-1", "--yes")
	if code != 2 || !strings.Contains(errOut, "cycle") || !strings.Contains(errOut, "t-1 waits for t-2 waits for t-1") || len(m.ran) != 0 || au.n != 0 {
		t.Fatalf("code=%d %q ran=%v", code, errOut, m.ran)
	}
}

// the task changed between the confirm and the write: refused, nothing run, nothing audited
func TestEditRefusedWhenTheTaskChangedAfterTheConfirm(t *testing.T) {
	m := newEditMod()
	m.changeAfter = 1
	m.again = core.Task{ID: "t-1", Title: "one", Status: "open", Priority: 3, Assignee: "eve", Labels: []string{"ui", "bug"}}
	for name, args := range map[string][]string{
		"priority": {"priority", "t-1", "0"},
		"assign":   {"assign", "t-1", "ada"},
		"label":    {"label", "add", "t-1", "bug"},
	} {
		m.reads, m.ran = 0, nil
		code, _, errOut, au := runIn(t, "", []core.Module{m}, append(append([]string{"tasks"}, args...), "--yes")...)
		if code != 1 || len(m.ran) != 0 || au.n != 0 || !strings.Contains(errOut, "nothing was written") {
			t.Errorf("%s: code=%d ran=%v audit=%d err=%q", name, code, m.ran, au.n, errOut)
		}
	}
}

func TestEditNeedsToReadTheTaskAndFailsClosed(t *testing.T) {
	m := newEditMod()
	m.readErr = errors.New("no such issue")
	code, _, errOut, au := runIn(t, "", []core.Module{m}, "tasks", "label", "add", "t-1", "bug", "--yes")
	if code != 1 || len(m.ran) != 0 || au.n != 0 || !strings.Contains(errOut, "could not read t-1 first") {
		t.Fatalf("code=%d %q", code, errOut)
	}
	// a provider that writes but cannot read refuses these edits (create, update and close keep working)
	w := writeOnlyMod{m: newTaskMod()}
	code, _, errOut, _ = runIn(t, "", []core.Module{w}, "tasks", "label", "add", "t-1", "bug", "--yes")
	if code != 1 || !strings.Contains(errOut, "cannot read the task's current state") {
		t.Fatalf("code=%d %q", code, errOut)
	}
	code, _, errOut, _ = runIn(t, "", nil, "tasks", "label", "add", "t-1", "bug", "--yes")
	if code != 1 || !strings.Contains(errOut, "not connected") {
		t.Fatalf("code=%d %q", code, errOut)
	}
}

func TestEditFailureAndUnknownOutcomeAreSaidHonestly(t *testing.T) {
	m := newEditMod()
	m.fail = core.UnknownOutcome("bd timed out; the comment may or may not have been added: check the task's comments before retrying")
	code, _, errOut, au := runIn(t, "", []core.Module{m}, "tasks", "comment", "t-1", "hello", "--yes")
	if code != 1 || strings.Contains(errOut, "not applied") || !strings.Contains(errOut, "may or may not") || au.n != 2 {
		t.Fatalf("code=%d err=%q audit=%d", code, errOut, au.n)
	}
	m = newEditMod()
	m.fail = errors.New("bd: boom")
	code, _, errOut, _ = runIn(t, "", []core.Module{m}, "tasks", "comment", "t-1", "hello", "--yes")
	if code != 1 || !strings.Contains(errOut, "not applied") {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
}

func TestEditAuditFailureBlocksTheWrite(t *testing.T) {
	m := newEditMod()
	au := &memAudit{err: errors.New("disk full")}
	code, _, errOut, _ := runAudit(t, "", []core.Module{m}, au, "tasks", "label", "add", "t-1", "bug", "--yes")
	if code != 1 || len(m.ran) != 0 || !strings.Contains(errOut, "disk full") {
		t.Fatalf("code=%d ran=%v err=%q", code, m.ran, errOut)
	}
}

// a cycle made between the plan and the write is refused client-side: nothing runs, nothing is audited
func TestEditDepCycleCreatedAfterThePlanIsRefusedAtApply(t *testing.T) {
	m := newEditMod()
	m.cycleAfter = 1
	code, _, errOut, au := runIn(t, "", []core.Module{m}, "tasks", "dep", "add", "t-3", "--blocked-by", "t-2", "--yes")
	if code != 1 || len(m.ran) != 0 || au.n != 0 || !strings.Contains(errOut, "cycle") || !strings.Contains(errOut, "nothing was written") {
		t.Fatalf("code=%d ran=%v audit=%d err=%q", code, m.ran, au.n, errOut)
	}
}

// --json prints exactly one JSON document on stdout, whatever the outcome; failures print none
func TestEditJSONIsOneDocumentAndFailuresPrintNoneOnStdout(t *testing.T) {
	one := func(out string) {
		t.Helper()
		dec := json.NewDecoder(strings.NewReader(out))
		var v any
		if err := dec.Decode(&v); err != nil || dec.More() {
			t.Fatalf("want exactly one JSON document, got %q (%v)", out, err)
		}
	}
	for name, args := range map[string][]string{
		"label": {"label", "add", "t-1", "bug"}, "assign": {"assign", "t-1", "ada"}, "priority": {"priority", "t-1", "0"},
		"dep": {"dep", "add", "t-3", "--blocked-by", "t-2"}, "comment": {"comment", "t-1", "hi"},
	} {
		for _, mode := range []string{"--dry-run", "--yes"} {
			_, out, _, _ := runIn(t, "", []core.Module{newEditMod()}, append(append([]string{"tasks"}, args...), mode, "--json")...)
			one(out)
			_ = name
		}
	}
	m := newEditMod()
	m.fail = errors.New("bd: boom")
	code, out, _, _ := runIn(t, "", []core.Module{m}, "tasks", "comment", "t-1", "hi", "--yes", "--json")
	if code != 1 || errorCode(out) != "failed" {
		t.Fatalf("a failed write prints only the error document on stdout: %d %q", code, out)
	}
}
