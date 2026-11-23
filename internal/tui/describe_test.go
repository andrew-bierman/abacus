package tui

import (
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/andrew-bierman/abacus/internal/core"
)

func overOf(m tea.Model) *viewer { return m.(*Model).over }

func TestDescribeShowsTheSelectedTaskReadOnly(t *testing.T) {
	m := press(pm(t, 100, 32), "2")
	m = press(m, "T") // flat, so the first row is a known task
	s := tasksSec(m)
	id := s.rows()[s.cursor].ID
	m = press(m, "d")
	v := overOf(m)
	if v == nil {
		t.Fatal("d did not open the panel")
	}
	detail, _ := s.list.view.Detail(id)
	want := strings.Join(s.detailLines(detail, 90), "\n")
	if got := strings.Join(v.content(90), "\n"); got != want {
		t.Errorf("describe differs from the detail pane:\n%s\n--- want\n%s", got, want)
	}
	out := strings.Join(lines(m), "\n")
	if !strings.Contains(out, "Task "+id) || !strings.Contains(footer(m), "esc close") {
		t.Errorf("panel title or footer missing:\n%s", out)
	}
	if tasksSec(m).mode != modeList {
		t.Error("describe changed the Tasks mode")
	}
	for _, k := range []string{"j", "k", "down", "up", "x", "n", "e", "/", "1", "?", ":", "ctrl+k"} { // nothing but scrolling and closing does anything
		m = press(m, k)
		if overOf(m) == nil || tasksSec(m).mode != modeList || activeTab(m) != "Tasks" || prm(m) != nil || m.(*Model).search != nil {
			t.Fatalf("%q acted through the panel", k)
		}
	}
	m = press(m, "esc")
	if overOf(m) != nil {
		t.Error("esc must close the panel")
	}
	for _, closer := range []string{"q", "d", "y"} {
		m = press(press(m, "d"), closer)
		if overOf(m) != nil {
			t.Errorf("%s did not close the panel", closer)
		}
	}
	// ctrl+c still quits
	m = press(m, "d")
	if _, c := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); c == nil || c() != tea.Quit() {
		t.Error("ctrl+c must quit even from the panel")
	}
}

func TestJSONPanelIsTheTypedReport(t *testing.T) {
	m := press(press(pm(t, 100, 32), "2"), "T")
	s := tasksSec(m)
	id := s.rows()[s.cursor].ID
	detail, _ := s.list.view.Detail(id)
	want, err := core.ReportJSON(detail)
	if err != nil {
		t.Fatal(err)
	}
	m = press(m, "y")
	v := overOf(m)
	if v == nil {
		t.Fatal("y did not open the panel")
	}
	if got := strings.Join(v.content(10000), "\n") + "\n"; got != string(want) {
		t.Errorf("JSON panel differs from core.ReportJSON (what --json prints):\n%s\n--- want\n%s", got, want)
	}
	if !strings.Contains(string(want), `"schema_version": 1`) || !strings.Contains(string(want), `"`+id+`"`) {
		t.Error("the report is missing schema_version or the id")
	}
	if !strings.Contains(strings.Join(lines(m), "\n"), "JSON · Task "+id) {
		t.Error("title")
	}
}

func TestDescribeOnTheGraphUsesTheSelectedNode(t *testing.T) {
	m := press(press(pm(t, 100, 32), "2"), "g")
	s := tasksSec(m)
	if s.list.cur != viewGraph || s.graph.sel == "" {
		t.Fatalf("no graph selection: view %d sel %q", s.list.cur, s.graph.sel)
	}
	m = press(m, "d")
	if overOf(m) == nil || !strings.Contains(overOf(m).title, s.graph.sel) {
		t.Errorf("the graph's selected node was not described: %+v", overOf(m))
	}
}

func TestDescribeSaysWhyThereIsNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		keys []string
		want string
	}{
		"another tab":   {[]string{"1", "d"}, ""}, // the key is simply not theirs: nothing opens, nothing is said
		"insights":      {[]string{"2", "V", "y"}, "nothing to describe"},
		"empty list":    {[]string{"2", "T", "/", "z", "z", "z", "z", "z", "z", "enter", "d"}, "no task selected"},
		"queues":        {[]string{"4", "y"}, ""},
		"doctor":        {[]string{"5", "d"}, ""},
		"help is shown": {[]string{"2", "?", "d"}, ""},
	} {
		m := pm(t, 100, 32)
		for _, k := range tc.keys {
			m = press(m, k)
		}
		if overOf(m) != nil {
			t.Errorf("%s: a panel opened", name)
		}
		if tc.want != "" && !strings.Contains(footer(m), tc.want) {
			t.Errorf("%s: footer %q lacks %q", name, footer(m), tc.want)
		}
	}
}

// The confirm screen keeps y: it applies there, the JSON panel never opens over it, and the prompt
// cannot reach it.
func TestYStillMeansApplyOnTheConfirmScreenOnly(t *testing.T) {
	m := press(press(pm(t, 100, 32), "2"), "x")
	m, _ = m.Update(taskVerifiedMsg{id: "ac-12", task: fixtureTasks().Recent[0]})
	m = press(m, "y")
	if overOf(m) != nil || tasksSec(m).mode != modeBusy {
		t.Errorf("y on the confirm screen must apply (mode %d, panel %v)", tasksSec(m).mode, overOf(m) != nil)
	}
	m = press(press(pm(t, 100, 32), "2"), "/")
	m = press(press(m, "d"), "y") // typing a filter: both are letters
	if overOf(m) != nil || tasksSec(m).list.text != "dy" {
		t.Errorf("d and y are text while the filter is open: %q", tasksSec(m).list.text)
	}
}

func TestViewerScrollsAndClamps(t *testing.T) {
	m := press(press(press(pm(t, 100, 20), "2"), "T"), "d") // the flat list's first row, ac-12, has a long detail
	v := overOf(m)
	total := len(v.content(innerW(100)))
	if total < 20 {
		t.Fatalf("the fixture's detail is only %d lines: this test needs a long one", total)
	}
	m = press(m, "k")
	if v.off != 0 {
		t.Errorf("scrolled above the top: %d", v.off)
	}
	_ = lines(m) // a draw sets the page size
	for _, k := range []string{"j", "j", "down"} {
		m = press(m, k)
	}
	if v.off != 3 {
		t.Errorf("three downs: %d", v.off)
	}
	m = press(m, "G")
	_ = lines(m)
	if v.off < 3 || v.off > total {
		t.Errorf("end: %d of %d", v.off, total)
	}
	last := v.off
	m = press(m, "j")
	_ = lines(m)
	if v.off != last {
		t.Errorf("scrolled past the end: %d then %d", last, v.off)
	}
	m = press(m, "g")
	_ = lines(m)
	if v.off != 0 {
		t.Errorf("home: %d", v.off)
	}
	m = m.(*Model)
	if out := strings.Join(lines(m), "\n"); !strings.Contains(out, "more") {
		t.Errorf("a clipped panel says how much is below:\n%s", out)
	}
}

