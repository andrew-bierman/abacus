package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// Live refresh. The Tasks tab asks the tracker a cheap question every few seconds (has its change
// fingerprint moved? it runs no process and a read never moves it) and reads the whole tracker only
// when it has. The schedule is taskview.Poller's: one check at a time, none while the tab is hidden,
// a longer wait after a slow or failed read. Which fingerprint is cheap and safe was measured, see
// internal/bd/testdata/README.md.
const (
	tasksPollEvery = 5 * time.Second  // between checks of an embedded tracker
	tasksPollMax   = 60 * time.Second // the longest wait after slow or failed reads
	tasksSlowRead  = 2 * time.Second  // a read slower than this doubles the wait
	// tasksSlowPollEvery is the wait for a tracker with no cheap fingerprint (a server-mode store):
	// the whole tracker is read every time, so it is read as seldom as before.
	tasksSlowPollEvery = 30 * time.Second
	tasksSlowPollMax   = 120 * time.Second
	// changedFor is how long the "changed" marker stays on the panel title.
	changedFor = 30 * time.Second
)

// fingerprintTimeout bounds one fingerprint read (a stat and a 150-byte read); a variable so a test
// can shorten it.
var fingerprintTimeout = 2 * time.Second

var keyFollow = key.NewBinding(key.WithKeys("F"), key.WithHelp("F", "follow change"))

// tasksPollMsg is the answer to a fingerprint check.
type tasksPollMsg struct {
	fp  string
	err error
}

// liveState is everything the live refresh remembers.
type liveState struct {
	fps  core.TaskFingerprinter // nil: no cheap fingerprint
	poll *taskview.Poller
	// fp is the fingerprint read just before the snapshot on screen was read: a write that lands
	// during that read still moves the next fingerprint away from it, so it is never missed.
	fp      string
	pending bool      // a check led to a full read that has not answered yet
	started time.Time // when that check began
	noFP    bool      // the tracker has no cheap fingerprint: slow full reads

	snap core.TaskSnapshot // the last full read, to see what a new one changed
	have bool
	// checkedAt is when a check found the tracker unchanged since the snapshot read at checkedFor;
	// a newer read ignores it, so it can only make a snapshot look fresher while it is the one on screen.
	checkedAt, checkedFor time.Time

	changed   map[string]bool // ids touched by the last change
	changedAt time.Time
	removed   int
	newest    string // the most recently changed task, where F goes
	// own: the user's own write just landed, so the snapshot that answers it shows their change, which
	// is not "changed by someone else". It holds until the last read of a chain started by the write.
	own bool
}

func newLiveState(fps core.TaskFingerprinter) liveState {
	return liveState{fps: fps, poll: taskview.NewPoller(tasksPollEvery, tasksPollMax, tasksSlowRead)}
}

// pollEvery is the current wait between checks: the staleness mark is relative to it.
func (s *tasksSection) pollEvery() time.Duration {
	if s.live.poll == nil {
		return tasksPollEvery
	}
	return s.live.poll.Wait()
}

// seenAt is when the tracker was last known to be what is on screen.
func (s *tasksSection) seenAt() time.Time {
	if s.live.checkedFor.Equal(s.list.readAt) && s.live.checkedAt.After(s.list.readAt) {
		return s.live.checkedAt
	}
	return s.list.readAt
}

// activate and deactivate tell the poller whether the tab is on screen. A hidden tab never checks;
// showing it again checks at once.
func (s *tasksSection) activate() tea.Cmd {
	s.live.poll.SetHidden(false)
	return s.livePoll()
}

func (s *tasksSection) deactivate() { s.live.poll.SetHidden(true) }

// livePoll starts a fingerprint check when one is due; never while a read is in flight.
func (s *tasksSection) livePoll() tea.Cmd {
	l := &s.live
	if s.src == nil || s.snapBusy || s.tasksBusy || l.pending {
		return nil
	}
	if !l.poll.Start(nowFn()) {
		return nil
	}
	l.started = nowFn()
	fps := l.fps
	return func() tea.Msg {
		if fps == nil {
			return tasksPollMsg{err: core.ErrNoFingerprint}
		}
		ctx, cancel := context.WithTimeout(context.Background(), fingerprintTimeout)
		defer cancel()
		fp, err := fps.Fingerprint(ctx)
		return tasksPollMsg{fp: fp, err: err}
	}
}

