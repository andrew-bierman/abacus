package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
	"github.com/andrew-bierman/abacus/internal/lambe"
)

// showsWhole reports whether a framed screen shows needle in full the number of times want says
// (whitespace and the frame ignored, so wrapped text counts).
func showsWhole(t *testing.T, view, needle string, times int) bool {
	t.Helper()
	return strings.Count(panelText(panelRows(t, view)), needle) >= times
}

// confirmAt opens the Tasks confirm screen for a new task with the given title at w x h.
func confirmAt(t *testing.T, w, h int, title string) (tea.Model, tasksRig) {
	t.Helper()
	m, r, _ := tasksShell(t, w, h)
	m = press(m, "n")
	m = typeText(m, title)
	m = press(m, "enter")
	m = press(m, "enter")
	m = press(m, "enter")
	return m, r
}

// The confirm screen applies only what it shows whole: when the plan or the exact command is
// clipped, y does nothing and the more-row says to enlarge the terminal.
func TestConfirmAppliesOnlyWhenTheWholeCommandIsVisible(t *testing.T) {
	for _, size := range [][2]int{{40, 12}, {80, 12}, {80, 16}, {80, 60}} {
		w, h := size[0], size[1]
		t.Run(fmt.Sprintf("%dx%d", w, h), func(t *testing.T) {
			m, r := confirmAt(t, w, h, long200)
			view := m.View()
			whole := showsWhole(t, view, long200, 2)
			if whole != !strings.Contains(view, "↕") {
				t.Fatalf("a more-row and a whole command disagree (whole=%v):\n%s", whole, view)
			}
			if !whole && !strings.Contains(view, "enlarge the terminal") {
				t.Errorf("the more-row must say to enlarge the terminal:\n%s", view)
			}
			if !whole && strings.Contains(view, "y apply") {
				t.Errorf("the footer must not offer y while it cannot apply:\n%s", view)
			}
			m = pressRun(t, m, "y")
			ran := len(*r.prov.ran)
			if whole && ran != 1 {
				t.Fatalf("the whole command is shown, y must apply once, ran=%d", ran)
			}
			if !whole && ran != 0 {
				t.Fatalf("the command is clipped, y must not apply, ran=%d:\n%s", ran, view)
			}
			if !whole && r.audit.n != 0 {
				t.Fatalf("audited a clipped confirm: %d", r.audit.n)
			}
			if !whole {
				if v := m.View(); !strings.Contains(v, "Confirm this change") {
					t.Fatalf("y on a clipped confirm leaves the screen as it was:\n%s", v)
				}
				// enlarged, the same confirm applies
				m = sized(m, 80, 60)
				m = pressRun(t, m, "y")
				if n := len(*r.prov.ran); n != 1 {
					t.Fatalf("after enlarging, y applies once, ran=%d", n)
				}
			}
		})
	}
	// the sizes above must include both outcomes
	short, _ := confirmAt(t, 40, 12, long200)
	tall, _ := confirmAt(t, 80, 60, long200)
	if showsWhole(t, short.View(), long200, 2) || !showsWhole(t, tall.View(), long200, 2) {
		t.Fatal("the test needs 40x12 clipped and 80x60 whole")
	}
}

// guidedClipRig is the guided setup confirm with a workspace path too long for a short screen.
func guidedClipRig(t *testing.T, w, h int) (tea.Model, *freshBd) {
	t.Helper()
	bd := &freshBd{Fake: &modtest.Fake{IDStr: "bd"}, dir: "/data/" + long200}
	m, _ := guidedRig(t, bd)
	return press(sized(m, w, h), "i"), bd
}

func TestGuidedConfirmAppliesOnlyWhenTheWholePlanIsVisible(t *testing.T) {
	for _, size := range [][2]int{{40, 12}, {80, 12}, {80, 60}} {
		w, h := size[0], size[1]
		t.Run(fmt.Sprintf("%dx%d", w, h), func(t *testing.T) {
			m, bd := guidedClipRig(t, w, h)
			view := m.View()
			whole := showsWhole(t, view, long200, 1) && !strings.Contains(view, "↕")
			if !whole && !strings.Contains(view, "enlarge the terminal") {
				t.Errorf("the more-row must say to enlarge the terminal:\n%s", view)
			}
			if got := strings.Contains(lastLine(view), "y apply"); got != whole {
				t.Errorf("footer offers y = %v, but the whole plan shown = %v: %q", got, whole, lastLine(view))
			}
			m = pressSettle(t, m, "y")
			if whole && len(bd.ran) != 1 {
				t.Fatalf("whole plan shown, y must apply, ran=%d", len(bd.ran))
			}
			if !whole && len(bd.ran) != 0 {
				t.Fatalf("clipped plan, y must not apply, ran=%d:\n%s", len(bd.ran), view)
			}
		})
	}
	short, _ := guidedClipRig(t, 40, 12)
	if !strings.Contains(short.View(), "↕") {
		t.Fatalf("the test needs the guided confirm clipped at 40x12:\n%s", short.View())
	}
}

