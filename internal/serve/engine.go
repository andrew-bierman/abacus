package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/query"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// topics are what subscribe accepts.
var topics = []string{"tasks", "workspace", "agents"}

// Engine is the domain side of the server: it reads through core's providers and plans writes through
// core's gate. It holds no logic of its own that the CLI and the TUI do not also have, only the glue.
type Engine struct {
	cfg    Config
	notify func(core.ServeNotice) bool
	plans  *planStore
	since  sinceStore

	emu      sync.Mutex
	reg      *core.Registry
	cached   []core.Entry
	cachedAt time.Time

	writeMu chan struct{} // one write at a time: the client's applies are serialized here
	closing atomic.Bool   // the server is shutting down: no apply that has not started may start

	root context.Context
	wmu  sync.Mutex
	subs map[string]context.CancelFunc
}

func newEngine(cfg Config, notify func(core.ServeNotice) bool) *Engine {
	return &Engine{cfg: cfg, notify: notify, plans: newPlanStore(cfg.Limits.MaxPlans, cfg.PlanTTL, cfg.Now),
		writeMu: make(chan struct{}, 1), subs: map[string]context.CancelFunc{}}
}

// attach gives the engine the server's lifetime: its watchers end with it.
func (e *Engine) attach(root context.Context) { e.root = root }

// entries detects the providers, reusing the answer for EntryTTL (a refresh the TUI would do on r).
func (e *Engine) entries(ctx context.Context) []core.Entry {
	e.emu.Lock()
	defer e.emu.Unlock()
	if e.cached != nil && time.Since(e.cachedAt) < e.cfg.EntryTTL {
		return e.cached
	}
	if e.reg == nil {
		e.reg = e.cfg.Build()
	}
	en := e.reg.Detect(ctx)
	if ctx.Err() == nil { // a detection cut short is not remembered
		e.cached, e.cachedAt = en, time.Now()
	}
	return en
}

func (e *Engine) invalidate() {
	e.emu.Lock()
	e.cached = nil
	e.emu.Unlock()
}

// errDemoApply is the refusal of a write in demo mode.
func errDemoApply(what string) error {
	return failErr(core.CodeRefused, "%s: demo data cannot be changed (nothing was written)", what)
}

// demoAudit is the backstop behind the demo refusal of apply: in demo mode an audit record cannot be written,
// so a write that slipped past the guard is stopped by the gate (fail closed) before it runs. It is not a
// reader, so a board plan made in demo mode never reads the real audit log for a repeat.
type demoAudit struct{}

func (demoAudit) Record(core.AuditEntry) error {
	return errors.New("demo mode: nothing can be written")
}

func (e *Engine) audit() core.Audit {
	if e.cfg.Demo {
		return demoAudit{}
	}
	if e.cfg.Audit != nil {
		return e.cfg.Audit
	}
	return core.DefaultAudit()
}

// decodeParams reads the params object strictly: an unknown field is an error, not ignored.
func decodeParams(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return usageErr("invalid params: %s", core.Sanitize(err.Error()))
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return usageErr("invalid params: trailing data")
	}
	return nil
}

