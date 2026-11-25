package serve_test

import (
	"errors"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/demo"
	"github.com/andrew-bierman/abacus/internal/lambe"
	"github.com/andrew-bierman/abacus/internal/serve"
)

func subscribe(t *testing.T, c *client, topics ...string) core.ServeSubscribed {
	t.Helper()
	var s core.ServeSubscribed
	c.call("subscribe", map[string]any{"topics": topics}).into(t, &s)
	return s
}

func wantNote(t *testing.T, c *client, method, topic string) core.ServeNotice {
	t.Helper()
	m, ok := c.note(3 * time.Second)
	if !ok {
		t.Fatalf("no %s notification", method)
	}
	if m.Method != method || len(m.ID) != 0 {
		t.Fatalf("notification %+v, want %s", m, method)
	}
	var n core.ServeNotice
	if err := jsonUnmarshal(m.Params, &n); err != nil || n.Topic != topic {
		t.Fatalf("params %s: %v", m.Params, err)
	}
	return n
}

func noNote(t *testing.T, c *client, d time.Duration) {
	t.Helper()
	if m, ok := c.note(d); ok {
		t.Fatalf("unexpected notification %+v", m)
	}
}

func TestTasksChangedFollowsTheFingerprint(t *testing.T) {
	c, f := readyClient(t)
	f.tasks.setFP("a", nil)
	if s := subscribe(t, c, "tasks"); len(s.Active) != 1 || s.Active[0] != "tasks" || len(s.Unavailable) != 0 {
		t.Fatalf("%+v", s)
	}
	if s := subscribe(t, c, "tasks"); len(s.Active) != 1 { // idempotent: still one watcher
		t.Fatalf("%+v", s)
	}
	noNote(t, c, 100*time.Millisecond) // the first look is a baseline, not a change
	f.tasks.setFP("b", nil)
	n := wantNote(t, c, "tasks.changed", "tasks")
	if n.Seq != 1 || n.Reason != "fingerprint" {
		t.Errorf("%+v", n)
	}
	noNote(t, c, 100*time.Millisecond) // unchanged: quiet
	f.tasks.setFP("c", nil)
	if n := wantNote(t, c, "tasks.changed", "tasks"); n.Seq != 2 {
		t.Errorf("%+v", n)
	}
	// a read never changes what a notification says: it carries no task data
	if m, _ := c.call("tasks.list", nil), 0; m.Error != nil {
		t.Fatal(m.Error)
	}
	c.call("unsubscribe", map[string]any{"topics": []string{"tasks"}})
	f.tasks.setFP("d", nil)
	noNote(t, c, 150*time.Millisecond)
}

func TestTasksChangedWithoutAFingerprintComparesSnapshots(t *testing.T) {
	f := newFixture(t)
	nf := noFPTasks{f.tasks}
	f.cfg.Build = func() *core.Registry {
		return core.NewRegistry(lambe.NewDemo(), f.leases, nf, f.board, fixedAgents{demo.NewAgents()})
	}
	c := startClient(t, f.cfg)
	c.init()
	subscribe(t, c, "tasks")
	noNote(t, c, 100*time.Millisecond)
	f.tasks.mu.Lock()
	f.tasks.snapGen = 1
	f.tasks.mu.Unlock()
	if n := wantNote(t, c, "tasks.changed", "tasks"); n.Reason != "snapshot" {
		t.Errorf("%+v", n)
	}
	noNote(t, c, 100*time.Millisecond)
}

func TestWorkspaceChangedFollowsTheProvidersFeed(t *testing.T) {
	c, f := readyClient(t)
	if s := subscribe(t, c, "workspace"); len(s.Active) != 1 || s.Active[0] != "workspace" {
		t.Fatalf("%+v", s)
	}
	var changed func()
	select {
	case changed = <-f.leases.changed:
	case <-time.After(3 * time.Second):
		t.Fatal("the watcher never started")
	}
	changed()
	wantNote(t, c, "workspace.changed", "workspace")
	both := subscribe(t, c, "tasks", "workspace")
	if len(both.Active) != 2 {
		t.Errorf("%+v", both)
	}
}

func TestSubscribeToATopicNothingProvidesSaysSo(t *testing.T) {
	f := newFixture(t)
	f.cfg.Build = func() *core.Registry { return core.NewRegistry(f.board) }
	c := startClient(t, f.cfg)
	c.init()
	s := subscribe(t, c, "tasks", "workspace")
	if len(s.Active) != 0 || s.Unavailable["tasks"] == "" || s.Unavailable["workspace"] == "" {
		t.Errorf("%+v", s)
	}
}

func TestEngineDegradedIsToldOnceAndRecovers(t *testing.T) {
	c, f := readyClient(t)
	f.tasks.setFP("a", nil)
	subscribe(t, c, "tasks")
	time.Sleep(50 * time.Millisecond)
	f.tasks.setFP("", errors.New("disk"))
	n := wantNote(t, c, "engine.degraded", "engine")
	if n.Reason == "" {
		t.Errorf("%+v", n)
	}
	noNote(t, c, 150*time.Millisecond) // once per episode, not every look
	f.tasks.setFP("a", nil)
	time.Sleep(60 * time.Millisecond)
	f.tasks.setFP("", errors.New("disk again"))
	wantNote(t, c, "engine.degraded", "engine") // a new episode after a good look
}

func TestNotificationsStopWithTheServer(t *testing.T) {
	f := newFixture(t)
	c := startClient(t, f.cfg)
	c.init()
	f.tasks.setFP("a", nil)
	subscribe(t, c, "tasks", "workspace")
	if err := c.shutdownAndWait(); err != nil {
		t.Fatal(err)
	}
	f.tasks.setFP("zzz", nil)
	noNote(t, c, 100*time.Millisecond)
}

var _ = serve.New

func TestAProviderPanicInAWatcherBecomesEngineDegraded(t *testing.T) {
	c, f := readyClient(t)
	f.tasks.mu.Lock()
	f.tasks.panicFP = true
	f.tasks.mu.Unlock()
	subscribe(t, c, "tasks")
	if n := wantNote(t, c, "engine.degraded", "engine"); n.Reason == "" {
		t.Errorf("%+v", n)
	}
	if m := c.call("version", nil); m.Error != nil {
		t.Fatal("the server did not survive")
	}
	// the watcher is gone, so subscribing starts a fresh one
	f.tasks.mu.Lock()
	f.tasks.panicFP = false
	f.tasks.mu.Unlock()
	f.tasks.setFP("a", nil)
	if s := subscribe(t, c, "tasks"); len(s.Active) != 1 {
		t.Errorf("%+v", s)
	}
}
