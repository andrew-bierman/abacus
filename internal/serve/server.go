// Package serve is `abacus serve --stdio`: a long-lived JSON-RPC 2.0 server, one message per line on
// stdin and stdout, no network port. It exposes the typed reports core already prints and the same
// gated writes (plan, then a single-use apply) to a native client. This file is the transport: framing,
// limits, request ids, cancellation, deadlines, concurrency and shutdown. engine.go holds the methods.
package serve

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/andrew-bierman/abacus/internal/core"
)

// Limits bound what one client can ask. A zero field takes its default.
type Limits struct {
	MaxRequestBytes  int           // one request line
	MaxResponseBytes int           // one result
	MaxDepth         int           // JSON nesting
	MaxInFlight      int           // requests being served at once
	MaxQueued        int           // requests past MaxInFlight that wait their turn before "busy"
	MaxPlans         int           // plans held for apply
	DefaultDeadline  time.Duration // per request, when the client names none
	MaxDeadline      time.Duration // the most a client may ask for
}

func (l Limits) withDefaults() Limits {
	def := func(p *int, v int) {
		if *p <= 0 {
			*p = v
		}
	}
	def(&l.MaxRequestBytes, 1<<20)
	def(&l.MaxResponseBytes, 16<<20)
	def(&l.MaxDepth, 32)
	def(&l.MaxInFlight, 8)
	def(&l.MaxQueued, 32)
	def(&l.MaxPlans, 64)
	if l.DefaultDeadline <= 0 {
		l.DefaultDeadline = 30 * time.Second
	}
	if l.MaxDeadline <= 0 {
		l.MaxDeadline = 10 * time.Minute
	}
	return l
}

// busyRetryAfterMS is the retry_after_ms of a "busy" refusal.
const busyRetryAfterMS = 250

var (
	errCancelled = errors.New("request cancelled")
	errClosed    = errors.New("server closed")
)

type handler func(ctx context.Context, params json.RawMessage) (any, error)

type method struct {
	fn handler
	// domain names the one-read-at-a-time lock this method takes (the TUI rule: one in-flight read per
	// domain); "" takes none.
	domain string
	// deadline is the default for this method when the client names none; 0 means Limits.DefaultDeadline.
	deadline time.Duration
	// inline methods run on the reading goroutine, so their order against the next line is fixed.
	inline bool
	// write marks a method that may write: shutdown lets it finish (up to the grace) instead of cancelling it.
	write bool
}

type request struct {
	JSONRPC    string          `json:"jsonrpc"`
	ID         json.RawMessage `json:"id"`
	Method     string          `json:"method"`
	Params     json.RawMessage `json:"params"`
	DeadlineMS *int64          `json:"deadline_ms"`
}

type call struct {
	cancel context.CancelCauseFunc
	write  bool
}

type outMsg struct {
	b     []byte
	topic string // set for a notification: it is coalesced per topic
}

// Server serves one client on one pair of streams.
type Server struct {
	cfg Config
	lim Limits
	eng *Engine
	ms  map[string]method

	root       context.Context
	cancelRoot context.CancelCauseFunc

	mu          sync.Mutex
	closing     bool
	inflight    map[string]*call
	wg          sync.WaitGroup
	pendingN    map[string]*core.ServeNotice // topics with a notification queued, still open to merging
	active      int                          // accepted requests that are not inline: running or waiting for a slot
	seq         map[string]uint64
	initialized atomic.Bool

	locks map[string]chan struct{}
	slots chan struct{} // one per request being served; the others wait here, in arrival order

	out        chan outMsg
	flush      chan struct{}
	writerDone chan struct{}
	finished   chan struct{}
	stalled    chan struct{} // closed when a write has been stuck too long
	stallOnce  sync.Once
	writing    atomic.Int64 // unix nanoseconds when the current write began; 0 when none
	failure    atomic.Value // error: why the server stopped, if not asked to
}

