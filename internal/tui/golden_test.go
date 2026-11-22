package tui

import (
	"context"
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
	goldenHeights = []int{10, 30}
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

func leaseFake() named {
	f := &modtest.Fake{IDStr: "beads-lease", Det: core.Detection{State: core.Ready, Version: "1.0.0"}}
	f.Stat = core.Status{Summary: "2 held, 1 stale, 1 free", Lines: []string{
		"ac-zoj  held by glm-scheduler-01 (expires 12m)", "ac-00h  held by codex-macbookpro (expires 3m)",
		"ac-1k2  stale (last holder claude-mac)", "ac-9x1  free"}}
	f.Stat.Rows = []core.Row{
		{Resource: "ac-zoj", State: "held", Tone: core.ToneInfo, Detail: "by glm-scheduler-01 (expires 12m)"},
		{Resource: "ac-00h", State: "held", Tone: core.ToneInfo, Detail: "by codex-macbookpro (expires 3m)"},
		{Resource: "ac-1k2", State: "stale", Tone: core.ToneWarn, Detail: "(last holder claude-mac)"},
		{Resource: "ac-9x1", State: "free", Tone: core.ToneOK}}
	f.Checks = []core.Check{{Name: "hub reachable", Status: core.StatusOK, Detail: "fixture"}, {Name: "lease schema", Status: core.StatusWarn, Detail: "newer minor version", Fix: "upgrade beads-lease"}}
	return named{f, "beads-lease"}
}

func brokenFake() named {
	f := &modtest.Fake{IDStr: "ghost", Det: core.Detection{State: core.Broken, Detail: "ghost version --json exited 2"}}
	return named{f, "ghost"}
}

// load wires the shell to fixture data by injecting the messages the commands would deliver.
func load(t *testing.T, th Theme, view func(lambe.View) lambe.View) tea.Model {
	t.Helper()
	demo := lambe.NewDemo()
	lease, ghost := leaseFake(), brokenFake()
	en := []core.Entry{
		{Module: demo, Detection: demo.Detect(context.Background())},
		{Module: lease, Detection: lease.Det},
		{Module: ghost, Detection: ghost.Det},
	}
	var m tea.Model = New(en, WithTheme(th))
	v, err := demo.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view != nil {
		v = view(v)
	}
	m, _ = m.Update(lambeViewMsg{view: v})
	m, _ = m.Update(genericMsg{id: "beads-lease", status: lease.Stat})
	m, _ = m.Update(doctorMsg{report: core.DoctorReport(context.Background(), "", en)})
	return m
}

func scenarios() []scenario {
	std := func(t *testing.T, th Theme) tea.Model { return load(t, th, nil) }
	return []scenario{
		{name: "lambe", build: std, tab: "Lambe"},
		{name: "lambe-drilled", build: std, keys: []string{"enter"}, tab: "Lambe"},
		{name: "lambe-tracker", tab: "Lambe", build: func(t *testing.T, th Theme) tea.Model {
			return load(t, th, func(v lambe.View) lambe.View { v.Source, v.Queue = lambe.SourceTracker, lambe.Queue{}; return v })
		}},
		{name: "leases", build: std, keys: []string{"2"}, tab: "beads-lease"},
		{name: "doctor", build: std, keys: []string{"4"}, tab: "Doctor"},
		{name: "help", build: std, keys: []string{"?"}, tab: "Lambe"},
		{name: "empty", build: func(*testing.T, Theme) tea.Model { return New(nil) }, empty: true},
	}
}

func render(t *testing.T, sc scenario, th Theme, w, h int) string {
	t.Helper()
	m := sc.build(t, th)
	if sc.empty {
		m = New(nil, WithTheme(th))
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
		name := "lambe.truecolor-dark.golden"
		if !dark {
			name = "lambe.truecolor-light.golden"
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
	if sc.name != "empty" && !strings.Contains(ls[0], "["+sc.tab+"]") && w >= 60 {
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
	want := map[string]bool{"lambe.truecolor-dark.golden": true, "lambe.truecolor-light.golden": true}
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
	golden, err := os.ReadFile(filepath.Join("testdata", "golden", "lambe.ascii.golden"))
	if err != nil {
		t.Fatal(err)
	}
	block := strings.SplitN(strings.SplitN(string(golden), "=== 80x30 ===\n", 2)[1], "=== ", 2)[0]
	ls := strings.Split(strings.TrimRight(block, "\n"), "\n")
	foot := ls[len(ls)-1]
	for len(ls) > 0 && ls[len(ls)-1] == "" {
		ls = ls[:len(ls)-1]
	}
	want := strings.Join(append(ls[:13:13], foot), "\n")
	path := filepath.Join("..", "..", "README.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const marker = "data plus a fixture beads-lease tab and a fixture broken tool:\n\n```\n"
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
