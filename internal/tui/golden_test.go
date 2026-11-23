package tui

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
	"github.com/andrew-bierman/abacus/internal/lambe"
)

var update = flag.Bool("update", false, "rewrite golden files in testdata/golden")

var (
	goldenWidths  = []int{40, 80, 120}
	goldenHeights = []int{12, 30}
)

// named gives a Fake a product-like display name.
type named struct {
	*modtest.Fake
	name string
}

func (n named) Name() string { return n.name }

// scenario builds a model from demo/fixture data only: the lambe demo module (embedded
// fixtures), fakes for the other tools. Nothing here touches a daemon, tracker or hub.
type scenario struct {
	name  string
	build func(t *testing.T, th Theme) tea.Model
	keys  []string // pressed after the data is loaded
	tab   string   // name of the tab that must be marked active in the header
	empty bool     // no tabs: the header/footer invariant does not apply
}

// leaseProv is a fixture Leases provider (a fake module that also provides the domain).
type leaseProv struct{ named }

func (leaseProv) Leases(context.Context) (core.LeasesReport, error) {
	return core.LeasesReport{Header: core.Header{Source: core.SourceLive}, Leases: []core.Lease{
		{Resource: "ac-zoj", State: core.LeaseHeld, Holder: "glm-scheduler-01", ExpiresAt: "12m"},
		{Resource: "ac-00h", State: core.LeaseHeld, Holder: "codex-macbookpro", ExpiresAt: "3m"},
		{Resource: "ac-1k2", State: core.LeaseStale, Holder: "claude-mac"},
		{Resource: "ac-9x1", State: core.LeaseFree}}}, nil
}

func leaseFake() leaseProv {
	f := &modtest.Fake{IDStr: "beads-lease", Det: core.Detection{State: core.Ready, Version: "1.0.0"}}
	f.Checks = []core.Check{{Name: "hub reachable", Status: core.StatusOK, Detail: "fixture"}, {Name: "lease schema", Status: core.StatusWarn, Detail: "newer minor version", Fix: "upgrade beads-lease"}}
	return leaseProv{named{f, "beads-lease"}}
}

// taskProv is a fixture Tasks provider and writer; it records writes and never touches a tracker.
type taskProv struct {
	named
	ran     *[][]string
	fresh   *map[string]core.Task // what ReadTask returns now; absent ids fall back to the fixture
	readErr *error
}

func fixtureTasks() core.TasksReport {
	return core.TasksReport{Header: core.Header{Source: core.SourceLive}, Counts: core.TaskCounts{Total: 14, Open: 6, InProgress: 3, Blocked: 2, Deferred: 1, Closed: 2, Ready: 4},
		Recent: []core.Task{
			{ID: "ac-12", Title: "wire the lease heartbeat", Status: "in_progress", Priority: 1},
			{ID: "ac-09", Title: "queue attention digest", Status: "open", Priority: 2},
			{ID: "ac-03", Title: "retire the old poller", Status: "closed", Priority: 3}}}
}

func fixtureInsights() core.InsightsReport {
	return core.InsightsReport{Header: core.Header{Source: core.SourceGraph, State: core.Connected}, TotalActionable: 4, TotalBlocked: 2,
		TopPicks:     []core.Pick{{ID: "ac-12", Title: "wire the lease heartbeat", Unblocks: 2}},
		Blockers:     []core.Pick{{ID: "ac-12", Title: "wire the lease heartbeat", Unblocks: 2}},
		CriticalPath: []core.Ranked{{ID: "ac-12", Value: 3}, {ID: "ac-09", Value: 2}},
		Cycles:       [][]string{{"ac-20", "ac-21", "ac-20"}}}
}

func (taskProv) Tasks(context.Context) (core.TasksReport, error) { return fixtureTasks(), nil }
func (taskProv) Insights(context.Context) (core.InsightsReport, error) {
	return fixtureInsights(), nil
}
func (p taskProv) BuildTaskArgv(ch core.TaskChange) ([]string, error) {
	a := []string{"bd", string(ch.Op)}
	if ch.ID != "" {
		a = append(a, ch.ID)
	}
	if ch.Title != "" {
		a = append(a, "--title="+ch.Title)
	}
	if ch.Priority != nil {
		a = append(a, fmt.Sprintf("--priority=%d", *ch.Priority))
	}
	if ch.Status != "" {
		a = append(a, "--status="+ch.Status)
	}
	return a, nil
}
func (p taskProv) RunTask(_ context.Context, argv []string) (core.TaskResult, error) {
	*p.ran = append(*p.ran, argv)
	return core.TaskResult{ID: "ac-99", Title: "made"}, nil
}
func (taskProv) TrackerDir() string { return "" }

