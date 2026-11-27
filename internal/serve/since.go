package serve

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// maxSinceStates is how many states tasks.since remembers (one per fingerprint it handed out, the oldest
// forgotten first): a client that names an older fingerprint is told "full" and refetches.
const maxSinceStates = 8

// sinceEntry is one remembered state, with what an "unchanged" answer needs without reading the tracker.
type sinceEntry struct {
	st      taskview.RowState
	cheap   string // the tracker's own change fingerprint when the state was read ("" when it has none or moved while reading)
	total   int
	tracker string
	readAt  string
}

type sinceStore struct {
	mu    sync.Mutex
	order []string
	by    map[string]sinceEntry
}

func (s *sinceStore) get(fp string) (sinceEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.by[fp]
	return e, ok
}

func (s *sinceStore) put(fp string, e sinceEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.by[fp]; ok {
		return
	}
	if s.by == nil {
		s.by = map[string]sinceEntry{}
	}
	s.by[fp] = e
	s.order = append(s.order, fp)
	for len(s.order) > maxSinceStates {
		delete(s.by, s.order[0])
		s.order = s.order[1:]
	}
}

// cheapFingerprint is the tracker's own change check, "" when it has none or it could not be read now.
func cheapFingerprint(ctx context.Context, pr core.Providers, timeout time.Duration) string {
	if pr.Fingerprint == nil {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	fp, err := pr.Fingerprint.Fingerprint(cctx)
	if err != nil {
		return ""
	}
	return fp
}

// tasksSince answers which task rows changed since a fingerprint this server handed out. When the tracker's
// own cheap fingerprint says nothing was written since that state was read, the answer is "unchanged" without
// reading the tracker at all. Otherwise the tracker is read whole (under the request's deadline, as tasks.list
// reads it) and compared with the state kept under that fingerprint. The answer is "full", with every row from
// the same snapshot, when the fingerprint is empty, unknown or forgotten, when it came from another tracker,
// when the snapshot holds a task id twice (a by-id comparison would hide a change), and for a tracker with more
// than maxWatchTasks tasks (too many to keep and compare by task, the watcher's own bound; the size is only
// known once it is read). A full answer from an over-cap or duplicate-id snapshot is not kept.
func (e *Engine) tasksSince(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	pr := core.ResolveProviders(e.entries(ctx))
	if pr.Source == nil {
		return nil, failErr(core.CodeNotConnected, "tasks: not connected. %s", core.HowToConnectFor(core.DomainTasks))
	}
	cheap := cheapFingerprint(ctx, pr, e.cfg.PollSlow)
	prev, known := e.since.get(p.Fingerprint)
	if known && cheap != "" && prev.cheap == cheap {
		return core.TasksSince{Kind: core.SinceDelta, Fingerprint: p.Fingerprint, Rows: []core.TaskRow{}, Removed: []string{}, Total: prev.total, Tracker: prev.tracker, ReadAt: prev.readAt}, nil
	}
	snap, err := pr.Source.Snapshot(ctx)
	if err != nil {
		return nil, fromErr(err)
	}
	if cheap != "" && cheapFingerprint(ctx, pr, e.cfg.PollSlow) != cheap {
		cheap = "" // it moved while it was read: the state cannot be tied to that fingerprint
	}
	rep := core.TasksSince{Kind: core.SinceFull, Total: len(snap.Issues), Tracker: snap.Tracker}
	if !snap.ReadAt.IsZero() {
		rep.ReadAt = snap.ReadAt.UTC().Format(time.RFC3339)
	}
	v := taskview.Build(snap, e.cfg.Now())
	now := v.State()
	rep.Fingerprint = now.Fingerprint()
	var base taskview.RowState // empty: every row is new
	if len(snap.Issues) <= maxWatchTasks && !now.Duplicates() {
		if known && prev.st.Ident() == now.Ident() {
			rep.Kind, base = core.SinceDelta, prev.st
		}
		e.since.put(rep.Fingerprint, sinceEntry{now, cheap, rep.Total, rep.Tracker, rep.ReadAt})
	}
	rep.Rows, rep.Removed = v.Since(base, now)
	return rep, nil
}
