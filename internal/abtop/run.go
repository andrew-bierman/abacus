// Package abtop is abacus's own full-screen monitor for the AI coding agents running on this
// machine, modelled on the Rust abtop but reading through abacus's Sessions domain. It is a
// front-end: it parses flags, asks for the typed report and draws it. The read is the one the
// abacus Sessions tab renders, so both show the same facts.
//
// It is read-only toward agents: every read is local, nothing is sent anywhere, and no agent is
// ever started, driven or stopped.
package abtop

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/agents"
	"github.com/andrew-bierman/abacus/internal/brand"
	"github.com/andrew-bierman/abacus/internal/core"
)

const (
	defaultInterval = 2 * time.Second
	readTimeout     = 30 * time.Second
)

// Reader is one read of the Sessions domain. It is an injectable seam: the binary supplies the
// read through core, and a test supplies an invented report so no test reads this machine.
type Reader func(context.Context) (core.SessionsReport, error)

// Deps are the process edges, injectable for tests.
type Deps struct {
	Out, Err io.Writer
	Version  string
	// Read is the complete one-shot read behind --once and --json. It drains a long transcript
	// so a script and a monitor see the same totals.
	Read Reader
	// Tick is the bounded per-frame read. nil means Read, which a test may supply alone; the
	// binary passes the tab's non-draining read so a refresh never waits on a backlog.
	Tick Reader
	// Now is the clock for quota staleness in the text snapshot and the first frame; nil means
	// time.Now.
	Now func() time.Time
	// StartTUI runs the interactive program. nil means the real full-screen program on the
	// alternate screen.
	StartTUI func(tea.Model) error
}

