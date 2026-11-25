package serve_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/serve"
)

func mustMap(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("not an object: %v\n%s", err, raw)
	}
	return m
}

// ---- handshake ----

func TestInitializeHandshakeAndGate(t *testing.T) {
	f := newFixture(t)
	c := startClient(t, f.cfg)
	// nothing but initialize works before it
	c.call("tasks.list", nil).wantErr(t, core.CodeRefused)
	if e := c.call("tasks.list", nil).Error; e.Code != -32002 {
		t.Errorf("not initialized: code %d", e.Code)
	}
	// a protocol version the server does not speak is refused, and the gate stays shut
	if e := c.call("initialize", map[string]any{"protocol_version": 2}).wantErr(t, core.CodeUnsupported); !strings.Contains(e.Message, "version 1") {
		t.Errorf("message: %q", e.Message)
	}
	c.call("version", nil).wantErr(t, core.CodeRefused)
	init := c.init()
	if init.ProtocolVersion != 1 || init.AbacusVersion != "0.0.0-serve" {
		t.Errorf("initialize: %+v", init)
	}
	for _, m := range []string{"initialize", "shutdown", "version", "doctor", "status", "board", "leases", "queues", "agents", "search", "tasks.summary", "tasks.list", "tasks.tree", "tasks.board",
		"tasks.show", "tasks.graph", "tasks.history", "workspace", "setup.state", "config.show", "subscribe", "unsubscribe", "plan.task.create", "plan.task.update", "plan.task.close", "plan.task.reopen",
		"plan.task.label-add", "plan.task.label-remove", "plan.task.assign", "plan.task.priority", "plan.task.dep-add", "plan.task.dep-remove", "plan.task.comment", "plan.tasks.bulk",
		"plan.board.post", "plan.board.reply", "plan.cancel", "apply"} {
		found := false
		for _, x := range init.Capabilities.Methods {
			found = found || x == m
		}
		if !found {
			t.Errorf("capabilities.methods lacks %s", m)
		}
	}
	l := init.Capabilities.Limits
	if l.MaxRequestBytes != 1<<20 || l.MaxDepth != 32 || l.MaxInFlight != 8 || l.MaxQueued != 32 || l.PlanTTLSeconds != 600 || l.DefaultDeadlineMS != 30000 {
		t.Errorf("limits: %+v", l)
	}
	var raw rpcMsg
	raw = c.call("initialize", map[string]any{"protocol_version": 1})
	if v := mustMap(t, raw.Result)["schema_version"]; string(v) != "1" {
		t.Errorf("schema_version: %s", v)
	}
}

// ---- every read method answers with its typed report ----

func TestEveryReadMethod(t *testing.T) {
	c, _ := readyClient(t)
	for _, tc := range []struct {
		method string
		params any
		key    string // a key the typed report has
	}{
		{"version", nil, "modules"}, {"doctor", nil, "overview"}, {"status", nil, "domains"},
		{"board", nil, "channels"}, {"leases", nil, "leases"}, {"queues", nil, "runs"}, {"agents", nil, "agents"},
		{"tasks.summary", nil, "counts"}, {"tasks.list", nil, "rows"}, {"tasks.tree", nil, "rows"}, {"tasks.board", nil, "columns"},
		{"tasks.show", map[string]any{"id": "ac-12"}, "comments"}, {"tasks.graph", map[string]any{"id": "ac-12"}, "nodes"},
		{"tasks.graph", map[string]any{"whole": true}, "layers"}, {"tasks.history", map[string]any{"id": "ac-12"}, "events"},
		{"search", map[string]any{"query": "lease"}, "groups"}, {"workspace", nil, "placements"},
		{"setup.state", nil, "steps"}, {"config.show", nil, "path"},
	} {
		m := c.call(tc.method, tc.params)
		if m.Error != nil {
			t.Errorf("%s: %+v", tc.method, m.Error)
			continue
		}
		obj := mustMap(t, m.Result)
		if string(obj["schema_version"]) != "1" {
			t.Errorf("%s: schema_version %s", tc.method, obj["schema_version"])
		}
		if _, ok := obj[tc.key]; !ok {
			t.Errorf("%s: no %q in %s", tc.method, tc.key, m.Result)
		}
		if strings.ContainsAny(m.Raw, "\n") {
			t.Errorf("%s: the response is not one line", tc.method)
		}
	}
}

