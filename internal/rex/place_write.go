package rex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/andrew-bierman/abacus/internal/core"
)

var (
	_ core.WorkspaceActor    = (*Module)(nil)
	_ core.WorkspaceCapturer = (*Module)(nil)
	_ core.WorkspaceWatcher  = (*Module)(nil)
)

// notInSession is why work cannot be placed when this process is not inside a workspace session.
const notInSession = "abacus is not running inside a workspace session, so there is no session to open this in. Start abacus from a Rex terminal."

// PlacementTarget names where a write goes, for the audit log: how the server was found and, when
// the environment names one, which.
func (m *Module) PlacementTarget() string {
	m.mu.Lock()
	server := m.server
	m.mu.Unlock()
	t := "workspace"
	if server != "" {
		t += " (" + server + ")"
	}
	if s := m.o.Getenv("REX_SERVER"); s != "" {
		t += " " + safe(s, 128)
	}
	return t
}

// currentSession is the session this process runs in, from its environment: the one "work" and the
// layout open terminals in. It is validated, since it ends up in a command.
func (m *Module) currentSession() (string, error) {
	s := m.o.Getenv("REX_SESSION")
	if !strings.HasPrefix(s, "session:") || !sessionRE.MatchString(s) {
		return "", errors.New(notInSession)
	}
	return s, nil
}

func (m *Module) command(argv []string) []string { return append([]string{core.RexBin}, argv...) }

// BuildPlacement turns a request into the exact commands and a description of them. It runs only
// reads (the layout looks at what is already open) and refuses anything the write allowlist does
// not accept, so the command a person confirms is a command that can run.
func (m *Module) BuildPlacement(ctx context.Context, r core.PlacementRequest) (core.PlacementBuild, error) {
	var b core.PlacementBuild
	switch r.Action {
	case core.PlaceFocus:
		p := r.Target
		host, err := m.targetHost(p.Host)
		if err != nil {
			return b, err
		}
		b.Steps = [][]string{m.command(focusArgv(host, p.Session, p.Block))}
		b.Lines = []string{"bring the terminal for " + safe(r.Item.ID, core.MaxIDRunes) + " to the front", "session: " + safe(orID(p.Label, p.Session), core.MaxLabelRunes)}
		if host != "" {
			b.Lines = append(b.Lines, "machine: "+host)
		}
	case core.PlaceWork:
		session, err := m.currentSession()
		if err != nil {
			return b, err
		}
		label := r.Item.ID
		if !labelRE.MatchString(label) {
			return b, errors.New("this item's id is too long or unusual to name a terminal after")
		}
		cwd := r.Cwd
		if cwd != "" && !cwdOK(cwd) {
			cwd = ""
		}
		where := "a new tab"
		argv := runArgv(session, label, cwd, r.Command)
		if r.Split {
			anchor := m.o.Getenv("REX_BLOCK")
			if !blockRE.MatchString(anchor) {
				anchor = ""
			}
			argv, where = splitArgv(session, anchor, label, cwd, r.Command), "a split beside this terminal"
		}
		b.Steps = [][]string{m.command(argv)}
		b.Lines = []string{"open " + where + " named " + label + " in session " + session, "it runs: " + core.DisplayArgv(r.Command)}
		if cwd != "" {
			b.Lines = append(b.Lines, "in folder: "+cwd)
		}
		b.Lines = append(b.Lines, "nothing is typed into any existing terminal")
	case core.PlaceLayout:
		return m.buildLayout(ctx, r)
	default:
		return b, fmt.Errorf("unsupported action %q", r.Action)
	}
	return b, m.checkWrites(b)
}

