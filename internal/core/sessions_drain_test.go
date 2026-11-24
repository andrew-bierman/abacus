package core_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/agents"
	"github.com/andrew-bierman/abacus/internal/core"
)

// heavyClaudeTranscript writes enough invented records to pass the readers' shared 8 MB budget.
func heavyClaudeTranscript(t *testing.T, lines, inputTokens int) string {
	t.Helper()
	body := "x" + strings.Repeat("y", 900)
	var b strings.Builder
	for i := 0; i < lines; i++ {
		b.WriteString(`{"type":"assistant","sessionId":"ses_heavy","cwd":"/home/dev/heavy","timestamp":"2026-11-24T09:01:00Z","message":{"id":"msg_`)
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`","model":"claude-sonnet-5","content":[{"type":"text","text":"`)
		b.WriteString(body)
		b.WriteString(`"}],"usage":{"input_tokens":`)
		b.WriteString(strconv.Itoa(inputTokens))
		b.WriteString(`,"output_tokens":1}}}`)
		b.WriteString("\n")
	}
	return b.String()
}

// SessionsComplete finishes a history that one bounded read leaves partial, and keeps the totals
// the cursors already accumulated.
func TestSessionsCompleteDrainsPastOneReadsBudget(t *testing.T) {
	home, cwd := t.TempDir(), "/home/dev/heavy"
	started := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	const lines, perLine = 8000, 10
	path := writeHeavy(t, home, heavyClaudeTranscript(t, lines, perLine), started)

	m := &fakeMachine{
		procs: map[int]agents.Proc{2000: {PID: 2000, PPID: 1, Name: "claude", Started: started}},
		cwds:  map[int]string{2000: cwd},
	}
	s := core.NewSessions(core.SessionsOptions{
		Machine: m, Files: fakeFiles{}, Runner: &recRun{}, Home: home, Getenv: noEnv,
		Now: func() time.Time { return started.Add(time.Hour) },
	})

	one, err := s.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(one.Sessions) != 1 {
		t.Fatalf("sessions = %d, want one", len(one.Sessions))
	}
	partial := one.Sessions[0].Tokens.Input
	if partial == 0 {
		t.Fatalf("one read read nothing from %s", path)
	}
	if partial >= lines*perLine {
		t.Fatalf("one read already had the whole history (%d tokens): the fixture no longer exceeds the pass budget", partial)
	}

	full, err := s.SessionsComplete(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := full.Sessions[0].Tokens.Input; got != lines*perLine {
		t.Fatalf("complete read input = %d, want %d (%d records)", got, lines*perLine, lines)
	}

	// A later read still sees the totals the cursors accumulated: the drain finishes the history,
	// it does not replace it.
	after, err := s.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := after.Sessions[0].Tokens.Input; got != lines*perLine {
		t.Fatalf("read after the drain input = %d, want the accumulated %d", got, lines*perLine)
	}
}

// writeHeavy places a transcript where Claude's cross-project fallback finds it, with a write
// time at or after the process start so the join adopts it.
func writeHeavy(t *testing.T, home, body string, started time.Time) string {
	t.Helper()
	path := claudeProjectDir(home, "-other-project") + "/ses_heavy.jsonl"
	writeFile(t, path, body)
	setMtime(t, path, started.Add(time.Minute))
	return path
}

// flakyMachine fails every process read after the first n, so a test can make a later drain
// pass fail after an earlier one already produced a usable report.
type flakyMachine struct {
	*fakeMachine
	ok   int
	seen int
}

func (m *flakyMachine) Procs(ctx context.Context) (map[int]agents.Proc, error) {
	m.seen++
	if m.seen > m.ok {
		return nil, errors.New("boom")
	}
	return m.fakeMachine.Procs(ctx)
}

// A drain that fails after a good pass keeps that report and says why it is partial: the
// alternative is an empty, degraded answer for a session the command had just read.
func TestSessionsCompleteKeepsTheLastGoodPassWhenALaterOneFails(t *testing.T) {
	home, cwd := t.TempDir(), "/home/dev/heavy"
	started := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	const lines, perLine = 8000, 10
	writeHeavy(t, home, heavyClaudeTranscript(t, lines, perLine), started)

	m := &flakyMachine{
		fakeMachine: &fakeMachine{
			procs: map[int]agents.Proc{3000: {PID: 3000, PPID: 1, Name: "claude", Started: started}},
			cwds:  map[int]string{3000: cwd},
		},
		ok: 1,
	}
	s := core.NewSessions(core.SessionsOptions{
		Machine: m, Files: fakeFiles{}, Runner: &recRun{}, Home: home, Getenv: noEnv,
		Now: func() time.Time { return started.Add(time.Hour) },
	})
	rep, err := s.SessionsComplete(context.Background())
	if err != nil {
		t.Fatalf("a later pass failing must not fail the read: %v", err)
	}
	if len(rep.Sessions) != 1 {
		t.Fatalf("sessions = %d, want the first pass's session", len(rep.Sessions))
	}
	if got := rep.Sessions[0].Tokens.Input; got == 0 || got >= lines*perLine {
		t.Fatalf("input = %d, want the first pass's partial tokens", got)
	}
	if len(rep.Unreadable) != 1 || !strings.Contains(rep.Unreadable[0], "boom") {
		t.Fatalf("unreadable = %q, want the later pass's reason", rep.Unreadable)
	}
}