func (e *Engine) methods() map[string]method {
	m := map[string]method{
		"version":       {fn: e.report("version"), domain: "meta"},
		"doctor":        {fn: e.report("doctor"), domain: "meta"},
		"status":        {fn: e.report("status"), domain: "meta"},
		"board":         {fn: e.domain(core.DomainBoard), domain: "board"},
		"leases":        {fn: e.domain(core.DomainLeases), domain: "leases"},
		"queues":        {fn: e.domain(core.DomainQueues), domain: "queues"},
		"tasks.summary": {fn: e.domain(core.DomainTasks), domain: "tasks"},
		"agents":        {fn: e.agents, domain: "agents"},
		"search":        {fn: e.search, domain: "search"},
		"tasks.list":    {fn: e.tasksList(false), domain: "tasks"},
		"tasks.tree":    {fn: e.tasksList(true), domain: "tasks"},
		"tasks.board":   {fn: e.tasksBoard, domain: "tasks"},
		"tasks.since":   {fn: e.tasksSince, domain: "tasks"},
		"tasks.show":    {fn: e.tasksShow, domain: "tasks"},
		"tasks.graph":   {fn: e.tasksGraph, domain: "tasks"},
		"tasks.history": {fn: e.tasksHistory, domain: "tasks"},
		"workspace":     {fn: e.workspace, domain: "workspace"},
		"setup.state":   {fn: e.setupState, domain: "setup"},
		"config.show":   {fn: e.configShow, domain: "config"},

		"subscribe":   {fn: e.subscribe(true)},
		"unsubscribe": {fn: e.subscribe(false)},

		"plan.tasks.bulk":  {fn: e.planBulk},
		"plan.board.post":  {fn: e.planBoard(core.BoardPost)},
		"plan.board.reply": {fn: e.planBoard(core.BoardReply)},
		"plan.setup":       {fn: e.planSetup},
		"plan.config.set":  {fn: e.planConfigSet},
		"plan.cancel":      {fn: e.planCancel},
		"apply":            {fn: e.apply, write: true, deadline: core.BulkTotalTimeout + 30*time.Second},
	}
	for op := range taskFields {
		m["plan.task."+string(op)] = method{fn: e.planTask(op)}
	}
	if e.cfg.Demo {
		// Demo data cannot be changed: apply is always refused. Every plan is made against the demo data (the
		// setup steps and the settings too), flagged "demo": true with a demo-marked digest, so a client can show
		// its confirm sheets; nothing it holds can be applied.
		md := m["apply"]
		md.fn = func(context.Context, json.RawMessage) (any, error) { return nil, errDemoApply("apply") }
		m["apply"] = md
	}
	return m
}

// ---- reads ----

func (e *Engine) report(kind string) handler {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		if err := decodeParams(raw, &struct{}{}); err != nil {
			return nil, err
		}
		en := e.entries(ctx)
		switch kind {
		case "version":
			return core.VersionReport(e.cfg.Version, en), nil
		case "doctor":
			return core.DoctorReport(ctx, e.cfg.Version, en), nil
		}
		return core.FleetStatusReport(ctx, e.cfg.Version, en), nil
	}
}

func (e *Engine) domain(id core.DomainID) handler {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		if err := decodeParams(raw, &struct{}{}); err != nil {
			return nil, err
		}
		d := core.ReadDomain(ctx, e.entries(ctx), id)
		switch id {
		case core.DomainBoard:
			return d.Board, nil
		case core.DomainLeases:
			return d.Leases, nil
		case core.DomainQueues:
			return d.Queues, nil
		}
		return d.Tasks, nil
	}
}

func (e *Engine) agents(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Harness string `json:"harness"`
		State   string `json:"state"`
		Repo    string `json:"repo"`
		Query   string `json:"query"`
	}
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	f := core.AgentFilter{Harness: p.Harness, Repo: p.Repo, Query: p.Query}
	if p.State != "" {
		f.State = core.AgentState(strings.ToLower(p.State))
		if !f.State.Valid() {
			return nil, usageErr("unknown state %q", core.Sanitize(p.State))
		}
	}
	r := core.ReadDomain(ctx, e.entries(ctx), core.DomainAgents).Agents
	r.Agents = core.FilterAgents(r.Agents, f)
	return r, nil
}