func TestReadParamsAreValidated(t *testing.T) {
	c, _ := readyClient(t)
	for _, tc := range []struct {
		method string
		params any
	}{
		{"tasks.list", map[string]any{"sort": "nosuch"}}, {"tasks.list", map[string]any{"preset": "nosuch"}}, {"tasks.list", map[string]any{"query": "status:"}},
		{"tasks.list", map[string]any{"bogus": 1}}, {"tasks.list", map[string]any{"reverse": "yes"}}, {"tasks.board", map[string]any{"group": "nosuch"}},
		{"tasks.show", nil}, {"tasks.graph", map[string]any{"depth": 9}}, {"tasks.graph", map[string]any{"whole": true, "id": "ac-12"}},
		{"tasks.history", map[string]any{"id": "bad id"}}, {"tasks.history", map[string]any{"id": "ac-12", "limit": 100000}},
		{"search", map[string]any{"query": ""}}, {"search", map[string]any{"query": "x", "limit": 0 - 1}}, {"search", map[string]any{"query": "x", "domains": []string{"nosuch"}}},
		{"agents", map[string]any{"state": "nosuch"}}, {"version", map[string]any{"x": 1}}, {"subscribe", map[string]any{"topics": []string{}}},
		{"subscribe", map[string]any{"topics": []string{"nosuch"}}},
	} {
		m := c.call(tc.method, tc.params)
		if e := m.wantErr(t, core.CodeUsage); e.Code != -32602 {
			t.Errorf("%s %v: rpc code %d", tc.method, tc.params, e.Code)
		}
	}
	c.call("tasks.show", map[string]any{"id": "nosuch-1"}).wantErr(t, core.CodeNotFound)
	c.call("tasks.graph", map[string]any{"id": "nosuch-1"}).wantErr(t, core.CodeNotFound)
}

func TestNotConnectedKeepsItsShapeAndClosedCode(t *testing.T) {
	f := newFixture(t)
	f.cfg.Build = func() *core.Registry { return core.NewRegistry() }
	c := startClient(t, f.cfg)
	c.init()
	// a domain read is not an error: the report says it is not connected
	var b struct{ State string }
	c.call("board", nil).into(t, &b)
	if b.State != "not-connected" {
		t.Errorf("board: %q", b.State)
	}
	// a read that needs the domain is the closed code not_connected, with how to connect
	e := c.call("tasks.list", nil).wantErr(t, core.CodeNotConnected)
	if !strings.Contains(e.Message, "connect") && !strings.Contains(strings.ToLower(e.Message), "tasks") {
		t.Errorf("message: %q", e.Message)
	}
	c.call("plan.task.close", map[string]any{"id": "ac-1"}).wantErr(t, core.CodeNotConnected)
	c.call("plan.board.post", map[string]any{"channel": "dev", "text": "x"}).wantErr(t, core.CodeNotConnected)
}

// ---- framing, limits and malformed input ----

