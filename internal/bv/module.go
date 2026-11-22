// Package bv is the graph-insights provider. It runs the bv binary as a subprocess only
// (never imports, vendors or copies its code), always with an explicit --robot-* mode and
// `--format json`, behind a read-only argv allowlist. Bare bv launches a TUI and is never run.
package bv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// Options configure the module; every process boundary is injectable.
type Options struct {
	Bin      string
	Dir      string // working directory for bv (its tracker); empty means the current one
	Label    string // optional --label scope
	Recipe   string // optional --recipe scope
	LookPath func(string) (string, error)
	Runner   core.Runner
	Timeout  time.Duration
}

type Module struct {
	o   Options
	bin string
}

func New(o Options) *Module {
	if o.Runner == nil {
		o.Runner = core.ExecRunner{Dir: o.Dir}
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.Timeout <= 0 {
		o.Timeout = core.DefaultExecTimeout
	}
	return &Module{o: o}
}

func (m *Module) ID() string   { return "bv" }
func (m *Module) Name() string { return "bv" }

// Detect is by PATH lookup alone: abacus runs nothing to detect it.
func (m *Module) Detect(context.Context) core.Detection {
	bin, absent := core.Locate("bv", m.o.Bin, m.o.LookPath)
	if absent != "" {
		return core.Detection{State: core.Absent, Detail: absent}
	}
	m.bin = bin
	return core.Detection{State: core.Ready}
}

func (m *Module) Doctor(context.Context) []core.Check {
	return []core.Check{{Name: "installation", Status: core.StatusOK, Detail: "found at " + core.Sanitize(m.bin) + " (detected by PATH only)"}}
}

func (m *Module) Status(ctx context.Context) core.Status {
	r, err := m.Insights(ctx)
	if err != nil {
		return core.Status{Unavailable: core.Sanitize(err.Error())}
	}
	return core.Status{Summary: r.Summarize(), Source: core.SourceGraph}
}

var modes = []string{"--robot-triage", "--robot-plan", "--robot-insights", "--robot-graph"}

var scopeRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// allowedArgv is the whole set of bv argvs this module may run, matched element by element:
// one of the four robot modes, then `--format json` (explicit, so BV_OUTPUT_FORMAT=toon in the
// environment cannot change the output), then optionally `--label X` and `--recipe X`, each once.
func allowedArgv(a []string) bool {
	if len(a) < 3 || a[1] != "--format" || a[2] != "json" {
		return false
	}
	ok := false
	for _, m := range modes {
		ok = ok || a[0] == m
	}
	if !ok {
		return false
	}
	seen := map[string]bool{}
	for rest := a[3:]; len(rest) > 0; rest = rest[2:] {
		if len(rest) < 2 || (rest[0] != "--label" && rest[0] != "--recipe") || seen[rest[0]] || !scopeRE.MatchString(rest[1]) {
			return false
		}
		seen[rest[0]] = true
	}
	return true
}

func (m *Module) argv(mode string) []string {
	a := []string{mode, "--format", "json"}
	if m.o.Label != "" {
		a = append(a, "--label", m.o.Label)
	}
	if m.o.Recipe != "" {
		a = append(a, "--recipe", m.o.Recipe)
	}
	return a
}

// run runs one allowlisted robot command. stdout is data only; any non-zero exit means the
// section is unavailable and its stdout is ignored.
func (m *Module) run(ctx context.Context, mode string, into any) error {
	a := m.argv(mode)
	if !allowedArgv(a) {
		return fmt.Errorf("refused: %v is not an allowed argv", a)
	}
	ctx, cancel := context.WithTimeout(ctx, m.o.Timeout)
	defer cancel()
	out, exit, err := m.o.Runner.Run(ctx, m.bin, a...)
	if err != nil {
		return fmt.Errorf("could not run: %s", core.Sanitize(err.Error()))
	}
	if exit != 0 {
		return fmt.Errorf("exited %d", exit)
	}
	if json.Unmarshal(out, into) != nil {
		return errors.New("unreadable output")
	}
	return nil
}

// bv's real shape (v0.27.0): ids are `id` in triage/plan and `ID` in the insights metrics.
type pickJSON struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Score         float64  `json:"score"`
	Reasons       []string `json:"reasons"`
	Unblocks      int      `json:"unblocks"`
	UnblocksCount int      `json:"unblocks_count"`
}