func (e *Engine) search(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Query   string   `json:"query"`
		Domains []string `json:"domains"`
		Limit   int      `json:"limit"`
	}
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	text := strings.TrimSpace(p.Query)
	if text == "" {
		return nil, usageErr("search needs a query")
	}
	if p.Limit == 0 {
		p.Limit = core.DefaultSearchLimit
	}
	if p.Limit < 1 || p.Limit > core.MaxSearchLimit {
		return nil, usageErr("limit is 1 to %d", core.MaxSearchLimit)
	}
	if utf8.RuneCountInString(text) > query.MaxLen {
		return nil, usageErr("bad query: longer than %d characters", query.MaxLen)
	}
	if _, err := query.Parse(text); err != nil {
		return nil, usageErr("bad query: %v", err)
	}
	var doms []core.DomainID
	for _, d := range p.Domains {
		id := core.DomainID(strings.ToLower(strings.TrimSpace(d)))
		if !slices.Contains(core.SearchOrder, id) {
			return nil, usageErr("unknown domain %q (tasks, board, leases, queues, agents)", core.Sanitize(d))
		}
		doms = append(doms, id)
	}
	now := e.cfg.Now()
	feeds := taskview.ReadSearchFeeds(ctx, e.entries(ctx), now, doms)
	rep, err := core.NewSearchIndex(feeds...).Search(text, core.SearchOptions{Now: now, Limit: p.Limit, Domains: doms})
	if err != nil {
		return nil, usageErr("bad query: %v", err)
	}
	for _, s := range rep.Domains {
		if s.Searched() {
			return rep, nil
		}
	}
	return nil, failErr(core.CodeNotConnected, "no domain could be searched")
}

// snapshot reads the whole tracker, as the list reads it.
func (e *Engine) snapshot(ctx context.Context) (core.TaskSnapshot, core.Providers, error) {
	pr := core.ResolveProviders(e.entries(ctx))
	if pr.Source == nil {
		return core.TaskSnapshot{}, pr, failErr(core.CodeNotConnected, "tasks: not connected. %s", core.HowToConnectFor(core.DomainTasks))
	}
	snap, err := pr.Source.Snapshot(ctx)
	if err != nil {
		return snap, pr, fromErr(err)
	}
	return snap, pr, nil
}

type listParams struct {
	Query   string `json:"query"`
	Sort    string `json:"sort"`
	Reverse bool   `json:"reverse"`
	Preset  string `json:"preset"`
}

func (e *Engine) tasksList(tree bool) handler {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p listParams
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		pq, err := query.Parse(p.Query)
		if err != nil {
			return nil, usageErr("bad query: %v", err)
		}
		opts := taskview.ListOptions{Query: pq, Sort: taskview.DefaultSort()}
		if p.Sort != "" {
			k := taskview.SortKey(strings.ToLower(p.Sort))
			if !k.Valid() {
				return nil, usageErr("unknown sort %q", core.Sanitize(p.Sort))
			}
			opts.Sort = taskview.Sort{Key: k, Desc: k.DefaultDesc()}
		}
		if p.Reverse {
			opts.Sort = opts.Sort.Flip()
		}
		if p.Preset != "" {
			opts.Preset = taskview.Preset(strings.ToLower(p.Preset))
			if !opts.Preset.Valid() {
				return nil, usageErr("unknown preset %q (open, ready, blocked, closed, all)", core.Sanitize(p.Preset))
			}
		}
		snap, _, err := e.snapshot(ctx)
		if err != nil {
			return nil, err
		}
		v := taskview.Build(snap, e.cfg.Now())
		if tree {
			return v.Tree(taskview.TreeOptions{ListOptions: opts}), nil
		}
		return v.List(opts), nil
	}
}

func (e *Engine) tasksBoard(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Query  string `json:"query"`
		Group  string `json:"group"`
		Closed bool   `json:"closed"`
	}
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	bo := taskview.BoardOptions{ShowClosed: p.Closed, Group: strings.ToLower(p.Group)}
	if bo.Group == "" {
		bo.Group = taskview.GroupStatus
	}
	if !slices.Contains(taskview.Groups, bo.Group) {
		return nil, usageErr("unknown grouping %q (%s)", core.Sanitize(p.Group), strings.Join(taskview.Groups, ", "))
	}
	pq, err := query.Parse(p.Query)
	if err != nil {
		return nil, usageErr("bad query: %v", err)
	}
	bo.Query = pq
	snap, _, err := e.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return taskview.Build(snap, e.cfg.Now()).Board(bo), nil
}

func (e *Engine) tasksShow(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	if p.ID == "" {
		return nil, usageErr("tasks.show needs an id")
	}
	snap, _, err := e.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	rep, ok := taskview.Build(snap, e.cfg.Now()).Detail(p.ID)
	if !ok {
		return nil, failErr(core.CodeNotFound, "no task %q", core.Sanitize(p.ID))
	}
	return rep, nil
}

