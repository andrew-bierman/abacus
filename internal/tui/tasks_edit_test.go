package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/demo"
)

// editAt is the Tasks tab over fixtures with a writer and a reader, the audit recorded.
func editAt(w, h int) (*tasksSection, taskProv, *memAudit) {
	p, au := tasksFake(), &memAudit{}
	s := newTasksSection(core.Providers{Writer: p, Reader: p, Source: p}, &config{audit: au})
	s.SetTheme(asciiTheme())
	s.Update(tea.WindowSizeMsg{Width: w, Height: h})
	s.Update(tasksMsg{report: demo.TasksReport()})
	s.Update(taskSnapshotMsg{snap: fixtureSnapshot()})
	for _, i := range fixtureSnapshot().Issues { // a re-read answers like the tracker the list was read from
		(*p.fresh)[i.ID] = baseTask(i.ID)
	}
	return s, p, au
}

// onTask moves the list cursor to the task id.
func onTask(t *testing.T, s *tasksSection, id string) {
	t.Helper()
	for i := 0; i < 40; i++ {
		if cur, ok := s.current(); ok && cur.ID == id {
			return
		}
		send(s, "j")
	}
	t.Fatalf("task %s is not in the list", id)
}

func clearField(s *tasksSection) {
	for range []rune(s.fields[s.fi].value) {
		send(s, "backspace")
	}
}

// yes presses y on the confirm screen and delivers the answer of the write.
func yes(s *tasksSection) {
	_, cmd := s.Update(keyRunes("y"))
	if cmd != nil {
		s.Update(cmd())
	}
}

func ran(p taskProv) string { return ranMoves(p) }

// baseTask is a fixture task as a re-read of it would return it.
func baseTask(id string) core.Task {
	for _, i := range fixtureSnapshot().Issues {
		if i.ID != id {
			continue
		}
		t := core.Task{ID: i.ID, Title: i.Title, Status: i.Status, Priority: i.Priority, Type: i.Type, Assignee: i.Assignee, Labels: i.Labels}
		for _, d := range i.Deps {
			if d.Type == "blocks" {
				t.DependsOn = append(t.DependsOn, d.DependsOn)
			}
		}
		return t
	}
	return core.Task{ID: id, Status: "open"}
}

// ---- the keys open one-field forms on the selected task, in the List view only ----

func TestEditKeysOpenAOneFieldFormOnTheSelectedTask(t *testing.T) {
	for key, want := range map[string]struct {
		op     core.TaskOp
		label  string
		prefil string
	}{
		"t": {core.TaskLabelAdd, "Label", ""},
		"u": {core.TaskLabelRemove, "Label", ""},
		"a": {core.TaskAssign, "Assignee (blank: unassign)", "ada"},
		"p": {core.TaskPriority, "Priority 0-4", "0"},
		"b": {core.TaskDepAdd, "Waits for (task id)", ""},
		"B": {core.TaskDepRemove, "No longer waits for (task id)", ""},
		"c": {core.TaskAddComment, "Comment", ""},
	} {
		s, p, au := editAt(100, 32)
		onTask(t, s, "ac-12")
		send(s, key)
		if s.mode != modeForm || s.op != want.op || len(s.fields) != 1 || s.fields[0].label != want.label || s.fields[0].value != want.prefil || s.target.ID != "ac-12" {
			t.Errorf("%s: mode=%v op=%v fields=%+v target=%s", key, s.mode, s.op, s.fields, s.target.ID)
		}
		v := s.View()
		if !strings.Contains(v, editTitle(want.op, "ac-12")) || !strings.Contains(v, want.label) {
			t.Errorf("%s: the form names the task and the field:\n%s", key, v)
		}
		send(s, "esc")
		if s.mode != modeList || !strings.Contains(s.notice, "nothing was changed") || ran(p) != "" || au.n != 0 {
			t.Errorf("%s: esc cancels: %v %q", key, s.mode, s.notice)
		}
	}
	// removing a label offers the only label there is
	s, _, _ := editAt(100, 32)
	onTask(t, s, "ac-33") // one label: ui
	send(s, "u")
	if s.fields[0].value != "ui" {
		t.Errorf("a single label is prefilled: %q", s.fields[0].value)
	}
}

