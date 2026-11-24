package tui

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/lambe"
)

// pm is the shell over fixture data, w x h, in the ascii theme.
func pm(t *testing.T, w, h int) tea.Model { return sized(load(t, asciiTheme(), nil), w, h) }

// typeLine presses the keys of a line, space included.
func typeLine(m tea.Model, line string) tea.Model {
	for _, r := range line {
		m = press(m, string(r))
	}
	return m
}

// cmd types ":line" and presses enter.
func cmd(m tea.Model, line string) tea.Model { return press(typeLine(press(m, ":"), line), "enter") }

func prm(m tea.Model) *prompt { return m.(*Model).prompt }

func footer(m tea.Model) string { l := lines(m); return l[len(l)-1] }

func tasksSec(m tea.Model) *tasksSection { return secAt(m, tabTasks).(*tasksSection) }

func TestPromptOpensOnEveryTabAndCloses(t *testing.T) {
	for i, tab := range []string{"Board", "Tasks", "Leases", "Queues", "Agents", "Doctor"} {
		m := press(pm(t, 100, 32), string(rune('1'+i)))
		m = press(m, ":")
		if prm(m) == nil {
			t.Fatalf("%s: : did not open the prompt", tab)
		}
		if f := footer(m); !strings.HasPrefix(f, ":") {
			t.Errorf("%s: footer %q does not show the prompt", tab, f)
		}
		m = press(m, "esc")
		if prm(m) != nil || activeTab(m) != tab {
			t.Errorf("%s: esc must close the prompt and stay put (tab %s)", tab, activeTab(m))
		}
	}
}

func TestPromptJumpsToTabs(t *testing.T) {
	for line, want := range map[string]string{"board": "Board", "tasks": "Tasks", "leases": "Leases", "queues": "Queues", "doctor": "Doctor", "agents": "Agents", "lease": "Leases", "TASKS": "Tasks"} {
		m := cmd(press(pm(t, 100, 32), "6"), line)
		if activeTab(m) != want || prm(m) != nil {
			t.Errorf(":%s -> tab %s, prompt open %v", line, activeTab(m), prm(m) != nil)
		}
	}
}

func TestPromptSelectsTasksViews(t *testing.T) {
	for line, want := range map[string]taskViewID{"graph": viewGraph, "insights": viewInsights, "list": viewList} {
		m := cmd(press(pm(t, 100, 32), "4"), line)
		s := tasksSec(m)
		if activeTab(m) != "Tasks" || s.list.cur != want {
			t.Errorf(":%s -> tab %s view %d, want Tasks view %d", line, activeTab(m), s.list.cur, want)
		}
	}
	m := cmd(press(pm(t, 100, 32), "2"), "list")
	if tasksSec(m).treeOn() {
		t.Error(":list leaves the tree on")
	}
	m = cmd(m, "tree")
	if !tasksSec(m).treeOn() || tasksSec(m).list.cur != viewList {
		t.Error(":tree must show the tree in the List view")
	}
	m = cmd(m, "graph")
	m = cmd(m, "list")
	if tasksSec(m).list.cur != viewList {
		t.Error(":list from the graph returns to the list")
	}
}

func TestPromptFilterAppliesAndClears(t *testing.T) {
	m := cmd(cmd(press(pm(t, 100, 32), "1"), "list"), "filter label:ui") // the flat list: the tree also shows the parents of a match
	s := tasksSec(m)
	if activeTab(m) != "Tasks" || s.list.text != "label:ui" || len(s.rows()) == 0 || len(s.rows()) >= len(fixtureSnapshot().Issues) {
		t.Fatalf("filter not applied: tab %s text %q rows %d", activeTab(m), s.list.text, len(s.rows()))
	}
	for _, r := range s.rows() {
		if !strings.Contains(strings.Join(r.Labels, ","), "ui") {
			t.Errorf("row %s does not match label:ui", r.ID)
		}
	}
	m = cmd(m, "filter")
	if tasksSec(m).list.text != "" {
		t.Errorf(":filter with no query must clear it, text %q", tasksSec(m).list.text)
	}
	// a bad filter keeps the prompt open and names the problem; nothing is applied
	m = cmd(press(pm(t, 100, 32), "2"), "filter nosuch:x")
	if prm(m) == nil || !strings.Contains(footer(m), "bad filter") || tasksSec(m).list.text != "" {
		t.Errorf("bad filter: prompt %v footer %q", prm(m) != nil, footer(m))
	}
}