func TestMalformedAndHostileInput(t *testing.T) {
	f := newFixture(t)
	f.cfg.Limits.MaxRequestBytes = 4096
	c := startClient(t, f.cfg)
	c.init()
	deep := strings.Repeat(`{"a":`, 40) + "1" + strings.Repeat("}", 40)
	for name, tc := range map[string]struct {
		line string
		rpc  int
	}{
		"not json":          {`{"jsonrpc":"2.0","id":1,`, -32700},
		"garbage":           {`hello`, -32700},
		"invalid utf8":      {"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"\xff\"}", -32700},
		"array batch":       {`[{"jsonrpc":"2.0","id":1,"method":"version"}]`, -32600},
		"scalar":            {`42`, -32600},
		"too deep":          {`{"jsonrpc":"2.0","id":7,"method":"version","params":` + deep + `}`, -32600},
		"unknown member":    {`{"jsonrpc":"2.0","id":7,"method":"version","extra":1}`, -32600},
		"float id":          {`{"jsonrpc":"2.0","id":1.5,"method":"version"}`, -32600},
		"null id":           {`{"jsonrpc":"2.0","id":null,"method":"version"}`, -32600},
		"object id":         {`{"jsonrpc":"2.0","id":{},"method":"version"}`, -32600},
		"huge id":           {`{"jsonrpc":"2.0","id":99999999999999999999,"method":"version"}`, -32600},
		"long string id":    {`{"jsonrpc":"2.0","id":"` + strings.Repeat("x", 129) + `","method":"version"}`, -32600},
		"oversized":         {`{"jsonrpc":"2.0","id":8,"method":"search","params":{"query":"` + strings.Repeat("a", 5000) + `"}}`, -32600},
		"huge unterminated": {`{"jsonrpc":"2.0","id":8,"method":"search","params":{"query":"` + strings.Repeat("a", 300000) + `"}}`, -32600},
	} {
		c.raw(tc.line)
		m := c.anonResponse()
		if m.Error == nil || m.Error.Code != tc.rpc {
			// a request with a usable id answers under that id instead
			t.Errorf("%s: %+v", name, m.Error)
			continue
		}
		if m.Error.Data.Code != core.CodeUsage {
			t.Errorf("%s: code %s", name, m.Error.Data.Code)
		}
		// and the server is still there
		if m := c.call("version", nil); m.Error != nil {
			t.Fatalf("%s: the server did not survive: %+v", name, m.Error)
		}
	}
	// a bad method, params that are not an object, a bad deadline: answered under the request's id
	c.call("no.such", nil).wantErr(t, core.CodeUsage)
	if e := c.call("no.such", nil).Error; e.Code != -32601 {
		t.Errorf("unknown method: %d", e.Code)
	}
	id := c.send("version", []int{1})
	c.await(id).wantErr(t, core.CodeUsage)
	for _, d := range []int{0, -5, 99999999} {
		c.call("version", nil, d).wantErr(t, core.CodeUsage)
	}
	// blank lines are ignored, a notification gets no answer
	c.raw("")
	c.raw(`{"jsonrpc":"2.0","method":"version"}`)
	if m := c.call("version", nil); m.Error != nil {
		t.Fatalf("%+v", m.Error)
	}
	select {
	case m := <-c.anon:
		t.Errorf("unexpected response with a null id: %+v", m)
	default:
	}
}