// New builds a server. The engine is built from cfg; nothing is read until a request asks.
func New(cfg Config) *Server {
	cfg = cfg.withDefaults()
	s := &Server{cfg: cfg, lim: cfg.Limits, inflight: map[string]*call{}, pendingN: map[string]*core.ServeNotice{}, slots: make(chan struct{}, cfg.Limits.MaxInFlight), seq: map[string]uint64{},
		out: make(chan outMsg, 256), flush: make(chan struct{}), writerDone: make(chan struct{}), finished: make(chan struct{}), stalled: make(chan struct{})}
	s.eng = newEngine(cfg, s.notify)
	s.ms = s.eng.methods()
	s.ms["initialize"] = method{fn: s.initialize, inline: true}
	s.locks = map[string]chan struct{}{}
	for _, d := range []string{"meta", "tasks", "board", "leases", "queues", "agents", "workspace", "setup", "search", "config"} {
		s.locks[d] = make(chan struct{}, 1)
	}
	return s
}

// Serve reads requests from in and writes responses to out until the client sends shutdown, in is
// closed, parent ends or the client stops reading. It returns nil for the first three.
func (s *Server) Serve(parent context.Context, in io.Reader, out io.Writer) error {
	s.root, s.cancelRoot = context.WithCancelCause(parent)
	defer s.cancelRoot(errClosed)
	s.eng.attach(s.root)
	go s.writer(out)
	go s.watchStall()
	go func() {
		select {
		case <-parent.Done():
			s.drain(nil)
		case <-s.finished:
		}
	}()
	go func() {
		s.readLoop(in)
		s.drain(nil) // the client closed its end
	}()
	<-s.finished
	if err, _ := s.failure.Load().(error); err != nil {
		return err
	}
	return nil
}

// fail stops the server for a reason that is not the client's own request.
func (s *Server) fail(err error) {
	s.failure.CompareAndSwap(nil, err)
	s.drain(nil)
}

// drain stops accepting requests, cancels the reads, gives writes in flight WriteGrace to finish (a
// cancelled write is a result unknown), runs then (the shutdown reply) and ends the server.
func (s *Server) drain(then func()) bool {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return false
	}
	s.closing = true
	for _, c := range s.inflight {
		if !c.write {
			c.cancel(errClosed)
		}
	}
	s.mu.Unlock()
	go func() {
		grace := time.AfterFunc(s.cfg.WriteGrace, func() {
			s.mu.Lock()
			for _, c := range s.inflight {
				c.cancel(errClosed)
			}
			s.mu.Unlock()
		})
		s.wg.Wait()
		grace.Stop()
		if then != nil {
			then()
		}
		s.cancelRoot(errClosed)
		close(s.flush)
		select { // a writer stuck in a write that never returns is abandoned
		case <-s.writerDone:
		case <-s.stalled:
		}
		close(s.finished)
	}()
	return true
}

// ---- output ----

func (s *Server) writer(out io.Writer) {
	defer close(s.writerDone)
	dead := false
	write := func(m outMsg) {
		if m.topic != "" { // a notification is made as it is written, so changes that arrived while it waited are in it
			s.mu.Lock()
			n := s.pendingN[m.topic]
			delete(s.pendingN, m.topic)
			s.mu.Unlock()
			if n == nil {
				return
			}
			m.b = noticeBytes(*n)
		}
		if dead {
			return
		}
		s.writing.Store(time.Now().UnixNano())
		_, err := out.Write(append(m.b, '\n'))
		s.writing.Store(0)
		if err != nil { // the client is gone: keep draining so no sender blocks, and stop
			dead = true
			s.fail(fmt.Errorf("the client stopped reading: %w", err))
		}
	}
	for {
		select {
		case m := <-s.out:
			write(m)
		case <-s.flush:
			for {
				select {
				case m := <-s.out:
					write(m)
				default:
					return
				}
			}
		}
	}
}

