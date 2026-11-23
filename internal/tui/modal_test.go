package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
	"github.com/andrew-bierman/abacus/internal/lambe"
)

// ---- helpers ----

// tasksShell is the Tasks tab in the shell at w x h, with the section behind it.
func tasksShell(t *testing.T, w, h int) (tea.Model, tasksRig, *tasksSection) {
	t.Helper()
	r := newTasksRig(t)
	m := sized(r.m, w, h)
	return m, r, m.(*Model).tabs[1].sec.(*tasksSection)
}

// panelRows is the rows of the first framed panel in view, edge to edge.
func panelRows(t *testing.T, view string) []string {
	t.Helper()
	var out []string
	in := false
	for _, l := range strings.Split(view, "\n") {
		l = strings.TrimRight(l, " ")
		switch {
		case strings.HasPrefix(l, "╭"):
			in = true
		case !in:
			continue
		}
		out = append(out, l)
		if strings.HasPrefix(l, "╰") {
			return out
		}
	}
	t.Fatalf("no closed panel in:\n%s", view)
	return nil
}

// panelText is the text inside a panel with the frame and all whitespace removed, so wrapped text can be
// compared with what it was wrapped from.
func panelText(rows []string) string {
	var b strings.Builder
	for _, l := range rows[1 : len(rows)-1] {
		l = strings.TrimPrefix(strings.TrimSuffix(l, "│"), "│")
		b.WriteString(strings.Join(strings.Fields(l), ""))
	}
	return b.String()
}

// oneFramedPanel checks view holds exactly one panel, titled title, whose every row is inside the frame.
func oneFramedPanel(t *testing.T, name, view, title string, w int) []string {
	t.Helper()
	hostileOK(t, name, view, w)
	if n := strings.Count(view, "╭"); n != 1 {
		t.Errorf("%s: want one panel, found %d:\n%s", name, n, view)
	}
	rows := panelRows(t, view)
	if !strings.Contains(rows[0], "─ ▸ "+title+" ─") && !strings.Contains(rows[0], "─ ▸ "+title) {
		t.Errorf("%s: the title %q is not in the top border: %q", name, title, rows[0])
	}
	for _, r := range rows[1 : len(rows)-1] {
		if !strings.HasPrefix(r, "│") || !strings.HasSuffix(r, "│") {
			t.Errorf("%s: row not inside the frame: %q", name, r)
		}
	}
	return rows
}

// formatting is hostile text a title may legally carry: invisible and direction-changing runes.
const formatting = "x\u202etxt\u200b\u200d\ufeffy日本語"

const (
	hostile = "x\x1b[2J\x1b]0;pwn\x07\u202etxt\u200b\u200d\ufeffy\x00日本語"
	long200 = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789" +
		"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789" +
		"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789" +
		"abcdefghijklmnopqrstuvwxyzABCDEFGHIJ"
)

func long300() string { return long200 + long200[:100] }

