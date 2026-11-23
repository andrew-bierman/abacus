package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ToolLine is one tool in the overview: where it came from and which version.
type ToolLine struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	State   State  `json:"state"`
	Where   string `json:"where"` // bundled, PATH, override, demo or missing
	Version string `json:"version,omitempty"`
	Path    string `json:"path,omitempty"`
	Detail  string `json:"detail,omitempty"`
	// Note is an informational line: a bundled copy that was refused (and what ran instead), or a
	// version that differs from the pinned one. It never blocks anything.
	Note string `json:"note,omitempty"`
	// Bundled is true for the tools abacus ships itself; Optional for a tool it does not ship and
	// does not need (the Vercel Board CLI): its absence is not a problem.
	Bundled  bool `json:"bundled_tool,omitempty"`
	Optional bool `json:"optional,omitempty"`
}

// Label is the one word shown for where a tool came from. A tool that was not located is never
// "missing" to the user: abacus's own copy is "unavailable" (and Note says what to do), and an
// optional tool is "optional". Where keeps the raw value for JSON consumers.
func (t ToolLine) Label() string {
	if t.Where == "" && t.State == Ready {
		return "found"
	}
	if t.Where != "missing" && t.Where != "" {
		return t.Where
	}
	if t.Bundled {
		return "unavailable"
	}
	if t.Optional {
		return "optional"
	}
	return "not installed"
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
	// Bundled is the self-contained check: how many of the tools abacus ships were found as its
	// own verified copies. Additive; Ready needs every one of them usable.
	Bundled BundledSummary `json:"bundled"`
}

// BuildOverview reads each domain (without graph insights, which Doctor does not need) and
// summarizes it. Ready means Tasks works and no tool is broken; the other domains are
// optional and only add attention items.
func BuildOverview(ctx context.Context, entries []Entry) Overview {
	return overviewFrom(entries, readDomainsLite(ctx, entries))
}

// readDomainsLite reads every domain except the graph insights.
func readDomainsLite(ctx context.Context, entries []Entry) DomainsReport {
	var d DomainsReport
	d.readTasks(ctx, entries)
	d.readLeases(ctx, entries)
	d.readQueues(ctx, entries)
	d.readBoard(ctx, entries)
	return d
}

func overviewFrom(entries []Entry, d DomainsReport) Overview {
	o := Overview{Ready: true}
	pinned := PinnedVersions()
	var required []string
	broken := false
	var shown []ModuleReport
	var unusable []string
	for _, e := range entries {
		if q, ok := e.Module.(interface{ Quiet() bool }); ok && q.Quiet() {
			continue
		}
		t := ToolLine{ID: e.Module.ID(), Name: e.Module.Name(), State: e.Detection.State, Where: string(e.Detection.Source),
			Version: e.Detection.Version, Path: e.Detection.Path, Detail: e.Detection.Detail, Bundled: IsBundledTool(e.Module.ID())}
		_, t.Optional = optionalAdds[t.ID]
		if e.Detection.State == Absent {
			t.Where = "missing"
		}
		t.Note = toolNote(e, t.Where, pinned)
		shown = append(shown, base(e))
		if e.Detection.State == Broken {
			broken = true
			required = append(required, t.Name+" is installed but cannot run: "+e.Detection.Detail)
		}
		if t.Bundled && e.Detection.State == Incompatible { // usable by nobody: Ready needs a reason to wait
			required = append(required, t.ID+" cannot be used: "+e.Detection.Detail)
		}
		if t.Bundled && e.Detection.State == Absent {
			unusable = append(unusable, t.ID)
			if !IsDevBuild() {
				required = append(required, AbsentSentence(t.ID, e.Detection.Detail))
			}
		}
		o.Tools = append(o.Tools, t)
	}
	if IsDevBuild() && len(unusable) > 0 {
		required = append(required, fmt.Sprintf("abacus's own tools are not set up for this build (%s): run %s (or make bundle) in the abacus source folder.", strings.Join(unusable, ", "), DevBundleCommand))
	}
	o.Bundled = SummarizeBundled(shown)
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
	for i := range lines {
		l := lines[i]
		o.Domains = append(o.Domains, l)
		if l.State == Connected {
			continue
		}
		if tool, ok := bundledOwner(l.Domain); ok && l.State == NotConnected && contains(unusable, tool) {
			continue // the bundled-tool line above already says what is wrong and how to fix it
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

func trimV(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "v") }

// toolNote explains, in plain words, a tool that is not abacus's own verified copy: a bundled
// tool that is unavailable (and how to restore it), one that fell back to PATH (and why), an
// optional tool that is not installed (and what it adds), and, only informationally, a bundled
// version that differs from the pin in bundled-tools.json.
func toolNote(e Entry, where string, pinned map[string]string) string {
	d, id := e.Detection, e.Module.ID()
	var parts []string
	if where == "missing" {
		if s := AbsentSentence(id, d.Detail); s != d.Detail {
			parts = append(parts, s)
		}
	} else if IsBundledTool(id) {
		if n := bundledNote(id, d); n != "" {
			parts = append(parts, n)
		}
	}
	if p := pinned[id]; where == "bundled" && p != "" && d.Version != "" && trimV(d.Version) != trimV(p) {
		parts = append(parts, fmt.Sprintf("version %s differs from the pinned %s (informational)", trimV(d.Version), trimV(p)))
	}
	return strings.Join(parts, "; ")
}

func bundledOwner(d DomainID) (string, bool) {
	for tool, dom := range bundledDomain {
		if dom == d {
			return tool, true
		}
	}
	return "", false
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// PinnedVersions reads bundled-tools.json as shipped in the archive (third_party/ next to the
// executable or its bundled/ directory) and returns tool -> pinned version. Absent or unreadable
// means no pins are known; this is only ever used for an informational note.
func PinnedVersions() map[string]string {
	out := map[string]string{}
	for _, d := range SidecarDirs() {
		for _, p := range []string{filepath.Join(d, "third_party", "bundled-tools.json"), filepath.Join(d, "..", "third_party", "bundled-tools.json")} {
			b, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			var m struct {
				Tools []struct{ Name, Version string } `json:"tools"`
			}
			if json.Unmarshal(b, &m) != nil {
				continue
			}
			for _, t := range m.Tools {
				out[t.Name] = t.Version
			}
			return out
		}
	}
	return out
}