type triageDoc struct {
	Triage struct {
		QuickRef struct {
			TopPicks []pickJSON `json:"top_picks"`
		} `json:"quick_ref"`
		Recommendations []pickJSON `json:"recommendations"`
		Blockers        []pickJSON `json:"blockers_to_clear"`
	} `json:"triage"`
}

type planDoc struct {
	Plan struct {
		Tracks []struct {
			ID    string `json:"track_id"`
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		} `json:"tracks"`
		TotalActionable int `json:"total_actionable"`
		TotalBlocked    int `json:"total_blocked"`
	} `json:"plan"`
}

type rankedJSON struct {
	ID    string  `json:"ID"`
	Value float64 `json:"Value"`
}

type insightsDoc struct {
	Bottlenecks []rankedJSON `json:"Bottlenecks"`
	Keystones   []rankedJSON `json:"Keystones"`
	Cycles      [][]string   `json:"Cycles"`
	Status      struct {
		Critical struct {
			State  string `json:"state"`
			Reason string `json:"reason"`
		} `json:"Critical"`
	} `json:"status"`
}

type graphDoc struct {
	Nodes json.RawMessage `json:"nodes"`
	Edges json.RawMessage `json:"edges"`
}

func count(raw json.RawMessage) int {
	var n int
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	return 0
}

func picks(in []pickJSON, limit int) []core.Pick {
	var out []core.Pick
	for _, p := range in {
		if len(out) == limit {
			break
		}
		u := p.Unblocks
		if u == 0 {
			u = p.UnblocksCount
		}
		cp := core.Pick{ID: core.Sanitize(p.ID), Title: core.Sanitize(p.Title), Score: p.Score, Unblocks: u}
		for _, r := range p.Reasons {
			cp.Reasons = append(cp.Reasons, core.Sanitize(r))
		}
		out = append(out, cp)
	}
	return out
}

func ranked(in []rankedJSON, limit int) []core.Ranked {
	var out []core.Ranked
	for _, r := range in {
		if len(out) == limit {
			break
		}
		out = append(out, core.Ranked{ID: core.Sanitize(r.ID), Value: r.Value})
	}
	return out
}

// Insights runs the four robot reads. A section that fails or cannot be read is left empty
// and named in Reason (degraded); only when every section fails is it an error.
func (m *Module) Insights(ctx context.Context) (core.InsightsReport, error) {
	r := core.InsightsReport{Header: core.Header{Source: core.SourceGraph}}
	var failed, why []string
	fail := func(section string, err error) {
		failed = append(failed, section)
		why = append(why, section+": "+err.Error())
	}
	var tri triageDoc
	if err := m.run(ctx, "--robot-triage", &tri); err != nil {
		fail("triage", err)
	} else {
		tp := tri.Triage.QuickRef.TopPicks
		if len(tp) == 0 {
			tp = tri.Triage.Recommendations
		}
		r.TopPicks, r.Blockers = picks(tp, 3), picks(tri.Triage.Blockers, 5)
	}
	var plan planDoc
	if err := m.run(ctx, "--robot-plan", &plan); err != nil {
		fail("plan", err)
	} else {
		r.TotalActionable, r.TotalBlocked = plan.Plan.TotalActionable, plan.Plan.TotalBlocked
		for _, t := range plan.Plan.Tracks {
			tr := core.Track{ID: core.Sanitize(t.ID)}
			for _, it := range t.Items {
				tr.Items = append(tr.Items, core.Sanitize(it.ID))
			}
			r.Tracks = append(r.Tracks, tr)
		}
	}
	var ins insightsDoc
	if err := m.run(ctx, "--robot-insights", &ins); err != nil {
		fail("insights", err)
	} else {
		r.CriticalPath, r.Bottlenecks = ranked(ins.Keystones, 5), ranked(ins.Bottlenecks, 5)
		for _, c := range ins.Cycles {
			var cyc []string
			for _, id := range c {
				cyc = append(cyc, core.Sanitize(id))
			}
			r.Cycles = append(r.Cycles, cyc)
		}
		if c := ins.Status.Critical; c.State == "skipped" {
			r.CriticalPathNote = core.Sanitize("critical path skipped: " + c.Reason)
		}
	}
	var g graphDoc
	if err := m.run(ctx, "--robot-graph", &g); err != nil {
		fail("graph", err)
	} else {
		r.GraphNodes, r.GraphEdges = count(g.Nodes), count(g.Edges)
	}
	if len(failed) == len(modes) {
		return core.InsightsReport{}, errors.New(strings.Join(why, "; "))
	}
	if len(why) > 0 {
		r.Reason = strings.Join(why, "; ")
	}
	return r, nil
}