// The command shown is the command applied: an argument with an invisible or bidi rune is quoted
// so nothing is stripped between the screen and the argv.
func TestShownCommandEqualsAppliedArgvForHostileTitles(t *testing.T) {
	for name, title := range map[string]string{
		"bidi override": "pay\u202eevil\u202c me",
		"zero-width":    "a\u200db\u200bc",
		"nbsp":          "a\u00a0b",
	} {
		t.Run(name, func(t *testing.T) {
			m, r, _ := tasksShell(t, 100, 40)
			m = press(m, "n")
			m = typeText(m, title)
			for i := 0; i < 3; i++ {
				m = press(m, "enter")
			}
			shown := panelText(panelRows(t, m.View()))
			m = pressRun(t, m, "y")
			if len(*r.prov.ran) != 1 {
				t.Fatalf("ran %d times", len(*r.prov.ran))
			}
			want := strings.Join(strings.Fields(core.DisplayArgv((*r.prov.ran)[0])), "")
			if !strings.Contains(shown, want) {
				t.Errorf("shown command is not the applied argv\nwant %q\nin   %q", want, shown)
			}
		})
	}
}

func lastLine(v string) string {
	l := strings.Split(strings.TrimRight(v, "\n"), "\n")
	return l[len(l)-1]
}

// On a screen that asks for a decision, the keys for that decision lead the footer, so a narrow
// footer drops the lower-priority hints first.
func TestConfirmFooterKeepsYAndNAtNarrowWidths(t *testing.T) {
	for _, w := range []int{40, 48} {
		m, _ := confirmAt(t, w, 40, "short")
		g, _ := guidedClipRig(t, w, 40)
		for name, v := range map[string]string{"tasks": m.View(), "guided": g.View()} {
			foot := lastLine(v)
			for _, want := range []string{"y apply", "n/esc cancel"} {
				if !strings.Contains(foot, want) {
					t.Errorf("%s w=%d: footer %q lost %q", name, w, foot, want)
				}
			}
		}
	}
}

func TestFooterOffersYOnlyWhileTheConfirmCanApply(t *testing.T) {
	short, _ := confirmAt(t, 100, 12, long200)
	if strings.Contains(lastLine(short.View()), "y apply") {
		t.Errorf("clipped confirm still offers y: %q", lastLine(short.View()))
	}
	if !strings.Contains(lastLine(short.View()), "n/esc cancel") {
		t.Errorf("the way out is always offered: %q", lastLine(short.View()))
	}
	tall, _ := confirmAt(t, 100, 60, long200)
	if !strings.Contains(lastLine(tall.View()), "y apply") {
		t.Errorf("a whole confirm offers y: %q", lastLine(tall.View()))
	}
}

// At a short height the how-to-connect line of Board, Leases and Queues is never the part that
// is clipped away.
func TestNotConnectedPanelsKeepHowToConnectAtShortHeights(t *testing.T) {
	for _, name := range []string{"board-not-connected", "leases-not-connected", "queues-not-connected"} {
		for h := 3; h <= 14; h++ {
			sc := framedScreens(t, 40, h)[name]
			if !strings.Contains(sc.view, "how to connect:") {
				t.Errorf("%s at %d rows hides how to connect:\n%s", name, h, sc.view)
			}
			if rows := len(strings.Split(strings.TrimRight(sc.view, "\n"), "\n")); rows > h {
				t.Errorf("%s at %d rows draws %d rows", name, h, rows)
			}
		}
	}
	// and the natural order (state, why, how) is kept when everything fits
	v := framedScreens(t, 40, 30)["leases-not-connected"].view
	if i, j, k := strings.Index(v, "not connected"), strings.Index(v, "no hub"), strings.Index(v, "how to connect:"); !(i < j && j < k) {
		t.Errorf("a roomy panel keeps state, why, how in order:\n%s", v)
	}
}

