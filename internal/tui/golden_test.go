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
	empty bool     // no tabs: the header/footer invariant does not apply
}

func leaseFake() named {
	f := &modtest.Fake{IDStr: "beads-lease", Det: core.Detection{State: core.Ready, Version: "1.0.0"}}
	f.Stat = core.Status{Summary: "2 held, 1 stale, 1 free", Lines: []string{
		"ac-zoj  held by glm-scheduler-01 (expires 12m)", "ac-00h  held by codex-macbookpro (expires 3m)",
		"ac-1k2  stale (last holder claude-mac)", "ac-9x1  free"}}
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
		{name: "lambe", build: std},
		{name: "lambe-drilled", build: std, keys: []string{"enter"}},
		{name: "lambe-tracker", build: func(t *testing.T, th Theme) tea.Model {
			return load(t, th, func(v lambe.View) lambe.View { v.Source, v.Queue = lambe.SourceTracker, lambe.Queue{}; return v })
		}},
		{name: "leases", build: std, keys: []string{"2"}},
		{name: "doctor", build: std, keys: []string{"4"}},
		{name: "help", build: std, keys: []string{"?"}},
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

func checkGolden(t *testing.T, file, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", file)
	if *update {
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
			for _, w := range goldenWidths {
				for _, h := range goldenHeights {
					// Trailing spaces are padding, not content; trim them so goldens diff cleanly.
					var lines []string
					for _, l := range strings.Split(render(t, sc, th, w, h), "\n") {
						lines = append(lines, strings.TrimRight(l, " "))
					}
					fmt.Fprintf(&b, "=== %dx%d ===\n%s\n", w, h, strings.Join(lines, "\n"))
				}
			}
			checkGolden(t, sc.name+".ascii.golden", b.String())
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
		checkGolden(t, name, got)
	}
}

func TestEveryRenderRespectsWidthHeightAndKeepsItsChrome(t *testing.T) {
	th := asciiTheme()
	for _, sc := range scenarios() {
		for _, w := range append([]int{1, 10, 99, 100}, goldenWidths...) {
			for _, h := range append([]int{2, 5}, goldenHeights...) {
				out := render(t, sc, th, w, h)
				ls := strings.Split(out, "\n")
				if len(ls) > h {
					t.Errorf("%s %dx%d: %d lines", sc.name, w, h, len(ls))
				}
				for i, l := range ls {
					if lipgloss.Width(l) > w {
						t.Errorf("%s %dx%d line %d is %d cells: %q", sc.name, w, h, i, lipgloss.Width(l), l)
					}
					for _, r := range l {
						if unicode.IsControl(r) {
							t.Errorf("%s %dx%d: control character %q in %q", sc.name, w, h, r, l)
						}
					}
				}
				if sc.empty || w < 10 {
					continue
				}
				if strings.TrimSpace(ls[0]) == "" {
					t.Errorf("%s %dx%d: header missing", sc.name, w, h)
				}
				if h >= 3 && !strings.Contains(ls[len(ls)-1], "help") {
					t.Errorf("%s %dx%d: footer missing, last line %q", sc.name, w, h, ls[len(ls)-1])
				}
			}
		}
	}
}
