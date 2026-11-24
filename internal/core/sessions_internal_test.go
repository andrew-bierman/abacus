package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/agents"
)

func TestSessionsReadOrderRotatesAndCoversEverySession(t *testing.T) {
	s := &Sessions{}
	const n = 4
	leads := map[int]bool{}
	for i := 0; i < n; i++ {
		order := s.readOrder(n)
		seen := map[int]bool{}
		for _, x := range order {
			if x < 0 || x >= n || seen[x] {
				t.Fatalf("read %d order %v is not a permutation of 0..%d", i, order, n-1)
			}
			seen[x] = true
		}
		leads[order[0]] = true
	}
	if len(leads) != n {
		t.Fatalf("over %d reads the lead session was %v, want every session to lead once", n, leads)
	}
	if got := s.readOrder(0); len(got) != 0 {
		t.Fatalf("an empty table has no order: %v", got)
	}
}

func TestSessionsMergeKeepsTheCPUExecutionAndEmptyFields(t *testing.T) {
	s := agents.Session{Title: "project", State: agents.StateExecuting}
	mergeStored(&s, agents.StoredSession{State: agents.StateWaiting, Tokens: agents.Tokens{Input: 3}})
	if s.State != agents.StateExecuting {
		t.Errorf("state = %q, want the CPU delta to win", s.State)
	}
	if s.Title != "project" || s.Tokens.Input != 3 {
		t.Errorf("session = %+v", s)
	}
	s.State = agents.StateWaiting
	mergeStored(&s, agents.StoredSession{State: agents.StateThinking})
	if s.State != agents.StateThinking {
		t.Errorf("state = %q, want the transcript's open turn", s.State)
	}
}

func TestSessionsWithinDoesNotMatchASiblingPrefix(t *testing.T) {
	if !within("/home/dev/.codex", "/home/dev/.codex/sessions/a.jsonl") {
		t.Error("a rollout inside the root must match")
	}
	if within("/home/dev/.codex", "/home/dev/.codex-old/sessions/a.jsonl") {
		t.Error("a sibling with the same prefix must not match")
	}
	if !within("/home/dev/.codex", "/home/dev/.codex") {
		t.Error("the root is contained in itself; the .jsonl filter is what keeps it out")
	}
}

func TestSessionsUnreadableIsBounded(t *testing.T) {
	var r SessionsReport
	for i := 0; i < maxUnreadable+5; i++ {
		r.unreadable(agents.HarnessClaude, errors.New(strings.Repeat("x", maxUnreadableReason*2)))
	}
	if len(r.Unreadable) != maxUnreadable {
		t.Fatalf("kept %d reasons, want %d", len(r.Unreadable), maxUnreadable)
	}
	if len(r.Unreadable[0]) > maxUnreadableReason {
		t.Errorf("reason is %d bytes, want at most %d", len(r.Unreadable[0]), maxUnreadableReason)
	}
}

// probeMachine implements both the cheap and the full read, so a test can see which one
// detection chooses.
type probeMachine struct {
	probes, procs int
	err           error
}

func (m *probeMachine) Procs(context.Context) (map[int]agents.Proc, error) {
	m.procs++
	return nil, m.err
}

func (m *probeMachine) CWD(context.Context, int) string { return "" }

func (m *probeMachine) Listeners(context.Context) ([]agents.Socket, error) { return nil, nil }

func (m *probeMachine) Probe(context.Context) error {
	m.probes++
	return m.err
}

func TestSessionsDetectionUsesTheCheapProbe(t *testing.T) {
	m := &probeMachine{}
	s := NewSessions(SessionsOptions{Machine: m, Home: t.TempDir()})
	if d := s.Detect(context.Background()); d.State != Ready {
		t.Fatalf("detection = %+v", d)
	}
	if m.probes != 1 || m.procs != 0 {
		t.Fatalf("probe=%d procs=%d, want the narrow read only", m.probes, m.procs)
	}
}

func TestSessionsConfigDirsAreDeduped(t *testing.T) {
	got := dedupeDirs([]string{"/home/dev/.claude", "/home/dev/.claude/", "", "/other"})
	if len(got) != 2 || got[0] != "/home/dev/.claude" || got[1] != "/other" {
		t.Fatalf("dirs = %v", got)
	}
}