func TestPromptSearchOpensTheOverlayWithText(t *testing.T) {
	m := cmd(pm(t, 100, 32), "search lease")
	if m.(*Model).search == nil || string(m.(*Model).search.text) != "lease" || prm(m) != nil {
		t.Fatalf("search overlay %v", m.(*Model).search)
	}
	if m.(*Model).search.rep.Total == 0 {
		t.Error("the search did not run")
	}
	m = cmd(press(m, "esc"), "search")
	if m.(*Model).search == nil || len(m.(*Model).search.text) != 0 {
		t.Error(":search with no text opens an empty search")
	}
}

func TestPromptHelpQuitAndContext(t *testing.T) {
	m := cmd(pm(t, 100, 32), "help")
	if !m.(*Model).showHelp {
		t.Error(":help must open the help overlay")
	}
	m, c := pm(t, 100, 32), tea.Cmd(nil)
	m = typeLine(press(m, ":"), "q")
	_, c = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if c == nil || c() != tea.Quit() {
		t.Error(":q must quit")
	}
	m = cmd(pm(t, 100, 32), "ctx")
	if f := footer(m); !strings.Contains(f, "fleet") {
		t.Errorf(":ctx should name the tracker: %q", f)
	}
	m = cmd(pm(t, 100, 32), "ctx other")
	if f := footer(m); !strings.Contains(f, "not available") {
		t.Errorf(":ctx NAME is not available yet: %q", f)
	}
}

func TestPromptUnknownCommandShowsOneLineAndChangesNothing(t *testing.T) {
	m := press(pm(t, 100, 32), "3")
	m = cmd(m, "bord")
	if prm(m) == nil {
		t.Fatal("an unknown command keeps the prompt open to fix it")
	}
	f := footer(m)
	if !strings.Contains(f, "unknown command: bord (try :help)") || !strings.Contains(f, "Did you mean :board?") {
		t.Errorf("footer %q", f)
	}
	if activeTab(m) != "Leases" {
		t.Errorf("an unknown command moved to %s", activeTab(m))
	}
	m = press(m, "d") // the next key clears the message and edits the text
	if prm(m) == nil || strings.Contains(footer(m), "unknown command") || string(prm(m).text) != "bordd" {
		t.Errorf("typing clears the error: %q text %q", footer(m), string(prm(m).text))
	}
}

func TestPromptEditingKeys(t *testing.T) {
	m := typeLine(press(pm(t, 100, 32), ":"), "tasx")
	m = press(m, "backspace")
	if string(prm(m).text) != "tas" {
		t.Errorf("backspace: %q", string(prm(m).text))
	}
	m = press(m, "ctrl+u")
	if string(prm(m).text) != "" {
		t.Errorf("ctrl+u: %q", string(prm(m).text))
	}
	m = press(m, "backspace") // on an empty line it closes, like k9s
	if prm(m) != nil {
		t.Error("backspace on an empty prompt closes it")
	}
	m = press(press(m, ":"), "enter") // an empty enter just closes
	if prm(m) != nil || activeTab(m) != "Board" {
		t.Error("an empty command closes and changes nothing")
	}
	m = typeLine(press(m, ":"), strings.Repeat("x", 400))
	if n := len(prm(m).text); n != 400 { // never cut silently: enter refuses it, with the reason
		t.Errorf("the line holds %d runes, want 400", n)
	}
	if m = press(m, "enter"); prm(m) == nil || !strings.Contains(footer(m), "too long") {
		t.Errorf("an over-long line is refused at enter: %q", footer(m))
	}
	m = press(m, "ctrl+u")
	m = press(press(m, "esc"), ":")
	if len(prm(m).text) != 0 {
		t.Error("a reopened prompt starts empty")
	}
}