// A hostile or huge item stays inside the width, free of control characters, and bounded.
func TestDescribeAndJSONSurviveHostileAndHugeItems(t *testing.T) {
	evil := "evil \x1b[31mred\x1b[0m ‮ rtl \x07bell " + strings.Repeat("界", 80) + " <script>&amp;"
	snap := fixtureSnapshot()
	huge := core.TaskIssue{ID: "ac-huge", Title: evil, Description: evil + "\n" + strings.Repeat("line of a very long description 0123456789\n", 40000) + strings.Repeat("x", 2<<20),
		Status: "open", Priority: 1, Labels: []string{evil}, UpdatedAt: goldenNow, CreatedAt: goldenNow}
	for i := range 3000 {
		huge.Comments = append(huge.Comments, core.TaskComment{Author: evil, Text: evil + strconv.Itoa(i), CreatedAt: goldenNow})
	}
	snap.Issues = append(snap.Issues, huge)
	for _, w := range []int{40, 60, 100, 140} {
		m := pm(t, w, 30)
		m, _ = m.Update(taskSnapshotMsg{snap: snap})
		m = press(press(m, "2"), "T")
		m = press(m, "s") // sort so the huge task is findable by id below
		s := tasksSec(m)
		if !s.selectID(huge.ID) {
			t.Fatal("the huge task is not listed")
		}
		for _, k := range []string{"d", "y"} {
			start := time.Now()
			m = press(m, k)
			v := overOf(m)
			if v == nil {
				t.Fatalf("%dx30 %s: no panel", w, k)
			}
			if !strings.Contains(v.title, "ac-huge") {
				t.Fatalf("%dx30: not the huge task: %q (lines %d)", w, v.title, len(v.content(innerW(w))))
			}
			out := lines(m)
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("%dx30 %s: opening took %v", w, k, d)
			}
			for i, l := range out {
				if lipgloss.Width(l) > w {
					t.Fatalf("%dx30 %s line %d is %d wide", w, k, i, lipgloss.Width(l))
				}
				for _, r := range l {
					if unicode.IsControl(r) || r == '‮' {
						t.Fatalf("%dx30 %s: control or format character %q in %q", w, k, r, l)
					}
				}
			}
			if n := len(v.content(innerW(w))); n > maxViewerLines+1 {
				t.Errorf("%dx30 %s: %d lines kept", w, k, n)
			}
			cut := strings.Join(v.content(innerW(w)), "\n")
			if !strings.Contains(cut, "more lines not shown") && !strings.Contains(cut, "cut here") && !strings.Contains(cut, "the rest is not shown") {
				t.Errorf("%dx30 %s: a huge item is cut without saying so", w, k)
			}
			m = press(m, "esc")
		}
	}
}

func TestViewerIsHiddenWhenTheTerminalIsTooSmall(t *testing.T) {
	m := press(press(pm(t, 100, 32), "2"), "d")
	m = sized(m, 30, 8)
	if got := strings.Join(lines(m), "\n"); !strings.Contains(got, "too small") {
		t.Errorf("a panel on a tiny screen should give way to the notice:\n%s", got)
	}
	m = press(m, "j")
	if overOf(m) == nil {
		t.Error("a scroll key closed the panel")
	}
	m = press(m, "esc")
	if overOf(m) != nil {
		t.Error("esc must still close it")
	}
}

// A resize drag over a 5 MB description costs a few milliseconds a step, not the whole item's
// wrapping each time (measured at about 1 ms for describe and 4 ms for JSON after the first open;
// before the bound it was about 100 ms and 60 ms). The limit is loose so a loaded machine passes.
func TestResizeDragOverAHugeDescriptionIsCheap(t *testing.T) {
	snap := fixtureSnapshot()
	desc := strings.Repeat("a line of a long description 0123456789 with some words to wrap around the panel\n", 65000)
	snap.Issues = append(snap.Issues, core.TaskIssue{ID: "ac-huge", Title: "huge", Description: desc, Status: "open", Priority: 1, UpdatedAt: goldenNow, CreatedAt: goldenNow})
	for _, k := range []string{"d", "y"} {
		m := pm(t, 100, 30)
		m, _ = m.Update(taskSnapshotMsg{snap: snap})
		m = press(press(m, "2"), "T")
		if !tasksSec(m).selectID("ac-huge") {
			t.Fatal("not listed")
		}
		m = press(m, k)
		_ = lines(m) // the first draw formats the item
		start, n := time.Now(), 0
		for w := 140; w >= 40; w-- {
			m = sized(m, w, 30)
			_ = lines(m)
			n++
		}
		if avg := time.Since(start) / time.Duration(n); avg > 30*time.Millisecond {
			t.Errorf("%s: a resize step costs %v on average", k, avg)
		}
	}
}
