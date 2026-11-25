package serve_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
	"github.com/andrew-bierman/abacus/internal/demo"
	"github.com/andrew-bierman/abacus/internal/lambe"
	"github.com/andrew-bierman/abacus/internal/serve"
)

func TestMain(m *testing.M) {
	core.SetEmbeddedForTest(nil)
	os.Exit(m.Run())
}

var fixedNow = time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)

// ---- fake providers: the demo ones, with the knobs the protocol tests need ----

// tasksMod is the demo Tasks provider at a fixed clock, with a history, a fingerprint and hooks.
type tasksMod struct {
	demo.Tasks
	mu       sync.Mutex
	ran      [][]string
	runHook  func(ctx context.Context, argv []string) error // called inside RunTask
	snapHook func(ctx context.Context) error                // called inside Snapshot
	readSlow func(ctx context.Context) error                // called at the start of ReadTask
	readHook func(id string, t core.Task) core.Task         // changes what a re-read of a task returns
	panicFP  bool
	fp       string
	fpErr    error
	snapGen  int                      // bumps the snapshot (a change for the no-fingerprint path)
	snapMut  func(*core.TaskSnapshot) // edits every snapshot read (tasks.since tests)
	cur, max atomic.Int32
}

func newTasksMod() *tasksMod { return &tasksMod{Tasks: demo.NewTasks()} }

func (m *tasksMod) Snapshot(ctx context.Context) (core.TaskSnapshot, error) {
	n := m.cur.Add(1)
	defer m.cur.Add(-1)
	for {
		old := m.max.Load()
		if n <= old || m.max.CompareAndSwap(old, n) {
			break
		}
	}
	m.mu.Lock()
	hook, gen, mut := m.snapHook, m.snapGen, m.snapMut
	m.mu.Unlock()
	if hook != nil {
		if err := hook(ctx); err != nil {
			return core.TaskSnapshot{}, err
		}
	}
	s := demo.TaskSnapshot(fixedNow)
	if gen > 0 {
		s.Issues[0].Title += strings.Repeat("!", gen)
	}
	if mut != nil {
		mut(&s)
	}
	return s, nil
}

func (m *tasksMod) RunTask(ctx context.Context, argv []string) (core.TaskResult, error) {
	m.mu.Lock()
	m.ran = append(m.ran, argv)
	hook := m.runHook
	m.mu.Unlock()
	if hook != nil {
		if err := hook(ctx, argv); err != nil {
			return core.TaskResult{}, err
		}
	}
	return core.TaskResult{ID: argv[2], Title: "ran"}, nil
}

func (m *tasksMod) runs() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.ran)
}

func (m *tasksMod) ReadTask(ctx context.Context, id string) (core.Task, error) {
	m.mu.Lock()
	slow := m.readSlow
	m.mu.Unlock()
	if slow != nil {
		if err := slow(ctx); err != nil {
			return core.Task{}, err
		}
	}
	t, err := m.Tasks.ReadTask(ctx, id)
	m.mu.Lock()
	hook := m.readHook
	m.mu.Unlock()
	if hook != nil && err == nil {
		t = hook(id, t)
	}
	return t, err
}

func (m *tasksMod) History(_ context.Context, id string, _ int) (core.TaskHistoryReport, error) {
	return core.TaskHistoryReport{ID: id, Events: []core.TaskHistoryEvent{{At: fixedNow, Kind: "created", Actor: "ada"}}, ReadAt: fixedNow}, nil
}

func (m *tasksMod) Fingerprint(context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.panicFP {
		panic("provider bug")
	}
	return m.fp, m.fpErr
}

func (m *tasksMod) setFP(fp string, err error) {
	m.mu.Lock()
	m.fp, m.fpErr = fp, err
	m.mu.Unlock()
}

// noFPTasks has no cheap fingerprint: the watcher must fall back to reading the tracker.
type noFPTasks struct{ *tasksMod }

func (noFPTasks) Fingerprint(context.Context) (string, error) { return "", core.ErrNoFingerprint }

// boardMod provides the Board domain and its writer, recording every write.
type boardMod struct {
	*modtest.Fake
	mu  sync.Mutex
	ran [][]string
	err error
}

func newBoardMod() *boardMod {
	return &boardMod{Fake: &modtest.Fake{IDStr: "board", Det: core.Detection{State: core.Ready, Version: "0.54.0"}}}
}

