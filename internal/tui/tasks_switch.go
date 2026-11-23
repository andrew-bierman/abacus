package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
)

var keySwitch = key.NewBinding(key.WithKeys("P"), key.WithHelp("P", "switch tracker"))

type switchMode int

const (
	switchNone    switchMode = iota
	switchPick               // choosing a tracker
	switchConfirm            // the gated confirm screen
	switchBusy               // the switch is being applied
	switchLoading            // the disk is being read (off the UI goroutine)
)

// switchUI is the tracker switcher: a picker over the trackers the user opened or configured, then the
// confirm screen every setup-style action has.
type switchUI struct {
	sw      *core.Switcher // nil: this session cannot switch
	mode    switchMode
	cands   []core.RecentTracker
	current string
	cur     int
	plan    core.SwitchPlan
	touched string // the tracker last remembered, so each is written once
	seq     int    // the switcher request that is waited for
}

// trackerSwitchedMsg is the answer to applying a switch.
type trackerSwitchedMsg struct {
	dir string
	err error
}

// switchDeadline bounds the disk work of the switcher (finding the known trackers, checking one): a
// dead network mount must not freeze the TUI.
var switchDeadline = 2 * time.Second

// switchInline makes the switcher's disk work run in line: tests that press keys without running the
// commands they return set it.
var switchInline = false

// switchLoadedMsg is the answer to the switcher's disk work.
type switchLoadedMsg struct {
	seq     int
	cands   []core.RecentTracker
	current string
	plan    *core.SwitchPlan
	note    string // why there is nothing to show
}

// switchWork runs work off the UI goroutine under switchDeadline and opens the loading state, which
// owns the keys (esc cancels). The answer is dropped if the state moved on.
func (s *tasksSection) switchWork(work func() switchLoadedMsg) tea.Cmd {
	w := &s.sw
	w.mode = switchLoading
	w.seq++
	seq := w.seq
	run := func() tea.Msg {
		ch := make(chan switchLoadedMsg, 1)
		go func() { ch <- work() }()
		select {
		case m := <-ch:
			m.seq = seq
			return m
		case <-time.After(switchDeadline):
			return switchLoadedMsg{seq: seq, note: "reading the tracker folders took too long: is a network drive not answering?"}
		}
	}
	if switchInline {
		return s.switchLoaded(run().(switchLoadedMsg))
	}
	return run
}

// switchLoaded takes the answer of switchWork: the picker, the confirm screen, or a note.
func (s *tasksSection) switchLoaded(m switchLoadedMsg) tea.Cmd {
	w := &s.sw
	if m.seq != w.seq || w.mode != switchLoading { // cancelled, or another request was made since
		return nil
	}
	switch {
	case m.note != "":
		w.mode, s.notice = switchNone, m.note
	case m.plan != nil:
		w.cands, w.current, w.plan, w.mode = m.cands, m.current, *m.plan, switchConfirm
	case len(m.cands) < 2:
		w.mode, s.notice = switchNone, "no other tracker to switch to: trackers you open are remembered here"
	default:
		w.cands, w.current, w.cur, w.mode = m.cands, m.current, 0, switchPick
		for i, c := range w.cands { // start on the first tracker that is not the one in use
			if c.Dir != w.current {
				w.cur = i
				break
			}
		}
	}
	return nil
}

// openSwitch lists the trackers abacus knows: the one in use, the remembered ones and the configured one.
func (s *tasksSection) openSwitch() tea.Cmd {
	if s.sw.sw == nil {
		s.notice = "switching tracker is not available here"
		return nil
	}
	sw := s.sw.sw
	return s.switchWork(func() switchLoadedMsg {
		c, cur := sw.Candidates()
		return switchLoadedMsg{cands: c, current: cur}
	})
}

// switchOverlayKey handles the keys of the picker and the confirm screen.
func (s *tasksSection) switchOverlayKey(k tea.KeyMsg) tea.Cmd {
	w := &s.sw
	switch w.mode {
	case switchPick:
		switch {
		case key.Matches(k, keyBack):
			w.mode = switchNone
		case key.Matches(k, keyUp):
			w.cur = max(0, w.cur-1)
		case key.Matches(k, keyDown):
			w.cur = min(len(w.cands)-1, w.cur+1)
		case key.Matches(k, keyPickEnter):
			sw, dir, cands, cur := w.sw, w.cands[w.cur].Dir, w.cands, w.current
			return s.switchWork(func() switchLoadedMsg {
				p, err := sw.Plan(dir)
				if err != nil {
					return switchLoadedMsg{note: core.Sanitize(err.Error())}
				}
				return switchLoadedMsg{cands: cands, current: cur, plan: &p}
			})
		}
	case switchLoading:
		if key.Matches(k, keyBack) {
			w.mode, w.seq = switchNone, w.seq+1 // the answer, when it comes, is dropped
		}
	case switchConfirm:
		switch {
		case key.Matches(k, keyYes):
			if !s.switchWhole() { // a clipped confirm screen is not what was reviewed
				return nil
			}
			w.mode = switchBusy
			plan, sw := w.plan.Confirm(), w.sw
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 2*fetchTimeout)
				defer cancel()
				return trackerSwitchedMsg{dir: plan.Dir, err: sw.Apply(ctx, plan)}
			}
		case key.Matches(k, keyNo):
			w.mode, s.notice = switchNone, "cancelled: nothing was changed"
		}
	}
	return nil
}