// every framed screen of the Tasks tab and the guided prompts, by the title in its border.
func framedScreens(t *testing.T, w, h int) map[string]struct{ view, title string } {
	t.Helper()
	out := map[string]struct{ view, title string }{}
	add := func(name, title, view string) { out[name] = struct{ view, title string }{view, title} }

	shell := func() tea.Model { m, _, _ := tasksShell(t, w, h); return m } // a model is mutated in place: one per screen
	confirm := func() tea.Model {
		m := press(shell(), "x")
		m, _ = m.Update(taskVerifiedMsg{id: "ac-12", task: fixtureTasks().Recent[0]})
		return m
	}
	add("new", "New task", press(shell(), "n").View())
	add("edit", "Edit task ac-12", press(shell(), "e").View())
	add("verify", "Checking the current state…", press(shell(), "x").View())
	add("confirm", "Confirm this change", confirm().View())
	stale := press(shell(), "x")
	fresh := fixtureTasks().Recent[0]
	fresh.Status = "blocked"
	stale, _ = stale.Update(taskVerifiedMsg{id: "ac-12", task: fresh})
	add("confirm-stale", "Confirm this change", stale.View())
	add("busy", "Applying this change", press(confirm(), "y").View())

	g, _ := guidedRig(t, &freshBd{Fake: &modtest.Fake{IDStr: "bd"}})
	g = sized(g, w, h)
	add("guided", "Tasks", g.View())
	add("guided-confirm", "Confirm this setup step", press(g, "i").View())

	nc := newNotConnectedSection(core.Header{Title: "Board", Reason: "no board here", HowToConnect: "run abacus config set board_url <url>"}, nil)
	nc.SetTheme(asciiTheme())
	nc.Update(tea.WindowSizeMsg{Width: w, Height: h})
	add("not-connected", "Board", nc.View())

	ls := leasesWith(&leasesRead{unavailable: "no hub", howToConnect: "start the hub"})
	ls.SetTheme(asciiTheme())
	ls.Update(tea.WindowSizeMsg{Width: w, Height: h})
	ls.Update(leasesMsg{read: leasesRead{unavailable: "no hub", howToConnect: "start the hub"}})
	add("leases-not-connected", "Leases", ls.View())
	ls.Update(leasesMsg{read: leasesRead{unavailable: "read failed"}})
	add("leases-unavailable", "Leases", ls.View())

	q := newLambeSection(lambe.NewDemo(), nil)
	q.SetTheme(asciiTheme())
	q.Update(tea.WindowSizeMsg{Width: w, Height: h})
	q.Update(lambeViewMsg{err: lambe.ErrNoDaemon})
	add("queues-not-connected", "Queues", q.View())
	q.Update(lambeViewMsg{err: errors.New("read failed")})
	add("queues-unavailable", "Queues", q.View())

	b := boardAt(w, h, core.BoardReport{})
	b, _ = b.Update(boardMsg{err: &core.NotConnectedError{Reason: "no board selected", HowToConnect: "set a board url"}})
	add("board-not-connected", "Board", b.View())
	b, _ = b.Update(boardMsg{err: errors.New("could not reach the board")})
	add("board-unavailable", "Board", b.View())

	_, _, ts := tasksShell(t, w, h)
	ts.SetTheme(asciiTheme())
	ts.Update(tea.WindowSizeMsg{Width: w, Height: h})
	ts.Update(tasksMsg{err: errors.New("tracker down")})
	add("tasks-unavailable", "Tasks", ts.View())
	return out
}

// ---- layout ----

func TestFramedScreensAreOnePanelAtEveryBreakpoint(t *testing.T) {
	for _, w := range []int{40, 60, 100, 140} {
		for name, sc := range framedScreens(t, w, 30) {
			oneFramedPanel(t, fmt.Sprintf("%s %d", name, w), sc.view, sc.title, w)
		}
	}
}

func TestFramedScreensFitInEveryGridSizeWithoutOverflow(t *testing.T) {
	for _, w := range []int{1, 3, 5, 39, 40, 41, 59, 60, 99, 100, 140, 200} {
		for _, h := range []int{1, 2, 3, 5, 11, 12, 13, 24} {
			for name, sc := range framedScreens(t, w, h) {
				for i, l := range strings.Split(strings.TrimSuffix(sc.view, "\n"), "\n") {
					if lipgloss.Width(l) > w {
						t.Errorf("%s %dx%d row %d is %d wide: %q", name, w, h, i, lipgloss.Width(l), l)
					}
				}
			}
		}
	}
}

