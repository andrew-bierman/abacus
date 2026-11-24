package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
)

// The workspace actions of a row: bring its terminal forward, open a terminal to work on it, and
// read what its terminal shows. Core plans every write and the shared gate applies it (confirm,
// single use, audit); this file only asks, draws the plan and runs it on an explicit y. Nothing
// here types into a terminal, and a read of a terminal never writes.

var (
	keyFocus      = key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "show terminal"))
	keyWork       = key.NewBinding(key.WithKeys("W"), key.WithHelp("W", "work on this"))
	keyPlaceSplit = key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "tab/split"))
)

// wsMode is where a layer's own write is: nothing, waiting for a confirm, or running.
type wsMode int

const (
	wsIdle wsMode = iota
	wsConfirm
	wsBusy
)

// writeTimeout bounds one applied plan; the layout is the longest, a few commands.
const writeTimeout = 30 * time.Second

// wsHost is a section that hosts the layer: it says which row is selected and which items to ask about.
type wsHost interface {
	workspaceHost
	layer() *wsLayer
	wsItems() []core.WorkspaceItem
	// wsSelected is the row under the cursor, with the title that fills the agent command.
	wsSelected() (it core.WorkspaceItem, title string, ok bool)
	// wsDetailVisible says the row's detail is on screen now, so what its terminal shows is wanted.
	wsDetailVisible() bool
}

type wsActionMsg struct {
	tag  string
	item core.WorkspaceItem
	act  core.PlacementAction
	res  core.PlacementResult
	err  error
}

type wsOutputMsg struct {
	tag  string
	item core.WorkspaceItem
	cap  core.WorkspaceCapture
	err  error
}

// wsChangedMsg says the workspace's change feed reported something that matters.
type wsChangedMsg struct{}

// wsOutput is the last read of an item's terminal.
type wsOutput struct {
	item core.WorkspaceItem
	cap  core.WorkspaceCapture
	err  error
}

// attach gives the layer the optional capabilities of the workspace provider and the one thing it
// reads at key time: the configured agent command. A demo session gets none, so it never runs anything.
func (w *wsLayer) attach(p core.Providers, audit core.Audit, agent func() []string, cwd func() string) {
	if w == nil || w.demo {
		return
	}
	w.placer, w.capt, w.agent, w.cwd = p.Placer, p.Capturer, agent, cwd
	if p.Placer != nil && audit != nil {
		w.applier = core.NewPlacementApplier(p.Placer, audit)
	}
}

// modal reports that the layer owns the keys: a plan is waiting for y or n, or running.
func (w *wsLayer) modal() bool { return w != nil && w.mode != wsIdle }

// actionBindings are the footer keys of the row's actions, enabled only when they can do something.
func (w *wsLayer) actionBindings(h wsHost) []key.Binding {
	if w == nil || w.applier == nil {
		return nil
	}
	if w.modal() {
		return w.modalBindings()
	}
	it, _, ok := h.wsSelected()
	focus, work := keyFocus, keyWork
	p, _, _ := w.find(it)
	focus.SetEnabled(ok && p != nil && p.Block != "")
	work.SetEnabled(ok && it.Kind != core.WorkspaceAgent) // "work on this" starts an agent for a task; an agent row is already one
	return []key.Binding{focus, work}
}

func (w *wsLayer) modalBindings() []key.Binding {
	if w.mode == wsBusy {
		return nil
	}
	b := []key.Binding{keyYes, keyNo}
	if w.plan.Request.Action == core.PlaceWork {
		b = append(b, keyPlaceSplit)
	}
	return b
}

// actionKey handles m and W on the selected row. Planning runs only reads, so it is quick enough
// to do here; the write waits for y on the confirm screen.
func (w *wsLayer) actionKey(k tea.KeyMsg, h wsHost) (tea.Cmd, bool) {
	if w == nil || w.applier == nil || !key.Matches(k, keyFocus, keyWork) {
		return nil, false
	}
	if w.swallow() {
		return nil, true
	}
	it, title, ok := h.wsSelected()
	if !ok {
		return nil, true
	}
	if it.Kind == core.WorkspaceAgent && key.Matches(k, keyWork) {
		w.note = []string{"Work on this starts a new agent for a task. Open it from Tasks."}
		return nil, true
	}
	req := core.PlacementRequest{Item: it}
	switch {
	case key.Matches(k, keyFocus):
		p, cands, _ := w.find(it)
		switch {
		case p == nil && len(cands) > 0:
			w.note = []string{fmt.Sprintf("%d terminals are named for %s, so none was chosen. Pick one yourself.", len(cands), it.ID)}
			return nil, true
		case p == nil || p.Block == "":
			w.note = []string{"No terminal is named for " + it.ID + ", so there is none to bring forward."}
			return nil, true
		}
		req.Action, req.Target = core.PlaceFocus, p
	default:
		var tpl []string
		if w.agent != nil {
			tpl = w.agent()
		}
		if len(tpl) == 0 {
			w.note = []string{"Set the agent command first, for example:", `abacus config set agent_command '["claude","Work on {task_id}: {title}"]'`}
			return nil, true
		}
		req.Action, req.Title, req.Template = core.PlaceWork, title, tpl
		if w.cwd != nil {
			req.Cwd = w.cwd()
		}
	}
	return nil, w.plan_(req)
}

// plan_ asks core for the plan of req and opens the confirm screen, or says why not.
func (w *wsLayer) plan_(req core.PlacementRequest) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	p, err := core.PlanPlacement(ctx, w.placer, req)
	if err != nil {
		w.note = []string{"Nothing was opened: " + core.Sanitize(err.Error())}
		return true
	}
	w.plan, w.mode, w.note = p, wsConfirm, nil
	return true
}