func TestPromptCompletion(t *testing.T) {
	m := typeLine(press(pm(t, 100, 32), ":"), "lea")
	m = press(m, "tab")
	if string(prm(m).text) != "lease" { // the common prefix of leases and lease
		t.Errorf("lea<tab> = %q", string(prm(m).text))
	}
	if m = press(m, "tab"); string(prm(m).text) != "leases" {
		t.Errorf("lea<tab><tab> = %q", string(prm(m).text))
	}
	// a single candidate completes fully; a command with an argument gets its space
	m = press(press(press(pm(t, 100, 32), ":"), "f"), "i")
	m = press(m, "tab")
	if string(prm(m).text) != "filter " {
		t.Errorf("fi<tab> = %q", string(prm(m).text))
	}
	m = typeLine(m, "sta")
	m = press(m, "tab")
	if string(prm(m).text) != "filter status:" {
		t.Errorf("filter sta<tab> = %q", string(prm(m).text))
	}
	// several candidates: the common prefix first, then each in turn
	m = typeLine(press(pm(t, 100, 32), ":"), "ta")
	m = press(m, "tab")
	got := []string{string(prm(m).text)}
	m = press(m, "tab")
	got = append(got, string(prm(m).text))
	m = press(m, "tab")
	got = append(got, string(prm(m).text))
	if strings.Join(got, ",") != "task,tasks,task" {
		t.Errorf("ta<tab><tab><tab> = %v", got)
	}
	m = press(m, "shift+tab")
	if string(prm(m).text) == got[2] && len(core.CompleteCommand("ta")) > 1 {
		t.Errorf("shift+tab must step back, still %q", string(prm(m).text))
	}
	// the footer lists the candidates while there are several
	m = typeLine(press(pm(t, 100, 32), ":"), "q")
	if f := footer(m); !strings.Contains(f, "queues") || !strings.Contains(f, "quit") {
		t.Errorf("footer should list the candidates for q: %q", f)
	}
	// nothing to complete changes nothing
	m = typeLine(press(pm(t, 100, 32), ":"), "zz")
	m = press(m, "tab")
	if string(prm(m).text) != "zz" {
		t.Errorf("zz<tab> = %q", string(prm(m).text))
	}
	// any edit ends a cycle
	m = typeLine(press(pm(t, 100, 32), ":"), "ta")
	m = press(press(m, "tab"), "tab")
	m = press(m, "backspace")
	m = press(m, "tab")
	if strings.HasPrefix(string(prm(m).text), "tasks,") || prm(m).cycle > 1 {
		t.Errorf("a stale cycle survived an edit: %q", string(prm(m).text))
	}
}

func TestPromptHistory(t *testing.T) {
	m := pm(t, 100, 32)
	for _, l := range []string{"leases", "queues", "queues", "doctor"} {
		m = cmd(m, l)
	}
	if got := m.(*Model).promptHist; strings.Join(got, ",") != "leases,queues,doctor" {
		t.Fatalf("history %v (consecutive duplicates are dropped)", got)
	}
	m = press(m, ":")
	m = press(m, "up")
	if string(prm(m).text) != "doctor" {
		t.Errorf("up: %q", string(prm(m).text))
	}
	m = press(m, "up")
	m = press(m, "ctrl+p") // ctrl+p is up in the prompt, as it is in the search box
	if string(prm(m).text) != "leases" {
		t.Errorf("up, ctrl+p: %q", string(prm(m).text))
	}
	m = press(m, "up") // at the oldest it stays
	if string(prm(m).text) != "leases" {
		t.Errorf("oldest: %q", string(prm(m).text))
	}
	m = press(press(m, "down"), "ctrl+n")
	if string(prm(m).text) != "doctor" {
		t.Errorf("down, ctrl+n: %q", string(prm(m).text))
	}
	m = press(m, "down") // past the newest: back to what was being typed
	if string(prm(m).text) != "" {
		t.Errorf("past the newest: %q", string(prm(m).text))
	}
	m = press(typeLine(press(press(m, "esc"), ":"), "gra"), "up")
	m = press(m, "down")
	if string(prm(m).text) != "gra" {
		t.Errorf("the draft is restored: %q", string(prm(m).text))
	}
	// a failed command is not kept; a hundred are
	m = cmd(pm(t, 100, 32), "nonsense")
	if len(m.(*Model).promptHist) != 0 {
		t.Error("a failed command entered the history")
	}
	m = pm(t, 100, 32)
	for i := range 130 {
		m = cmd(m, []string{"board", "tasks"}[i%2])
	}
	if n := len(m.(*Model).promptHist); n != promptHistMax {
		t.Errorf("history holds %d, want %d", n, promptHistMax)
	}
}