func TestADuplicateInFlightIdIsRefused(t *testing.T) {
	c, f := readyClient(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	f.tasks.mu.Lock()
	f.tasks.snapHook = func(ctx context.Context) error { started <- struct{}{}; <-release; return nil }
	f.tasks.mu.Unlock()
	id := c.expect(`"dup"`)
	c.raw(`{"jsonrpc":"2.0","id":"dup","method":"tasks.list"}`)
	<-started
	c.raw(`{"jsonrpc":"2.0","id":"dup","method":"version"}`)
	// the refusal and the real answer both carry the id; the refusal comes first
	first := c.await(id)
	first.wantErr(t, core.CodeUsage)
	if !strings.Contains(first.Error.Message, "still in flight") {
		t.Errorf("message: %q", first.Error.Message)
	}
	close(release)
	// once the request is done its id may be used again
	c.expect(`"dup"`)
	for i := 0; i < 100; i++ {
		c.raw(`{"jsonrpc":"2.0","id":"dup","method":"version"}`)
		m := c.await(`"dup"`)
		if m.Error == nil {
			return
		}
		c.expect(`"dup"`)
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the id was never free again")
}

func TestAWrongJSONRPCVersionIsAnsweredUnderItsId(t *testing.T) {
	c, _ := readyClient(t)
	id := c.expect(`"a"`)
	c.raw(`{"jsonrpc":"1.0","id":"a","method":"version"}`)
	if e := c.await(id).wantErr(t, core.CodeUsage); e.Code != -32600 {
		t.Errorf("code %d", e.Code)
	}
}

// ---- cancellation, deadlines, concurrency ----

func slowTasks(f *fixture) (started chan struct{}) {
	started = make(chan struct{}, 16)
	f.tasks.mu.Lock()
	f.tasks.snapHook = func(ctx context.Context) error {
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}
	f.tasks.mu.Unlock()
	return started
}

func TestCancelRequest(t *testing.T) {
	c, f := readyClient(t)
	started := slowTasks(f)
	id := c.send("tasks.list", nil)
	<-started
	c.cancel(id)
	e := c.await(id).wantErr(t, core.CodeTimeout)
	if e.Code != -32800 || !e.Data.Retryable {
		t.Errorf("cancelled: %+v", e)
	}
	// a cancel for an id nobody is serving, and a malformed one, are ignored
	c.cancel("12345")
	c.raw(`{"jsonrpc":"2.0","method":"$/cancelRequest","params":{"id":[1]}}`)
	c.raw(`{"jsonrpc":"2.0","method":"$/cancelRequest"}`)
	if m := c.call("version", nil); m.Error != nil {
		t.Fatal(m.Error)
	}
}

func TestPerRequestDeadline(t *testing.T) {
	c, f := readyClient(t)
	slowTasks(f)
	start := time.Now()
	e := c.call("tasks.list", nil, 60).wantErr(t, core.CodeTimeout)
	if time.Since(start) > 3*time.Second || !strings.Contains(e.Message, "out of time") {
		t.Errorf("deadline: %v %q", time.Since(start), e.Message)
	}
	// the default deadline is announced and applies to every method
	if v := c.init().Capabilities.Limits.DefaultDeadlineMS; v != 30000 {
		t.Errorf("default deadline %d", v)
	}
}

func TestOneReadInFlightPerDomain(t *testing.T) {
	f := newFixture(t)
	f.cfg.Limits.MaxInFlight = 3
	c := startClient(t, f.cfg)
	c.init()
	started := slowTasks(f)
	ids := []string{c.send("tasks.list", nil), c.send("tasks.board", nil), c.send("tasks.tree", nil)}
	<-started
	time.Sleep(50 * time.Millisecond)
	select {
	case <-started:
		t.Fatal("two reads of the tracker ran at once")
	default:
	}
	if n := f.tasks.max.Load(); n != 1 {
		t.Errorf("at most one read of the tracker at a time, saw %d", n)
	}
	for _, id := range ids {
		c.cancel(id)
	}
	for _, id := range ids {
		c.await(id).wantErr(t, core.CodeTimeout)
	}
}

// Past MaxInFlight a request waits for a slot, in arrival order, up to MaxQueued; the one after that is
// refused at once with "busy" and a retry_after_ms, and nothing of it ran.
func TestRequestsPastTheInFlightLimitQueueThenAreRefusedBusy(t *testing.T) {
	f := newFixture(t)
	f.cfg.Limits.MaxInFlight, f.cfg.Limits.MaxQueued = 3, 2
	c := startClient(t, f.cfg)
	if l := c.init().Capabilities.Limits; l.MaxInFlight != 3 || l.MaxQueued != 2 {
		t.Fatalf("limits: %+v", l)
	}
	started := slowTasks(f)
	slow := []string{c.send("tasks.list", nil), c.send("tasks.board", nil), c.send("tasks.tree", nil)}
	<-started
	q1, q2 := c.send("version", nil), c.send("status", nil) // wait for a slot
	time.Sleep(100 * time.Millisecond)
	e := c.call("leases", nil).wantErr(t, core.CodeBusy)
	if e.Data.RetryAfterMS <= 0 || !e.Data.Retryable || e.Data.UnknownOutcome {
		t.Errorf("busy: %+v", e.Data)
	}
	// a slot frees: the first to wait runs, the second still waits its turn
	c.cancel(slow[0])
	c.await(slow[0]).wantErr(t, core.CodeTimeout)
	if m := c.await(q1); m.Error != nil {
		t.Fatalf("the first queued request: %+v", m.Error)
	}
	c.mu.Lock()
	_, early := c.early[q2]
	c.mu.Unlock()
	if early {
		t.Error("the second queued request ran before a second slot was free")
	}
	c.cancel(slow[1])
	c.await(slow[1]).wantErr(t, core.CodeTimeout)
	if m := c.await(q2); m.Error != nil {
		t.Fatalf("the second queued request: %+v", m.Error)
	}
	// the queue has room again
	if m := c.call("leases", nil); m.Error != nil {
		t.Errorf("after the queue drained: %+v", m.Error)
	}
	c.cancel(slow[2])
	c.await(slow[2]).wantErr(t, core.CodeTimeout)
}

// A request's deadline covers the time it waits for a slot, and a cancel reaches it there.
func TestAQueuedRequestKeepsItsDeadlineAndCanBeCancelled(t *testing.T) {
	f := newFixture(t)
	f.cfg.Limits.MaxInFlight = 1
	c := startClient(t, f.cfg)
	c.init()
	started := slowTasks(f)
	slow := c.send("tasks.list", nil)
	<-started
	start := time.Now()
	c.call("version", nil, 80).wantErr(t, core.CodeTimeout)
	if time.Since(start) > 3*time.Second {
		t.Errorf("a queued request ran past its deadline: %v", time.Since(start))
	}
	q := c.send("version", nil)
	time.Sleep(50 * time.Millisecond)
	c.cancel(q)
	if e := c.await(q).wantErr(t, core.CodeTimeout); e.Code != -32800 {
		t.Errorf("cancelled while queued: %+v", e)
	}
	c.cancel(slow)
	c.await(slow).wantErr(t, core.CodeTimeout)
}

func TestConcurrentRequestsAreRaceFree(t *testing.T) {
	c, f := readyClient(t)
	f.cfg.Limits.MaxInFlight = 64
	methods := []string{"version", "status", "board", "leases", "queues", "agents", "tasks.summary", "tasks.list", "tasks.tree", "tasks.board", "workspace", "setup.state", "config.show"}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, m := range methods {
				for {
					r := c.call(m, nil)
					if r.Error != nil && r.Error.Data.Code == core.CodeBusy { // the queue is full: wait and ask again
						time.Sleep(5 * time.Millisecond)
						continue
					}
					if r.Error != nil {
						t.Errorf("%s: %+v", m, r.Error)
					}
					break
				}
			}
		}()
	}
	wg.Wait()
}