// watchStall ends the server when one write has been blocked longer than WriteStall: a client that
// does not read must not hold every response, and the process, forever.
func (s *Server) watchStall() {
	t := time.NewTicker(max(s.cfg.WriteStall/4, time.Millisecond))
	defer t.Stop()
	for {
		select {
		case <-s.finished:
			return
		case <-t.C:
			if at := s.writing.Load(); at != 0 && time.Since(time.Unix(0, at)) > s.cfg.WriteStall {
				s.stallOnce.Do(func() { close(s.stalled) })
				s.fail(errors.New("the client did not read for " + s.cfg.WriteStall.String()))
				return
			}
		}
	}
}

// send queues a response. It waits for room (a response is never dropped) unless the server has ended.
func (s *Server) send(b []byte) {
	select {
	case s.out <- outMsg{b: b}:
	case <-s.writerDone:
	case <-s.stalled:
	}
}

// maxNoticeIDs bounds the ids one notification lists; past it the notice says "full" (refetch the topic).
const maxNoticeIDs = 256

// notify queues a notification without ever waiting. While one for the topic is still queued, another is
// merged into it (its ids are added, or it becomes "full"), so the client's single refetch covers both and a
// client that refetches only the listed ids misses nothing. A full queue reports false so the watcher tries
// again at its next look. Nothing is dropped silently.
func (s *Server) notify(n core.ServeNotice) bool {
	if n.Kind != "ids" || len(n.ChangedIDs) > maxNoticeIDs {
		n.Kind, n.ChangedIDs = "full", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return true
	}
	if p := s.pendingN[n.Topic]; p != nil {
		if n.Fingerprint != "" {
			p.Fingerprint = n.Fingerprint
		}
		if p.Kind == "ids" && n.Kind == "ids" {
			p.ChangedIDs = mergeIDs(p.ChangedIDs, n.ChangedIDs)
		}
		if p.Kind != "ids" || n.Kind != "ids" || len(p.ChangedIDs) > maxNoticeIDs {
			p.Kind, p.ChangedIDs = "full", nil
		}
		return true
	}
	s.seq[n.Topic]++
	n.Seq = s.seq[n.Topic]
	select {
	case s.out <- outMsg{topic: n.Topic}:
		s.pendingN[n.Topic] = &n
		return true
	default:
		s.seq[n.Topic]--
		return false
	}
}

