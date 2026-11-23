package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
	"unicode/utf8"
)

// The workspace is an optional capability, not a fifth domain: a terminal workspace server
// (today Rex) may know where an item of the fleet lives as a terminal session or tab. Core
// defines the typed placements and an open plan; an adapter fills them in; a front-end shows
// them and runs a plan only on an explicit keypress. Nothing here runs a command.

// WorkspaceKind says what sort of item a placement is for. The known kinds are below; a newer
// provider may report one this build has not heard of, which passes through unchanged (JSON
// stays a plain string).
type WorkspaceKind string

const (
	WorkspaceRun   WorkspaceKind = "run"
	WorkspaceLease WorkspaceKind = "lease"
	WorkspaceTask  WorkspaceKind = "task"
	WorkspaceQueue WorkspaceKind = "queue"
)

// Known reports whether the kind is one this build recognizes.
func (k WorkspaceKind) Known() bool {
	switch k {
	case WorkspaceRun, WorkspaceLease, WorkspaceTask, WorkspaceQueue:
		return true
	}
	return false
}

// WorkspaceItem names one row a placement can belong to: a typed kind plus the id the row
// carries (a run's issue, a lease's resource, a task or queue id). Host is the machine the row
// already carries (a run's machine), never one guessed by core.
type WorkspaceItem struct {
	Kind WorkspaceKind `json:"kind"`
	ID   string        `json:"id"`
	Host string        `json:"host,omitempty"`
}

// PlacementMatch is how a placement was found. A provider may pass through an unknown value.
type PlacementMatch string

const (
	// MatchExact means the session or tab label is exactly the naming convention for the item.
	MatchExact PlacementMatch = "exact"
	// MatchLabel means the item's id appears in a label, as a whole word.
	MatchLabel PlacementMatch = "label"
	// MatchHostNotConfigured means the item runs on a machine abacus was not told it may contact,
	// so its sessions were not read: there is no session, only the command to open that machine.
	MatchHostNotConfigured PlacementMatch = "host-not-configured"
)

// Placement is where an item lives as a terminal: on which host, in which session and tab. The
// text fields come from other programs (anyone can name a session) and are sanitized and
// bounded by core before a front-end sees them.
type Placement struct {
	// ID is stable for the same item in the same place, so a front-end can remember a choice.
	ID      string         `json:"id"`
	Item    WorkspaceItem  `json:"item"`
	Host    string         `json:"host,omitempty"`
	Session string         `json:"session,omitempty"`
	Tab     string         `json:"tab,omitempty"`
	Label   string         `json:"label,omitempty"`
	Match   PlacementMatch `json:"match,omitempty"`
}

// Ambiguity is an item that more than one place claims. Core never picks one: the front-end
// shows the candidates and the user decides.
type Ambiguity struct {
	Item       WorkspaceItem `json:"item"`
	Candidates []Placement   `json:"candidates"`
	// Truncated says there were more claimants than Candidates lists.
	Truncated bool `json:"truncated,omitempty"`
}

// WorkspaceReport is what a provider found. State says whether a workspace was reachable; a
// workspace that is simply not running is NotConnected with how to start it, never an error.
type WorkspaceReport struct {
	State        Connection  `json:"state,omitempty"`
	Reason       string      `json:"reason,omitempty"`
	HowToConnect string      `json:"how_to_connect,omitempty"`
	Placements   []Placement `json:"placements,omitempty"`
	Ambiguous    []Ambiguity `json:"ambiguous,omitempty"`
	// Stale says some of this came from a recent earlier read (a short per-process cache), not a
	// fresh one. Truncated says something was cut: more sessions than are looked at, more
	// placements than are kept, or a placement dropped because its machine name was not safe.
	Stale     bool `json:"stale,omitempty"`
	Truncated bool `json:"truncated,omitempty"`
}