func TestFormValuesAlignInAColumnAndStackWhenNarrow(t *testing.T) {
	th := asciiTheme()
	fields := []formField{{label: "Title", value: "AAA"}, {label: "Description (blank: keep)", value: "BBB"}, {label: "Priority 0-4", value: "CCC"}}
	for _, inner := range []int{56, 96} {
		rows := fieldRows(th, fields, 0, inner)
		cell := func(row, v string) int { return lipgloss.Width(row[:max(strings.Index(row, v), 0)]) }
		col := cell(rows[0], "AAA")
		if col <= 0 || cell(rows[1], "BBB") != col || cell(rows[2], "CCC") != col {
			t.Errorf("inner %d: values must start in one column:\n%s", inner, strings.Join(rows, "\n"))
		}
		if len(rows) != 3 {
			t.Errorf("inner %d: one row per field: %d", inner, len(rows))
		}
	}
	rows := fieldRows(th, fields, 0, 36)
	if len(rows) != 6 || !strings.Contains(rows[0], "Title") || strings.TrimSpace(rows[1]) != "AAA▏" || !strings.HasPrefix(rows[1], "  ") {
		t.Errorf("narrow: each label sits above its value:\n%s", strings.Join(rows, "\n"))
	}
	// the switch happens exactly where the value column would get narrower than fieldLabelMin
	lw := len("Description (blank: keep)")
	edge := 2 + lw + 2 + fieldLabelMin
	if n := len(fieldRows(th, fields, 0, edge)); n != 3 {
		t.Errorf("inner %d is the narrowest aligned layout: %d rows", edge, n)
	}
	if n := len(fieldRows(th, fields, 0, edge-1)); n != 6 {
		t.Errorf("inner %d is stacked: %d rows", edge-1, n)
	}
}

// A long value wraps inside its own column: in the aligned layout the continuation rows sit under
// the first row's value, and in the stacked layout they keep the two-cell indent; no row is wider
// than the panel's inner width, so the panel never has to cut one.
func TestLongFormValueWrapsUnderItsColumn(t *testing.T) {
	th := asciiTheme()
	fields := []formField{{label: "Description (blank: keep)", value: long200}}
	for _, tc := range []struct{ inner, first, indent int }{
		{inner: 30, first: 2, indent: 2}, // stacked: the value starts on the second row, indented two
		{inner: 40, first: 2, indent: 2},
		{inner: 41, first: 1, indent: 29}, // aligned: marker, the 25-cell label and a gap
		{inner: 56, first: 1, indent: 29},
		{inner: 96, first: 1, indent: 29},
	} {
		rows := fieldRows(th, fields, 0, tc.inner)
		var got strings.Builder
		for i, r := range rows {
			if lipgloss.Width(r) > tc.inner {
				t.Errorf("inner %d: row %d is %d wide: %q", tc.inner, i, lipgloss.Width(r), r)
			}
			if i >= tc.first {
				if lead := len(r) - len(strings.TrimLeft(r, " ")); lead != tc.indent {
					t.Errorf("inner %d: row %d starts at column %d, want %d: %q", tc.inner, i, lead, tc.indent, r)
				}
			}
			got.WriteString(strings.Join(strings.Fields(r), ""))
		}
		if !strings.Contains(got.String(), long200+"▏") {
			t.Errorf("inner %d: the value is cut:\n%s", tc.inner, strings.Join(rows, "\n"))
		}
	}
}

func TestConfirmBodySanitizesEveryString(t *testing.T) {
	th := asciiTheme()
	for _, inner := range []int{36, 96} {
		body := confirmBody(th, hostile, []string{hostile, "ok"}, []string{"bd", "--title=" + hostile}, true, inner)
		hostileOK(t, "confirm body", strings.Join(body, "\n"), inner)
		hostileOK(t, "not connected body", strings.Join(notConnectedBody(th, hostile, hostile, inner), "\n"), inner)
		hostileOK(t, "unavailable body", strings.Join(unavailableBody(th, hostile, inner), "\n"), inner)
		fields := []formField{{label: "Title", value: hostile}}
		hostileOK(t, "fields", strings.Join(fieldRows(th, fields, 0, inner), "\n"), inner)
	}
}