func TestPromptCapturesEveryKey(t *testing.T) {
	m := press(pm(t, 100, 32), "3")
	m = press(m, ":")
	for _, k := range []string{"q", "1", "5", "?", "j", "/", "n", "x", "o", "r", "y", "d", "g"} {
		m = press(m, k)
		if activeTab(m) != "Leases" || prm(m) == nil {
			t.Fatalf("%q leaked out of the prompt (tab %s, open %v)", k, activeTab(m), prm(m) != nil)
		}
	}
	if string(prm(m).text) != "q15?j/nxoryd"+"g" {
		t.Errorf("typed %q", string(prm(m).text))
	}
	if m.(*Model).showHelp {
		t.Error("? opened help from inside the prompt")
	}
}

func TestPromptNeverOpensOverAScreenThatOwnsTheKeys(t *testing.T) {
	cases := map[string]func(tea.Model) tea.Model{
		"a form":            func(m tea.Model) tea.Model { return press(press(m, "2"), "n") },
		"an edit form":      func(m tea.Model) tea.Model { return press(press(m, "2"), "e") },
		"the filter":        func(m tea.Model) tea.Model { return press(press(m, "2"), "/") },
		"the queues filter": func(m tea.Model) tea.Model { return press(press(m, "4"), "/") },
		"search":            func(m tea.Model) tea.Model { return press(m, "ctrl+k") },
		"help":              func(m tea.Model) tea.Model { return press(m, "?") },
		"a describe":        func(m tea.Model) tea.Model { return press(press(m, "2"), "d") },
		"the confirm": func(m tea.Model) tea.Model {
			m = press(press(m, "2"), "x")
			m, _ = m.Update(taskVerifiedMsg{id: "ac-12", task: fixtureTasks().Recent[0]})
			return m
		},
	}
	for name, setup := range cases {
		m := setup(pm(t, 100, 32))
		before := activeTab(m)
		m = press(m, ":")
		if prm(m) != nil {
			t.Errorf("the prompt opened over %s", name)
		}
		if activeTab(m) != before {
			t.Errorf("%s: a colon changed the tab", name)
		}
	}
	// a confirm that is hidden (terminal too small) cannot be answered or skipped through the prompt
	m := press(press(pm(t, 100, 32), "2"), "x")
	m, _ = m.Update(taskVerifiedMsg{id: "ac-12", task: fixtureTasks().Recent[0]})
	m = sized(m, 30, 10)
	m = press(press(m, ":"), "q")
	if prm(m) != nil || tasksSec(m).mode != modeConfirm {
		t.Error("a hidden confirm was disturbed")
	}
	// too small: the prompt does not open at all
	m = press(sized(pm(t, 100, 32), 30, 10), ":")
	if prm(m) != nil {
		t.Error("the prompt opened on a screen that is not drawn")
	}
	// typing into a form field puts the colon in the field
	m = press(press(pm(t, 100, 32), "2"), "n")
	m = press(m, ":")
	if !strings.Contains(tasksSec(m).fields[0].value, ":") {
		t.Error("a colon in a form field is text")
	}
}

// ctrl+k and ctrl+p/ctrl+n: the modals never stack. Inside the prompt ctrl+k hands over to search; in
// search, : is text and ctrl+p/n recall searches; in the prompt they recall commands.
func TestPromptAndSearchPrecedence(t *testing.T) {
	m := press(typeLine(press(pm(t, 100, 32), ":"), "tas"), "ctrl+k")
	if prm(m) != nil || m.(*Model).search == nil {
		t.Error("ctrl+k in the prompt must replace it with search")
	}
	m = press(press(pm(t, 100, 32), "ctrl+k"), ":")
	if prm(m) != nil || string(m.(*Model).search.text) != ":" {
		t.Errorf("inside search : is text, got %q", string(m.(*Model).search.text))
	}
	m = pm(t, 100, 32)
	m = cmd(m, "leases")
	m = press(press(m, ":"), "ctrl+p")
	if string(prm(m).text) != "leases" {
		t.Error("ctrl+p in the prompt recalls a command")
	}
	m = press(press(m, "esc"), "ctrl+k")
	m = press(m, "ctrl+p")
	if m.(*Model).search == nil || prm(m) != nil {
		t.Error("ctrl+p in search stays in search")
	}
}

// ---- the prompt reaches no writer ----