func TestEditKeysActOnlyInTheListViewAndNeedAWriter(t *testing.T) {
	for _, view := range []string{"V", "v", "g"} {
		s, _, _ := editAt(100, 32)
		send(s, view)
		for _, k := range []string{"t", "u", "a", "p", "b", "B", "c"} {
			send(s, k)
			if s.mode != modeList || !strings.Contains(s.notice, "List view") {
				t.Errorf("view key %s then %s: mode %v notice %q", view, k, s.mode, s.notice)
			}
		}
	}
	ro := listAt(100, 32) // no writer
	for _, k := range []string{"t", "u", "a", "p", "b", "B", "c"} {
		send(ro, k)
		if ro.mode != modeList || !strings.Contains(ro.notice, "writes are not available") {
			t.Errorf("%s without a writer: %v %q", k, ro.mode, ro.notice)
		}
	}
}

func TestEditKeysAreInTheHelpAndTheFooterBindings(t *testing.T) {
	s, _, _ := editAt(100, 32)
	got := map[string]string{}
	for _, b := range s.Bindings() {
		got[b.Help().Key] = b.Help().Desc
	}
	for k, d := range map[string]string{"t": "add label", "u": "remove label", "a": "assign", "p": "priority", "b": "add blocker", "B": "remove blocker", "c": "comment"} {
		if got[k] != d {
			t.Errorf("binding %s: %q, want %q", k, got[k], d)
		}
	}
	for k, d := range got {
		if strings.Contains(strings.ToLower(k+d), "bd ") {
			t.Errorf("a tool name in the help: %s %s", k, d)
		}
	}
}

// ---- form -> re-read -> confirm -> y: the exact command, once ----

type flow struct {
	task  string
	keys  []string
	clear bool
	text  string
	fresh core.Task // what the re-read returns
	argv  string
	lines []string
	note  string
}