func TestFocusAndCaretAreVisibleWithoutColor(t *testing.T) {
	th := asciiTheme()
	fields := []formField{{label: "Title", value: "a"}, {label: "Description"}, {label: "Priority 0-4"}}
	for fi := range fields {
		for _, inner := range []int{36, 90} {
			rows := strings.Join(fieldRows(th, fields, fi, inner), "\n")
			if strings.Count(rows, "▸") != 1 || strings.Count(rows, "▏") != 1 {
				t.Errorf("focus %d inner %d: exactly one marker and one caret:\n%s", fi, inner, rows)
			}
			for _, l := range strings.Split(rows, "\n") {
				if strings.Contains(l, "▸") != strings.Contains(l, fields[fi].label) && strings.Contains(l, fields[fi].label) {
					t.Errorf("focus %d inner %d: the marker is on the focused label's row only: %q", fi, inner, l)
				}
			}
		}
	}
	m, _, _ := tasksShell(t, 100, 30)
	m = press(m, "n")
	m = press(m, "tab")
	v := m.View()
	if strings.Count(v, "▏") != 1 || !strings.Contains(v, "▸ Description") {
		t.Errorf("tab moves the marker and the caret to Description:\n%s", v)
	}
}

func TestFormErrorSitsUnderTheFields(t *testing.T) {
	m, _, s := tasksShell(t, 80, 30)
	m = press(m, "e")
	s.fields[0].value, s.fields[0].edited = "", true
	m = press(m, "enter")
	m = press(m, "enter")
	m = press(m, "enter")
	m = press(m, "enter")
	rows := oneFramedPanel(t, "form error", m.View(), "Edit task ac-12", 80)
	text := strings.Join(rows, "\n")
	iErr, iStatus := strings.Index(text, "title must not be empty"), strings.Index(text, "Status")
	if iErr < 0 || iErr < iStatus {
		t.Errorf("the error belongs under the last field:\n%s", text)
	}
}

func TestLongTitleWrapsInTheFormAndNeverClips(t *testing.T) {
	for _, w := range []int{40, 60, 100} {
		m, _, _ := tasksShell(t, w, 40)
		m = press(m, "n")
		m = typeText(m, long200)
		rows := oneFramedPanel(t, fmt.Sprintf("form %d", w), m.View(), "New task", w)
		if got := panelText(rows); !strings.Contains(got, long200+"▏") {
			t.Errorf("w=%d: the 200-character title is cut:\n%s", w, strings.Join(rows, "\n"))
		}
	}
}

func TestLongTitleAndArgvWrapOnTheConfirmScreenAndNeverClip(t *testing.T) {
	for _, title := range []string{long200, long300()} {
		for _, w := range []int{40, 60, 100, 140} {
			m, _, _ := tasksShell(t, w, 60)
			m = press(m, "n")
			m = typeText(m, title)
			m = press(m, "enter")
			m = press(m, "enter")
			m = press(m, "enter")
			rows := oneFramedPanel(t, fmt.Sprintf("confirm %d/%d", len(title), w), m.View(), "Confirm this change", w)
			got := panelText(rows)
			// once in the plan line and once in the exact command
			if n := strings.Count(got, title); n < 2 {
				t.Errorf("%d-char title at w=%d appears %d times, want it whole in the plan and in the command:\n%s", len(title), w, n, strings.Join(rows, "\n"))
			}
			if !strings.Contains(got, "exactcommand:") {
				t.Errorf("w=%d: the exact command label is missing", w)
			}
		}
	}
}

func TestConfirmScreenShowsPlanNoteAndCommandInOrder(t *testing.T) {
	m, r, _ := tasksShell(t, 100, 40)
	(*r.prov.fresh)["ac-12"] = core.Task{ID: "ac-12", Title: "wire the lease heartbeat v2", Status: "blocked", Priority: 1}
	m = pressRun(t, m, "x")
	rows := oneFramedPanel(t, "stale", m.View(), "Confirm this change", 100)
	text := strings.Join(rows, "\n")
	order := []string{"changed since you opened it", "close task ac-12", "exact command:", "bd close ac-12"}
	last := -1
	for _, want := range order {
		i := strings.Index(text, want)
		if i < 0 || i < last {
			t.Fatalf("%q missing or out of order in:\n%s", want, text)
		}
		last = i
	}
}

func TestConfirmWithoutANoteHasNoBlankFirstRow(t *testing.T) {
	m, _, _ := tasksShell(t, 100, 40)
	m = pressRun(t, m, "x")
	rows := oneFramedPanel(t, "plain confirm", m.View(), "Confirm this change", 100)
	if strings.TrimSpace(strings.Trim(rows[1], "│")) == "" {
		t.Errorf("the first row inside the frame is the plan, not a blank:\n%s", strings.Join(rows, "\n"))
	}
	if strings.Contains(strings.Join(rows, "\n"), "changed since") {
		t.Error("no note when nothing changed")
	}
}