func (p taskProv) ReadTask(_ context.Context, id string) (core.Task, error) {
	if *p.readErr != nil {
		return core.Task{}, *p.readErr
	}
	if t, ok := (*p.fresh)[id]; ok {
		return t, nil
	}
	for _, t := range fixtureTasks().Recent {
		if t.ID == id {
			return t, nil
		}
	}
	return core.Task{}, fmt.Errorf("no such issue %s", id)
}

func tasksFake() taskProv {
	f := &modtest.Fake{IDStr: "bd", Det: core.Detection{State: core.Ready, Version: "1.3.4"}}
	fresh, readErr := map[string]core.Task{}, error(nil)
	return taskProv{named{f, "bd"}, new([][]string), &fresh, &readErr}
}

// boardProv is a fixture Board provider: nothing here runs a CLI or touches a board.
type boardProv struct {
	named
	err error
}

func fixtureBoard() core.BoardReport {
	return core.BoardReport{Header: core.Header{Source: core.SourceLive},
		Channels: []core.BoardChannel{{Path: "dev/web", Threads: 3}, {Path: "work", Threads: 3}, {Path: "dev", Threads: 1}, {Path: "ops"}},
		Threads: []core.BoardThread{
			{Root: core.BoardMessage{ID: "15", Channel: "dev/web", Author: "ada", At: "2026-11-22T20:12:05Z", Text: "checkout times out for carts over 40 items, started after the 14:02 deploy"}, Replies: 2,
				Last: &core.BoardMessage{ID: "17", Channel: "dev/web", Author: "ada/claude", At: "2026-11-22T20:14:40Z", Text: "rolled back to d4e5f6; watching the error rate"}},
			{Root: core.BoardMessage{ID: "2", Channel: "dev", Author: "ada", At: "2026-11-22T19:01:00Z", Text: "deploys are green again"}}},
		Tasks: []core.BoardTask{
			{ID: "16", Channel: "work", Text: "fix CVE-123 in acme/web", State: "claimed", Claimant: "codex via ada", Until: "2026-11-22T21:09:29Z"},
			{ID: "6", Channel: "work", Text: "review the fix", State: "ready"}}}
}

func (b boardProv) Board(context.Context) (core.BoardReport, error) {
	if b.err != nil {
		return core.BoardReport{}, b.err
	}
	return fixtureBoard(), nil
}

func boardFake() boardProv {
	f := &modtest.Fake{IDStr: "board", Det: core.Detection{State: core.Ready, Version: "0.54.0"}}
	return boardProv{named: named{f, "Vercel Board"}}
}

func brokenFake() named {
	f := &modtest.Fake{IDStr: "ghost", Det: core.Detection{State: core.Broken, Detail: "ghost version --json exited 2"}}
	return named{f, "ghost"}
}

type memAudit struct{ n int }

func (a *memAudit) Record(core.AuditEntry) error { a.n++; return nil }

func noFullView(string) tea.Cmd { return nil }

// load wires the shell to fixture data by injecting the messages the commands would deliver.
func load(t *testing.T, th Theme, view func(lambe.View) lambe.View) tea.Model {
	t.Helper()
	demo := lambe.NewDemo()
	lease, tasks, ghost, brd := leaseFake(), tasksFake(), brokenFake(), boardFake()
	en := []core.Entry{
		{Module: brd, Detection: brd.Det},
		{Module: demo, Detection: demo.Detect(context.Background())},
		{Module: lease, Detection: lease.Det},
		{Module: tasks, Detection: tasks.Det},
		{Module: ghost, Detection: ghost.Det},
	}
	var m tea.Model = New(en, WithTheme(th), WithFullView(noFullView), WithAudit(&memAudit{}))
	v, err := demo.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view != nil {
		v = view(v)
	}
	m, _ = m.Update(lambeViewMsg{view: v})
	m, _ = m.Update(boardMsg{report: fixtureBoard()})
	m, _ = m.Update(leasesMsg{read: readLeases(context.Background(), lease)})
	m, _ = m.Update(tasksMsg{report: fixtureTasks()})
	m, _ = m.Update(insightsMsg{report: fixtureInsights()})
	m, _ = m.Update(doctorMsg{report: core.DoctorReport(context.Background(), "", en)})
	return m
}

