package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/demo"
	"github.com/andrew-bierman/abacus/internal/lambe"
)

func demoEntries() []core.Entry {
	l := lambe.NewDemo()
	var out []core.Entry
	for _, m := range []core.Module{l, demo.NewLeases(), demo.NewTasks(), demo.NewBoard()} {
		out = append(out, core.Entry{Module: m, Detection: m.Detect(context.Background())})
	}
	return out
}

// A demo session reaches nothing real: not the full-view tool on PATH (a fake b9s leaves a marker
// if anything runs it, probing included), not the audit log, and a confirmed "write" changes nothing.
func TestDemoSessionReachesNoRealToolOrState(t *testing.T) {
	bin, state := t.TempDir(), t.TempDir()
	marker := filepath.Join(bin, "ran")
	if err := os.WriteFile(filepath.Join(bin, "b9s"), []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_STATE_HOME", state)

	m := sized(New(demoEntries(), WithTheme(asciiTheme())), 120, 40) // no WithAudit, no WithFullView
	m, _ = m.Update(tasksMsg{report: demo.TasksReport()})
	m, _ = m.Update(insightsMsg{report: demo.InsightsReport()})
	m = press(m, "2")

	m = pressRun(t, m, "o") // the full view: nothing to open in a demo
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("o in a demo ran the real full-view tool")
	}
	if !strings.Contains(m.View(), "demo") {
		t.Fatalf("o in a demo says why nothing opened:\n%s", m.View())
	}

	m = press(m, "n")
	m = typeText(m, "ship it")
	for i := 0; i < 3; i++ {
		m = press(m, "enter")
	}
	if !strings.Contains(m.View(), "Confirm this change") {
		t.Fatalf("the demo still shows the confirm step:\n%s", m.View())
	}
	m = pressRun(t, m, "y")
	if !strings.Contains(m.View(), "demo-1") {
		t.Fatalf("the demo applies the change to nothing and reports its made-up id:\n%s", m.View())
	}
	if files, _ := filepath.Glob(filepath.Join(state, "*", "*")); len(files) != 0 {
		t.Fatalf("a demo session wrote to the state dir: %v", files)
	}
}