// Empty reports whether the report has nothing worth putting in JSON: no placements, nothing
// ambiguous and no degraded read. A provider that is merely not connected is empty.
func (r WorkspaceReport) Empty() bool {
	return len(r.Placements) == 0 && len(r.Ambiguous) == 0 && r.State != Degraded
}

// For returns the placement of an item, or the candidates when it is ambiguous. Neither is set
// for an item the workspace knows nothing about.
func (r WorkspaceReport) For(it WorkspaceItem) (p *Placement, candidates []Placement) {
	for i := range r.Placements {
		if r.Placements[i].Item == it {
			return &r.Placements[i], nil
		}
	}
	for _, a := range r.Ambiguous {
		if a.Item == it {
			return nil, a.Candidates
		}
	}
	return nil, nil
}

// OpenRung is one way to open a placement: an argv and a stable name, so a front-end can say
// which rung worked and a later rung is a new value, not a new type.
type OpenRung struct {
	Name string   `json:"name"`
	Argv []string `json:"argv"`
	// Interactive says the command takes over the terminal (a front-end must suspend its own UI
	// to run it) rather than running quietly in the background.
	Interactive bool `json:"interactive"`
}

// OpenPlan is how to get to a placement. Rungs are tried in order, only on an explicit keypress
// and never by core; if none works (or the front-end cannot run one) Copy is the command to
// show for the user to paste. Note says why there is no rung, when there is none.
type OpenPlan struct {
	Rungs []OpenRung `json:"rungs"`
	Copy  string     `json:"copy,omitempty"`
	Note  string     `json:"note,omitempty"`
}

// WorkspaceProvider is the optional capability a module implements. Workspace reads where the
// given items live (an unknown or unplaced item is simply absent from the result) and Open
// builds the plan for one placement without running anything.
type WorkspaceProvider interface {
	Workspace(ctx context.Context, items []WorkspaceItem) (WorkspaceReport, error)
	Open(p Placement) OpenPlan
}

// Bounds on what a provider may hand a front-end.
const (
	maxWorkspaceItems = 200
	maxPlacements     = 500
	maxCandidates     = 8
	workspaceReadTime = 5 * time.Second
	// MaxLabelRunes bounds a session or tab label; MaxIDRunes bounds an id or host.
	MaxLabelRunes = 64
	MaxIDRunes    = 128
	// MaxHostRunes is the longest machine name; a longer one is rejected, never clipped, because
	// clipping can turn one host into another.
	MaxHostRunes = 253
)