func orID(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// checkWrites refuses a plan with a command the write allowlist would not run.
func (m *Module) checkWrites(b core.PlacementBuild) error {
	for _, s := range b.Steps {
		if len(s) < 2 || s[0] != core.RexBin || !allowedWriteArgv(s[1:]) {
			return errors.New("refused: not an allowed workspace write")
		}
	}
	return nil
}

func (m *Module) buildLayout(ctx context.Context, r core.PlacementRequest) (core.PlacementBuild, error) {
	var b core.PlacementBuild
	tabs := core.LayoutTabs()
	cwd := r.Cwd
	if cwd != "" && !cwdOK(cwd) {
		cwd = ""
	}
	cmd := func(t core.LayoutTab) []string { return []string{r.Exe, "tui", "--tab", t.Arg} }
	if r.NewSession != "" {
		name := r.NewSession
		if strings.HasPrefix(name, "session:") || !labelRE.MatchString(name) || !sessionRE.MatchString(name) {
			return b, errors.New("the session name must be letters, digits, dot, dash or underscore")
		}
		res := m.sessions(ctx, "")
		switch {
		case errors.Is(res.err, errNoServer):
			return b, errors.New("no workspace server is running, and this never starts one")
		case res.err != nil:
			return b, errors.New("could not read the sessions: " + safe(res.err.Error(), 120))
		}
		for _, s := range res.ss {
			if strings.EqualFold(s.text, name) {
				return b, fmt.Errorf("a session named %s already exists; choose another name, or run this inside it to add the tabs there", name)
			}
		}
		for i, t := range tabs {
			if i == 0 {
				b.Steps = append(b.Steps, m.command(newSessionArgv(name, t.Name, cwd, cmd(t))))
			} else {
				b.Steps = append(b.Steps, m.command(newWindowArgv(name, t.Name, cwd, false, cmd(t))))
			}
		}
		b.Lines = append(b.Lines, "create the session "+name+" with these tabs: "+tabNames(tabs))
	} else {
		session, err := m.currentSession()
		if err != nil {
			return b, errors.New(notInSession + " Or name a new session to create.")
		}
		open, err := m.openTabs(ctx, session)
		if err != nil {
			return b, err
		}
		first := true
		var skipped []string
		for _, t := range tabs {
			if open[strings.ToLower(t.Name)] {
				skipped = append(skipped, t.Name)
				continue
			}
			b.Steps = append(b.Steps, m.command(newWindowArgv(session, t.Name, cwd, first, cmd(t))))
			first = false
		}
		if len(b.Steps) == 0 {
			return b, errors.New("all of the cockpit tabs are already open in this session")
		}
		b.Lines = append(b.Lines, "add the cockpit tabs to session "+session)
		if len(skipped) > 0 {
			b.Lines = append(b.Lines, "already open, left alone: "+strings.Join(skipped, ", "))
		}
	}
	b.Lines = append(b.Lines, "each tab runs: "+core.DisplayArgv([]string{r.Exe, "tui", "--tab", "<tab>"}))
	if cwd != "" {
		b.Lines = append(b.Lines, "in folder: "+cwd)
	}
	b.Lines = append(b.Lines, "nothing is typed into any existing terminal")
	return b, m.checkWrites(b)
}

func tabNames(tabs []core.LayoutTab) string {
	var n []string
	for _, t := range tabs {
		n = append(n, t.Name)
	}
	return strings.Join(n, ", ")
}

// openTabs reads the tab names a session already has (lower-cased).
func (m *Module) openTabs(ctx context.Context, session string) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(ctx, m.o.CallTimeout)
	defer cancel()
	out, exit, err := m.run.Run(ctx, m.Bin(), windowsArgv("", session)...)
	switch {
	case err != nil:
		return nil, errors.New("could not read the tabs " + runFailure(err))
	case exit == exitNoServer:
		return nil, errors.New("no workspace server is running, and this never starts one")
	case exit != 0:
		return nil, fmt.Errorf("could not read the tabs (exit %d)", exit)
	}
	var v struct {
		Windows []struct {
			Label string `json:"label"`
		} `json:"windows"`
	}
	if json.Unmarshal(out, &v) != nil {
		return nil, errors.New("could not read the tabs: unrecognized output")
	}
	open := map[string]bool{}
	for _, w := range v.Windows {
		open[strings.ToLower(strings.TrimSpace(core.Sanitize(w.Label)))] = true
	}
	return open, nil
}

// RunPlacement runs one command from a plan after checking it against the write allowlist again.
// Rex's own failures before it acts (no server, a target that is not there) are "not applied";
// a timeout, a signal or any other failure may have happened after, so it is "result unknown".
func (m *Module) RunPlacement(ctx context.Context, argv []string) (core.StepResult, error) {
	if len(argv) < 2 || argv[0] != core.RexBin || !allowedWriteArgv(argv[1:]) {
		return core.StepResult{}, errors.New("refused: not an allowed workspace write")
	}
	if m.Bin() == "" {
		return core.StepResult{}, errors.New("Rex was not found")
	}
	ctx, cancel := context.WithTimeout(ctx, m.o.WriteTimeout)
	defer cancel()
	out, exit, err := m.wrun.Run(ctx, m.Bin(), argv[1:]...)
	unknown := func() error {
		return core.UnknownOutcome("the workspace did not confirm; the terminal may or may not have opened. Look at it before trying again")
	}
	switch {
	case errors.Is(err, core.ErrTimedOut), errors.Is(err, core.ErrCanceled):
		return core.StepResult{}, unknown()
	case err != nil:
		return core.StepResult{}, errors.New("could not run: " + safe(err.Error(), 120))
	case exit < 0:
		return core.StepResult{}, unknown()
	case exit == exitNoServer:
		return core.StepResult{}, errors.New("no workspace server is running")
	case exit == exitAmbiguous:
		return core.StepResult{}, errors.New("the terminal or session was not found, or more than one server answers")
	case exit == 2:
		return core.StepResult{}, errors.New("Rex did not accept the command (exit 2)")
	case exit != 0:
		return core.StepResult{}, unknown()
	}
	return core.StepResult{Pane: paneOf(out)}, nil
}

// paneOf reads the id of the terminal a creating command made, from its --json answer: block_ids
// (run, split, window new) or initial_windows[0].block_ids (new). Not finding one is not an error:
// the write happened.
func paneOf(out []byte) string {
	var v struct {
		BlockIDs []string `json:"block_ids"`
		Windows  []struct {
			BlockIDs []string `json:"block_ids"`
		} `json:"initial_windows"`
	}
	if json.Unmarshal(out, &v) != nil {
		return ""
	}
	ids := v.BlockIDs
	if len(ids) == 0 && len(v.Windows) > 0 {
		ids = v.Windows[0].BlockIDs
	}
	if len(ids) > 0 && blockRE.MatchString(ids[0]) {
		return ids[0]
	}
	return ""
}