// A session whose every writer is a recorder: the prompt is driven with every command, every
// alias, and hostile arguments, and nothing may be written, audited, applied or run.
func TestPromptReachesNoWriter(t *testing.T) {
	tasks, audit := tasksFake(), &memAudit{}
	brd, lease, ghost, demoQ := boardFake(), leaseFake(), brokenFake(), lambe.NewDemo()
	en := []core.Entry{
		{Module: brd, Detection: brd.Det}, {Module: demoQ, Detection: demoQ.Detect(context.Background())},
		{Module: lease, Detection: lease.Det}, {Module: tasks, Detection: tasks.Det}, {Module: ghost, Detection: ghost.Det},
	}
	var m tea.Model = New(en, WithTheme(asciiTheme()), WithFullView(func(string) tea.Cmd { t.Error("the full view was run"); return nil }), WithAudit(audit), WithSearchHistory(core.SearchHistory{}))
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	m, _ = m.Update(tasksMsg{report: fixtureTasks()})
	m, _ = m.Update(taskSnapshotMsg{snap: fixtureSnapshot()})
	var lines []string
	for _, c := range core.Commands() {
		for _, w := range append([]string{c.Name}, c.Aliases...) {
			lines = append(lines, w, w+" close ac-12", w+" x\x1b[2J; rm -rf /", w+" --title=boom")
		}
	}
	lines = append(lines, "close ac-12", "claim ac-12", "delete ac-12", "y", "yes", "apply", "x", "bd close ac-12", "tasks; y", "search close ac-12", "filter status:closed")
	for _, l := range lines {
		for _, tab := range []string{"1", "2", "3", "4", "5", "6"} {
			m = press(m, tab)
			m = cmd(m, l)
			for _, k := range []string{"esc", "esc", "esc"} {
				m = press(m, k)
			}
			if mm := m.(*Model); mm.search != nil || mm.prompt != nil || mm.showHelp || mm.over != nil {
				t.Fatalf("%q left a layer open", l)
			}
			if tasksSec(m).mode != modeList {
				t.Fatalf("%q put Tasks in mode %d", l, tasksSec(m).mode)
			}
		}
	}
	if n := len(*tasks.ran); n != 0 {
		t.Errorf("the prompt ran %d writes: %v", n, *tasks.ran)
	}
	if audit.n != 0 {
		t.Errorf("the prompt recorded %d audit entries", audit.n)
	}
}

// The files that make up the prompt name no writer, runner or process: a source-level guard beside
// the behavior test above.
func TestPromptSourcesNameNoWriter(t *testing.T) {
	// fragments of an identifier that mean writing, confirming, auditing, running or handing off
	banned := []string{"Appl", "Writ", "RunTask", "Argv", "Confirm", "Audit", "Exec", "Handoff", "handoff", "FullView", "fullView", "Plan", "Record", "Runner"}
	for _, f := range []string{"prompt.go", "describe.go", "jsonview.go", "header_context.go", "tasks_prompt.go"} {
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range af.Imports {
			if strings.Contains(imp.Path.Value, "os/exec") {
				t.Errorf("%s imports %s", f, imp.Path.Value)
			}
		}
		ast.Inspect(af, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				for _, b := range banned {
					if strings.Contains(id.Name, b) {
						t.Errorf("%s names %s", f, id.Name)
					}
				}
			}
			return true
		})
	}
}

// ---- the registry against the shell ----

func TestEveryCommandResolvesToARealTargetAndNamesNoTool(t *testing.T) {
	m := pm(t, 100, 32).(*Model)
	views := map[string]bool{"tree": true}
	for _, v := range taskViews {
		views[strings.ToLower(v.name)] = true
	}
	for _, c := range core.Commands() {
		switch c.Kind {
		case core.ActGoTab, core.ActGoView, core.ActFilter:
			found := false
			for _, tb := range m.tabs {
				found = found || tb.name == c.Tab
			}
			if !found {
				t.Errorf("%s targets the tab %q, which does not exist", c.Name, c.Tab)
			}
		}
		if c.Kind == core.ActGoView && !views[c.View] {
			t.Errorf("%s targets the view %q, which does not exist", c.Name, c.View)
		}
		for _, w := range []string{c.Name, c.Desc, c.Arg.Name, strings.Join(c.Aliases, " ")} {
			for _, tool := range []string{"bd ", " bd", "bv", "lambe", "beads", "b9s", "rex"} {
				if strings.Contains(" "+strings.ToLower(w)+" ", tool) {
					t.Errorf("%s: %q names a tool (%s)", c.Name, w, tool)
				}
			}
		}
	}
}