// modalKey handles the keys of the confirm screen. y applies only while the screen shows the whole
// plan and commands; a clipped screen is not what was reviewed.
func (w *wsLayer) modalKey(k tea.KeyMsg) tea.Cmd {
	if w.mode != wsConfirm {
		return nil
	}
	switch {
	case key.Matches(k, keyYes):
		if !w.confirmWhole() {
			return nil
		}
		w.mode = wsBusy
		ap, plan, tag, it := w.applier, w.plan.Confirm(), w.tag, w.plan.Request.Item
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
			defer cancel()
			res, err := ap.Apply(ctx, plan)
			return wsActionMsg{tag: tag, item: it, act: plan.Request.Action, res: res, err: err}
		}
	case key.Matches(k, keyNo):
		w.mode, w.note = wsIdle, []string{"cancelled: nothing was changed"}
	case key.Matches(k, keyPlaceSplit) && w.plan.Request.Action == core.PlaceWork:
		req := w.plan.Request
		req.Split = !req.Split
		w.plan_(req)
	}
	return nil
}

// receiveAction takes the answer of an applied plan and says what happened. The workspace is read
// again either way: an unknown outcome in particular is settled by looking.
func (w *wsLayer) receiveAction(m wsActionMsg, h wsHost) tea.Cmd {
	if w == nil || m.tag != w.tag {
		return nil
	}
	w.mode = wsIdle
	switch {
	case m.err != nil:
		w.note = []string{core.DescribeWriteError(m.err)}
	case m.act == core.PlaceFocus:
		w.note = []string{"Brought the terminal for " + m.item.ID + " to the front."}
	default:
		w.note = []string{"Opened a terminal for " + m.item.ID + "."}
	}
	if inv, ok := w.placer.(interface{ Invalidate() }); ok {
		inv.Invalidate()
	}
	return tea.Batch(w.fetch(h.wsItems()))
}

// confirmBody is the whole confirm screen text: what the plan does and every command, exactly.
func (w *wsLayer) confirmBody(th Theme, inner int) []string {
	var body []string
	for _, l := range w.plan.Lines {
		body = append(body, wrap(san(l), inner)...)
	}
	body = append(body, "")
	for _, s := range w.plan.Steps {
		body = append(body, wrap("command: "+core.DisplayArgv(s), inner)...)
	}
	body = append(body, "")
	if w.mode == wsBusy {
		return append(body, th.Text(KindMuted, "working…"))
	}
	hint := "y runs it · n cancels"
	if w.plan.Request.Action == core.PlaceWork {
		hint += " · s switches tab or split"
	}
	return append(body, th.Text(KindMuted, hint))
}

func (w *wsLayer) confirmWhole() bool {
	return fitsBox(w.confirmBody(DefaultTheme(), innerW(max(w.lastW, 1))), w.lastH)
}

func (w *wsLayer) confirmBox(th Theme, width, h int) []string {
	w.lastW, w.lastH = width, h
	return reviewBox(th, "Workspace", w.confirmBody(th, innerW(width)), width, h)
}

// captureCmd reads the selected item's terminal when one is named for it, the detail is on screen
// and either the row changed or force is set (a poll result or an explicit refresh). One read at a
// time. Nothing is read for a detail nobody sees.
func (w *wsLayer) captureCmd(h wsHost, force bool) tea.Cmd {
	if w == nil || w.capt == nil || w.outBusy || !h.wsDetailVisible() {
		return nil
	}
	it, _, ok := h.wsSelected()
	if !ok {
		w.out = nil
		return nil
	}
	p, _, _ := w.find(it)
	if p == nil || p.Block == "" {
		w.out = nil
		return nil
	}
	if !force && w.out != nil && w.out.item == it {
		return nil
	}
	w.outBusy = true
	capt, pl, tag := w.capt, *p, w.tag
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		c, err := capt.Capture(ctx, pl)
		return wsOutputMsg{tag: tag, item: it, cap: c, err: err}
	}
}

func (w *wsLayer) receiveOutput(m wsOutputMsg) {
	if w == nil || m.tag != w.tag {
		return
	}
	w.outBusy = false
	w.out = &wsOutput{item: m.item, cap: m.cap, err: m.err}
}

// maxOutputLines is how much of a terminal's end the detail shows.
const maxOutputLines = 8

// outputLines is the terminal's output block of the detail panel, empty without a read of this item.
func (w *wsLayer) outputLines(th Theme, it core.WorkspaceItem, pad int) []string {
	if w == nil || w.out == nil || w.out.item != it {
		return nil
	}
	label := th.Label.Render(padTo("output", pad))
	if w.out.err != nil {
		return []string{label + th.Text(KindMuted, "not available: "+san(w.out.err.Error()))}
	}
	lines := w.out.cap.Lines
	if len(lines) == 0 {
		return []string{label + th.Text(KindMuted, "nothing shown yet")}
	}
	more := w.out.cap.Truncated || len(lines) > maxOutputLines
	if len(lines) > maxOutputLines {
		lines = lines[len(lines)-maxOutputLines:]
	}
	out := []string{label + th.Text(KindMuted, "the end of what the terminal shows")}
	if more {
		out = append(out, "  "+th.Text(KindMuted, "…"))
	}
	for _, l := range lines {
		out = append(out, "  "+trunc(strings.TrimRight(san(l), " "), 100))
	}
	return out
}
