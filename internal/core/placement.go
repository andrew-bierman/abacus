package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Placement writes and reads that go beyond "where is it": focus the terminal an item lives in,
// open a terminal to work on an item, arrange the cockpit tabs, and read what a terminal shows.
// Every write goes through the same gate as the Tasks and Board writes (plan, confirm, single use,
// audit before and after, an unknown outcome is never "not applied"). Nothing here types into an
// existing terminal: a write only focuses a pane or opens a new one.

// PlacementAction is a workspace write verb. Only these exist.
type PlacementAction string

const (
	PlaceFocus  PlacementAction = "focus"  // bring an item's terminal to the front
	PlaceWork   PlacementAction = "work"   // open a new terminal running the agent command
	PlaceLayout PlacementAction = "layout" // open the cockpit tabs
)

// LayoutTab is one tab of the cockpit layout: the name it shows and the `tui --tab` value it opens.
type LayoutTab struct{ Name, Arg string }

// LayoutTabs are the cockpit tabs in order. The first is the one left in front.
func LayoutTabs() []LayoutTab {
	return []LayoutTab{{"Board", "board"}, {"Tasks", "tasks"}, {"Leases", "leases"}, {"Queues", "queues"}}
}

// ValidTab reports whether name is a tab `tui --tab` accepts: a fleet domain or doctor.
func ValidTab(name string) bool {
	n := strings.ToLower(name)
	if n == "doctor" {
		return true
	}
	for _, d := range DomainOrder {
		if string(d) == n {
			return true
		}
	}
	return false
}

// PlacementRequest is what a user asked for.
type PlacementRequest struct {
	Action PlacementAction
	Item   WorkspaceItem // focus, work
	Title  string        // work: fills {title}
	// Target is where the item lives (focus). It must name a pane.
	Target *Placement
	// Template is the configured agent command (work); the provider is given Command, its expansion.
	Template []string
	Command  []string
	Cwd      string
	// Split opens the terminal beside the current pane instead of in a new tab (work).
	Split bool
	// Exe is the program the layout tabs run, and NewSession a session to create for them; empty
	// arranges the tabs in the current session (layout).
	Exe        string
	NewSession string
}

// PlacementBuild is the provider's exact plan: one or more commands run in order, and the lines
// that tell a person what they do.
type PlacementBuild struct {
	Steps [][]string
	Lines []string
}

// PlacementResult is what a write did.
type PlacementResult struct {
	Action PlacementAction `json:"action"`
	Steps  int             `json:"steps"`
	// Panes are the ids of the terminals that were opened.
	Panes []string `json:"panes,omitempty"`
}

// StepResult is what one command reported.
type StepResult struct{ Pane string }

// WorkspaceActor is what a workspace provider offers for writes. BuildPlacement refuses anything
// outside the provider's write allowlist (and reads what it needs to plan); RunPlacement re-checks
// each command before it runs it. A timeout or cancel must come back as UnknownOutcome.
type WorkspaceActor interface {
	BuildPlacement(ctx context.Context, r PlacementRequest) (PlacementBuild, error)
	// CheckPlacement re-validates a plan's commands (the allowlist and which machine they reach)
	// before any of them runs, so a plan not made by PlanPlacement cannot get past it.
	CheckPlacement(steps [][]string) error
	RunPlacement(ctx context.Context, argv []string) (StepResult, error)
	// PlacementTarget says where the write goes, for the audit log and the confirm step.
	PlacementTarget() string
}

// PlacementPlan is a validated request with its exact commands. It cannot be applied until Confirm.
type PlacementPlan struct {
	id        uint64
	Request   PlacementRequest
	Steps     [][]string
	Lines     []string
	confirmed bool
}

// Confirm marks the plan as explicitly approved by the user.
func (p PlacementPlan) Confirm() PlacementPlan { p.confirmed = true; return p }

// Argv is the first command, which is the whole plan for focus and work.
func (p PlacementPlan) Argv() []string {
	if len(p.Steps) == 0 {
		return nil
	}
	return p.Steps[0]
}

var placementSeq atomic.Uint64

// PlanPlacement validates a request and asks the provider for its commands. Nothing runs.
func PlanPlacement(ctx context.Context, w WorkspaceActor, r PlacementRequest) (PlacementPlan, error) {
	switch r.Action {
	case PlaceFocus:
		if r.Target == nil || r.Target.Block == "" {
			return PlacementPlan{}, errors.New("there is no terminal for this item to bring to the front")
		}
		if !ItemIDRE.MatchString(r.Item.ID) {
			return PlacementPlan{}, fmt.Errorf("%q is not an id that can be put in a command", ClipRunes(Sanitize(r.Item.ID), 40))
		}
	case PlaceWork:
		cmd, err := ExpandAgentCommand(r.Template, r.Item.ID, r.Title)
		if err != nil {
			return PlacementPlan{}, err
		}
		r.Command = cmd
	case PlaceLayout:
		if r.Exe == "" {
			return PlacementPlan{}, errors.New("the layout needs the path of this program")
		}
	default:
		return PlacementPlan{}, fmt.Errorf("unsupported action %q", r.Action)
	}
	if r.Item.Host != "" && !ValidHandoffHost(r.Item.Host) {
		return PlacementPlan{}, errors.New("this item's machine name is not safe to pass on")
	}
	b, err := w.BuildPlacement(ctx, r)
	if err != nil {
		return PlacementPlan{}, err
	}
	if len(b.Steps) == 0 {
		return PlacementPlan{}, errors.New("nothing to do")
	}
	return PlacementPlan{id: placementSeq.Add(1), Request: r, Steps: b.Steps, Lines: b.Lines}, nil
}