func TestSessionsStatusTonesAndFallbacks(t *testing.T) {
	r := SessionsReport{Snapshot: agents.Snapshot{Sessions: []agents.Session{
		{Harness: agents.HarnessClaude, PID: 5, State: agents.StateExecuting, Model: "claude-sonnet-5"},
		{Harness: agents.HarnessClaude, PID: 6, CWD: "/home/dev/shop", State: agents.StateThinking},
		{Harness: agents.HarnessClaude, PID: 7, CWD: "/home/dev/tools", State: agents.StateWaiting},
		{Harness: agents.HarnessClaude, PID: 8},
	}}}
	st := r.Status()
	if len(st.Rows) != 4 {
		t.Fatalf("rows = %+v", st.Rows)
	}
	if st.Rows[0].Resource != "claude:5" || st.Rows[0].Detail != "claude-sonnet-5" || st.Rows[0].Tone != ToneOK {
		t.Errorf("row 0 = %+v", st.Rows[0])
	}
	if st.Rows[1].Resource != "shop" || st.Rows[1].State != "thinking" || st.Rows[1].Tone != ToneOK {
		t.Errorf("row 1 = %+v", st.Rows[1])
	}
	if st.Rows[2].State != "waiting" || st.Rows[2].Tone != ToneMuted {
		t.Errorf("row 2 = %+v", st.Rows[2])
	}
	if st.Rows[3].Resource != "claude:8" || st.Rows[3].State != "unknown" || st.Rows[3].Detail != "claude pid 8" || st.Rows[3].Tone != ToneWarn {
		t.Errorf("row 3 = %+v", st.Rows[3])
	}
}

func TestSessionsQuotaListOrdersSourcesAndDropsEmpty(t *testing.T) {
	s := &Sessions{}
	got := s.quotaList(map[agents.Harness]agents.Quota{
		agents.HarnessCodex:  {Source: agents.HarnessCodex},
		agents.HarnessClaude: {Source: agents.HarnessClaude},
	})
	if len(got) != 2 || got[0].Source != agents.HarnessClaude || got[1].Source != agents.HarnessCodex {
		t.Fatalf("quotas = %+v, want source order", got)
	}
	seen := map[agents.Harness]agents.Quota{}
	keepQuota(seen, agents.Quota{})
	if len(seen) != 0 {
		t.Fatalf("a sourceless gauge must not be kept: %+v", seen)
	}
}

func TestSessionsSummaryMentionsOrphans(t *testing.T) {
	r := SessionsReport{Snapshot: agents.Snapshot{Orphans: []agents.Port{{Port: 3000}}}}
	if got := r.Summarize(); !strings.Contains(got, "1 orphaned ports") {
		t.Fatalf("summary = %q", got)
	}
}

func TestSessionsSameDirAllowsAnUnrecordedDirectory(t *testing.T) {
	if !sameSessionDir("", "/home/dev/shop") || !sameSessionDir("/home/dev/shop", "") {
		t.Fatal("an empty recorded or live directory is left to the reader's heuristic")
	}
}

func TestSessionsMergeKeepsThePriorActivityOnAZeroUpdate(t *testing.T) {
	prior := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	s := agents.Session{LastActive: prior}
	mergeStored(&s, agents.StoredSession{Updated: time.Time{}})
	if !s.LastActive.Equal(prior) {
		t.Fatalf("last active = %v, want the prior read", s.LastActive)
	}
}

type failingFiles struct{}

func (failingFiles) OpenFiles(context.Context, int) ([]string, error) {
	return nil, errors.New("no file table")
}

func TestSessionsRolloutIsEmptyWhenTheFileTableCannotBeRead(t *testing.T) {
	s := &Sessions{files: failingFiles{}, codexRoot: "/home/dev/.codex/sessions"}
	if path, ok := s.rolloutFor(context.Background(), 1); ok || path != "" {
		t.Fatalf("rollout = %q, %v", path, ok)
	}
}
