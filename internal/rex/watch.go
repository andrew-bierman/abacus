package rex

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"
)

// The change feed: `rex events --json` is a stream of server events, one JSON object per line. The
// feed makes reads push-driven: while it is connected a read is reused until an event that matters
// drops it (see read and readBlocks), instead of being asked again every few seconds.
//
// It never starts a server (--autostart=false, and the allowlist has no other form), and its child
// process is killed with everything it started when ctx ends. A feed that cannot connect, or
// dies, is retried with back-off, and while it is down reads go back to polling.

// Variables so tests can shorten them.
var (
	watchBackoffMin = time.Second
	watchBackoffMax = time.Minute
	// watchBackoffIdleMax is the longest wait when the feed has never connected: no server is
	// running, so there is little to look for.
	watchBackoffIdleMax = 5 * time.Minute
	watchDebounce       = 300 * time.Millisecond
)

// relevantEvent reports whether an event line can change what a read returns: a session came or
// went, or one changed its name or its panes or tabs. Client connections (every rex command makes
// one, including this module's own reads) and block ownership are not: counting them would make
// every read cause the next. An event this build does not know is ignored; the cache ceiling
// bounds how stale that can be.
func relevantEvent(line []byte) bool {
	var e struct {
		Type    string `json:"type"`
		Payload struct {
			Event struct {
				Type string `json:"type"`
			} `json:"event"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &e) != nil {
		return false
	}
	switch e.Type {
	case "session_created", "session_destroyed":
		return true
	case "session_event":
		switch e.Payload.Event.Type {
		case "session_label_changed", "session_view_changed", "block_created", "block_closed", "window_created", "window_closed":
			return true
		}
	}
	return false
}

// Watch implements core.WorkspaceWatcher. It returns when ctx ends, with the child process gone.
func (m *Module) Watch(ctx context.Context, changed func()) {
	if m.Bin() == "" || !allowedStreamArgv(eventsArgv()) {
		return
	}
	kick := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() { // one caller at a time, and a burst of events is one call
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-kick:
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(watchDebounce):
			}
			select {
			case <-kick:
			default:
			}
			changed()
		}
	}()
	defer func() { <-done }()
	wait := watchBackoffMin
	var everConnected atomic.Bool
	for ctx.Err() == nil {
		sctx, cancel := context.WithCancel(ctx)
		settle := time.AfterFunc(m.o.EventSettle, func() { m.live.Store(true); everConnected.Store(true) })
		start := time.Now()
		_, _ = m.o.Stream(sctx, m.Bin(), eventsArgv(), func(line []byte) {
			if relevantEvent(line) {
				m.invalidate()
				select {
				case kick <- struct{}{}:
				default:
				}
			}
		})
		cancel()
		settle.Stop()
		if m.live.Swap(false) { // the feed was up: what was read since may be stale
			m.invalidate()
			select {
			case kick <- struct{}{}:
			default:
			}
		}
		if time.Since(start) > 10*time.Second { // it was healthy for a while: start the back-off over
			wait = watchBackoffMin
		}
		limit := watchBackoffMax
		if !everConnected.Load() {
			limit = watchBackoffIdleMax
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, limit)
	}
}