func TestEditFlowsGoThroughReReadConfirmAndRunTheExactCommandOnce(t *testing.T) {
	gi := func(id string, mod func(*core.Task)) core.Task {
		x := baseTask(id)
		mod(&x)
		return x
	}
	flows := map[string]flow{
		"label add": {task: "ac-12", keys: []string{"t"}, text: "bug", fresh: baseTask("ac-12"),
			argv: "bd label-add ac-12 --label=bug", lines: []string{"add a label to task ac-12", `label: "bug"`}, note: "labeled ac-99"},
		"label remove": {task: "ac-12", keys: []string{"u"}, text: "core", fresh: gi("ac-12", func(x *core.Task) { x.Labels = []string{"core"} }),
			argv: "bd label-remove ac-12 --label=core", lines: []string{"remove a label from task ac-12"}, note: "unlabeled ac-99"},
		"assign": {task: "ac-12", keys: []string{"a"}, clear: true, text: "mei", fresh: baseTask("ac-12"),
			argv: "bd assign ac-12 --assignee=mei --if-assignee=ada", lines: []string{`assignee: "ada" -> "mei"`}, note: "assigned ac-99"},
		"unassign": {task: "ac-12", keys: []string{"a"}, clear: true, text: "", fresh: baseTask("ac-12"),
			argv: "bd assign ac-12 --assignee= --if-assignee=ada", lines: []string{"unassign task ac-12", `"ada" -> "nobody"`}, note: "assigned ac-99"},
		"priority": {task: "ac-12", keys: []string{"p"}, clear: true, text: "3", fresh: baseTask("ac-12"),
			argv: "bd priority ac-12 --priority=3", lines: []string{`priority: "0" -> "3"`}, note: "reprioritized ac-99"},
		"dep add": {task: "ac-09", keys: []string{"b"}, text: "ac-15", fresh: baseTask("ac-09"),
			argv: "bd dep-add ac-09 ac-15", lines: []string{"make task ac-09 wait for ac-15", `blocker: "ac-15 lease holder column"`}, note: "ac-99 now waits for ac-15"},
		"dep remove": {task: "ac-09", keys: []string{"B"}, text: "ac-12", fresh: gi("ac-09", func(x *core.Task) { x.DependsOn = []string{"ac-12"} }),
			argv: "bd dep-remove ac-09 ac-12", lines: []string{"stop task ac-09 waiting for ac-12"}, note: "ac-99 no longer waits for ac-12"},
		"comment": {task: "ac-12", keys: []string{"c"}, text: "-x seems fine", fresh: baseTask("ac-12"),
			argv: `bd comment ac-12 -- "-x seems fine"`, lines: []string{"add a comment to task ac-12", `comment: "-x seems fine"`}, note: "commented on ac-99"},
	}
	for name, f := range flows {
		s, p, au := editAt(100, 32)
		onTask(t, s, f.task)
		send(s, f.keys...)
		if f.clear {
			clearField(s)
		}
		typeIn(s, f.text)
		send(s, "enter")
		if s.mode != modeVerify || ran(p) != "" || au.n != 0 {
			t.Fatalf("%s: submit re-reads the task first, nothing is written: mode %v ran %q formErr %q", name, s.mode, ran(p), s.formErr)
		}
		s.Update(taskVerifiedMsg{id: f.task, task: f.fresh})
		if s.mode != modeConfirm || ran(p) != "" || au.n != 0 {
			t.Fatalf("%s: mode %v notice %q", name, s.mode, s.notice)
		}
		if got := core.DisplayArgv(s.plan.Argv); got != f.argv {
			t.Errorf("%s: command %q, want %q", name, got, f.argv)
		}
		if v := s.View(); !strings.Contains(v, "Confirm this change") || !strings.Contains(v, strings.TrimPrefix(f.argv, "bd ")) {
			t.Errorf("%s: the confirm screen shows the exact command:\n%s", name, v)
		}
		for _, l := range f.lines {
			if !strings.Contains(strings.Join(s.plan.Lines, "|"), l) {
				t.Errorf("%s: plan lacks %q: %v", name, l, s.plan.Lines)
			}
		}
		// n cancels without a write; the same plan then applies on y exactly once
		send(s, "n")
		if s.mode != modeList || ran(p) != "" || au.n != 0 {
			t.Fatalf("%s: n cancels: %v", name, s.mode)
		}
		send(s, f.keys...)
		if f.clear {
			clearField(s)
		}
		typeIn(s, f.text)
		send(s, "enter")
		s.Update(taskVerifiedMsg{id: f.task, task: f.fresh})
		yes(s)
		if n := len(*p.ran); n != 1 || strings.Join((*p.ran)[0], " ") != strings.Replace(f.argv, `"-x seems fine"`, "-x seems fine", 1) {
			t.Errorf("%s: ran %v", name, *p.ran)
		}
		if s.mode != modeList || !strings.Contains(s.notice, f.note) {
			t.Errorf("%s: notice %q, want %q", name, s.notice, f.note)
		}
	}
}

// the audit attempt/ok pair comes from the shared gate: two lines per applied edit
func TestEditAppliedThroughTheGateIsAudited(t *testing.T) {
	s, p, au := editAt(100, 32)
	onTask(t, s, "ac-12")
	send(s, "c")
	typeIn(s, "hello")
	send(s, "enter")
	s.Update(taskVerifiedMsg{id: "ac-12", task: baseTask("ac-12")})
	yes(s)
	if len(*p.ran) != 1 || au.n != 2 {
		t.Fatalf("ran %d, audit %d", len(*p.ran), au.n)
	}
}

// ---- refusals before anything is planned stay in the form ----