// A one-row panel still says that more is hidden.
func TestOneRowPanelShowsAMoreMarker(t *testing.T) {
	th := asciiTheme()
	body := []string{"first line of three", "second", "third"}
	got := box(th, "T", body, 30, 3)
	if len(got) != 3 || !strings.Contains(got[1], "↕+2") || !strings.Contains(got[1], "first line") {
		t.Errorf("a one-row body marks the hidden rows:\n%s", strings.Join(got, "\n"))
	}
	if got := box(th, "T", body[:1], 30, 3); strings.Contains(strings.Join(got, ""), "↕") {
		t.Errorf("nothing hidden, no marker:\n%s", strings.Join(got, "\n"))
	}
}

// ---- #22 follow-ups ----

func bindingOn(bs []key.Binding, k string) (enabled, found bool) {
	for _, b := range bs {
		for _, kk := range b.Keys() {
			if kk == k {
				return b.Enabled(), true
			}
		}
	}
	return false, false
}

func TestQueuesOmitsEnterWhenThereAreNoRuns(t *testing.T) {
	s := newLambeSection(lambe.NewDemo(), nil)
	s.SetTheme(asciiTheme())
	var m tea.Model = s
	m, _ = m.Update(lambeViewMsg{view: lambe.View{Snapshot: lambe.Snapshot{}}})
	m = sized(m, 60, 30)
	if len(s.runs()) != 0 {
		t.Fatal("the test needs zero runs")
	}
	if on, _ := bindingOn(s.Bindings(), "enter"); on {
		t.Fatal("stacked with no runs: enter is offered")
	}
	m = sized(m, 140, 30)
	if on, _ := bindingOn(s.Bindings(), "enter"); on {
		t.Fatal("side by side: enter is not offered")
	}
}

func TestBoardIdsEqualTheIdsOfItsRows(t *testing.T) {
	for name, r := range map[string]core.BoardReport{"demo": fixtureBoard(), "empty": {}} {
		s := newBoardSection(boardFake())
		s.report = r
		var want []string
		for _, row := range s.rows() {
			want = append(want, row.id)
		}
		got := s.ids()
		if len(got) != len(want) {
			t.Fatalf("%s: ids %v, rows %v", name, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%s: ids[%d]=%q, row id %q", name, i, got[i], want[i])
			}
		}
		if name == "demo" && len(got) == 0 {
			t.Fatal("the demo board has rows")
		}
	}
}

func TestBoardDrilledInStackedLayoutIsClearedWhenTheReportEmpties(t *testing.T) {
	s := newBoardSection(boardFake())
	s.SetTheme(asciiTheme())
	var m tea.Model = s
	m, _ = m.Update(boardMsg{report: fixtureBoard()})
	m = sized(m, 60, 30)
	m = press(m, "enter")
	if !s.drilled {
		t.Fatal("enter drills in the stacked layout")
	}
	m, _ = m.Update(boardMsg{report: core.BoardReport{Header: core.Header{Source: core.SourceLive}}})
	if s.drilled {
		t.Fatalf("an emptied report cannot stay drilled:\n%s", m.View())
	}
	if on, _ := bindingOn(s.Bindings(), "esc"); on {
		t.Fatal("no back key with nothing to go back from")
	}
}

// A wide (CJK) value in a form keeps every row inside the panel's inner width.
func TestWideRuneFormValueStaysInsideTheInnerWidth(t *testing.T) {
	th := asciiTheme()
	const inner = 36
	val := strings.Repeat("日本語のタイトル", 12)
	for _, focus := range []int{0, 1} {
		fields := []formField{{label: "Title", value: val}, {label: "Priority 0-4", value: "2"}}
		rows := fieldRows(th, fields, focus, inner)
		for _, r := range rows {
			if w := lipgloss.Width(r); w > inner {
				t.Errorf("row %q is %d cells wide, inner is %d", r, w, inner)
			}
		}
		if got := strings.Join(strings.Fields(strings.Join(rows, "")), ""); !strings.Contains(got, val) {
			t.Errorf("the wide value is cut:\n%s", strings.Join(rows, "\n"))
		}
	}
	m, _, _ := tasksShell(t, 40, 30)
	m = press(m, "n")
	m = typeText(m, val)
	for _, l := range strings.Split(m.View(), "\n") {
		if lipgloss.Width(l) > 40 {
			t.Errorf("view row is %d cells wide: %q", lipgloss.Width(l), l)
		}
	}
}