func scenarios() []scenario {
	std := func(t *testing.T, th Theme) tea.Model { return load(t, th, nil) }
	return []scenario{
		{name: "queues", build: std, keys: []string{"4"}, tab: "Queues"},
		{name: "queues-drilled", build: std, keys: []string{"4", "enter"}, tab: "Queues"},
		{name: "queues-tracker", keys: []string{"4"}, tab: "Queues", build: func(t *testing.T, th Theme) tea.Model {
			return load(t, th, func(v lambe.View) lambe.View { v.Source, v.Queue = lambe.SourceTracker, lambe.Queue{}; return v })
		}},
		{name: "tasks", build: std, keys: []string{"2"}, tab: "Tasks"},
		{name: "tasks-new", build: std, keys: []string{"2", "n", "h", "i"}, tab: "Tasks"},
		{name: "tasks-confirm", tab: "Tasks", build: func(t *testing.T, th Theme) tea.Model {
			m := press(press(load(t, th, nil), "2"), "x") // the check of the current state is async
			m, _ = m.Update(taskVerifiedMsg{id: "ac-12", task: fixtureTasks().Recent[0]})
			return m
		}},
		{name: "tasks-confirm-stale", tab: "Tasks", build: func(t *testing.T, th Theme) tea.Model {
			m := press(press(load(t, th, nil), "2"), "x")
			fresh := fixtureTasks().Recent[0]
			fresh.Status = "blocked"
			m, _ = m.Update(taskVerifiedMsg{id: "ac-12", task: fresh})
			return m
		}},
		{name: "leases", build: std, keys: []string{"3"}, tab: "Leases"},
		{name: "board", build: std, tab: "Board"},
		{name: "board-detail", build: std, keys: []string{"down"}, tab: "Board"},
		{name: "board-task", build: std, keys: []string{"down", "down"}, tab: "Board"},
		{name: "board-absent", tab: "Board", build: func(t *testing.T, th Theme) tea.Model {
			return New(nil, WithTheme(th), WithFullView(noFullView))
		}},
		{name: "board-degraded", tab: "Board", build: func(t *testing.T, th Theme) tea.Model {
			b := boardFake()
			var m tea.Model = New([]core.Entry{{Module: b, Detection: b.Det}}, WithTheme(th), WithAudit(&memAudit{}))
			m, _ = m.Update(boardMsg{err: errors.New("could not reach the board")})
			return m
		}},
		{name: "board-setup", tab: "Board", build: func(t *testing.T, th Theme) tea.Model {
			b := boardFake()
			b.err = &core.NotConnectedError{Reason: core.BoardNotSetUpReason, HowToConnect: core.BoardNotSetUp}
			var m tea.Model = New([]core.Entry{{Module: b, Detection: b.Det}}, WithTheme(th), WithAudit(&memAudit{}))
			m, _ = m.Update(boardMsg{err: b.err})
			return m
		}},
		{name: "doctor", build: std, keys: []string{"5"}, tab: "Doctor"},
		{name: "help", build: std, keys: []string{"?"}, tab: "Board"},
		{name: "none", build: func(*testing.T, Theme) tea.Model { return New(nil, WithFullView(noFullView)) }, keys: []string{"2"}, tab: "Tasks"},
	}
}

func render(t *testing.T, sc scenario, th Theme, w, h int) string {
	t.Helper()
	m := sc.build(t, th)
	if sc.name == "none" {
		m = New(nil, WithTheme(th), WithFullView(noFullView))
	}
	m = sized(m, w, h)
	for _, k := range sc.keys {
		m = press(m, k)
	}
	return m.View()
}

func checkGolden(t *testing.T, file, got string, valid bool) {
	t.Helper()
	path := filepath.Join("testdata", "golden", file)
	if *update {
		if !valid {
			t.Errorf("%s: not written, the render violates the invariants", file)
			return
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/tui -update to create it)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from the render (go test ./internal/tui -update to accept):\n--- want\n%s\n--- got\n%s", file, want, got)
	}
}

func TestGoldenAsciiRenders(t *testing.T) {
	th := asciiTheme()
	for _, sc := range scenarios() {
		t.Run(sc.name, func(t *testing.T) {
			var b strings.Builder
			valid := true
			for _, w := range goldenWidths {
				for _, h := range goldenHeights {
					if errs := invariantErrs(sc, render(t, sc, th, w, h), w, h); len(errs) > 0 {
						valid = false
						t.Errorf("%dx%d: %s", w, h, strings.Join(errs, "; "))
					}
					// Trailing spaces are padding, not content; trim them so goldens diff cleanly.
					var lines []string
					for _, l := range strings.Split(render(t, sc, th, w, h), "\n") {
						lines = append(lines, strings.TrimRight(l, " "))
					}
					fmt.Fprintf(&b, "=== %dx%d ===\n%s\n", w, h, strings.Join(lines, "\n"))
				}
			}
			checkGolden(t, sc.name+".ascii.golden", b.String(), valid)
		})
	}
}

