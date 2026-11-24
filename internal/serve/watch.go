package serve

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// subscribe is subscribe and unsubscribe: the topics to start or stop watching. A notification says that
// something changed and never what, so a missed one costs one refetch. At most one watcher per topic
// runs, whatever the client asks, and each stops with the server.
func (e *Engine) subscribe(on bool) handler {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			Topics []string `json:"topics"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		if len(p.Topics) == 0 || len(p.Topics) > len(topics) {
			return nil, usageErr("topics is a list of: tasks, workspace")
		}
		for _, t := range p.Topics {
			if !slices.Contains(topics, t) {
				return nil, usageErr("unknown topic %q (tasks, workspace)", core.Sanitize(t))
			}
		}
		out := core.ServeSubscribed{Active: []string{}}
		e.wmu.Lock()
		defer e.wmu.Unlock()
		for _, t := range p.Topics {
			if !on {
				if cancel := e.subs[t]; cancel != nil {
					cancel()
					delete(e.subs, t)
				}
				continue
			}
			if e.subs[t] != nil {
				continue
			}
			pr := core.ResolveProviders(e.entries(ctx))
			wctx, cancel := context.WithCancel(e.root)
			switch {
			case t == "tasks" && (pr.Fingerprint != nil || pr.Source != nil):
				go e.watchTasks(wctx, pr)
			case t == "workspace" && pr.Watcher != nil:
				w := pr.Watcher
				go w.Watch(wctx, func() { e.notify("workspace", "events") })
			default:
				cancel()
				if out.Unavailable == nil {
					out.Unavailable = map[string]string{}
				}
				out.Unavailable[t] = "no provider for this topic is connected"
				continue
			}
			e.subs[t] = cancel
		}
		for _, t := range topics {
			if e.subs[t] != nil {
				out.Active = append(out.Active, t)
			}
		}
		return out, nil
	}
}

// watchTasks is the TUI's live refresh without the screen: a cheap fingerprint check on the poller's
// rhythm (one at a time, a longer wait after a slow or failed check), and a notification when it moved.
// A tracker with no cheap fingerprint is read in full on a slow rhythm and compared by task.
func (e *Engine) watchTasks(ctx context.Context, pr core.Providers) {
	poll := taskview.NewPoller(e.cfg.PollBase, e.cfg.PollMax, e.cfg.PollSlow)
	var last string             // the fingerprint the client has been told about
	var snap *core.TaskSnapshot // the snapshot, for a tracker with no fingerprint
	fails, degraded := 0, false
	noFP := pr.Fingerprint == nil
	for {
		timer := time.NewTimer(poll.Wait())
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		start := time.Now()
		if !poll.Start(start) {
			continue
		}
		ok := true
		if !noFP {
			cctx, cancel := context.WithTimeout(ctx, e.cfg.PollSlow)
			fp, err := pr.Fingerprint.Fingerprint(cctx)
			cancel()
			switch {
			case errors.Is(err, core.ErrNoFingerprint):
				noFP = true
				poll.Base, poll.Max = e.cfg.SlowBase, 2*e.cfg.SlowBase
			case err != nil:
				ok = false
			case last == "": // the first look is the baseline, not a change
				last = fp
			case taskview.Changed(last, fp):
				if e.notify("tasks", "fingerprint") {
					last = fp
				}
			}
		}
		if noFP && pr.Source != nil {
			rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			cur, err := pr.Source.Snapshot(rctx)
			cancel()
			switch {
			case err != nil:
				ok = false
			case snap == nil:
				snap = &cur
			case !taskview.Diff(*snap, cur).Empty():
				if e.notify("tasks", "snapshot") {
					snap = &cur
				}
			}
		}
		poll.Done(time.Now(), time.Since(start), ok)
		if ok {
			fails, degraded = 0, false
		} else if fails++; fails >= 3 && !degraded {
			degraded = true
			e.notify("engine", "the tracker could not be checked")
		}
		if ctx.Err() != nil {
			return
		}
	}
}
