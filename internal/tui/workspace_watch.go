package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
)

// wsWatch runs the workspace provider's change feed for the life of the shell and turns each change
// into a message. A nil *wsWatch is no feed (every method does nothing), which is how a session
// without one, and every test, behaves. The provider owns the child process and its back-off; this
// only starts it once and, on Close, stops it and waits for it to be gone.
type wsWatch struct {
	w       core.WorkspaceWatcher
	ch      chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	started bool
}

func newWsWatch(w core.WorkspaceWatcher) *wsWatch {
	return &wsWatch{w: w, ch: make(chan struct{}, 1)}
}

// start launches the feed once and returns the command that waits for its first change.
func (x *wsWatch) start() tea.Cmd {
	if x == nil || x.started {
		return nil
	}
	x.started = true
	x.ctx, x.cancel = context.WithCancel(context.Background())
	x.done = make(chan struct{})
	go func() {
		defer close(x.done)
		x.w.Watch(x.ctx, func() {
			select {
			case x.ch <- struct{}{}:
			default: // one is already waiting to be read
			}
		})
	}()
	return x.wait()
}

// wait blocks until the next change (or Close) and reports it as a wsChangedMsg.
func (x *wsWatch) wait() tea.Cmd {
	if x == nil || !x.started {
		return nil
	}
	ch, ctx := x.ch, x.ctx
	return func() tea.Msg {
		select {
		case <-ch:
			return wsChangedMsg{}
		case <-ctx.Done():
			return nil
		}
	}
}

// stop ends the feed and waits (a few seconds at most) for it to finish.
func (x *wsWatch) stop() {
	if x == nil || x.cancel == nil {
		return
	}
	x.cancel()
	select {
	case <-x.done:
	case <-time.After(3 * time.Second):
	}
	x.cancel = nil
}