// PlacementApplier is the one door to a workspace write: the confirm step, then the shared gate
// (single use, audit before and after), then the provider, one command at a time. The gate audits
// the first command; every later one is audited here before it runs, and a failed audit stops the
// rest, so nothing runs unrecorded.
type PlacementApplier struct {
	w WorkspaceActor
	g *gate
}

func NewPlacementApplier(w WorkspaceActor, a Audit) *PlacementApplier {
	return &PlacementApplier{w: w, g: newGate(a)}
}

func (a *PlacementApplier) Apply(ctx context.Context, p PlacementPlan) (PlacementResult, error) {
	if !p.confirmed {
		return PlacementResult{}, ErrNotConfirmed
	}
	if p.id == 0 || len(p.Steps) == 0 {
		return PlacementResult{}, errors.New("this plan was not made by PlanPlacement, so it is not run")
	}
	if err := a.w.CheckPlacement(p.Steps); err != nil {
		return PlacementResult{}, err
	}
	res := PlacementResult{Action: p.Request.Action}
	e := AuditEntry{Domain: "workspace", Verb: string(p.Request.Action), Argv: p.Steps[0], Tracker: a.w.PlacementTarget()}
	err := a.g.do(p.id, e, func() error {
		for i, step := range p.Steps {
			if i > 0 {
				e2 := AuditEntry{Time: time.Now().UTC(), Domain: e.Domain, Verb: e.Verb, Phase: "attempt", Argv: step, Tracker: e.Tracker}
				if err := a.g.audit.Record(e2); err != nil {
					return fmt.Errorf("stopped after %d of %d steps: %w", i, len(p.Steps), err)
				}
			}
			sr, err := a.w.RunPlacement(ctx, step)
			if err != nil {
				if len(p.Steps) > 1 {
					err = wrapStep(err, i, len(p.Steps))
				}
				return err
			}
			res.Steps++
			if sr.Pane != "" {
				res.Panes = append(res.Panes, sr.Pane)
			}
		}
		return nil
	})
	return res, err
}

// wrapStep says which step of several failed, keeping an unknown outcome unknown.
func wrapStep(err error, i, n int) error {
	msg := fmt.Sprintf("step %d of %d: %s", i+1, n, Sanitize(err.Error()))
	if errors.Is(err, ErrOutcomeUnknown) {
		return unknownOutcomeError{msg: msg}
	}
	return errors.New(msg)
}

// ---- Reading a terminal ----

// Bounds on what a terminal read may hand a front-end.
const (
	CaptureMaxLines     = 200
	CaptureMaxLineRunes = 240
)

// WorkspaceCapture is the end of what a terminal shows, as plain text lines: control characters are
// gone, lines are bounded, trailing blank lines are dropped. Truncated says there was more before it.
type WorkspaceCapture struct {
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated,omitempty"`
}

// WorkspaceCapturer is the optional read of a terminal's output. It never writes to the terminal.
type WorkspaceCapturer interface {
	Capture(ctx context.Context, p Placement) (WorkspaceCapture, error)
}

// CleanCapture makes a terminal's output safe and bounded: sanitized per line (a hostile program
// can print any control sequence), each line clipped, and only the last CaptureMaxLines kept.
// cut says the source was already cut before it got here.
func CleanCapture(raw []byte, cut bool) WorkspaceCapture {
	text := strings.ToValidUTF8(string(raw), "�")
	if cut { // the first line is probably a fragment
		if _, rest, ok := strings.Cut(text, "\n"); ok {
			text = rest
		}
	}
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"), "\n")
	for len(lines) > 0 && strings.TrimSpace(Sanitize(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	out := WorkspaceCapture{Truncated: cut, Lines: []string{}}
	if len(lines) > CaptureMaxLines {
		lines, out.Truncated = lines[len(lines)-CaptureMaxLines:], true
	}
	for _, l := range lines {
		l = strings.TrimRight(Sanitize(l), " ")
		if utf8.RuneCountInString(l) > CaptureMaxLineRunes {
			l, out.Truncated = ClipRunes(l, CaptureMaxLineRunes), true
		}
		out.Lines = append(out.Lines, l)
	}
	return out
}

// ---- Live changes ----

// WorkspaceWatcher is the optional live feed of a workspace: Watch runs until ctx is done and calls
// changed (debounced, never concurrently) when something that matters changed. It never starts a
// server and reconnects by itself; a front-end that gets no calls just keeps polling.
type WorkspaceWatcher interface {
	Watch(ctx context.Context, changed func())
}

// ---- Typed state ----

// WorkspaceInfo is the typed state of the optional workspace layer for status and doctor JSON.
// Additive: it appears only on the workspace provider's status.
type WorkspaceInfo struct {
	Connection Connection `json:"connection"`
	// Server says how the server was found: "environment" (this runs inside it) or "probe".
	Server      string `json:"server,omitempty"`
	APIRevision int    `json:"api_revision,omitempty"`
	// Supported is the range of revisions this build was checked against; Compatible says the
	// server's range overlaps it.
	SupportedMin int  `json:"supported_min"`
	SupportedMax int  `json:"supported_max"`
	Compatible   bool `json:"compatible"`
	// Live says the change feed is connected, so reads are pushed, not polled.
	Live bool `json:"live"`
	// InSession says this process runs inside a workspace session, which "work on this" needs.
	InSession bool `json:"in_session"`
}
