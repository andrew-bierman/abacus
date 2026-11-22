package core

import (
	"context"
	"fmt"
	"strings"
)

// ToolLine is one tool in the overview: where it came from and which version.
type ToolLine struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	State   State  `json:"state"`
	Where   string `json:"where"` // bundled, PATH, override or missing
	Version string `json:"version,omitempty"`
	Path    string `json:"path,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// DomainLine is one domain in the overview, in one plain line.
type DomainLine struct {
	Domain DomainID   `json:"domain"`
	Title  string     `json:"title"`
	State  Connection `json:"state"`
	Line   string     `json:"line"`
	Guide  *Guide     `json:"guide,omitempty"`
}

// Overview is what Doctor leads with: ready or needs attention, in plain language, then each
// domain in one line and each tool with where it was found. Additive to the doctor report.
type Overview struct {
	Ready     bool         `json:"ready"`
	Headline  string       `json:"headline"`
	Attention []string     `json:"attention,omitempty"`
	Domains   []DomainLine `json:"domain_lines"`
	Tools     []ToolLine   `json:"tools"`
}

// BuildOverview reads each domain (without graph insights, which Doctor does not need) and
// summarizes it. Ready means Tasks works and no tool is broken; the other domains are
// optional and only add attention items.
func BuildOverview(ctx context.Context, entries []Entry) Overview {
	var d DomainsReport
	d.readTasks(ctx, entries)
	d.readLeases(ctx, entries)
	d.readQueues(ctx, entries)
	d.readBoard(ctx, entries)
	o := Overview{Ready: true}
	var required []string
	broken := false
	for _, e := range entries {
		t := ToolLine{ID: e.Module.ID(), Name: e.Module.Name(), State: e.Detection.State, Where: string(e.Detection.Source),
			Version: e.Detection.Version, Path: e.Detection.Path, Detail: e.Detection.Detail}
		if e.Detection.State == Absent {
			t.Where = "missing"
		}
		if e.Detection.State == Broken {
			broken = true
			required = append(required, t.Name+" is installed but cannot run: "+e.Detection.Detail)
		}
		o.Tools = append(o.Tools, t)
	}
	lines := []DomainLine{
		domainLine(d.Tasks.Header, func() string {
			if d.Tasks.Counts.Total == 0 {
				return "no tasks yet — add one from the Tasks tab (n)"
			}
			return d.Tasks.Summary
		}),
		domainLine(d.Leases.Header, func() string {
			if len(d.Leases.Leases) == 0 {
				return "no leases held right now"
			}
			return d.Leases.Summary
		}),
		domainLine(d.Queues.Header, func() string {
			if len(d.Queues.Runs) == 0 && len(d.Queues.Queue) == 0 {
				return "no runs or queued work yet"
			}
			return d.Queues.Summary
		}),
		domainLine(d.Board.Header, func() string { return d.Board.Summary }),
	}
	// Board's one-liner keeps the upstream detail that the tab itself no longer shows.
	lines[3].Line = "not connected yet: waiting on an upstream board contract (board-beads issue 15); press o on the Board tab to open b9s"
	for i := range lines {
		l := lines[i]
		o.Domains = append(o.Domains, l)
		if l.Domain == DomainBoard || l.State == Connected {
			continue
		}
		item := l.Title + ": " + l.Line
		if l.Domain == DomainTasks {
			required = append(required, item)
			continue
		}
		o.Attention = append(o.Attention, item)
	}
	o.Attention = append(required, o.Attention...)
	switch {
	case len(required) > 0 || broken:
		o.Ready = false
		o.Headline = "Needs attention: " + required[0]
	case len(o.Attention) > 0:
		o.Headline = "Ready"
		if n := len(o.Attention); n == 1 {
			o.Headline += " — 1 optional thing to set up"
		} else {
			o.Headline += fmt.Sprintf(" — %d optional things to set up", n)
		}
	default:
		o.Headline = "Ready"
	}
	return o
}

func domainLine(h Header, connected func() string) DomainLine {
	l := DomainLine{Domain: h.Domain, Title: h.Title, State: h.State}
	switch h.State {
	case Connected:
		l.Line = connected()
	case NeedsSetup:
		l.Line, l.Guide = h.Setup.CLILine(), h.Setup
	case NotConnected, Degraded:
		// One line: the reason. The tab itself carries how to fix it.
		l.Line = strings.TrimSpace(h.Reason)
		if l.Line == "" {
			l.Line = h.HowToConnect
		}
	}
	if l.Line == "" {
		l.Line = string(h.State)
	}
	return l
}