func TestAHandlerThatPanicsDoesNotTakeTheServerDown(t *testing.T) {
	c, f := readyClient(t)
	f.tasks.mu.Lock()
	f.tasks.snapHook = func(context.Context) error { panic("secret task text") }
	f.tasks.mu.Unlock()
	e := c.call("tasks.list", nil).wantErr(t, core.CodeFailed)
	if e.Code != -32603 || strings.Contains(e.Message, "secret") {
		t.Errorf("internal error: %+v", e)
	}
	f.tasks.mu.Lock()
	f.tasks.snapHook = nil
	f.tasks.mu.Unlock()
	if m := c.call("tasks.list", nil); m.Error != nil {
		t.Fatal(m.Error)
	}
}

func TestAnOversizedResultIsAnErrorNotAHugeLine(t *testing.T) {
	f := newFixture(t)
	f.cfg.Limits.MaxResponseBytes = 2000
	c := startClient(t, f.cfg)
	c.init()
	e := c.call("tasks.list", nil).wantErr(t, core.CodeFailed)
	if !strings.Contains(e.Message, "2000 bytes") {
		t.Errorf("message: %q", e.Message)
	}
}

// ---- shutdown and the ends of the stream ----

func TestShutdown(t *testing.T) {
	c, _ := readyClient(t)
	if err := c.shutdownAndWait(); err != nil {
		t.Fatal(err)
	}
}