func (e *Engine) tasksGraph(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ID    string `json:"id"`
		Whole bool   `json:"whole"`
		Depth int    `json:"depth"`
	}
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	if p.Depth == 0 {
		p.Depth = 2
	}
	if p.Depth < 1 || p.Depth > 6 || (p.Whole && p.ID != "") {
		return nil, usageErr("graph takes an id or whole, and a depth of 1 to 6")
	}
	snap, pr, err := e.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	var ins *core.InsightsReport
	if pr.Insights != nil { // the marks the TUI draws, when the engine is connected and read cleanly
		if rep := core.ReadInsights(ctx, pr.Insights, pr.InsightsWhy); rep.State == core.Connected && !rep.Empty {
			ins = &rep
		}
	}
	gv := taskview.NewGraphIndex(taskview.Build(snap, e.cfg.Now())).View(taskview.GraphOptions{Focus: p.ID, Depth: p.Depth, Whole: p.Whole, Insights: ins})
	if p.ID != "" && gv.Focus == "" {
		return nil, failErr(core.CodeNotFound, "no task %q", core.Sanitize(p.ID))
	}
	return gv, nil
}

func (e *Engine) tasksHistory(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ID    string `json:"id"`
		Limit int    `json:"limit"`
	}
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	if p.Limit == 0 {
		p.Limit = core.HistoryDefaultLimit
	}
	if !core.TaskIDRE.MatchString(p.ID) {
		return nil, usageErr("%q is not a task id", core.Sanitize(p.ID))
	}
	if p.Limit < 1 || p.Limit > core.HistoryMaxLimit {
		return nil, usageErr("limit is 1 to %d", core.HistoryMaxLimit)
	}
	src := core.ResolveProviders(e.entries(ctx)).History
	if src == nil {
		return nil, failErr(core.CodeNotConnected, "tasks: not connected. %s", core.HowToConnectFor(core.DomainTasks))
	}
	rep, err := src.History(ctx, p.ID, p.Limit)
	if err != nil {
		return nil, fromErr(err)
	}
	return rep, nil
}

func (e *Engine) workspace(ctx context.Context, raw json.RawMessage) (any, error) {
	if err := decodeParams(raw, &struct{}{}); err != nil {
		return nil, err
	}
	if w := core.ReadDomains(ctx, e.entries(ctx)).Workspace; w != nil {
		return *w, nil
	}
	return core.WorkspaceReport{}, nil
}

// newSetup is the first-run setup over the detected modules: the configured one, or the real one.
func (e *Engine) newSetup(en []core.Entry) *core.Setup {
	if e.cfg.Setup != nil {
		return e.cfg.Setup(en)
	}
	o := core.DefaultSetupOptions()
	o.Audit = e.audit()
	return core.NewSetup(en, o)
}

func (e *Engine) setupState(ctx context.Context, raw json.RawMessage) (any, error) {
	if err := decodeParams(raw, &struct{}{}); err != nil {
		return nil, err
	}
	if e.cfg.Demo { // demo data has no tracker to set up: the steps of a first run, as plans only
		return core.SetupReport{Steps: core.DemoSetupSteps(), PlanDigest: core.DemoDigest(core.SetupDigest(core.DemoSetupStepPlans()))}, nil
	}
	s := e.newSetup(e.entries(ctx))
	return core.SetupReport{Steps: s.Steps(ctx), PlanDigest: core.SetupDigest(s.SetupStepPlans(ctx))}, nil
}

func (e *Engine) configShow(_ context.Context, raw json.RawMessage) (any, error) {
	if err := decodeParams(raw, &struct{}{}); err != nil {
		return nil, err
	}
	st := e.cfg.Config
	if st.Path == "" {
		p, err := core.DefaultConfigPath()
		if err != nil {
			return nil, fromErr(err)
		}
		st = core.ConfigStore{Path: p}
	}
	c, err := st.Load()
	if err != nil {
		return nil, fromErr(err)
	}
	return core.ConfigShow{Path: st.Path, Config: c}, nil
}