// One truecolor golden pins the theme itself (escape codes shown as ␛ so a diff is readable).
func TestGoldenTrueColorTheme(t *testing.T) {
	for _, dark := range []bool{true, false} {
		th := NewTheme(rendererFor(termenv.TrueColor, dark))
		name := "queues.truecolor-dark.golden"
		if !dark {
			name = "queues.truecolor-light.golden"
		}
		got := strings.ReplaceAll(render(t, scenarios()[0], th, 120, 30), "\x1b", "␛")
		checkGolden(t, name, got, true)
	}
}

// invariantErrs are the properties every render must have, whatever the data.
func invariantErrs(sc scenario, out string, w, h int) []string {
	var errs []string
	ls := strings.Split(out, "\n")
	if len(ls) > h {
		errs = append(errs, fmt.Sprintf("%d lines for height %d", len(ls), h))
	}
	for i, l := range ls {
		if lipgloss.Width(l) > w {
			errs = append(errs, fmt.Sprintf("line %d is %d cells for width %d: %q", i, lipgloss.Width(l), w, l))
		}
		for _, r := range l {
			if unicode.IsControl(r) {
				errs = append(errs, fmt.Sprintf("control character %q in %q", r, l))
			}
		}
	}
	if sc.empty || w < 40 || h < 3 {
		return errs
	}
	if !strings.Contains(ls[0], sc.tab) {
		errs = append(errs, fmt.Sprintf("header %q lacks the tab %q", ls[0], sc.tab))
	}
	if !strings.Contains(ls[0], "["+sc.tab+"]") && w >= 60 {
		errs = append(errs, fmt.Sprintf("header %q does not mark %q active", ls[0], sc.tab))
	}
	last := ls[len(ls)-1]
	for _, hint := range []string{"? help", "q quit"} {
		if !strings.Contains(last, hint) {
			errs = append(errs, fmt.Sprintf("footer %q lacks %q", last, hint))
		}
	}
	return errs
}

func TestEveryRenderRespectsWidthHeightAndKeepsItsChrome(t *testing.T) {
	th := asciiTheme()
	for _, sc := range scenarios() {
		for _, w := range append([]int{1, 10, 39, 99, 100}, goldenWidths...) {
			for _, h := range append([]int{2, 3, 5}, goldenHeights...) {
				for _, e := range invariantErrs(sc, render(t, sc, th, w, h), w, h) {
					t.Errorf("%s %dx%d: %s", sc.name, w, h, e)
				}
			}
		}
	}
}

func expectedGoldens() map[string]bool {
	want := map[string]bool{"queues.truecolor-dark.golden": true, "queues.truecolor-light.golden": true}
	for _, sc := range scenarios() {
		want[sc.name+".ascii.golden"] = true
	}
	return want
}

// Orphaned goldens (a renamed or removed scenario) would silently stop guarding anything:
// -update deletes them, a normal run fails on them.
func TestNoOrphanedGoldens(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("testdata", "golden", "*"))
	want := expectedGoldens()
	for _, f := range files {
		if want[filepath.Base(f)] {
			continue
		}
		if *update {
			if err := os.Remove(f); err != nil {
				t.Fatal(err)
			}
			continue
		}
		t.Errorf("orphaned golden %s (go test ./internal/tui -update deletes it)", f)
	}
}

// The README's 80-column sample is the Lambe golden, not a hand-copied picture.
func TestReadmeSampleMatchesTheGolden(t *testing.T) {
	golden, err := os.ReadFile(filepath.Join("testdata", "golden", "tasks.ascii.golden"))
	if err != nil {
		t.Fatal(err)
	}
	block := strings.SplitN(strings.SplitN(string(golden), "=== 80x30 ===\n", 2)[1], "=== ", 2)[0]
	ls := strings.Split(strings.TrimRight(block, "\n"), "\n")
	foot := ls[len(ls)-1]
	for len(ls) > 0 && ls[len(ls)-1] == "" {
		ls = ls[:len(ls)-1]
	}
	want := strings.Join(append(ls[:17:17], foot), "\n")
	path := filepath.Join("..", "..", "README.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const marker = "data from fixture providers:\n\n```\n"
	head, rest, ok := strings.Cut(string(raw), marker)
	if !ok {
		t.Fatal("README sample marker not found")
	}
	got, tail, _ := strings.Cut(rest, "\n```")
	if got == want {
		return
	}
	if *update {
		if err := os.WriteFile(path, []byte(head+marker+want+"\n```"+tail), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatalf("README sample differs from the 80x30 golden (go test ./internal/tui -update rewrites it):\n--- readme\n%s\n--- golden\n%s", got, want)
}