func (m *boardMod) TargetSource() string { return "board_url (config)" }
func (m *boardMod) Describe(_ context.Context, ch core.BoardChange) []string {
	return []string{"existing channel " + ch.Channel + ch.ID}
}
func (m *boardMod) Board(context.Context) (core.BoardReport, error) {
	return core.BoardReport{Header: core.Header{Source: core.SourceLive},
		Channels: []core.BoardChannel{{Path: "dev", Threads: 1}, {Path: "work"}},
		Threads:  []core.BoardThread{{Root: core.BoardMessage{ID: "2", Channel: "dev", Author: "ada", Text: "deploys are green"}, Replies: 1}},
		Tasks:    []core.BoardTask{{ID: "4", State: "claimed", Claimant: "codex via ada", Text: "fix it"}}}, nil
}
func (m *boardMod) BuildBoardArgv(ch core.BoardChange, key string) ([]string, error) {
	return []string{"board", string(ch.Op), ch.Channel + ch.ID, "--idempotency-key", key, "--", ch.Text}, nil
}
func (m *boardMod) RunBoard(_ context.Context, argv []string, ch core.BoardChange) (core.BoardResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ran = append(m.ran, argv)
	return core.BoardResult{ID: "9", Text: ch.Text}, m.err
}
func (m *boardMod) Target() string { return "the default board" }
func (m *boardMod) runs() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.ran)
}

// leasesMod is the demo Leases provider (which carries the demo workspace) with a live feed the test can trigger.
type leasesMod struct {
	demo.Leases
	changed chan func()
}

func (m *leasesMod) Watch(ctx context.Context, changed func()) {
	select {
	case m.changed <- changed:
	default:
	}
	<-ctx.Done()
}

type fixedAgents struct{ demo.AgentsFeed }

func (fixedAgents) Agents(context.Context) ([]core.Agent, core.AgentsExtra, error) {
	return demo.AgentsAt(fixedNow)
}

// memAudit records every entry and can be told to fail; it reads back, like the file audit.
type memAudit struct {
	mu      sync.Mutex
	entries []core.AuditEntry
	err     error
}

func (a *memAudit) Record(e core.AuditEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	a.entries = append(a.entries, e)
	return nil
}

func (a *memAudit) Entries() ([]core.AuditEntry, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]core.AuditEntry(nil), a.entries...), nil
}

func (a *memAudit) phases() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var p []string
	for _, e := range a.entries {
		p = append(p, e.Phase)
	}
	return strings.Join(p, ",")
}

type fixture struct {
	tasks  *tasksMod
	board  *boardMod
	leases *leasesMod
	audit  *memAudit
	cfg    serve.Config
	clock  atomic.Int64 // unix nanoseconds; the server's clock
}

func (f *fixture) advance(d time.Duration) { f.clock.Add(int64(d)) }

// newFixture is a server config over the demo providers, a fixed clock, an in-memory audit and
// fast intervals. Nothing reads or writes a real tracker, board, hub or home directory.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{tasks: newTasksMod(), board: newBoardMod(), leases: &leasesMod{Leases: demo.NewLeases(), changed: make(chan func(), 1)}, audit: &memAudit{}}
	f.clock.Store(fixedNow.UnixNano())
	root := t.TempDir()
	cfgStore := core.ConfigStore{Path: filepath.Join(root, "c", "config.json")}
	f.cfg = serve.Config{
		Build: func() *core.Registry {
			return core.NewRegistry(lambe.NewDemo(), f.leases, f.tasks, f.board, fixedAgents{demo.NewAgents()})
		},
		Version: "0.0.0-serve", Audit: f.audit, Config: cfgStore, Now: func() time.Time { return time.Unix(0, f.clock.Load()).UTC() },
		Setup: func(en []core.Entry) *core.Setup {
			return core.NewSetup(en, core.SetupOptions{Config: cfgStore, Workspace: filepath.Join(root, "ws"), LambeConfig: filepath.Join(root, "c", "lambe.yaml"), Audit: f.audit,
				Suggest: func() string { return "me-mac" }})
		},
		PollBase: 5 * time.Millisecond, PollMax: 20 * time.Millisecond, PollSlow: time.Second, SlowBase: 5 * time.Millisecond,
		WriteGrace: 300 * time.Millisecond, WriteStall: 2 * time.Second,
	}
	return f
}

// ---- the in-process client ----

type rpcMsg struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
	Raw    string          `json:"-"`
}

type rpcError struct {
	Code    int                 `json:"code"`
	Message string              `json:"message"`
	Data    core.ServeErrorData `json:"data"`
}

func (m rpcMsg) into(t *testing.T, v any) {
	t.Helper()
	if m.Error != nil {
		t.Fatalf("expected a result, got error %+v", m.Error)
	}
	if err := json.Unmarshal(m.Result, v); err != nil {
		t.Fatalf("result: %v\n%s", err, m.Result)
	}
}

// wantErr checks that m is an error with the given abacus code and returns it.
func (m rpcMsg) wantErr(t *testing.T, code core.ErrorCode) *rpcError {
	t.Helper()
	if m.Error == nil {
		t.Fatalf("expected error %s, got result %s", code, m.Result)
	}
	if m.Error.Data.Code != code {
		t.Fatalf("error code = %s (%d, %q), want %s", m.Error.Data.Code, m.Error.Code, m.Error.Message, code)
	}
	return m.Error
}

type client struct {
	t     *testing.T
	w     *io.PipeWriter
	mu    sync.Mutex
	wait  map[string]chan rpcMsg
	early map[string]rpcMsg
	notes chan rpcMsg
	anon  chan rpcMsg // responses with a null id: parse errors and the like
	done  chan error
	next  int
	lines chan string // every line the server wrote, for transcripts
	log   *[]string
}