func TestBusyStateKeepsThePlanAndCommandVisible(t *testing.T) {
	m, r, _ := tasksShell(t, 100, 40)
	m = pressRun(t, m, "x")
	m = press(m, "y") // the apply command is not run: the screen stays busy
	rows := oneFramedPanel(t, "busy", m.View(), "Applying this change", 100)
	text := strings.Join(rows, "\n")
	for _, want := range []string{"close task ac-12", "exact command:", "bd close ac-12", "applying…"} {
		if !strings.Contains(text, want) {
			t.Errorf("busy screen lacks %q:\n%s", want, text)
		}
	}
	if len(*r.prov.ran) != 0 {
		t.Fatal("the test must not apply anything")
	}
}

// A body taller than the panel is clipped with a "+N more" row, never silently cut.
func TestBoxClipsWithAMoreRowAndNeverExceedsTheHeight(t *testing.T) {
	th := asciiTheme()
	body := []string{"1", "2", "3", "4", "5", "6"}
	out := box(th, "T", body, 20, 6)
	if len(out) != 6 || !strings.Contains(out[len(out)-2], "+") || !strings.Contains(out[len(out)-2], "more") {
		t.Errorf("a clipped body ends in a more row inside the frame:\n%s", strings.Join(out, "\n"))
	}
	if out := box(th, "T", body, 20, 8); len(out) != 8 || strings.Contains(strings.Join(out, "\n"), "more") {
		t.Errorf("a body that fits has no more row:\n%s", strings.Join(out, "\n"))
	}
	if out := box(th, "T", body, 20, 0); len(out) != 8 {
		t.Errorf("height not known: as tall as the body: %d rows", len(out))
	}
	if out := box(th, "T", body, 20, 2); len(out) > 2 {
		t.Errorf("a body too short for a frame is plain lines no taller than it: %d rows", len(out))
	}
	if out := box(th, "T", body, 3, 30); len(out) == 0 || strings.Contains(strings.Join(out, ""), "╭") {
		t.Errorf("a body too narrow for a frame is plain lines: %v", out)
	}
}

// ---- hostile strings through every new string ----

func TestHostileTextNeverReachesTheFormVerifyOrConfirmScreens(t *testing.T) {
	for _, w := range []int{40, 100} {
		m, r, s := tasksShell(t, w, 40)
		// typed into the form (runes are sent as they come)
		m = press(m, "n")
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(hostile)})
		hostileOK(t, fmt.Sprintf("form value %d", w), m.View(), w)
		s.formErr = hostile
		hostileOK(t, fmt.Sprintf("form error %d", w), m.View(), w)
		for range []rune(hostile) { // the plan refuses control characters: retype a title it accepts
			m = press(m, "backspace")
		}
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(formatting)})
		m = press(m, "enter")
		m = press(m, "enter")
		m = press(m, "enter")
		hostileOK(t, fmt.Sprintf("confirm plan and command %d", w), m.View(), w)
		if !strings.Contains(m.View(), "Confirm this change") {
			t.Fatalf("w=%d: no confirm screen:\n%s", w, m.View())
		}
		m = press(m, "esc")

		// an unreadable issue: the error text is the note
		*r.prov.readErr = errors.New(hostile)
		m = pressRun(t, m, "x")
		hostileOK(t, fmt.Sprintf("confirm note %d", w), m.View(), w)
		*r.prov.readErr = nil
	}
}

func TestHostileIssueIDReachesNeitherVerifyNorEditScreens(t *testing.T) {
	for _, w := range []int{40, 100} {
		m, _, _ := tasksShell(t, w, 40)
		rep := fixtureTasks()
		rep.Recent[0].ID, rep.Recent[0].Title = hostile, hostile
		m, _ = m.Update(tasksMsg{report: rep})
		hostileOK(t, "edit "+fmt.Sprint(w), press(m, "e").View(), w)
		hostileOK(t, "verify "+fmt.Sprint(w), press(m, "x").View(), w)
	}
}

