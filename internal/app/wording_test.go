package app

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/cli"
	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/embedtools/embedtest"
	"github.com/andrew-bierman/abacus/internal/rex"
)

// The tripwire: no message abacus prints may tell the user to install a tool, run the old bundle
// script, or put anything on PATH. Every tool is built in; a problem is described, and the one fix
// is a folder to write to or a fresh download.
var (
	bannedAdvice = regexp.MustCompile(`(?i)install|dev-bundle|make bundle|npm i|npx|scripts/|re-extract`)
	bannedPATH   = regexp.MustCompile(`\bPATH\b`)
)

func runAll(t *testing.T, e Env) string {
	t.Helper()
	var all strings.Builder
	for _, args := range [][]string{
		{"doctor"}, {"status"}, {"version"}, {"setup", "--dry-run"}, {"tasks"}, {"leases"}, {"queues"}, {"board"}, {"agents"},
		{"doctor", "--json"}, {"status", "--json"}, {"rex", "layout", "--dry-run"},
	} {
		var o, er bytes.Buffer
		cli.Run(args, cli.Deps{Out: &o, Err: &er, Version: "1.2.3", Config: e.Config,
			Build: func(op cli.Options) *core.Registry { return Build(op, e) }})
		all.WriteString("$ " + strings.Join(args, " ") + "\n" + o.String() + er.String() + "\n")
	}
	return all.String()
}

func assertNoAdvice(t *testing.T, state, out string, allowPATH bool) {
	t.Helper()
	// Paths that the test itself created are not advice.
	for _, line := range strings.Split(out, "\n") {
		if m := bannedAdvice.FindString(line); m != "" {
			t.Errorf("%s: %q appears in %q", state, m, line)
		}
		if !allowPATH && bannedPATH.MatchString(line) {
			t.Errorf("%s: PATH appears in %q", state, line)
		}
	}
}

func scripts(t *testing.T) *embedtest.Set {
	t.Helper()
	const tag = "builtin"
	body := map[string]string{
		"bd":          `echo '{"version":"` + tag + `","schema_version":1}'`,
		"lambe":       "case \"$1\" in\n  version) echo '{\"version\":\"" + tag + "\",\"api_version\":1}' ;;\n  doctor) echo '[]' ;;\nesac",
		"beads-lease": `echo '{"api_version":1,"version":"` + tag + `"}'`,
		"abtop":       `echo '{"sessions":[]}'`,
		"board":       "case \"$1\" in\n  --help) printf 'board 0.54.0: your Vercel team'\"'\"'s message board, for people and agents\\n\\nusage: board <command>\\n' ;;\nesac",
	}
	var ts []embedtest.Tool
	for _, n := range []string{"bd", "lambe", "beads-lease", "board", "abtop"} {
		ts = append(ts, embedtest.Tool{Name: n, Version: "1.0.0", Body: "#!/bin/sh\n" + body[n] + "\n"})
	}
	return embedtest.New("# notices\n", nil, ts...)
}