func mergeIDs(a, b []string) []string {
	out := slices.Clone(a)
	for _, id := range b {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

func noticeBytes(n core.ServeNotice) []byte {
	b, _ := json.Marshal(struct {
		JSONRPC string           `json:"jsonrpc"`
		Method  string           `json:"method"`
		Params  core.ServeNotice `json:"params"`
	}{"2.0", noticeMethod(n.Topic), n})
	return b
}

// noticeMethod is the method of a notification: tasks.changed, workspace.changed, agents.changed, engine.degraded.
func noticeMethod(topic string) string {
	if topic == "engine" {
		return "engine.degraded"
	}
	return topic + ".changed"
}

func (s *Server) reply(id json.RawMessage, result []byte) {
	if len(result) > s.lim.MaxResponseBytes {
		s.replyError(id, failErr(core.CodeFailed, "the result is larger than %d bytes: narrow the request (a query or a smaller limit)", s.lim.MaxResponseBytes))
		return
	}
	var b bytes.Buffer
	b.WriteString(`{"jsonrpc":"2.0","id":`)
	b.Write(idOrNull(id))
	b.WriteString(`,"result":`)
	b.Write(result)
	b.WriteByte('}')
	s.send(b.Bytes())
}

func (s *Server) replyError(id json.RawMessage, e *apiError) {
	body, _ := json.Marshal(struct {
		Code    int                 `json:"code"`
		Message string              `json:"message"`
		Data    core.ServeErrorData `json:"data"`
	}{e.rpc, core.Sanitize(e.msg), e.data()})
	var b bytes.Buffer
	b.WriteString(`{"jsonrpc":"2.0","id":`)
	b.Write(idOrNull(id))
	b.WriteString(`,"error":`)
	b.Write(body)
	b.WriteByte('}')
	s.send(b.Bytes())
}

func idOrNull(id json.RawMessage) []byte {
	if len(id) == 0 {
		return []byte("null")
	}
	return id
}

// ---- input ----

// readLoop reads newline-delimited requests. A line longer than MaxRequestBytes is read to its end
// and thrown away (never buffered past the limit), and answered with one error.
func (s *Server) readLoop(in io.Reader) {
	br := bufio.NewReaderSize(in, 64<<10)
	var buf []byte
	discarding := false
	for {
		chunk, err := br.ReadSlice('\n')
		if len(chunk) > 0 && !discarding {
			if len(buf)+len(chunk) > s.lim.MaxRequestBytes+1 { // +1: the newline
				discarding, buf = true, nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		switch {
		case err == nil:
			if discarding {
				s.replyError(nil, &apiError{rpc: rpcInvalidRequest, code: core.CodeUsage, msg: fmt.Sprintf("the request is larger than %d bytes", s.lim.MaxRequestBytes)})
				discarding = false
			} else {
				s.handleLine(buf)
			}
			buf = buf[:0]
		case errors.Is(err, bufio.ErrBufferFull):
		default: // EOF or a read error: an unterminated last line is not a request
			return
		}
	}
}

// maxDepth is the deepest nesting of arrays and objects in valid JSON.
func maxDepth(b []byte) int {
	depth, deepest, inStr, esc := 0, 0, false, false
	for _, c := range b {
		switch {
		case inStr:
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
		case c == '"':
			inStr = true
		case c == '{' || c == '[':
			depth++
			deepest = max(deepest, depth)
		case c == '}' || c == ']':
			depth--
		}
	}
	return deepest
}

// parseID accepts a string of at most 128 bytes or an integer; null, fractions and anything else are refused.
// It returns the canonical form, so "a" and "\u0061", or 0 and -0, are the same id for the in-flight
// table and for cancellation.
func parseID(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) != nil || len(s) > 128 {
			return "", false
		}
		b, _ := json.Marshal(s)
		return string(b), true
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		n, err := strconv.ParseInt(string(raw), 10, 64)
		if err != nil || n > 1<<53 || n < -(1<<53) {
			return "", false
		}
		return strconv.FormatInt(n, 10), true
	}
	return "", false
}

func (s *Server) handleLine(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	bad := func(rpc int, format string, a ...any) {
		s.replyError(nil, &apiError{rpc: rpc, code: core.CodeUsage, msg: fmt.Sprintf(format, a...)})
	}
	switch {
	case !utf8.Valid(line) || !json.Valid(line):
		bad(rpcParse, "the request is not valid JSON")
		return
	case line[0] == '[':
		bad(rpcInvalidRequest, "batch requests are not supported: send one request per line")
		return
	case line[0] != '{':
		bad(rpcInvalidRequest, "a request is a JSON object")
		return
	case maxDepth(line) > s.lim.MaxDepth:
		bad(rpcInvalidRequest, "the request is nested deeper than %d levels", s.lim.MaxDepth)
		return
	}
	var req request
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		bad(rpcInvalidRequest, "not a JSON-RPC 2.0 request: %s", core.Sanitize(err.Error()))
		return
	}
	hasID := len(req.ID) > 0
	key, okID := parseID(req.ID)
	if hasID && !okID {
		bad(rpcInvalidRequest, "the id is a string of at most 128 bytes or an integer")
		return
	}
	fail := func(e *apiError) {
		if hasID {
			s.replyError(req.ID, e)
		}
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		fail(&apiError{rpc: rpcInvalidRequest, code: core.CodeUsage, msg: `a request needs "jsonrpc":"2.0" and a method`})
		if !hasID {
			bad(rpcInvalidRequest, `a request needs "jsonrpc":"2.0" and a method`)
		}
		return
	}
	if !hasID { // a notification from the client: only cancel exists, and nothing answers it
		if req.Method == "$/cancelRequest" {
			s.cancelRequest(req.Params)
		}
		return
	}
	m, ok := s.ms[req.Method]
	switch {
	case req.Method == "shutdown":
		s.shutdown(req.ID)
		return
	case !ok:
		fail(&apiError{rpc: rpcMethodNotFound, code: core.CodeUsage, msg: "unknown method " + strconv.Quote(core.Sanitize(truncate(req.Method, 64)))})
		return
	case !s.initialized.Load() && req.Method != "initialize":
		fail(&apiError{rpc: rpcNotInitialized, code: core.CodeRefused, msg: "send initialize first"})
		return
	}
	deadline := s.lim.DefaultDeadline
	if m.deadline > 0 {
		deadline = m.deadline
	}
	if req.DeadlineMS != nil {
		d := time.Duration(*req.DeadlineMS) * time.Millisecond
		if d <= 0 || d > s.lim.MaxDeadline {
			fail(usageErr("deadline_ms is 1 to %d", s.lim.MaxDeadline.Milliseconds()))
			return
		}
		deadline = d
	}
	var params json.RawMessage
	if len(req.Params) > 0 && !bytes.Equal(req.Params, []byte("null")) {
		if req.Params[0] != '{' {
			fail(usageErr("params is an object"))
			return
		}
		params = req.Params
	}
	s.mu.Lock()
	switch {
	case s.closing:
		s.mu.Unlock()
		fail(&apiError{rpc: rpcInvalidRequest, code: core.CodeRefused, msg: "the server is shutting down"})
		return
	case s.inflight[key] != nil:
		s.mu.Unlock()
		fail(&apiError{rpc: rpcInvalidRequest, code: core.CodeUsage, msg: "a request with this id is still in flight"})
		return
	case !m.inline && s.active >= s.lim.MaxInFlight+s.lim.MaxQueued:
		s.mu.Unlock()
		fail(&apiError{rpc: rpcServer, code: core.CodeBusy, retryAfterMS: busyRetryAfterMS,
			msg: fmt.Sprintf("the server is busy: %d requests are being served and %d are waiting (nothing ran): try again in %d ms", s.lim.MaxInFlight, s.lim.MaxQueued, busyRetryAfterMS)})
		return
	}
	if !m.inline {
		s.active++
	}
	ctx, cancel := context.WithCancelCause(s.root)
	c := &call{cancel: cancel, write: m.write}
	s.inflight[key] = c
	s.wg.Add(1)
	s.mu.Unlock()
	run := func() {
		defer func() {
			s.mu.Lock()
			delete(s.inflight, key)
			if !m.inline {
				s.active--
			}
			s.mu.Unlock()
			cancel(nil)
			s.wg.Done()
		}()
		s.run(ctx, req.ID, m, params, deadline)
	}
	if m.inline {
		run()
	} else {
		go run()
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (s *Server) cancelRequest(params json.RawMessage) {
	var p struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	key, ok := parseID(p.ID)
	if !ok {
		return
	}
	s.mu.Lock()
	c := s.inflight[key]
	s.mu.Unlock()
	if c != nil {
		c.cancel(errCancelled)
	}
}

// run serves one request: it takes the method's domain lock, runs it under its deadline, and answers.
func (s *Server) run(ctx context.Context, id json.RawMessage, m method, params json.RawMessage, deadline time.Duration) {
	ctx, stop := context.WithTimeout(ctx, deadline)
	defer stop()
	if !m.inline { // past MaxInFlight a request waits here, in arrival order, under its own deadline
		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
		case <-ctx.Done():
			s.replyError(id, s.classify(ctx, ctx.Err()))
			return
		}
	}
	res, err := s.invoke(ctx, m, params)
	if err != nil {
		s.replyError(id, s.classify(ctx, err))
		return
	}
	var b []byte
	var merr error
	if r, ok := res.(core.Report); ok { // version, doctor and status print the struct as it is (it carries its own schema_version)
		r.Demo = s.cfg.Demo
		b, merr = json.Marshal(r)
	} else {
		b, merr = core.ReportJSONCompact(res, s.cfg.Demo)
	}
	if merr != nil {
		s.replyError(id, failErr(core.CodeFailed, "the result could not be encoded"))
		return
	}
	s.reply(id, b)
}

func (s *Server) invoke(ctx context.Context, m method, params json.RawMessage) (res any, err error) {
	defer func() {
		if r := recover(); r != nil { // a bug in a method must not take the server down; the panic value may hold task text, so it is not logged
			s.logf("a request handler failed")
			res, err = nil, &apiError{rpc: rpcInternal, code: core.CodeFailed, msg: "internal error"}
		}
	}()
	if m.domain != "" {
		lock := s.locks[m.domain]
		select {
		case lock <- struct{}{}:
			defer func() { <-lock }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return m.fn(ctx, params)
}

// classify turns a method's error into the response: a cancel or a deadline is the closed code
// "timeout" (a read ran out of time or was cancelled), anything typed keeps its own code.
func (s *Server) classify(ctx context.Context, err error) *apiError {
	ae := fromErr(err)
	if ae.code == core.CodeTimeout && ctx.Err() != nil {
		out := &apiError{rpc: rpcServer, code: core.CodeTimeout, msg: "the request ran out of time", planID: ae.planID, digest: ae.digest, outcome: ae.outcome}
		switch cause := context.Cause(ctx); {
		case errors.Is(cause, errCancelled):
			out.rpc, out.msg = rpcCancelled, "the request was cancelled"
		case errors.Is(cause, errClosed):
			out.msg = "the server is shutting down"
		}
		if out.planID != "" { // the plan was claimed before the request ended, and nothing was written
			out.msg += " after its plan was claimed: nothing was written, and the plan is spent (plan again)"
		}
		return out
	}
	return ae
}

func (s *Server) shutdown(id json.RawMessage) {
	ok := []byte(`{"schema_version":1}`)
	if !s.drain(func() { s.reply(id, ok) }) { // already stopping: the answer is the same
		s.reply(id, ok)
	}
}

func (s *Server) initialize(_ context.Context, params json.RawMessage) (any, error) {
	var p struct {
		ProtocolVersion int `json:"protocol_version"`
		Client          *struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"client"`
	}
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if p.ProtocolVersion != core.ServeProtocolVersion {
		return nil, failErr(core.CodeUnsupported, "this server speaks protocol version %d; the client asked for %d", core.ServeProtocolVersion, p.ProtocolVersion)
	}
	s.initialized.Store(true)
	names := make([]string, 0, len(s.ms)+1)
	for n := range s.ms {
		names = append(names, n)
	}
	names = append(names, "shutdown")
	slices.Sort(names)
	return core.ServeInitialize{ProtocolVersion: core.ServeProtocolVersion, AbacusVersion: s.cfg.Version, Capabilities: core.ServeCapabilities{
		Methods: names, Notifications: []string{"tasks.changed", "workspace.changed", "agents.changed", "engine.degraded"}, Topics: topics,
		Limits: core.ServeLimits{MaxRequestBytes: s.lim.MaxRequestBytes, MaxResponseBytes: s.lim.MaxResponseBytes, MaxDepth: s.lim.MaxDepth,
			MaxInFlight: s.lim.MaxInFlight, MaxQueued: s.lim.MaxQueued, MaxPlans: s.lim.MaxPlans, PlanTTLSeconds: int(s.cfg.PlanTTL / time.Second),
			DefaultDeadlineMS: int(s.lim.DefaultDeadline / time.Millisecond), MaxDeadlineMS: int(s.lim.MaxDeadline / time.Millisecond)}},
		Contract: core.NewContract()}, nil
}

// logf writes one lifecycle line to the log. It never carries a request, a parameter or a result.
func (s *Server) logf(format string, a ...any) {
	if s.cfg.Log != nil {
		fmt.Fprintf(s.cfg.Log, "serve: "+format+"\n", a...)
	}
}
