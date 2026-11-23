package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/query"
)

var (
	keyFilterSave = key.NewBinding(key.WithKeys("ctrl+s", "ctrl+o"), key.WithHelp("ctrl+s/o", "save/apply filter"))
	keyRecall     = key.NewBinding(key.WithKeys("ctrl+p", "ctrl+n"), key.WithHelp("ctrl+p/n", "recent"))
	keyPickEnter  = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "apply"))
	keyPickDelete = key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete"))
)

type filterMode int

const (
	filterNone   filterMode = iota
	filterName              // typing the name of the query being saved
	filterPick              // choosing a saved filter
	filterDelete            // confirming a delete
)

// filterUI is the saved-filter prompt and picker.
type filterUI struct {
	store core.SavedFilters
	mode  filterMode
	name  string
	err   string
	cur   int
	list  []core.SavedFilter
}

// recallState walks the recent `/` queries with ctrl+p and ctrl+n while typing one.
type recallState struct {
	hist  core.SearchHistory
	on    bool
	items []string // oldest first
	pos   int
	draft string
}

// openSave asks for a name for the query on screen.
func (s *tasksSection) openSave() {
	l := &s.list
	switch {
	case l.text == "" || l.qerr != nil:
		s.notice = "type a query with / first: ctrl+s saves it under a name"
	case s.fil.store.Path == "":
		s.notice = "saved filters are not kept in this session"
	default:
		l.typing = false // saving a query being typed keeps it, as enter does
		s.fil.mode, s.fil.name, s.fil.err = filterName, "", ""
	}
}

// openPicker lists the saved filters.
func (s *tasksSection) openPicker() {
	list := s.fil.store.List()
	if len(list) == 0 {
		s.notice = "no saved filters yet: type a query with / and press ctrl+s"
		return
	}
	s.fil.mode, s.fil.list, s.fil.cur, s.fil.err = filterPick, list, 0, ""
}

// typingKey is the extra keys while the `/` query is being typed: enter records it in the recent
// list (the section's own handler still keeps it), ctrl+p and ctrl+n walk that list, ctrl+s saves it.
func (s *tasksSection) typingKey(k tea.KeyMsg) (tea.Cmd, bool) {
	l, r := &s.list, &s.rec
	switch k.Type {
	case tea.KeyEnter:
		if l.qerr == nil {
			_ = r.hist.Add(core.HistoryTasksFilter, l.text) // a busy file only means this query is not recalled later
		}
		r.on = false
		return nil, false
	case tea.KeyCtrlP, tea.KeyCtrlN:
		if !r.on {
			r.items, r.pos, r.draft, r.on = r.hist.Load(core.HistoryTasksFilter), 0, l.text, true
			r.pos = len(r.items)
		}
		switch {
		case k.Type == tea.KeyCtrlP && r.pos > 0:
			r.pos--
			l.text = r.items[r.pos]
		case k.Type == tea.KeyCtrlN && r.pos < len(r.items):
			r.pos++
			l.text = r.draft
			if r.pos < len(r.items) {
				l.text = r.items[r.pos]
			}
		default:
			return nil, true
		}
		s.reparse()
		return nil, true
	case tea.KeyCtrlS:
		s.openSave()
		return nil, true
	case tea.KeyEsc:
		r.on = false
	}
	return nil, false
}