func TestEndOfInputStopsTheServer(t *testing.T) {
	f := newFixture(t)
	c := startClient(t, f.cfg)
	c.init()
	c.w.Close()
	select {
	case err := <-c.done:
		if err != nil {
			t.Fatal(err)
		}
		c.done <- err
	case <-time.After(5 * time.Second):
		t.Fatal("the server kept running after its input ended")
	}
}

func TestShutdownLetsAWriteInFlightFinishThenCancelsIt(t *testing.T) {
	f := newFixture(t)
	f.cfg.WriteGrace = 150 * time.Millisecond
	c := startClient(t, f.cfg)
	c.init()
	var plan core.ServePlan
	c.call("plan.task.close", map[string]any{"id": "ac-12"}).into(t, &plan)
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	f.tasks.mu.Lock()
	f.tasks.runHook = func(ctx context.Context, _ []string) error {
		started <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.tasks.mu.Unlock()
	id := c.send("apply", map[string]any{"plan_id": plan.PlanID})
	<-started
	sd := c.send("shutdown", nil)
	// the write outlives the grace: it is cancelled, and a cancelled write is a result unknown
	e := c.await(id).wantErr(t, core.CodeUnknownOutcome)
	if !e.Data.UnknownOutcome || e.Data.Outcome != "unknown" || e.Data.PlanID != plan.PlanID {
		t.Errorf("cancelled write: %+v", e)
	}
	if m := c.await(sd); m.Error != nil {
		t.Fatalf("shutdown: %+v", m.Error)
	}
	// after shutdown, requests are refused
	select {
	case err := <-c.done:
		if err != nil {
			t.Fatal(err)
		}
		c.done <- err
	case <-time.After(5 * time.Second):
		t.Fatal("no stop")
	}
	_ = release
}

func TestShutdownWaitsForAWriteThatFinishesInTheGrace(t *testing.T) {
	f := newFixture(t)
	f.cfg.WriteGrace = 3 * time.Second
	c := startClient(t, f.cfg)
	c.init()
	var plan core.ServePlan
	c.call("plan.task.close", map[string]any{"id": "ac-12"}).into(t, &plan)
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	f.tasks.mu.Lock()
	f.tasks.runHook = func(ctx context.Context, _ []string) error {
		started <- struct{}{}
		<-release
		return nil
	}
	f.tasks.mu.Unlock()
	id := c.send("apply", map[string]any{"plan_id": plan.PlanID})
	<-started
	sd := c.send("shutdown", nil)
	time.Sleep(50 * time.Millisecond)
	close(release)
	var ap core.ServeApplied
	c.await(id).into(t, &ap)
	if ap.Outcome != "applied" {
		t.Errorf("%+v", ap)
	}
	if m := c.await(sd); m.Error != nil {
		t.Fatal(m.Error)
	}
}

func TestRequestsAfterShutdownAreRefused(t *testing.T) {
	f := newFixture(t)
	c := startClient(t, f.cfg)
	c.init()
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	f.tasks.mu.Lock()
	f.tasks.snapHook = func(ctx context.Context) error { started <- struct{}{}; <-release; return nil }
	f.tasks.mu.Unlock()
	rd := c.send("tasks.list", nil)
	<-started
	sd := c.send("shutdown", nil)
	time.Sleep(30 * time.Millisecond)
	c.call("version", nil).wantErr(t, core.CodeRefused)
	close(release)
	c.await(rd) // a read in flight at shutdown is cancelled or finishes: either way it is answered
	if m := c.await(sd); m.Error != nil {
		t.Fatal(m.Error)
	}
}

// a client that never reads must not hold the server forever
type blockedWriter struct{ block chan struct{} }

func (w blockedWriter) Write(p []byte) (int, error) { <-w.block; return len(p), nil }

func TestAClientThatStopsReadingEndsTheServer(t *testing.T) {
	f := newFixture(t)
	f.cfg.WriteStall = 200 * time.Millisecond
	inR, inW := io.Pipe()
	defer inW.Close()
	bw := blockedWriter{make(chan struct{})}
	defer close(bw.block)
	done := make(chan error, 1)
	go func() { done <- serve.New(f.cfg).Serve(context.Background(), inR, bw) }()
	io.WriteString(inW, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocol_version":1}}`+"\n")
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "did not read") {
			t.Errorf("error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server waited on a client that does not read")
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestAClosedPipeEndsTheServerWithAnError(t *testing.T) {
	f := newFixture(t)
	inR, inW := io.Pipe()
	defer inW.Close()
	done := make(chan error, 1)
	go func() { done <- serve.New(f.cfg).Serve(context.Background(), inR, brokenWriter{}) }()
	io.WriteString(inW, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocol_version":1}}`+"\n")
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "stopped reading") {
			t.Errorf("error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no stop")
	}
}

func TestParentContextEndsTheServer(t *testing.T) {
	f := newFixture(t)
	inR, inW := io.Pipe()
	defer inW.Close()
	outR, outW := io.Pipe()
	go io.Copy(io.Discard, outR)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve.New(f.cfg).Serve(ctx, inR, outW) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no stop")
	}
}

var _ = os.Getenv
var _ = fmt.Sprint

// The limits are exact: a request of exactly the limit is served, one byte more is not.
func TestRequestSizeAndDepthLimitsAreExact(t *testing.T) {
	f := newFixture(t)
	f.cfg.Limits.MaxRequestBytes = 4096
	c := startClient(t, f.cfg)
	c.init()
	pad := func(n int) string { // a valid request of exactly n bytes (before the newline)
		base := `{"jsonrpc":"2.0","id":"p","method":"version"}`
		return base + strings.Repeat(" ", n-len(base))
	}
	id := c.expect(`"p"`)
	c.raw(pad(4096))
	if m := c.await(id); m.Error != nil {
		t.Fatalf("a request of exactly the limit was refused: %+v", m.Error)
	}
	c.raw(pad(4097))
	if m := c.anonResponse(); m.Error == nil || m.Error.Code != -32600 || !strings.Contains(m.Error.Message, "larger than 4096") {
		t.Fatalf("one byte over: %+v", m.Error)
	}
	// depth: the whole request counts, params included
	nest := func(total int) string { // total levels with the request object itself as level 1
		return `{"jsonrpc":"2.0","id":"d","method":"version","params":` + strings.Repeat(`{"a":`, total-2) + `{}` + strings.Repeat("}", total-2) + `}`
	}
	id = c.expect(`"d"`)
	c.raw(nest(32))
	if m := c.await(id); m.Error == nil || m.Error.Code != -32602 { // deep enough to be read, and then refused as params
		t.Fatalf("depth 32 must reach the method: %+v", m.Error)
	}
	c.raw(nest(33))
	if m := c.anonResponse(); m.Error == nil || m.Error.Code != -32600 {
		t.Fatalf("depth 33: %+v", m.Error)
	}
}

func TestShutdownCancelsReadsAtOnceAndOnlyWritesGetTheGrace(t *testing.T) {
	f := newFixture(t)
	f.cfg.WriteGrace = 30 * time.Second
	c := startClient(t, f.cfg)
	c.init()
	started := slowTasks(f)
	rd := c.send("tasks.list", nil)
	<-started
	begin := time.Now()
	sd := c.send("shutdown", nil)
	c.await(rd).wantErr(t, core.CodeTimeout)
	if m := c.await(sd); m.Error != nil {
		t.Fatal(m.Error)
	}
	if time.Since(begin) > 5*time.Second {
		t.Errorf("a read in flight held shutdown for %v: only writes get the grace", time.Since(begin))
	}
}

// In demo mode the server serves reads, marked "demo": true (plans and apply: see TestDemoModePlansEveryWriteAndRefusesApply).
func TestDemoModeServesReadsMarkedDemo(t *testing.T) {
	f := newFixture(t)
	f.cfg.Demo = true
	c := startClient(t, f.cfg)
	c.init()
	for _, m := range []string{"version", "board", "tasks.list", "status"} {
		r := c.call(m, nil)
		if r.Error != nil || string(mustMap(t, r.Result)["demo"]) != "true" {
			t.Errorf("%s: %+v %s", m, r.Error, r.Result)
		}
	}
}
