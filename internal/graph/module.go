package graph

import (
	"context"
	"fmt"
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

// Options configure the graph module.
type Options struct {
	Source   IssueSource
	Analyzer Analyzer
	Timeout  time.Duration
	// Now is the clock the export age is measured against (default time.Now).
	Now func() time.Time
}

// Module is the graph engine as a core module and core.InsightsProvider: bv's analysis package run
// in this process, always. It is Quiet: it is compiled in, so it is not a tool to list.
type Module struct {
	o       Options
	mu      sync.Mutex
	lastErr string
}

func NewModule(o Options) *Module {
	if o.Timeout <= 0 {
		o.Timeout = core.DefaultExecTimeout
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Module{o: o}
}

// AgeText renders an age compactly: 20s, 5m, 1h30m, 2d1h. Negative reads as 0s.
func AgeText(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		h, m := int(d/time.Hour), int(d%time.Hour/time.Minute)
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	}
	days, h := int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour)
	if h == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd%dh", days, h)
}

// freshness is the source's export freshness, when it has one.
func (m *Module) freshness() (Freshness, bool) {
	if fs, ok := m.o.Source.(FreshnessSource); ok {
		return fs.Freshness()
	}
	return Freshness{}, false
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

// Doctor reports why the last insights read failed, and how old the issue export is.
func (m *Module) Doctor(context.Context) []core.Check {
	m.mu.Lock()
	last := m.lastErr
	m.mu.Unlock()
	if last != "" {
		return []core.Check{{Name: "insights", Status: core.StatusWarn, Detail: "the last graph-insights read failed: " + last,
			Fix: "check that bd can read this tracker (bd status), then press r"}}
	}
	checks := []core.Check{{Name: "insights", Status: core.StatusOK, Detail: "in-process analysis"}}
	if f, ok := m.freshness(); ok {
		if f.Stale() {
			checks = append(checks, core.Check{Name: "export", Status: core.StatusWarn,
				Detail: "the issue export is " + AgeText(f.Behind()) + " older than the tracker data, so the insights may be out of date",
				Fix:    "let the tracker write its export again (it does after the next change)"})
		} else {
			checks = append(checks, core.Check{Name: "export", Status: core.StatusOK, Detail: "the issue export is " + AgeText(m.o.Now().Sub(f.Export)) + " old"})
		}
	}
	return checks
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
	if f, ok := m.freshness(); ok {
		if f.Stale() {
			r.Header.Reason = "the issue export is " + AgeText(f.Behind()) + " older than the tracker data, so these numbers may be out of date"
		} else {
			r.Header.Summary = "export " + AgeText(m.o.Now().Sub(f.Export)) + " old"
		}
	}
	return r, nil
}
