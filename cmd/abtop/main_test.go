package main

import (
	"context"
	"testing"

	"github.com/andrew-bierman/abacus/internal/agents"
	"github.com/andrew-bierman/abacus/internal/core"
)

// fakeSessions is a Sessions provider that records which read its caller used: Sessions is the
// tab's bounded read, SessionsComplete the draining one-shot read. The two return different
// session counts so a test can tell which read produced the report.
type fakeSessions struct {
	bounded, complete int
}

func (f *fakeSessions) ID() string                          { return string(core.DomainSessions) }
func (f *fakeSessions) Name() string                        { return "sessions" }
func (f *fakeSessions) Doctor(context.Context) []core.Check { return nil }
func (f *fakeSessions) Status(context.Context) core.Status  { return core.Status{} }

func (f *fakeSessions) Detect(context.Context) core.Detection {
	return core.Detection{State: core.Ready}
}

func (f *fakeSessions) Sessions(context.Context) (core.SessionsReport, error) {
	f.bounded++
	return report(agents.Session{PID: 1}), nil
}

func (f *fakeSessions) SessionsComplete(context.Context) (core.SessionsReport, error) {
	f.complete++
	return report(agents.Session{PID: 1}, agents.Session{PID: 2}), nil
}

func report(sessions ...agents.Session) core.SessionsReport {
	return core.SessionsReport{
		Header:   core.Header{Domain: core.DomainSessions, State: core.Connected, Source: core.SourceLive},
		Snapshot: agents.Snapshot{Sessions: sessions},
	}
}

func TestSessionReadersTakeTheCompleteReadOnceAndTheBoundedReadPerFrame(t *testing.T) {
	fake := &fakeSessions{}
	detect := func(context.Context) []core.Entry {
		return []core.Entry{{Module: fake, Detection: core.Detection{State: core.Ready}}}
	}
	once, tick := sessionReaders(detect)
	ctx := context.Background()

	rep, err := once(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if fake.complete != 1 || fake.bounded != 0 {
		t.Fatalf("--once used complete=%d bounded=%d, want the complete read", fake.complete, fake.bounded)
	}
	if len(rep.Sessions) != 2 {
		t.Fatalf("--once read %d sessions, want the draining read's 2", len(rep.Sessions))
	}

	rep, err = tick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if fake.complete != 1 || fake.bounded != 1 {
		t.Fatalf("a frame used complete=%d bounded=%d, want one bounded read", fake.complete, fake.bounded)
	}
	if len(rep.Sessions) != 1 {
		t.Fatalf("a frame read %d sessions, want the bounded read's 1", len(rep.Sessions))
	}
}