func TestTripwireNoAdviceInAnyState(t *testing.T) {
	empty := t.TempDir()
	core.SidecarDirs = func() []string { return []string{empty} }
	t.Cleanup(func() { core.SidecarDirs = func() []string { return nil } })
	t.Cleanup(core.CleanupSidecarEnv)

	t.Run("development build, no tools", func(t *testing.T) {
		t.Cleanup(core.SetEmbeddedForTest(nil))
		t.Setenv("PATH", empty)
		out := runAll(t, env(t, nil))
		if !strings.Contains(out, "Development build: tools are not built in; build with make build") {
			t.Fatal("Doctor's development-build line is missing")
		}
		assertNoAdvice(t, "dev build", out, false)
		// The development-build line is only in Doctor's summary, never in a domain's output.
		for _, cmd := range []string{"tasks", "leases", "queues", "board"} {
			if seg := between(out, "$ "+cmd+"\n", "\n$ "); strings.Contains(seg, "not built in") {
				t.Errorf("%s output carries the development-build line:\n%s", cmd, seg)
			}
		}
	})
	t.Run("development build, tools found on PATH", func(t *testing.T) {
		t.Cleanup(core.SetEmbeddedForTest(nil))
		t.Setenv("PATH", fakeBinDir(t, "dev")+string(os.PathListSeparator)+"/usr/bin:/bin")
		assertNoAdvice(t, "dev build with tools", runAll(t, env(t, nil)), true)
	})
	t.Run("built in and working", func(t *testing.T) {
		t.Cleanup(core.SetEmbeddedForTest(scripts(t).FS))
		t.Setenv(core.ToolsDirEnv, filepath.Join(t.TempDir(), "tools"))
		t.Setenv("PATH", empty)
		out := runAll(t, env(t, nil))
		if !strings.Contains(out, "Built-in tools: 5 of 5 ready") || !strings.Contains(out, "built in") {
			t.Fatalf("%s", out)
		}
		assertNoAdvice(t, "embedded", out, false)
	})
	t.Run("built in but the folder cannot be written", func(t *testing.T) {
		t.Cleanup(core.SetEmbeddedForTest(scripts(t).FS))
		blocker := filepath.Join(t.TempDir(), "file")
		os.WriteFile(blocker, []byte("x"), 0o600)
		t.Setenv(core.ToolsDirEnv, filepath.Join(blocker, "tools"))
		t.Setenv("PATH", fakeBinDir(t, "other")) // a different bd on PATH must not be used
		out := runAll(t, env(t, nil))
		if !strings.Contains(out, "ABACUS_TOOLS_DIR=<folder>") || strings.Contains(out, `"version": "other"`) {
			t.Fatalf("%s", out)
		}
		assertNoAdvice(t, "embedded, unwritable", out, false)
	})
	t.Run("built in but damaged", func(t *testing.T) {
		set := scripts(t)
		set.FS["bd.zst"].Data = set.FS["bd.zst"].Data[:10]
		t.Cleanup(core.SetEmbeddedForTest(set.FS))
		t.Setenv(core.ToolsDirEnv, filepath.Join(t.TempDir(), "tools"))
		t.Setenv("PATH", empty)
		out := runAll(t, env(t, nil))
		if !strings.Contains(out, "This copy of abacus is damaged; download it again.") {
			t.Fatalf("%s", out)
		}
		assertNoAdvice(t, "embedded, damaged", out, false)
	})
}

func between(s, from, to string) string {
	_, rest, ok := strings.Cut(s, from)
	if !ok {
		return ""
	}
	seg, _, _ := strings.Cut(rest, to)
	return seg
}

// Every rendered tab and state of the TUI (the golden renders) carries no such advice either.
func TestTripwireNoAdviceInAnyTuiRender(t *testing.T) {
	files, _ := filepath.Glob("../tui/testdata/golden/*.golden")
	if len(files) < 10 {
		t.Fatalf("expected the TUI golden renders, found %d", len(files))
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		for _, line := range strings.Split(string(b), "\n") {
			if m := bannedAdvice.FindString(line); m != "" {
				t.Errorf("%s: %q in %q", filepath.Base(f), m, line)
			}
			if bannedPATH.MatchString(line) {
				t.Errorf("%s: PATH in %q", filepath.Base(f), line)
			}
		}
	}
}

// The user-facing constants and sentences, directly.
func TestBuiltInSentencesCarryNoAdvice(t *testing.T) {
	for _, embedded := range []bool{true, false} {
		if embedded {
			t.Cleanup(core.SetEmbeddedForTest(scripts(t).FS))
		} else {
			t.Cleanup(core.SetEmbeddedForTest(nil))
		}
		for _, tool := range core.KnownTools {
			for _, why := range []string{"", "could not write into /x: permission denied", "does not match what was built into abacus"} {
				s := core.BundledProblem(tool, why)
				if bannedAdvice.MatchString(s) || bannedPATH.MatchString(s) {
					t.Errorf("%q", s)
				}
			}
		}
		for _, s := range []string{core.HowToConnectBoard, core.BoardNotSetUp, core.BoardNotSignedIn, core.HowToConnectBoardDegraded, core.HowToConnectDegraded, core.HowToConnectInsights(), core.FullViewMissing, core.DevBuildLine, rex.HowToConnect} {
			if bannedAdvice.MatchString(s) || bannedPATH.MatchString(s) {
				t.Errorf("%q", s)
			}
		}
	}
}