func TestHostileTextNeverReachesTheVerifyScreenOrALongFormError(t *testing.T) {
	for _, w := range []int{40, 100} {
		_, _, s := tasksShell(t, w, 40)
		s.Update(tea.WindowSizeMsg{Width: w, Height: 40})
		s.mode, s.pending = modeVerify, core.TaskChange{ID: hostile}
		hostileOK(t, "verify", s.View(), w)

		s.mode, s.op, s.fields, s.formErr = modeForm, core.TaskCreate, []formField{{label: "Title"}}, long200+" "+long200
		rows := oneFramedPanel(t, fmt.Sprintf("long form error %d", w), s.View(), "New task", w)
		if got := panelText(rows); !strings.Contains(got, long200+long200) {
			t.Errorf("w=%d: a long form error wraps and is never cut:\n%s", w, strings.Join(rows, "\n"))
		}
	}
}

func TestHostileTextNeverReachesTheGuidedAndNotConnectedPanels(t *testing.T) {
	for _, w := range []int{40, 100} {
		g := newGuidedSection(core.DomainTasks, newTasksSection(core.Providers{}, &config{}), nil)
		g.SetTheme(asciiTheme())
		g.Update(tea.WindowSizeMsg{Width: w, Height: 40})
		g.Update(guideMsg{domain: core.DomainTasks, g: &core.Guide{Domain: core.DomainID(hostile), Step: core.StepWorkspace, Message: hostile, Action: hostile, Detail: hostile}})
		g.notice = hostile
		hostileOK(t, "guided", g.View(), w)

		nc := newNotConnectedSection(core.Header{Title: hostile, Reason: hostile, HowToConnect: hostile}, nil)
		nc.SetTheme(asciiTheme())
		nc.Update(tea.WindowSizeMsg{Width: w, Height: 40})
		nc.notice = hostile
		hostileOK(t, "not connected", nc.View(), w)

		ls := leasesWith(new(leasesRead))
		ls.SetTheme(asciiTheme())
		ls.Update(tea.WindowSizeMsg{Width: w, Height: 40})
		ls.Update(leasesMsg{read: leasesRead{unavailable: hostile, howToConnect: hostile}})
		hostileOK(t, "leases not connected", ls.View(), w)
		ls.Update(leasesMsg{read: leasesRead{unavailable: hostile}})
		hostileOK(t, "leases unavailable", ls.View(), w)

		b := boardAt(w, 40, core.BoardReport{})
		b, _ = b.Update(boardMsg{err: &core.NotConnectedError{Reason: hostile, HowToConnect: hostile + "!"}})
		hostileOK(t, "board not connected", b.View(), w)
		b, _ = b.Update(boardMsg{err: errors.New(hostile)})
		hostileOK(t, "board unavailable", b.View(), w)

		ts, _, tsec := tasksShell(t, w, 40)
		tsec.Update(tasksMsg{err: errors.New(hostile)})
		tsec.notice = hostile
		hostileOK(t, "tasks unavailable", ts.View(), w)
	}
}

// ---- not-connected: the how-to-connect block stays blank-line delimited ----

func TestHowToConnectIsABlankDelimitedBlockInsideTheFrame(t *testing.T) {
	for _, w := range []int{40, 60, 100} {
		for _, name := range []string{"not-connected", "leases-not-connected", "queues-not-connected", "board-not-connected"} {
			sc := framedScreens(t, w, 40)[name]
			rows := panelRows(t, sc.view)
			at := -1
			for i, r := range rows {
				if strings.Contains(r, "how to connect:") {
					at = i
				}
			}
			if at < 0 {
				t.Fatalf("%s %d: no how-to-connect row:\n%s", name, w, sc.view)
			}
			if !blankRow(rows[at-1]) {
				t.Errorf("%s %d: the how-to-connect block starts after a blank row, got %q", name, w, rows[at-1])
			}
			// what follows it ends in a blank row or the bottom edge, never another text row after one
			end := at
			for end < len(rows) && !blankRow(rows[end]) {
				end++
			}
			for _, r := range rows[end:] {
				if !blankRow(r) {
					t.Errorf("%s %d: text after the how-to-connect block: %q", name, w, r)
				}
			}
			// the exemption in outsideAllowlist ends where the block does
			if rest := outsideAllowlist(strings.Join(rows[end:], "\n")); len(rest) != len(rows[end:]) {
				t.Errorf("%s %d: outsideAllowlist must resume at the end of the block", name, w)
			}
		}
	}
}

