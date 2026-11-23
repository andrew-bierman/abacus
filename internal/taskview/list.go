package taskview

import (
	"sort"
	"strings"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/query"
)

// Preset is a quick filter, applied before the query.
type Preset string

const (
	PresetOpen    Preset = "open"    // everything not closed
	PresetReady   Preset = "ready"   // open, unblocked, not deferred
	PresetBlocked Preset = "blocked" // held by an unfinished dependency, or stored as blocked
	PresetClosed  Preset = "closed"
	PresetAll     Preset = "all"
)

// Presets is the cycle order.
var Presets = []Preset{PresetOpen, PresetReady, PresetBlocked, PresetClosed, PresetAll}

// Next is the following preset, wrapping.
func (p Preset) Next() Preset {
	for i, k := range Presets {
		if k == p {
			return Presets[(i+1)%len(Presets)]
		}
	}
	return PresetOpen
}

// Valid reports a known preset.
func (p Preset) Valid() bool {
	for _, k := range Presets {
		if k == p {
			return true
		}
	}
	return false
}

func (p Preset) keeps(r row) bool {
	switch p {
	case PresetOpen:
		return r.Status != "closed"
	case PresetReady:
		return r.Ready
	case PresetBlocked:
		return r.Blocked || r.Status == "blocked"
	case PresetClosed:
		return r.Status == "closed"
	}
	return true
}

// ListOptions say which tasks and in what order.
type ListOptions struct {
	Query  query.Query
	Preset Preset // empty means open
	Sort   Sort   // zero Key means the default
}

// List filters and sorts the snapshot: the preset, then the query, then the order.
func (v *View) List(o ListOptions) core.TaskListReport {
	if o.Preset == "" {
		o.Preset = PresetOpen
	}
	if o.Sort.Key == "" {
		o.Sort = DefaultSort()
	}
	rows := make([]row, 0, len(v.rows))
	for _, r := range v.rows {
		if o.Preset.keeps(r) && (o.Query.Empty() || o.Query.Match(r, v.now)) {
			rows = append(rows, r)
		}
	}
	sortRows(rows, o.Sort)
	rep := core.TaskListReport{
		Header: core.Header{Domain: core.DomainTasks, Title: "Task list", State: core.Connected, Source: core.SourceLive},
		Query:  o.Query.String(), Preset: string(o.Preset), Sort: string(o.Sort.Key), Descending: o.Sort.Desc,
		Rows: make([]core.TaskRow, len(rows)), Matched: len(rows), Total: len(v.rows), Ready: v.Ready(),
		Tracker: v.snap.Tracker, ReadAt: rfc(v.snap.ReadAt), Warnings: v.snap.Warnings,
	}
	for i, r := range rows {
		rep.Rows[i] = r.TaskRow
	}
	return rep
}

// Row is one task's row by id.
func (v *View) Row(id string) (core.TaskRow, bool) {
	i, ok := v.byID[id]
	if !ok {
		return core.TaskRow{}, false
	}
	return v.rows[i].TaskRow, true
}

// Detail is one task in full: its text, comments, what blocks it and what it blocks (all of
// them, finished ones included), its children and its parent.
func (v *View) Detail(id string) (core.TaskDetailReport, bool) {
	i, ok := v.byID[id]
	if !ok {
		return core.TaskDetailReport{}, false
	}
	it := v.snap.Issues[i]
	d := core.TaskDetailReport{
		Header: core.Header{Domain: core.DomainTasks, Title: "Task", State: core.Connected, Source: core.SourceLive},
		Row:    v.rows[i].TaskRow, Description: strings.TrimSpace(it.Description), Design: strings.TrimSpace(it.Design),
		Notes: strings.TrimSpace(it.Notes), Acceptance: strings.TrimSpace(it.Acceptance), CloseReason: it.CloseReason, ClosedAt: rfc(it.ClosedAt),
	}
	for _, c := range it.Comments {
		d.Comments = append(d.Comments, core.TaskCommentView{Author: c.Author, Text: strings.TrimSpace(c.Text), CreatedAt: rfc(c.CreatedAt), Age: Age(c.CreatedAt, v.now)})
	}
	if p, ok := v.parent[id]; ok {
		ref := v.ref(p)
		d.Parent = &ref
	}
	d.BlockedBy = v.refs(v.blkBy[id])
	d.Blocks = v.refs(v.blks[id])
	d.Children = v.refs(v.kids[id])
	return d, true
}

func (v *View) ref(id string) core.TaskRef {
	if i, ok := v.byID[id]; ok {
		it := v.snap.Issues[i]
		return core.TaskRef{ID: it.ID, Title: it.Title, Status: it.Status}
	}
	return core.TaskRef{ID: id, Title: "not in this tracker", Status: "missing"}
}

// refs names ids in id order, so a detail reads the same whatever order the export came in.
func (v *View) refs(ids []string) []core.TaskRef {
	var out []core.TaskRef
	for _, id := range ids {
		out = append(out, v.ref(id))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