// livePolled acts on a check: read the tracker when it changed, otherwise note it is current.
func (s *tasksSection) livePolled(m tasksPollMsg) tea.Cmd {
	l := &s.live
	now := nowFn()
	switch {
	case errors.Is(m.err, core.ErrNoFingerprint):
		if !l.noFP { // no cheap check here: wait longer between full reads
			l.noFP = true
			l.poll.Base, l.poll.Max = tasksSlowPollEvery, tasksSlowPollMax
		}
		return s.liveRead(now)
	case m.err != nil: // this read failed (I/O, timeout): back off, and do not give up on cheap checks
		l.poll.Done(now, now.Sub(l.started), false)
		return nil
	}
	if l.noFP { // a fingerprint worked after all: back to the cheap rhythm
		l.noFP = false
		l.poll.Base, l.poll.Max = tasksPollEvery, tasksPollMax
	}
	if taskview.Changed(l.fp, m.fp) {
		return s.liveRead(now)
	}
	l.poll.Done(now, now.Sub(l.started), true)
	l.checkedAt, l.checkedFor = now, s.list.readAt
	return nil
}

// liveRead reads the whole tracker for a check; the check ends when the snapshot answers.
func (s *tasksSection) liveRead(now time.Time) tea.Cmd {
	s.live.pending = true
	cmd := s.fetchTasks()
	if !s.snapBusy { // the read was not started (a slow tracker is being left alone): the check ends here
		s.live.pending = false
		s.live.poll.Done(now, 0, false)
	}
	return cmd
}

// liveSnapshot runs when a snapshot read answers: it ends a pending check, remembers the fingerprint
// the read began under, and notes what changed since the last read.
func (s *tasksSection) liveSnapshot(m taskSnapshotMsg) {
	l := &s.live
	now := nowFn()
	if m.err != nil {
		if l.pending {
			l.pending = false
			l.poll.Done(now, now.Sub(l.started), false)
		}
		return
	}
	if m.fp != "" {
		l.fp = m.fp
	}
	s.rememberTracker(m.snap)
	own := l.own
	if l.own && !s.snapDirty { // another read follows while snapDirty: the flag stays for it
		l.own = false
	}
	if l.have && !own && m.snap.ReadAt.After(l.snap.ReadAt) { // a read that is not later than the last is not a later state
		if d := taskview.Diff(l.snap, m.snap); !d.Empty() {
			l.changed = map[string]bool{}
			for _, id := range d.Touched() {
				l.changed[id] = true
			}
			l.changedAt, l.removed = now, len(d.Removed)
			if d.Newest != "" {
				l.newest = d.Newest
			}
		}
	}
	l.snap, l.have = m.snap, true
	if l.pending {
		l.pending = false
		l.poll.Done(now, now.Sub(l.started), true)
		return
	}
	l.poll.Defer(now.Add(l.poll.Wait())) // a read the user asked for counts as a check
}

// markerActive is whether the "changed" marker is on: a change was seen within changedFor.
func (l *liveState) markerActive(now time.Time) bool {
	return !l.changedAt.IsZero() && now.Sub(l.changedAt) <= changedFor && now.Sub(l.changedAt) >= 0
}

// isChanged is whether the last change touched the task (and the marker is still on).
func (s *tasksSection) isChanged(id string) bool {
	return s.live.changed[id] && s.live.markerActive(nowFn())
}

// changedMarker is the text the panel title carries while the marker is on, "" otherwise.
func (s *tasksSection) changedMarker() string {
	l, now := &s.live, nowFn()
	if !l.markerActive(now) {
		return ""
	}
	n := len(l.changed) + l.removed
	when := "just now"
	if d := now.Sub(l.changedAt); d >= 10*time.Second {
		when = fmt.Sprintf("%ds ago", int(d/time.Second))
	}
	return fmt.Sprintf("● %d changed %s", n, when)
}

// follow moves the cursor to the most recently changed task.
func (s *tasksSection) follow() {
	id := s.live.newest
	if id == "" {
		s.notice = "nothing has changed since this was opened"
		return
	}
	for i, r := range s.rows() {
		if r.ID == id {
			s.cursor = i
			s.remember(s.ids())
			s.moved()
			s.notice = "following " + san(id)
			return
		}
	}
	if s.treeOn() && s.unfoldTo(id) { // it sits inside a folded parent: open the way to it
		s.follow()
		if s.notice != "" && !strings.HasPrefix(s.notice, "following") {
			return
		}
		s.notice = "following " + san(id) + " (unfolded its parents)"
		return
	}
	s.notice = san(id) + " changed but is not in this list: esc clears the query, f changes the quick filter"
}

