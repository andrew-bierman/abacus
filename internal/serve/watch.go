package serve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// subscribe is subscribe and unsubscribe: the topics to start or stop watching. A notification says that
// something changed and at most which ids, never the data, so a missed one costs one refetch. At most one watcher per topic
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
			return nil, usageErr("topics is a list of: %s", strings.Join(topics, ", "))
		}
		for _, t := range p.Topics {
			if !slices.Contains(topics, t) {
				return nil, usageErr("unknown topic %q (%s)", core.Sanitize(t), strings.Join(topics, ", "))
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
				go e.guard("tasks", func() { e.watchTasks(wctx, pr) })
			case t == "workspace" && pr.Watcher != nil:
				w := pr.Watcher
				go e.guard("workspace", func() {
					w.Watch(wctx, func() { e.notify(core.ServeNotice{Topic: "workspace", Reason: "events"}) })
				})
			case t == "agents":
				go e.guard("agents", func() { e.watchAgents(wctx) })
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
// When the tracker can be read whole, the notification also names the tasks that were added, changed or
// removed (the tracker is read once per change, as the TUI's refresh does, and compared with the last
// read); when it cannot, or a read fails, the notification says "full" and the client refetches
// everything. A tracker with no cheap fingerprint is read in full on a slow rhythm and compared by task.
func (e *Engine) watchTasks(ctx context.Context, pr core.Providers) {
	poll := taskview.NewPoller(e.cfg.PollBase, e.cfg.PollMax, e.cfg.PollSlow)
	var last string             // the fingerprint the client has been told about
	var snap *core.TaskSnapshot // the tracker as the client was last told about it
	fails, degraded := 0, false
	noFP := pr.Fingerprint == nil
	read := func() *core.TaskSnapshot {
		if pr.Source == nil {
			return nil
		}
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		cur, err := pr.Source.Snapshot(rctx)
		if err != nil {
			return nil
		}
		return &cur
	}
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
			case fp == "": // nothing readable this time: not a change, not a failure
			case last == "": // the first look is the baseline, not a change
				last, snap = fp, read()
			case taskview.Changed(last, fp):
				n := core.ServeNotice{Topic: "tasks", Reason: "fingerprint", Kind: "full", Fingerprint: fp}
				cur := read()
				if cur != nil && snap != nil {
					if d := taskview.Diff(*snap, *cur); !d.Empty() {
						n.Kind, n.ChangedIDs = "ids", changedIDs(d)
					}
				}
				if e.notify(n) {
					last, snap = fp, cur
				}
			}
		}
		if noFP && pr.Source != nil {
			cur := read()
			switch {
			case cur == nil:
				ok = false
			case snap == nil:
				snap = cur
			default:
				if d := taskview.Diff(*snap, *cur); !d.Empty() {
					if e.notify(core.ServeNotice{Topic: "tasks", Reason: "snapshot", Kind: "ids", Fingerprint: snapshotFingerprint(*cur), ChangedIDs: changedIDs(d)}) {
						snap = cur
					}
				}
			}
		}
		poll.Done(time.Now(), time.Since(start), ok)
		if ok {
			fails, degraded = 0, false
		} else if fails++; fails >= 3 && !degraded {
			degraded = true
			e.notify(core.ServeNotice{Topic: "engine", Reason: "the tracker could not be checked"})
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// changedIDs is every task a change touched: added, changed or removed, sorted.
func changedIDs(d taskview.Change) []string {
	ids := append(d.Touched(), d.Removed...)
	slices.Sort(ids)
	return ids
}

// snapshotFingerprint is an opaque token of a whole snapshot, for a tracker with no cheap fingerprint.
func snapshotFingerprint(s core.TaskSnapshot) string {
	lines := make([]string, len(s.Issues))
	for i, is := range s.Issues {
		lines[i] = is.ID + "\x00" + taskview.IssueHash(is)
	}
	return digestLines(lines)
}

func digestLines(lines []string) string {
	slices.Sort(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:16])
}

// watchAgents polls the agents on the slow rhythm (there is no cheap change signal for them) and notifies
// when an agent appears, goes, or changes state, task, branch or model. Volatile figures (tokens, last
// activity) are left out of the comparison, so a busy agent is not a change at every look. The first look
// is the baseline.
func (e *Engine) watchAgents(ctx context.Context) {
	var last map[string]string
	for {
		timer := time.NewTimer(e.cfg.SlowBase)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		rep := core.ReadDomain(rctx, e.entries(rctx), core.DomainAgents).Agents
		cancel()
		if ctx.Err() != nil {
			return
		}
		cur := make(map[string]string, len(rep.Agents))
		lines := make([]string, 0, len(rep.Agents))
		for _, a := range rep.Agents {
			v := strings.Join([]string{a.Harness, string(a.State), a.Task, a.Repo, a.Branch, a.Model, a.SessionID}, "\x00")
			cur[a.ID] = v
			lines = append(lines, a.ID+"\x00"+v)
		}
		if last == nil {
			last = cur
			continue
		}
		var ids []string
		for id, v := range cur {
			if w, had := last[id]; !had || w != v {
				ids = append(ids, id)
			}
		}
		for id := range last {
			if _, still := cur[id]; !still {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			continue
		}
		slices.Sort(ids)
		if e.notify(core.ServeNotice{Topic: "agents", Reason: "poll", Kind: "ids", Fingerprint: digestLines(lines), ChangedIDs: ids}) {
			last = cur
		}
	}
}

// guard runs a watcher so that a provider's panic ends that watcher, not the process (which may be
// in the middle of a write): the client is told with engine.degraded and may subscribe again.
func (e *Engine) guard(topic string, run func()) {
	defer func() {
		if r := recover(); r != nil {
			e.wmu.Lock()
			if cancel := e.subs[topic]; cancel != nil {
				cancel()
				delete(e.subs, topic)
			}
			e.wmu.Unlock()
			e.notify(core.ServeNotice{Topic: "engine", Reason: "the " + topic + " watcher stopped after a provider failed; subscribe again"})
		}
	}()
	run()
}