func startClient(t *testing.T, cfg serve.Config) *client {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &client{t: t, w: inW, wait: map[string]chan rpcMsg{}, early: map[string]rpcMsg{}, notes: make(chan rpcMsg, 256), anon: make(chan rpcMsg, 16), done: make(chan error, 1)}
	s := serve.New(cfg)
	go func() {
		err := s.Serve(context.Background(), inR, outW)
		outW.Close()
		c.done <- err
	}()
	go func() {
		br := bufio.NewReaderSize(outR, 1<<20)
		for {
			line, err := br.ReadBytes('\n')
			if len(line) > 0 {
				var m rpcMsg
				if json.Unmarshal(bytes.TrimSpace(line), &m) != nil {
					t.Errorf("the server wrote a line that is not JSON: %q", line)
					continue
				}
				m.Raw = strings.TrimSpace(string(line))
				c.mu.Lock()
				if c.log != nil {
					*c.log = append(*c.log, m.Raw)
				}
				c.mu.Unlock()
				c.deliver(m)
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		inW.Close()
		select {
		case <-c.done:
		case <-time.After(5 * time.Second):
			t.Error("the server did not stop when its input closed")
		}
	})
	return c
}

func (c *client) deliver(m rpcMsg) {
	switch {
	case m.Method != "" && len(m.ID) == 0:
		select {
		case c.notes <- m:
		default:
			c.t.Error("too many notifications piled up in the test client")
		}
	case len(m.ID) == 0 || string(m.ID) == "null":
		c.anon <- m
	default:
		c.mu.Lock()
		ch := c.wait[string(m.ID)]
		if ch == nil {
			c.early[string(m.ID)] = m
		}
		c.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	}
}

func (c *client) raw(line string) {
	c.t.Helper()
	if _, err := io.WriteString(c.w, line+"\n"); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

// send writes one request and returns its id.
func (c *client) send(method string, params any, deadlineMS ...int) string {
	c.t.Helper()
	c.mu.Lock()
	c.next++
	n := c.next
	id := fmt.Sprint(n)
	c.wait[id] = make(chan rpcMsg, 1)
	c.mu.Unlock()
	req := map[string]any{"jsonrpc": "2.0", "id": n, "method": method}
	if params != nil {
		req["params"] = params
	}
	if len(deadlineMS) > 0 {
		req["deadline_ms"] = deadlineMS[0]
	}
	b, _ := json.Marshal(req)
	c.raw(string(b))
	return id
}

// expect registers a waiter for the response to id (JSON text, e.g. "\"a\"" or 7); await collects it.
func (c *client) expect(id string) string {
	c.mu.Lock()
	c.wait[id] = make(chan rpcMsg, 1)
	c.mu.Unlock()
	return id
}

func (c *client) await(id string) rpcMsg {
	c.t.Helper()
	c.mu.Lock()
	ch := c.wait[id]
	c.mu.Unlock()
	select {
	case m := <-ch:
		return m
	case <-time.After(10 * time.Second):
		c.t.Fatalf("no response to request %s", id)
		return rpcMsg{}
	}
}

func (c *client) call(method string, params any, deadlineMS ...int) rpcMsg {
	c.t.Helper()
	return c.await(c.send(method, params, deadlineMS...))
}

func (c *client) cancel(id string) {
	c.raw(`{"jsonrpc":"2.0","method":"$/cancelRequest","params":{"id":` + id + `}}`)
}

func (c *client) init() core.ServeInitialize {
	c.t.Helper()
	var r core.ServeInitialize
	c.call("initialize", map[string]any{"protocol_version": 1, "client": map[string]any{"name": "test", "version": "1"}}).into(c.t, &r)
	return r
}

func (c *client) anonResponse() rpcMsg {
	c.t.Helper()
	select {
	case m := <-c.anon:
		return m
	case <-time.After(5 * time.Second):
		c.t.Fatal("no response with a null id")
		return rpcMsg{}
	}
}

func (c *client) note(d time.Duration) (rpcMsg, bool) {
	select {
	case m := <-c.notes:
		return m, true
	case <-time.After(d):
		return rpcMsg{}, false
	}
}

func (c *client) shutdownAndWait() error {
	c.t.Helper()
	if m := c.call("shutdown", nil); m.Error != nil {
		c.t.Fatalf("shutdown: %+v", m.Error)
	}
	select {
	case err := <-c.done:
		c.done <- err
		return err
	case <-time.After(5 * time.Second):
		c.t.Fatal("Serve did not return after shutdown")
		return errors.New("timeout")
	}
}

func readyClient(t *testing.T) (*client, *fixture) {
	t.Helper()
	f := newFixture(t)
	c := startClient(t, f.cfg)
	c.init()
	return c, f
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func (m *tasksMod) mutate(fn func(*core.TaskSnapshot)) {
	m.mu.Lock()
	m.snapMut = fn
	m.mu.Unlock()
}