// ClipRunes cuts s to at most n runes and never splits one (a hostile ten-thousand-character
// label becomes a short one). Invalid UTF-8 is replaced first.
func ClipRunes(s string, n int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

// PlacementID derives the stable id of an item in a place.
func PlacementID(it WorkspaceItem, host, session string) string {
	h := sha256.Sum256([]byte(string(it.Kind) + "\x00" + it.ID + "\x00" + host + "\x00" + session))
	return "pl-" + hex.EncodeToString(h[:6])
}

func cleanItem(it WorkspaceItem) WorkspaceItem {
	return WorkspaceItem{Kind: WorkspaceKind(ClipRunes(Sanitize(string(it.Kind)), MaxIDRunes)),
		ID: ClipRunes(Sanitize(it.ID), MaxIDRunes), Host: ClipRunes(Sanitize(it.Host), MaxIDRunes)}
}

func cleanPlacement(p Placement) Placement {
	return Placement{
		ID:      ClipRunes(Sanitize(p.ID), MaxIDRunes),
		Item:    cleanItem(p.Item),
		Host:    ClipRunes(Sanitize(p.Host), MaxIDRunes),
		Session: ClipRunes(Sanitize(p.Session), MaxIDRunes),
		Tab:     ClipRunes(Sanitize(p.Tab), MaxLabelRunes),
		Label:   ClipRunes(Sanitize(p.Label), MaxLabelRunes),
		Match:   PlacementMatch(ClipRunes(Sanitize(string(p.Match)), MaxIDRunes)),
	}
}

// cleanWorkspace makes a provider's report safe and bounded. Unknown kinds and match values are
// kept (only sanitized), never dropped or rewritten.
func cleanWorkspace(r WorkspaceReport) WorkspaceReport {
	out := WorkspaceReport{State: r.State, Reason: Sanitize(r.Reason), HowToConnect: Sanitize(r.HowToConnect)}
	for _, p := range r.Placements {
		if len(out.Placements) >= maxPlacements {
			break
		}
		out.Placements = append(out.Placements, cleanPlacement(p))
	}
	for _, a := range r.Ambiguous {
		if len(out.Ambiguous) >= maxPlacements {
			break
		}
		c := Ambiguity{Item: cleanItem(a.Item)}
		for _, p := range a.Candidates {
			if len(c.Candidates) >= maxCandidates {
				break
			}
			c.Candidates = append(c.Candidates, cleanPlacement(p))
		}
		out.Ambiguous = append(out.Ambiguous, c)
	}
	return out
}

// ReadWorkspace asks the first ready workspace provider where the items live. With no provider
// the result is empty and there is no error; a provider that fails is a Degraded report with
// the reason, never an error, so the cockpit never breaks over an optional layer.
func ReadWorkspace(ctx context.Context, entries []Entry, items []WorkspaceItem) WorkspaceReport {
	p, _, ok := pick[WorkspaceProvider](entries)
	if !ok {
		return WorkspaceReport{}
	}
	return ReadWorkspaceFrom(ctx, p, items)
}

// ReadWorkspaceFrom is ReadWorkspace for a provider already chosen (a nil one reads as empty).
func ReadWorkspaceFrom(ctx context.Context, p WorkspaceProvider, items []WorkspaceItem) WorkspaceReport {
	if p == nil || len(items) == 0 {
		return WorkspaceReport{}
	}
	if len(items) > maxWorkspaceItems {
		items = items[:maxWorkspaceItems]
	}
	ctx, cancel := context.WithTimeout(ctx, workspaceReadTime)
	defer cancel()
	rep, err := p.Workspace(ctx, items)
	if err != nil {
		return WorkspaceReport{State: Degraded, Reason: ClipRunes(Sanitize(err.Error()), 200)}
	}
	return cleanWorkspace(rep)
}

// WorkspaceItems lists the rows of the domains a placement can be asked for: runs (with the
// machine each carries), leases, recent tasks and queued items. Duplicates are dropped.
func WorkspaceItems(d DomainsReport) []WorkspaceItem {
	var items []WorkspaceItem
	seen := map[WorkspaceItem]bool{}
	add := func(k WorkspaceKind, id, host string) {
		it := WorkspaceItem{Kind: k, ID: id, Host: host}
		if id == "" || seen[it] || len(items) >= maxWorkspaceItems {
			return
		}
		seen[it] = true
		items = append(items, it)
	}
	for _, r := range d.Queues.Runs {
		add(WorkspaceRun, r.Issue, r.Machine)
	}
	for _, q := range d.Queues.Queue {
		add(WorkspaceQueue, q.ID, "")
	}
	for _, l := range d.Leases.Leases {
		add(WorkspaceLease, l.Resource, "")
	}
	for _, t := range d.Tasks.Recent {
		add(WorkspaceTask, t.ID, "")
	}
	return items
}

func (r *DomainsReport) readWorkspace(ctx context.Context, entries []Entry) {
	if rep := ReadWorkspace(ctx, entries, WorkspaceItems(*r)); !rep.Empty() {
		r.Workspace = &rep
	}
}

// ShellJoin renders an argv as one command a user can paste into a POSIX shell: words made only
// of safe characters stay bare, anything else is single-quoted.
func ShellJoin(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = shellWord(a)
	}
	return strings.Join(parts, " ")
}

func shellWord(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("@%+=:,./-_", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