func TestNotConnectedSectionScrollsToItsLastRowInAShortFrame(t *testing.T) {
	long := strings.Repeat("connect it with a very long instruction ", 6) + "THE-END"
	s := newNotConnectedSection(core.Header{Title: "Board", Reason: "no board", HowToConnect: long}, nil)
	s.SetTheme(asciiTheme())
	s.Update(tea.WindowSizeMsg{Width: 40, Height: 7})
	if strings.Contains(s.View(), "THE-END") {
		t.Fatalf("the test needs the end clipped at first:\n%s", s.View())
	}
	if !strings.Contains(s.View(), "more") {
		t.Fatalf("clipped text ends in a more row:\n%s", s.View())
	}
	for i := 0; i < 20; i++ {
		s.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	v := s.View()
	if !strings.Contains(v, "THE-END") {
		t.Fatalf("down must reach the last row of text:\n%s", v)
	}
	if got := len(strings.Split(strings.TrimSuffix(v, "\n"), "\n")); got > 7 {
		t.Fatalf("the frame is %d rows in a 7-row body", got)
	}
	for i := 0; i < 20; i++ {
		s.Update(tea.KeyMsg{Type: tea.KeyUp})
	}
	if !strings.Contains(s.View(), "not connected") {
		t.Fatalf("up returns to the top:\n%s", s.View())
	}
}

// ---- the write gate is unchanged by the framing ----

func TestBoardNotConnectedWithNothingToAddSaysItOnce(t *testing.T) {
	b := boardAt(80, 30, core.BoardReport{})
	b, _ = b.Update(boardMsg{err: &core.NotConnectedError{Reason: "no board selected", HowToConnect: "no board selected"}})
	v := b.View()
	if strings.Count(v, "no board selected") != 1 || strings.Contains(v, "how to connect:") {
		t.Errorf("a how-to that repeats the reason is not shown twice:\n%s", v)
	}
}

func TestGuidedBusyAndPromptScreens(t *testing.T) {
	g, _ := guidedRig(t, &freshBd{Fake: &modtest.Fake{IDStr: "bd"}})
	if v := g.View(); !strings.Contains(v, "(needs setup)") {
		t.Errorf("the prompt names its state without color:\n%s", v)
	}
	g = press(press(g, "i"), "y") // the apply command is not run: the screen stays busy
	rows := oneFramedPanel(t, "guided busy", g.View(), "Applying this setup step", 100)
	if text := strings.Join(rows, "\n"); !strings.Contains(text, "applying…") || !strings.Contains(text, "exact command:") {
		t.Errorf("busy keeps the command and says it is applying:\n%s", text)
	}
}

func TestFramedScreensNeverWriteBeforeY(t *testing.T) {
	m, r, _ := tasksShell(t, 60, 24)
	for _, k := range []string{"n", "x", "e"} {
		m = pressRun(t, m, k)
		for _, size := range [][2]int{{60, 24}, {40, 12}, {100, 32}} {
			m = sized(m, size[0], size[1])
			_ = m.View()
		}
		for _, other := range []string{"enter", "tab", "backspace", "esc"} {
			m = pressRun(t, m, other)
		}
		m = pressRun(t, m, "esc")
	}
	if n := len(*r.prov.ran); n != 0 {
		t.Fatalf("rendering, resizing and navigating wrote %d times", n)
	}
	if r.audit.n != 0 {
		t.Fatalf("audit entries before any apply: %d", r.audit.n)
	}
}