func TestEditFormRefusesBadInputAndStaysOpen(t *testing.T) {
	cases := map[string]struct {
		task, key, text, in string
		clear               bool
	}{
		"label space":       {"ac-12", "t", "a b", "invalid label", false},
		"label comma":       {"ac-12", "t", "a,b", "invalid label", false},
		"label empty":       {"ac-12", "t", "", "invalid label", false},
		"priority text":     {"ac-12", "p", "high", "priority must be", true},
		"priority 9":        {"ac-12", "p", "9", "priority must be", true},
		"priority same":     {"ac-12", "p", "0", "already priority", true},
		"assign same":       {"ac-12", "a", "ada", "already assigned", true},
		"unassign nobody":   {"ac-09", "a", "", "already unassigned", true},
		"comment blank":     {"ac-12", "c", "   ", "needs text", false},
		"dep cycle":         {"ac-12", "b", "ac-09", "cycle", false},
		"dep chain":         {"ac-15", "b", "ac-16", "cycle", false},
		"dep self":          {"ac-12", "b", "ac-12", "itself", false},
		"dep unknown":       {"ac-12", "b", "zz-1", "not in this tracker", false},
		"dep duplicate":     {"ac-09", "b", "ac-12", "already linked", false},
		"dep hierarchy":     {"ac-12", "b", "ac-01", "already linked", false},
		"dep remove absent": {"ac-12", "B", "ac-09", "does not wait", false},
		"dep remove child":  {"ac-12", "B", "ac-01", "not as a blocker", false},
		"dep flag-like":     {"ac-12", "b", "--force", "not in this tracker", false},
	}
	for name, c := range cases {
		s, p, au := editAt(100, 32)
		onTask(t, s, c.task)
		send(s, c.key)
		if c.clear {
			clearField(s)
		}
		typeIn(s, c.text)
		send(s, "enter")
		if s.mode != modeForm || !strings.Contains(s.formErr, c.in) || ran(p) != "" || au.n != 0 {
			t.Errorf("%s: mode %v formErr %q ran %q (want the form open with %q)", name, s.mode, s.formErr, ran(p), c.in)
		}
		if v := s.View(); !strings.Contains(v, c.in) {
			t.Errorf("%s: the reason is on screen:\n%s", name, v)
		}
	}
}

// the tracker's real cycle (ac-20 <-> ac-21) is shown but never extended
func TestEditCycleRejectionNamesTheChain(t *testing.T) {
	s, _, _ := editAt(100, 32)
	onTask(t, s, "ac-12")
	send(s, "b")
	typeIn(s, "ac-09")
	send(s, "enter")
	if !strings.Contains(s.formErr, "ac-09 waits for ac-12 waits for ac-09") {
		t.Fatalf("%q", s.formErr)
	}
}

func TestEditNeedsTheWholeTrackerForDependencies(t *testing.T) {
	s, _, _ := editAt(100, 32)
	snap := fixtureSnapshot()
	snap.DepsUnknown = true
	s.Update(taskSnapshotMsg{snap: snap})
	onTask(t, s, "ac-12")
	send(s, "b")
	typeIn(s, "ac-15")
	send(s, "enter")
	if s.mode != modeForm || !strings.Contains(s.formErr, "not read") {
		t.Fatalf("mode %v %q", s.mode, s.formErr)
	}
}

// ---- the task changed: refused, never planned anew, never written ----