// unfoldTo unfolds every folded ancestor of task id and reports whether it changed anything.
func (s *tasksSection) unfoldTo(id string) bool {
	l, changed := &s.list, false
	for n := 0; n < 1000 && l.view != nil; n++ {
		r, ok := l.view.Row(id)
		if !ok || r.Parent == "" {
			break
		}
		if l.tree.folded[r.Parent] {
			delete(l.tree.folded, r.Parent)
			changed = true
		}
		id = r.Parent
	}
	if changed {
		s.recompute()
	}
	return changed
}

// drain returns the commands queued by code that cannot return one (the recent-trackers write).
func (s *tasksSection) drain() tea.Cmd {
	if len(s.extra) == 0 {
		return nil
	}
	c := tea.Batch(s.extra...)
	s.extra = nil
	return c
}

// Update wraps the section's own update so that the live checks, the history read and queued
// commands are started after every message, and a fingerprint answer never reaches the rest.
func (s *tasksSection) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m, ok := msg.(tasksPollMsg); ok {
		return s, tea.Batch(s.livePolled(m), s.drain())
	}
	if m, ok := msg.(switchLoadedMsg); ok {
		return s, tea.Batch(s.switchLoaded(m), s.drain())
	}
	if cmd, stale := s.dropStale(msg); stale {
		return s, tea.Batch(cmd, s.drain())
	}
	mm, cmd := s.update(msg)
	return mm, tea.Batch(cmd, s.histEnsure(), s.drain())
}

// dropStale discards an answer started for a tracker the section has since switched away from, so old
// data is never painted under the new tracker's title and never compared with its snapshots. The
// bookkeeping a read leaves behind is still cleared: the snapshot read is over, and a read asked for
// while it was in flight (snapDirty) is started now.
func (s *tasksSection) dropStale(msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case taskSnapshotMsg:
		if m.gen == s.gen {
			return nil, false
		}
		s.snapBusy = false
		dirty := s.snapDirty
		s.snapDirty = false
		if dirty {
			return s.fetchSnapshot(), true
		}
		return nil, true
	case tasksMsg:
		return nil, m.gen != s.gen
	case insightsMsg:
		return nil, m.gen != s.gen
	case histMsg:
		return nil, m.gen != s.gen
	}
	return nil, false
}

// liveBindings are the keys of this unit, shown after the section's own.
func (s *tasksSection) liveBindings() []key.Binding {
	if s.list.cur != viewList {
		return []key.Binding{keySwitch}
	}
	var bs []key.Binding
	if s.live.newest != "" {
		bs = append(bs, keyFollow)
	}
	return append(bs, keyHistory, keyFilterSave, keySwitch)
}

// Bindings adds the live, history, saved-filter and switcher keys to the section's own, and gives
// an open picker its own keys.
func (s *tasksSection) Bindings() []key.Binding {
	if bs, ok := s.overlayBindings(); ok {
		return bs
	}
	bs := s.baseBindings()
	switch {
	case s.mode != modeList:
	case s.list.typing:
		if s.width() >= 80 { // a narrower footer must keep "q quit"
			bs = append(bs, keyRecall)
		}
	default:
		bs = append(bs, s.liveBindings()...)
	}
	return bs
}

// liveKey handles the keys of this unit; it runs before the section's own key handling.
func (s *tasksSection) liveKey(k tea.KeyMsg) (tea.Cmd, bool) {
	switch {
	case s.fil.mode != filterNone:
		return s.filterOverlayKey(k), true
	case s.sw.mode != switchNone:
		return s.switchOverlayKey(k), true
	case s.list.typing:
		return s.typingKey(k)
	}
	s.rec.on = false
	switch k.String() {
	case "P":
		return s.openSwitch(), true
	}
	if s.list.cur != viewList {
		return nil, false
	}
	switch k.String() {
	case "F":
		s.follow()
	case "H":
		return s.toggleHistory(), true
	case "ctrl+s":
		s.openSave()
	case "ctrl+o":
		s.openPicker()
	default:
		return nil, false
	}
	return nil, true
}