// switched runs when the switch was applied: reload everything from the new tracker.
func (s *tasksSection) switched(m trackerSwitchedMsg) tea.Cmd {
	s.sw.mode = switchNone
	if m.err != nil {
		s.notice = core.DescribeWriteError(m.err)
		return nil
	}
	s.notice = "switched to " + san(filepath.Base(m.dir))
	s.resetForTracker()
	return s.refresh()
}

// resetForTracker drops everything that described the old tracker, keeping the user's view choices.
func (s *tasksSection) resetForTracker() {
	old := s.list
	s.gen++ // answers of reads started for the old tracker are dropped on arrival
	s.insBusy = false
	s.list = newTasksList()
	s.list.tree.store = old.tree.store
	s.list.sort, s.list.preset, s.list.presetSet = old.sort, old.preset, old.presetSet
	s.listNav = listNav{}
	s.graph = graphState{}
	s.insights, s.err, s.loaded, s.report = nil, nil, false, core.TasksReport{}
	// A read of the old tracker may still be in flight: snapBusy stays, so the refresh below marks it
	// dirty and the answer is followed by a read of the new tracker, never two reads at once.
	s.live = newLiveState(s.live.fps)
	s.live.poll.SetHidden(false)
	s.histClear()
}

// switchBody is the confirm screen: what will happen, in plain sentences, and the equivalent command.
func (s *tasksSection) switchBody(inner int) []string {
	th, p := s.th, s.sw.plan
	sp := func(x string) string { return san(scrubPaths(x)) }
	var out []string
	if p.From != "" {
		out = append(out, hang("  from  ", sp(p.From), inner)...)
	}
	out = append(out, hang("  to    ", th.Header.Render(sp(p.Dir)), inner)...)
	out = append(out, "")
	for _, l := range p.Lines {
		out = append(out, hang("  ", sp(l), inner)...)
	}
	out = append(out, "", th.Text(KindMuted, "same as running:"))
	out = append(out, hang("  ", sp(core.DisplayArgv(p.Argv)), inner)...)
	if s.sw.mode == switchBusy {
		out = append(out, "", th.Text(KindMuted, "switching…"))
	}
	return out
}

func (s *tasksSection) switchWhole() bool {
	return fitsBox(s.switchBody(innerW(s.width())), s.h)
}

// switchView is the picker, the confirm screen or the busy screen.
func (s *tasksSection) trackerView() []string {
	th, w, inner := s.th, s.width(), innerW(s.width())
	sw := &s.sw
	if sw.mode == switchLoading {
		return box(th, "Switch tracker", []string{th.Text(KindMuted, "reading the known trackers…"), "", th.Text(KindMuted, "esc cancels")}, w, s.h)
	}
	if sw.mode != switchPick {
		title := "Switch tracker"
		if sw.mode == switchBusy {
			title = "Switching tracker"
		}
		return reviewBox(th, title, s.switchBody(inner), w, s.h)
	}
	rows := max(s.h-panelChromeH, 1)
	if s.h <= 0 {
		rows = len(sw.cands)
	}
	top := pickWindow(sw.cur, len(sw.cands), rows)
	var body []string
	for i := top; i < len(sw.cands) && i < top+rows; i++ {
		c := sw.cands[i]
		name := san(filepath.Base(c.Dir))
		mark := "  "
		if c.Dir == sw.current {
			mark = "● "
			name += " (current)"
		}
		dir := san(scrubPaths(shortHome(c.Dir)))
		if i == sw.cur {
			body = append(body, th.Selected.Render(padTo(trunc("▸ "+name+"  "+dir, inner), inner)))
			continue
		}
		body = append(body, trunc(mark+name+"  "+th.Text(KindMuted, dir), inner))
	}
	return box(th, fmt.Sprintf("Switch tracker (%d)", len(sw.cands)), body, w, s.h)
}

// scrubPaths post-processes every path the switcher draws. It is the identity; golden tests replace
// it so a temporary directory's random name never reaches a golden file.
var scrubPaths = func(s string) string { return s }