func startProgram(m tea.Model) error {
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

// Run parses the flags and returns the process exit code.
func Run(args []string, d Deps) int {
	if d.Out == nil {
		d.Out = io.Discard
	}
	if d.Err == nil {
		d.Err = io.Discard
	}
	if d.Now == nil {
		d.Now = time.Now
	}

	fs := flag.NewFlagSet(brand.Monitor, flag.ContinueOnError)
	fs.SetOutput(d.Err)
	var once, asJSON, showVersion bool
	interval := defaultInterval
	fs.BoolVar(&once, "once", false, "print one text snapshot and exit")
	fs.BoolVar(&asJSON, "json", false, "print one typed report as JSON and exit")
	fs.BoolVar(&showVersion, "version", false, "print the version and exit")
	fs.DurationVar(&interval, "interval", defaultInterval, "how often the monitor re-reads the machine")
	fs.Usage = func() { usage(d.Err) }
	if err := fs.Parse(args); err != nil {
		return core.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(d.Err, "%s: unexpected argument %q\n", brand.Monitor, fs.Arg(0))
		usage(d.Err)
		return core.ExitUsage
	}
	if showVersion {
		fmt.Fprintf(d.Out, "%s %s\n", brand.Monitor, d.Version)
		return core.ExitOK
	}
	if interval <= 0 {
		fmt.Fprintf(d.Err, "%s: --interval must be positive\n", brand.Monitor)
		usage(d.Err)
		return core.ExitUsage
	}
	if d.Read == nil {
		fmt.Fprintf(d.Err, "%s: no sessions read is configured\n", brand.Monitor)
		return core.ExitFail
	}
	if once || asJSON {
		return oneShot(d, asJSON)
	}
	start := d.StartTUI
	if start == nil {
		start = startProgram
	}
	tick := d.Tick
	if tick == nil {
		tick = d.Read
	}
	if err := start(newModel(tick, interval, d.Now)); err != nil {
		fmt.Fprintf(d.Err, "%s: %v\n", brand.Monitor, err)
		return core.ExitFail
	}
	return core.ExitOK
}

func usage(w io.Writer) {
	fmt.Fprintf(w, `usage: %s [--once | --json] [--interval %s]

full-screen monitor for the AI coding agents running on this machine.
It reads, and never starts, drives or stops an agent.

flags:
  --once          print one text snapshot and exit
  --json          print one typed report as JSON and exit (implies --once)
  --interval D    how often the monitor re-reads the machine (default %s)
  --version       print the version and exit
`, brand.Monitor, defaultInterval, defaultInterval)
}

// oneShot is the command-line read: the same complete read whether it is drawn as text or
// printed as the typed report, so a script and a person see the same facts.
func oneShot(d Deps, asJSON bool) int {
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()
	rep, err := d.Read(ctx)
	if err != nil {
		fmt.Fprintf(d.Err, "%s: %s\n", brand.Monitor, core.Sanitize(err.Error()))
		return core.ExitFail
	}
	if asJSON {
		b, err := core.ReportJSON(rep)
		if err != nil {
			fmt.Fprintf(d.Err, "%s: %v\n", brand.Monitor, err)
			return core.ExitFail
		}
		_, _ = d.Out.Write(b)
		return core.ExitOK
	}
	for _, line := range reportLines(rep, d.Now()) {
		fmt.Fprintln(d.Out, line)
	}
	return core.ExitOK
}

// headline is the one-line state a report is called by: the summary when the read is usable,
// else the connection state. The text snapshot and the full-screen header both use it, so the
// two front-ends cannot name the same read differently.
func headline(r core.SessionsReport) (head, source string) {
	head = r.Summarize()
	if r.State != core.Connected && r.State != "" {
		head = r.State.Label()
	}
	return head, r.Source
}

// reportLines is the plain-text frame behind --once.
func reportLines(r core.SessionsReport, now time.Time) []string {
	head, source := headline(r)
	line := fmt.Sprintf("%s  %s", brand.Monitor, head)
	if source != "" {
		line += "  (" + source + ")"
	}
	lines := []string{line}
	if r.Reason != "" {
		lines = append(lines, "  "+core.Sanitize(r.Reason))
	}
	if r.HowToConnect != "" {
		lines = append(lines, "  how to connect: "+core.Sanitize(r.HowToConnect))
	}
	lines = append(lines, "")
	// An empty session list is evidence of idleness only after a usable read: a degraded or
	// not-connected report has no sessions because nothing could be read.
	if len(r.Sessions) == 0 && r.State == core.Connected {
		lines = append(lines, "  no agent sessions are running")
	}
	for _, s := range r.Sessions {
		lines = append(lines, sessionRow(s))
	}
	for _, q := range r.Quotas {
		lines = append(lines, quotaRow(q, now))
	}
	for _, p := range r.Orphans {
		lines = append(lines, fmt.Sprintf("  orphan port %d  %s  (%s)",
			p.Port, core.Sanitize(p.Command), core.Sanitize(p.Project)))
	}
	for _, why := range r.Unreadable {
		lines = append(lines, "  unreadable "+core.Sanitize(why))
	}
	return lines
}

func sessionRow(s agents.Session) string {
	name := sessionName(s)
	state := stateString(s.State)
	fields := []string{
		fmt.Sprintf("%-10s", state),
		fmt.Sprintf("%-9s", string(s.Harness)),
		fmt.Sprintf("%-7d", s.PID),
		name,
	}
	if s.Model != "" {
		fields = append(fields, core.Sanitize(s.Model))
	}
	if t := s.Tokens.Total(); t > 0 {
		fields = append(fields, strconv.FormatInt(t, 10)+" tok")
	}
	if p := s.Context.Percent(); p > 0 {
		fields = append(fields, fmt.Sprintf("%.0f%% ctx", p))
	}
	if s.Git.Branch != "" {
		fields = append(fields, core.Sanitize(s.Git.Branch))
	}
	if nums := portNumbers(s.Ports); len(nums) > 0 {
		fields = append(fields, "ports "+strings.Join(nums, ","))
	}
	return "  " + strings.Join(fields, "  ")
}

func quotaRow(q agents.Quota, now time.Time) string {
	line := "  quota " + string(q.Source) + quotaWindow(q.Short, agents.QuotaShortWindowMinutes) +
		quotaWindow(q.Long, agents.QuotaLongWindowMinutes)
	if q.Stale(now) {
		line += "  (stale)"
	}
	return line
}

func quotaWindow(w *agents.QuotaWindow, fallbackMinutes int64) string {
	if w == nil {
		return ""
	}
	return fmt.Sprintf("  %s %.0f%%", w.Span(fallbackMinutes), w.UsedPercent)
}

func portNumbers(ports []agents.Port) []string {
	out := make([]string, len(ports))
	for i, p := range ports {
		out[i] = strconv.Itoa(p.Port)
	}
	return out
}
