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
// forgotten first): a client that names an older fingerprint gets a full set of rows.
const maxSinceStates = 8

type sinceStore struct {
	mu    sync.Mutex
	order []string
	by    map[string]taskview.RowState
}

func (s *sinceStore) get(fp string) (taskview.RowState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.by[fp]
	return st, ok
}

func (s *sinceStore) put(fp string, st taskview.RowState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.by[fp]; ok {
		return
	}
	if s.by == nil {
		s.by = map[string]taskview.RowState{}
	}
	s.by[fp] = st
	s.order = append(s.order, fp)
	for len(s.order) > maxSinceStates {
		delete(s.by, s.order[0])
		s.order = s.order[1:]
	}
}

// tasksSince answers which task rows changed since a fingerprint this server handed out. The tracker is read
// whole, as the watcher reads it, and compared with the state kept under that fingerprint; a fingerprint that is
// empty, unknown (another server, a restart) or forgotten, and a tracker with more than maxWatchTasks tasks (too
// many to keep and compare by task, the watcher's own bound), answer "full" with all rows from the same view.
func (e *Engine) tasksSince(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	snap, _, err := e.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	rep := core.TasksSince{Kind: core.SinceFull, Total: len(snap.Issues), Tracker: snap.Tracker}
	if !snap.ReadAt.IsZero() {
		rep.ReadAt = snap.ReadAt.UTC().Format(time.RFC3339)
	}
	v := taskview.Build(snap, e.cfg.Now())
	now := v.State()
	rep.Fingerprint = now.Fingerprint()
	var prev taskview.RowState
	if len(snap.Issues) <= maxWatchTasks {
		var known bool
		prev, known = e.since.get(p.Fingerprint)
		if known {
			rep.Kind = core.SinceDelta
		}
		e.since.put(rep.Fingerprint, now)
	}
	rep.Rows, rep.Removed = v.Since(prev, now)
	return rep, nil
}
