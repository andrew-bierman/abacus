package taskview

import (
	"strings"

	"github.com/andrew-bierman/abacus/internal/core"
)

// SearchDocs are every task of the snapshot (closed ones too) as global-search documents: the
// row's stored and derived fields, and a body of the description, design, notes, acceptance and
// comment text. The derived fields (blocked, ready) are KTD4's, so search agrees with the list.
func (v *View) SearchDocs() []core.SearchDoc {
	out := make([]core.SearchDoc, 0, len(v.rows))
	for i, r := range v.rows {
		it := v.snap.Issues[i]
		var body strings.Builder
		for _, s := range []string{it.Description, it.Design, it.Notes, it.Acceptance} {
			if s = strings.TrimSpace(s); s != "" {
				body.WriteString(s)
				body.WriteByte('\n')
			}
		}
		for _, c := range it.Comments {
			body.WriteString(c.Text)
			body.WriteByte('\n')
		}
		var has []string
		for _, h := range []struct {
			ok   bool
			name string
		}{{r.Comments > 0, "comments"}, {r.hasDesc, "description"}, {len(r.Labels) > 0, "labels"}, {r.Parent != "", "parent"}, {r.ChildTotal > 0, "children"}, {r.Assignee != "", "assignee"}} {
			if h.ok {
				has = append(has, h.name)
			}
		}
		stale := r.Status != "closed" && !r.updated.IsZero() && v.now.Sub(r.updated) > core.TaskStaleAfter
		meta := []string{strings.ReplaceAll(r.Status, "_", " "), "P" + string(rune('0'+min(max(r.Priority, 0), 9)))}
		if r.Assignee != "" {
			meta = append(meta, r.Assignee)
		}
		out = append(out, core.SearchDoc{
			Domain: core.DomainTasks, Kind: core.KindTask, ID: r.ID, Ref: r.ID, Title: r.Title, Body: body.String(),
			Meta: strings.Join(meta, " · "), Status: r.Status, Who: r.Assignee, Type: r.Type, Parent: r.Parent, Labels: r.Labels,
			Priority: r.Priority, HasPriority: true, Blocked: r.Blocked, Ready: r.Ready, HasDerived: !v.snap.DepsUnknown,
			Stale: stale, Has: has, Updated: r.updated, Created: r.created,
		})
	}
	return out
}