// filterOverlayKey handles the keys of the name prompt, the picker and the delete confirmation.
func (s *tasksSection) filterOverlayKey(k tea.KeyMsg) tea.Cmd {
	f := &s.fil
	switch f.mode {
	case filterName:
		switch k.Type {
		case tea.KeyEsc:
			f.mode = filterNone
		case tea.KeyBackspace:
			if r := []rune(f.name); len(r) > 0 {
				f.name, f.err = string(r[:len(r)-1]), ""
			}
		case tea.KeyEnter:
			if err := f.store.Save(f.name, s.list.text); err != nil {
				f.err = strings.TrimPrefix(core.Sanitize(err.Error()), "saved filters: ")
				return nil
			}
			s.notice = fmt.Sprintf("saved filter %q", san(f.name))
			f.mode = filterNone
		case tea.KeyRunes, tea.KeySpace:
			f.name, f.err = f.name+string(k.Runes), ""
		}
	case filterPick:
		switch {
		case key.Matches(k, keyBack):
			f.mode = filterNone
		case key.Matches(k, keyUp):
			f.cur = max(0, f.cur-1)
		case key.Matches(k, keyDown):
			f.cur = min(len(f.list)-1, f.cur+1)
		case key.Matches(k, keyPickEnter):
			s.applyFilter(f.list[f.cur])
			f.mode = filterNone
		case key.Matches(k, keyPickDelete):
			f.mode = filterDelete
		}
	case filterDelete:
		switch {
		case key.Matches(k, keyYes):
			name := f.list[f.cur].Name
			if err := f.store.Delete(name); err != nil {
				s.notice = strings.TrimPrefix(core.Sanitize(err.Error()), "saved filters: ")
			} else {
				s.notice = fmt.Sprintf("deleted filter %q", san(name))
			}
			f.list = f.store.List()
			f.cur = min(f.cur, len(f.list)-1)
			f.mode = filterPick
			if len(f.list) == 0 {
				f.mode = filterNone
			}
		case key.Matches(k, keyNo):
			f.mode = filterPick
		}
	}
	return nil
}

// applyFilter makes a saved query the one in force.
func (s *tasksSection) applyFilter(sf core.SavedFilter) {
	l := &s.list
	q, err := query.Parse(sf.Query)
	if err != nil { // parse first: a query that no longer parses must not replace the one in force
		s.notice = fmt.Sprintf("filter %q no longer parses", san(sf.Name))
		return
	}
	l.text, l.q, l.qerr = sf.Query, q, nil
	s.recompute()
	s.moved()
	s.notice = fmt.Sprintf("applied filter %q", san(sf.Name))
}

func (s *tasksSection) filterBindings() []key.Binding {
	switch s.fil.mode {
	case filterName:
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "save")), key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel"))}
	case filterDelete:
		return []key.Binding{keyYes, keyNo}
	}
	if s.width() < 60 { // a narrow footer must keep "q quit"
		return []key.Binding{keyPickEnter, keyBack}
	}
	return []key.Binding{keyPickEnter, keyPickDelete, moveBindings()[0], keyBack}
}

// pickWindow is the first row to draw so that cur is visible in rows rows.
func pickWindow(cur, n, rows int) int {
	if rows <= 0 || n <= rows {
		return 0
	}
	return max(0, min(cur-rows/2, n-rows))
}

// filterView is the screen of the name prompt, the picker and the delete confirmation.
func (s *tasksSection) filterView() []string {
	th, w, inner := s.th, s.width(), innerW(s.width())
	f := &s.fil
	switch f.mode {
	case filterName:
		body := wrap(th.Text(KindMuted, "query: ")+san(s.list.text), inner)
		body = append(body, "")
		body = append(body, fieldRows(th, []formField{{label: "Name", value: f.name}}, 0, inner)...)
		if f.err != "" {
			body = append(body, "")
			body = append(body, wrap(th.Text(KindFail, san(f.err)), inner)...)
		}
		return box(th, "Save this filter", body, w, s.h)
	case filterDelete:
		sf := f.list[f.cur]
		body := wrap("delete the saved filter "+th.Header.Render(san(sf.Name))+"?", inner)
		body = append(body, wrap(th.Text(KindMuted, san(sf.Query)), inner)...)
		return reviewBox(th, "Delete this filter", body, w, s.h)
	}
	rows := max(s.h-panelChromeH, 1)
	if s.h <= 0 {
		rows = len(f.list)
	}
	top := pickWindow(f.cur, len(f.list), rows)
	var body []string
	for i := top; i < len(f.list) && i < top+rows; i++ {
		sf := f.list[i]
		line := "  " + san(sf.Name) + "  " + th.Text(KindMuted, san(sf.Query))
		if i == f.cur {
			line = th.Selected.Render(padTo(trunc("▸ "+san(sf.Name)+"  "+san(sf.Query), inner), inner))
		} else {
			line = trunc(line, inner)
		}
		body = append(body, line)
	}
	return box(th, fmt.Sprintf("Saved filters (%d)", len(f.list)), body, w, s.h)
}