func TestEditRefusedWhenTheReReadShowsTheTaskChanged(t *testing.T) {
	gi := func(id string, mod func(*core.Task)) core.Task {
		x := baseTask(id)
		mod(&x)
		return x
	}
	cases := map[string]struct {
		task, key, text string
		clear           bool
		fresh           core.Task
	}{
		"label already there": {"ac-12", "t", "bug", false, gi("ac-12", func(x *core.Task) { x.Labels = []string{"bug"} })},
		"label already gone":  {"ac-12", "u", "core", false, gi("ac-12", func(x *core.Task) { x.Labels = nil })},
		"priority moved":      {"ac-12", "p", "3", true, gi("ac-12", func(x *core.Task) { x.Priority = 1 })},
		"priority now equal":  {"ac-12", "p", "3", true, gi("ac-12", func(x *core.Task) { x.Priority = 3 })},
		"assignee changed":    {"ac-12", "a", "mei", true, gi("ac-12", func(x *core.Task) { x.Assignee = "eve" })},
		"assignee freed":      {"ac-12", "a", "mei", true, gi("ac-12", func(x *core.Task) { x.Assignee = "" })},
		"edge already there":  {"ac-09", "b", "ac-15", false, gi("ac-09", func(x *core.Task) { x.DependsOn = []string{"ac-15"} })},
		"edge already gone":   {"ac-09", "B", "ac-12", false, gi("ac-09", func(x *core.Task) { x.DependsOn = nil })},
		"status moved":        {"ac-12", "c", "hi", false, gi("ac-12", func(x *core.Task) { x.Status = "blocked" })},
	}
	for name, c := range cases {
		s, p, au := editAt(100, 32)
		onTask(t, s, c.task)
		send(s, c.key)
		if c.clear {
			clearField(s)
		}
		typeIn(s, c.text)
		send(s, "enter")
		s.Update(taskVerifiedMsg{id: c.task, task: c.fresh})
		if s.mode != modeList || ran(p) != "" || au.n != 0 || !strings.Contains(s.notice, "nothing was written") {
			t.Errorf("%s: mode %v notice %q ran %q", name, s.mode, s.notice, ran(p))
		}
	}
}

func TestEditNeverConfirmedWhenTheTaskCannotBeReRead(t *testing.T) {
	s, p, au := editAt(100, 32)
	onTask(t, s, "ac-12")
	send(s, "c")
	typeIn(s, "hi")
	send(s, "enter")
	s.Update(taskVerifiedMsg{id: "ac-12", err: errors.New("bd show exited 1")})
	if s.mode != modeList || !strings.Contains(s.notice, "could not verify") || ran(p) != "" || au.n != 0 {
		t.Fatalf("%v %q", s.mode, s.notice)
	}
	// without a reader nothing opens a confirm screen at all
	pw := tasksFake()
	w := newTasksSection(core.Providers{Writer: pw}, &config{audit: &memAudit{}})
	w.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	w.Update(tasksMsg{report: demo.TasksReport()})
	w.Update(taskSnapshotMsg{snap: fixtureSnapshot()})
	onTask(t, w, "ac-12")
	send(w, "c")
	typeIn(w, "hi")
	send(w, "enter")
	if w.mode != modeList || !strings.Contains(w.notice, "could not verify") || ran(pw) != "" {
		t.Fatalf("%v %q", w.mode, w.notice)
	}
}