// shortHome shows a path under the home directory as ~/...
func shortHome(p string) string {
	if h, err := os.UserHomeDir(); err == nil && h != "" && strings.HasPrefix(p, h+string(filepath.Separator)) {
		return "~" + p[len(h):]
	}
	return p
}

func (s *tasksSection) switchBindings() []key.Binding {
	if s.sw.mode == switchConfirm {
		if !s.switchWhole() {
			return []key.Binding{keyNo}
		}
		return []key.Binding{keyYes, keyNo}
	}
	if s.sw.mode == switchBusy {
		return nil
	}
	if s.sw.mode == switchLoading {
		return []key.Binding{keyBack}
	}
	pick := key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "switch"))
	if s.width() < 60 { // a narrow footer must keep "q quit"
		return []key.Binding{pick, keyBack}
	}
	return []key.Binding{pick, moveBindings()[0], keyBack}
}

// rememberTracker queues a write of the tracker the first snapshot of a session came from, so the
// switcher offers it later. It writes abacus's own state file, once per tracker, off the UI goroutine.
func (s *tasksSection) rememberTracker(snap core.TaskSnapshot) {
	w := &s.sw
	if w.sw == nil || snap.Root == "" || snap.Root == w.touched || snap.DepsUnknown {
		return
	}
	w.touched = snap.Root
	rt, root := w.sw.Recents, snap.Root
	s.extra = append(s.extra, func() tea.Msg { _ = rt.Touch(root, nowFn()); return nil })
}

// overlayOpen is whether a picker, prompt or confirm screen owns the keys.
func (s *tasksSection) overlayOpen() bool {
	return s.fil.mode != filterNone || s.sw.mode != switchNone
}

// overlayBindings are the keys of whichever overlay is open.
func (s *tasksSection) overlayBindings() ([]key.Binding, bool) {
	switch {
	case s.fil.mode != filterNone:
		return s.filterBindings(), true
	case s.sw.mode != switchNone:
		return s.switchBindings(), true
	}
	return nil, false
}

// overlayView is the screen of whichever overlay is open.
func (s *tasksSection) overlayView() (string, bool) {
	switch {
	case s.fil.mode != filterNone:
		return joinLines(s.filterView()), true
	case s.sw.mode != switchNone:
		return joinLines(s.trackerView()), true
	}
	return "", false
}

// trackerDirOf is the tracker directory a writer reads, "" when there is no writer.
func trackerDirOf(w core.TaskWriter) func() string {
	return func() string {
		if w == nil {
			return ""
		}
		return w.TrackerDir()
	}
}

// WithSavedFilters sets where the Tasks saved filters live (default: the abacus config directory;
// a demo session keeps none).
func WithSavedFilters(f core.SavedFilters) Option {
	return func(c *config) { c.filters, c.filtersSet = f, true }
}

// WithRecentTrackers sets where the recently opened trackers are kept (default: the abacus state
// directory; a demo session keeps none).
func WithRecentTrackers(r core.RecentTrackers) Option {
	return func(c *config) { c.recents, c.recentsSet = r, true }
}

// WithTrackerSwitcher replaces the tracker switcher (tests inject one over fakes).
func WithTrackerSwitcher(sw *core.Switcher) Option { return func(c *config) { c.switcher = sw } }

// switchTo is `:ctx NAME`: find the known tracker called NAME (its folder name, any case; a unique
// prefix also works) and open the same confirm screen P does. It switches nothing by itself.
func (s *tasksSection) switchTo(name string) (tea.Cmd, string) {
	if s.sw.sw == nil {
		return nil, "switching tracker is not available here"
	}
	sw := s.sw.sw
	return s.switchWork(func() switchLoadedMsg {
		cands, cur := sw.Candidates()
		var hit []core.RecentTracker
		for _, c := range cands {
			if b := filepath.Base(c.Dir); strings.EqualFold(b, name) {
				hit = []core.RecentTracker{c}
				break
			} else if len(name) > 0 && len(b) >= len(name) && strings.EqualFold(b[:len(name)], name) {
				hit = append(hit, c)
			}
		}
		switch {
		case len(hit) == 0:
			return switchLoadedMsg{note: "no known tracker called " + san(name) + ": P lists the ones you opened"}
		case len(hit) > 1:
			return switchLoadedMsg{note: san(name) + " matches more than one tracker: P lists them"}
		case hit[0].Dir == cur:
			return switchLoadedMsg{note: san(filepath.Base(hit[0].Dir)) + " is already the tracker in use"}
		}
		p, err := sw.Plan(hit[0].Dir)
		if err != nil {
			return switchLoadedMsg{note: core.Sanitize(err.Error())}
		}
		return switchLoadedMsg{cands: cands, current: cur, plan: &p}
	}), ""
}
