package graph

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// Analyzer turns issues into the provider-neutral insights report. An implementation must
// return core.EmptyInsights() for no issues without analysing anything, and must sanitize every
// string it puts in the report (ids, titles, reasons).
type Analyzer interface {
	Analyze(ctx context.Context, issues []Issue) (core.InsightsReport, error)
}

// Engine is which graph-insights engine runs.
type Engine string

const (
	// Subprocess runs the bv binary (internal/bv): the default.
	Subprocess Engine = "subprocess"
	// Library runs bv's analysis package in this process (this package): opt-in.
	Library Engine = "library"
)

// EngineEnv is the environment variable that picks the engine.
const EngineEnv = "ABACUS_GRAPH_ENGINE"

// ParseEngine reads the ABACUS_GRAPH_ENGINE value. Unset or "subprocess" is the default engine,
// "library" opts in (case and surrounding spaces do not matter). Any other value is the default
// engine with known=false, so a typo never changes behavior and never fails: Doctor reports it.
func ParseEngine(raw string) (e Engine, known bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(Subprocess):
		return Subprocess, true
	case string(Library):
		return Library, true
	}
	return Subprocess, false
}

// Options configure the library module.
type Options struct {
	Source   IssueSource
	Analyzer Analyzer
	Timeout  time.Duration
}

// Module is the library engine as a core module and core.InsightsProvider. It is Quiet: it
// replaces bv in the registry only when the library engine is chosen, and is not a tool to list.
type Module struct {
	o       Options
	mu      sync.Mutex
	lastErr string
}

func NewModule(o Options) *Module {
	if o.Timeout <= 0 {
		o.Timeout = core.DefaultExecTimeout
	}
	return &Module{o: o}
}

func (*Module) ID() string   { return "graph" }
func (*Module) Name() string { return "graph engine" }
func (*Module) Quiet() bool  { return true }

// Detect is always ready: the engine is compiled in, so there is nothing to find or version.
func (*Module) Detect(context.Context) core.Detection { return core.Detection{State: core.Ready} }

func (m *Module) setErr(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastErr = ""
	if err != nil {
		m.lastErr = core.Sanitize(err.Error())
	}
}

// Doctor reports why the last insights read failed, as bv's module does.
func (m *Module) Doctor(context.Context) []core.Check {
	m.mu.Lock()
	last := m.lastErr
	m.mu.Unlock()
	if last != "" {
		return []core.Check{{Name: "insights", Status: core.StatusWarn, Detail: "the last graph-insights read failed: " + last,
			Fix: "check the tracker export, or unset " + EngineEnv + " to use the default engine, then press r"}}
	}
	return []core.Check{{Name: "insights", Status: core.StatusOK, Detail: "in-process analysis"}}
}

func (m *Module) Status(ctx context.Context) core.Status {
	r, err := m.Insights(ctx)
	if err != nil {
		return core.Status{Unavailable: core.Sanitize(err.Error())}
	}
	return core.Status{Summary: r.Summarize(), Source: core.SourceGraph}
}

// Insights reads the issues and analyzes them. A tracker with no issues is core.EmptyInsights()
// and the analyzer is not called.
func (m *Module) Insights(ctx context.Context) (core.InsightsReport, error) {
	ctx, cancel := context.WithTimeout(ctx, m.o.Timeout)
	defer cancel()
	issues, err := m.o.Source.Issues(ctx)
	if err != nil {
		m.setErr(err)
		return core.InsightsReport{}, err
	}
	if len(issues) == 0 {
		m.setErr(nil)
		return core.EmptyInsights(), nil
	}
	r, err := m.o.Analyzer.Analyze(ctx, issues)
	m.setErr(err)
	if err != nil {
		return core.InsightsReport{}, err
	}
	r.Header.Source = core.SourceGraph
	return r, nil
}

// EngineCheck is a Quiet module that only puts the engine choice in Doctor: which engine is
// active, and a warning when ABACUS_GRAPH_ENGINE holds a value that is not understood.
type EngineCheck struct{ raw string }

func NewEngineCheck(raw string) *EngineCheck { return &EngineCheck{raw: raw} }

func (*EngineCheck) ID() string                            { return "graph-engine" }
func (*EngineCheck) Name() string                          { return "graph engine" }
func (*EngineCheck) Detect(context.Context) core.Detection { return core.Detection{State: core.Ready} }
func (*EngineCheck) Status(context.Context) core.Status    { return core.Status{} }
func (*EngineCheck) Quiet() bool                           { return true }

func (c *EngineCheck) Doctor(context.Context) []core.Check {
	e, known := ParseEngine(c.raw)
	if !known {
		return []core.Check{{Name: "graph engine", Status: core.StatusWarn, Detail: EngineEnv + ": unknown value, using the default",
			Fix: "set " + EngineEnv + "=library to opt in, or unset it"}}
	}
	if e == Library {
		return []core.Check{{Name: "graph engine", Status: core.StatusOK, Detail: "library (in-process analysis, opt-in via " + EngineEnv + ")"}}
	}
	return []core.Check{{Name: "graph engine", Status: core.StatusOK, Detail: "subprocess (bv, the default)"}}
}