// a change by someone else between the confirm screen and y: the write is not run
func TestEditChangedBetweenConfirmAndYIsRefusedAndNothingRuns(t *testing.T) {
	for name, c := range map[string]struct {
		keys  []string
		text  string
		clear bool
		later core.Task
	}{
		"priority": {[]string{"p"}, "3", true, func() core.Task { x := baseTask("ac-12"); x.Priority = 1; return x }()},
		"assign":   {[]string{"a"}, "mei", true, func() core.Task { x := baseTask("ac-12"); x.Assignee = "eve"; return x }()},
		"label":    {[]string{"t"}, "bug", false, func() core.Task { x := baseTask("ac-12"); x.Labels = []string{"bug"}; return x }()},
		"comment":  {[]string{"c"}, "hi", false, func() core.Task { x := baseTask("ac-12"); x.Status = "closed"; return x }()},
	} {
		s, p, au := editAt(100, 32)
		onTask(t, s, "ac-12")
		send(s, c.keys...)
		if c.clear {
			clearField(s)
		}
		typeIn(s, c.text)
		send(s, "enter")
		s.Update(taskVerifiedMsg{id: "ac-12", task: baseTask("ac-12")})
		if s.mode != modeConfirm {
			t.Fatalf("%s: %v %q", name, s.mode, s.notice)
		}
		(*p.fresh)["ac-12"] = c.later
		yes(s)
		if ran(p) != "" || au.n != 0 || s.mode != modeList || !strings.Contains(s.notice, "nothing was written") {
			t.Errorf("%s: ran %q audit %d mode %v notice %q", name, ran(p), au.n, s.mode, s.notice)
		}
	}
	// and when the task cannot be read at that moment
	s, p, _ := editAt(100, 32)
	onTask(t, s, "ac-12")
	send(s, "c")
	typeIn(s, "hi")
	send(s, "enter")
	s.Update(taskVerifiedMsg{id: "ac-12", task: baseTask("ac-12")})
	*p.readErr = errors.New("gone")
	yes(s)
	if ran(p) != "" || !strings.Contains(s.notice, "nothing was written") {
		t.Errorf("unreadable at apply: %q ran %q", s.notice, ran(p))
	}
}

// ---- the prompt never writes ----

func TestPromptWithEditWordsNeverWritesOrOpensAForm(t *testing.T) {
	m := pm(t, 120, 36)
	m = press(m, "2")
	tasks := tasksSec(m)
	for _, line := range []string{"label add ac-12 bug", "assign ac-12 ada", "priority ac-12 0", "dep add ac-12 --blocked-by ac-15", "comment ac-12 hi", "tag ac-12 x", "t", "c hello"} {
		m = cmd(m, line)
		m = press(press(m, "esc"), "esc")
		if tasks.mode != modeList {
			t.Fatalf("%q put Tasks in mode %d", line, tasks.mode)
		}
	}
	if p, ok := tasksSec(m).writer.(taskProv); !ok || len(*p.ran) != 0 {
		t.Fatalf("the prompt wrote (or the writer is not the recorder): %v", ok)
	}
}

// a cycle made by someone else between the confirm screen and y is refused at apply
func TestEditDepCycleCreatedAfterTheConfirmIsRefusedAtApply(t *testing.T) {
	s, p, au := editAt(100, 32)
	onTask(t, s, "ac-12")
	send(s, "b")
	typeIn(s, "ac-30")
	send(s, "enter")
	s.Update(taskVerifiedMsg{id: "ac-12", task: baseTask("ac-12")})
	if s.mode != modeConfirm {
		t.Fatalf("%v %q", s.mode, s.notice)
	}
	snap := fixtureSnapshot() // ac-30 now waits for ac-12: ac-12 waiting for ac-30 closes a cycle
	for i := range snap.Issues {
		if snap.Issues[i].ID == "ac-30" {
			snap.Issues[i].Deps = append(snap.Issues[i].Deps, core.TaskDep{DependsOn: "ac-12", Type: "blocks"})
		}
	}
	s.src = freshSnap{snap}
	yes(s)
	if ran(p) != "" || au.n != 0 || !strings.Contains(s.notice, "cycle") {
		t.Errorf("ran %q audit %d notice %q", ran(p), au.n, s.notice)
	}
	// no source: a dependency edit is never applied unchecked
	s2, p2, _ := editAt(100, 32)
	onTask(t, s2, "ac-12")
	send(s2, "b")
	typeIn(s2, "ac-30")
	send(s2, "enter")
	s2.Update(taskVerifiedMsg{id: "ac-12", task: baseTask("ac-12")})
	s2.src = nil
	yes(s2)
	if ran(p2) != "" || !strings.Contains(s2.notice, "could not verify") {
		t.Errorf("ran %q notice %q", ran(p2), s2.notice)
	}
}

type freshSnap struct{ snap core.TaskSnapshot }

func (s freshSnap) Snapshot(context.Context) (core.TaskSnapshot, error) { return s.snap, nil }
