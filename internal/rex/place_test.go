package rex

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

const (
	sessA   = "session:01a20c1b-ae95-745d-ba13-9eaee76983fa"
	sessB   = "session:01a20c2b-d49c-7b89-8a70-0d4ec6a5ad37"
	blk43   = "block:01a20c2b-ed0a-7802-a0b6-b446d8779a01"
	blk44   = "block:01a20c2b-ed5d-7ae1-9ad2-5735b1a57c13"
	blkAnch = "block:01a20c1b-ae95-76c6-a459-fd28a1f35eda"
)

func blocksKey(s string) string { return "--autostart=false block ls -s " + s + " --json" }

// paneFake is a workspace with two sessions (captured from a real Rex) and the pane reads of both.
func paneFake(t *testing.T) *fake {
	return &fake{resp: map[string]resp{
		lsLocal:          {out: fixture(t, "ls-14.json")},
		verLocal:         {out: fixture(t, "version-14.json")},
		blocksKey(sessA): {out: fixture(t, "block-ls.json")},
		blocksKey(sessB): {out: `{"blocks":[{"block_id":"block:01a20c2b-d49c-7c21-96ab-d4034be68bcc","label":"shell"}]}`},
	}}
}

func item(id string) core.WorkspaceItem { return core.WorkspaceItem{Kind: core.WorkspaceTask, ID: id} }

func TestAPaneNamedForAnItemIsItsPlacementWithTheSessionThatHoldsIt(t *testing.T) {
	m := readyMod(t, paneFake(t), nil)
	rep, err := m.Workspace(context.Background(), []core.WorkspaceItem{item("bd-43"), item("bd-44"), item("LAM-7"), item("bd-99")})
	if err != nil || rep.State != core.Connected {
		t.Fatalf("%v %+v", err, rep)
	}
	p, _ := rep.For(item("bd-43"))
	if p == nil || p.Session != sessA || p.Block != blk43 || p.Label != "demo2" || p.Match != core.MatchExact {
		t.Fatalf("%+v", p)
	}
	if p, _ := rep.For(item("bd-44")); p == nil || p.Block != blk44 {
		t.Fatalf("%+v", p)
	}
	// A session named for the item, with no pane named for it, is still a placement, without a pane.
	if p, _ := rep.For(item("LAM-7")); p == nil || p.Session != sessB || p.Block != "" {
		t.Fatalf("%+v", p)
	}
	if p, c := rep.For(item("bd-99")); p != nil || c != nil {
		t.Fatalf("unplaced: %+v %+v", p, c)
	}
}

func TestTwoPanesNamedForOneItemAreAnAmbiguityNeverASilentPick(t *testing.T) {
	f := paneFake(t)
	f.resp[blocksKey(sessA)] = resp{out: `{"blocks":[{"block_id":"` + blk43 + `","label":"bd-43"},{"block_id":"` + blk44 + `","label":"bd-43"}]}`}
	rep, _ := readyMod(t, f, nil).Workspace(context.Background(), []core.WorkspaceItem{item("bd-43")})
	p, c := rep.For(item("bd-43"))
	if p != nil || len(c) != 2 || c[0].Block == c[1].Block {
		t.Fatalf("%+v %+v", p, c)
	}
}

func TestPaneReadsAreBestEffortAndBounded(t *testing.T) {
	f := paneFake(t)
	f.resp[blocksKey(sessA)] = resp{out: "not json"}
	f.resp[blocksKey(sessB)] = resp{exit: 1}
	rep, err := readyMod(t, f, nil).Workspace(context.Background(), []core.WorkspaceItem{item("LAM-7")})
	if err != nil || rep.State != core.Connected {
		t.Fatalf("a failed pane read must not fail the workspace read: %v %+v", err, rep)
	}
	if p, _ := rep.For(item("LAM-7")); p == nil || p.Session != sessB {
		t.Fatalf("the session names still place: %+v", p)
	}
	// Only maxBlockSessions sessions have their panes read.
	var ss []string
	f2 := &fake{resp: map[string]resp{verLocal: {out: fixture(t, "version-14.json")}}}
	for i := 0; i < maxBlockSessions+10; i++ {
		id := "session:00000000-0000-0000-0000-" + strings.Repeat("0", 11) + string(rune('a'+i%26)) + string(rune('a'+i/26))
		ss = append(ss, `{"session_id":"`+id+`","label":"s"}`)
		f2.resp[blocksKey(id)] = resp{out: `{"blocks":[]}`}
	}
	f2.resp[lsLocal] = resp{out: `{"sessions":[` + strings.Join(ss, ",") + `]}`}
	readyMod(t, f2, nil).Workspace(context.Background(), []core.WorkspaceItem{item("bd-1")})
	n := 0
	for _, c := range f2.called() {
		if strings.Contains(c, "block ls") {
			n++
		}
	}
	if n != maxBlockSessions {
		t.Fatalf("pane reads = %d, want %d", n, maxBlockSessions)
	}
}

func TestHostilePaneLabelsAndIdsAreNeverTrusted(t *testing.T) {
	f := paneFake(t)
	f.resp[blocksKey(sessA)] = resp{out: `{"blocks":[{"block_id":"block:x; rm -rf","label":"bd-43"},{"block_id":"` + blk44 + `","label":"bd-43\u0007\u001b[2J extra"}]}`}
	rep, _ := readyMod(t, f, nil).Workspace(context.Background(), []core.WorkspaceItem{item("bd-43")})
	p, _ := rep.For(item("bd-43"))
	if p == nil || p.Block != blk44 {
		t.Fatalf("a pane id that is not a pane id is dropped, the other stays: %+v", p)
	}
}

func TestPaneCacheIsReusedUntilAnEventDropsIt(t *testing.T) {
	f := paneFake(t)
	now := time.Unix(1000, 0)
	m := New(Options{LookPath: func(string) (string, error) { return "/fake/rex", nil }, Getenv: func(string) string { return "" }, Runner: f,
		Hostname: func() string { return "me" }, CacheTTL: time.Second, Now: func() time.Time { return now }})
	detect(m)
	read := func() { m.Workspace(context.Background(), []core.WorkspaceItem{item("bd-43")}) }
	count := func() int {
		n := 0
		for _, c := range f.called() {
			if strings.Contains(c, "block ls") {
				n++
			}
		}
		return n
	}
	read()
	read()
	if count() != 2 { // two sessions, read once
		t.Fatalf("pane reads = %d", count())
	}
	m.Invalidate()
	read()
	if count() != 4 {
		t.Fatalf("after Invalidate the panes are read again: %d", count())
	}
}

// ---- the write allowlist ----

func TestEveryBuiltWriteMatchesItsShapeAndNothingElseDoes(t *testing.T) {
	cmd := []string{"claude", "Work on bd-1: it's $(x); `y` \"z\"", "--flag"}
	good := map[string][]string{
		"focus":       focusArgv("", sessA, blk43),
		"focus host":  focusArgv("mbp", sessA, blk43),
		"run":         runArgv(sessA, "bd-1", "/work/x", cmd),
		"run no cwd":  runArgv(sessA, "bd-1", "", cmd),
		"split":       splitArgv(sessA, blkAnch, "bd-1", "/work/x", cmd),
		"split floor": splitArgv(sessA, "", "bd-1", "", cmd),
		"new":         newSessionArgv("abacus", "Board", "/w", []string{"/bin/abacus", "tui", "--tab", "board"}),
		"window":      newWindowArgv("abacus", "Tasks", "/w", false, []string{"/bin/abacus", "tui", "--tab", "tasks"}),
		"window fg":   newWindowArgv(sessA, "Tasks", "", true, []string{"/bin/abacus", "tui"}),
	}
	for name, a := range good {
		if !allowedWriteArgv(a) {
			t.Errorf("%s should be allowed: %q", name, a)
		}
		if allowedArgv(a) {
			t.Errorf("%s is a write and must not pass the read allowlist: %q", name, a)
		}
	}
	// Every read is not a write.
	for _, a := range [][]string{lsArgv(""), versionArgv(), blocksArgv("", sessA), windowsArgv("", sessA), captureArgv("", sessA, blk43)} {
		if allowedWriteArgv(a) || !allowedArgv(a) {
			t.Errorf("read: %q", a)
		}
	}
	bad := map[string][]string{
		"no autostart":      {"focus", "-s", sessA, blk43},
		"autostart true":    {"--autostart=true", "focus", "-s", sessA, blk43},
		"autostart default": {"--autostart", "focus", "-s", sessA, blk43},
		"send":              {autostartOff, "send", "-b", blk43, "rm -rf /"},
		"send-key":          {autostartOff, "send-key", "-b", blk43, "Enter"},
		"kill":              {autostartOff, "kill", sessA},
		"block close":       {autostartOff, "block", "close", blk43},
		"block send":        {autostartOff, "block", "send", blk43, "x"},
		"api call":          {autostartOff, "api", "call", "session.kill"},
		"do":                {autostartOff, "do", "session.select"},
		"rename":            {autostartOff, "session", "rename", sessA, "x"},
		"focus no block":    {autostartOff, "focus", "-s", sessA},
		"focus label":       {autostartOff, "focus", "-s", sessA, "bd-43"},
		"focus direction":   {autostartOff, "focus", "-s", sessA, "left"},
		"focus bad host":    {"-S", "a b", autostartOff, "focus", "-s", sessA, blk43},
		"focus dash host":   {"-S", "-oProxyCommand=x", autostartOff, "focus", "-s", sessA, blk43},
		"run host":          {"-S", "mbp", autostartOff, "run", "-s", sessA, "--label", "x", "--keep-open", "--json", "--", "true"},
		"run no label":      {autostartOff, "run", "-s", sessA, "--keep-open", "--json", "--", "true"},
		"run empty cmd":     {autostartOff, "run", "-s", sessA, "--label", "x", "--keep-open", "--json", "--"},
		"run no dashdash":   {autostartOff, "run", "-s", sessA, "--label", "x", "--keep-open", "--json", "true"},
		"run extra flag":    {autostartOff, "run", "-s", sessA, "--label", "x", "--wait", "--keep-open", "--json", "--", "true"},
		"run shell flag":    {autostartOff, "run", "-s", sessA, "--label", "x", "--shell", "sh", "--keep-open", "--json", "--", "true"},
		"run dash label":    {autostartOff, "run", "-s", sessA, "--label", "-x", "--keep-open", "--json", "--", "true"},
		"run relative cwd":  {autostartOff, "run", "-s", sessA, "--label", "x", "-c", "rel", "--keep-open", "--json", "--", "true"},
		"run dotdot cwd":    {autostartOff, "run", "-s", sessA, "--label", "x", "-c", "/a/../b", "--keep-open", "--json", "--", "true"},
		"run bad session":   {autostartOff, "run", "-s", "-x", "--label", "x", "--keep-open", "--json", "--", "true"},
		"run ctl in cmd":    {autostartOff, "run", "-s", sessA, "--label", "x", "--keep-open", "--json", "--", "true\x1b[2J"},
		"run huge cmd":      append([]string{autostartOff, "run", "-s", sessA, "--label", "x", "--keep-open", "--json", "--"}, make([]string, 40)...),
		"split dir":         {autostartOff, "split", "-s", sessA, "--split=below", "--label", "x", "--keep-open", "--json", "--", "true"},
		"split bad anchor":  {autostartOff, "split", "-s", sessA, "-b", "x y", "--split=right", "--label", "x", "--keep-open", "--json", "--", "true"},
		"window no json":    {autostartOff, "window", "new", "-s", sessA, "Tab", "--keep-open", "--", "true"},
		"window del":        {autostartOff, "window", "close", "-s", sessA, "Tab"},
		"trailing junk":     append(focusArgv("", sessA, blk43), "--"),
		"empty":             {},
	}
	for name, a := range bad {
		if allowedWriteArgv(a) {
			t.Errorf("%s must be refused: %q", name, a)
		}
	}
	if allowedStreamArgv(eventsArgv()) != true || allowedStreamArgv([]string{autostartOff, "events", "--all", "--json"}) || allowedStreamArgv([]string{"events", "--json"}) {
		t.Error("the event stream has exactly one shape, and never starts a server")
	}
}

func TestTheRunnersEnforceTheirOwnAllowlist(t *testing.T) {
	f := &fake{}
	m := newMod(f, nil)
	ctx := context.Background()
	if _, _, err := m.wrun.Run(ctx, "rex", lsArgv("")...); err == nil {
		t.Error("a read must not run through the write runner")
	}
	if _, _, err := m.run.Run(ctx, "rex", focusArgv("", sessA, blk43)...); err == nil {
		t.Error("a write must not run through the read runner")
	}
	if _, _, err := m.crun.Run(ctx, "rex", focusArgv("", sessA, blk43)...); err == nil {
		t.Error("a write must not run through the terminal-read runner")
	}
	if len(f.called()) != 0 {
		t.Fatalf("nothing may reach rex: %v", f.called())
	}
	if _, err := m.RunPlacement(ctx, append([]string{"rex"}, lsArgv("")...)); err == nil || len(f.called()) != 0 {
		t.Errorf("RunPlacement refuses a read: %v %v", err, f.called())
	}
	if _, err := m.RunPlacement(ctx, append([]string{"sh", "-c"}, focusArgv("", sessA, blk43)...)); err == nil {
		t.Error("RunPlacement refuses another program")
	}
}

// ---- planning ----

func inSession(extra map[string]string) map[string]string {
	e := map[string]string{"REX_SESSION": sessA, "REX_BLOCK": blkAnch}
	for k, v := range extra {
		e[k] = v
	}
	return e
}

func plan(t *testing.T, m *Module, r core.PlacementRequest) core.PlacementPlan {
	t.Helper()
	p, err := core.PlanPlacement(context.Background(), m, r)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestWorkOnThisKeepsEveryAgentWordAsOneArgument(t *testing.T) {
	m := readyMod(t, paneFake(t), inSession(nil))
	hostile := "x'; $(touch /tmp/pwned); `id` \"q\" && rm -rf ~\nnext line"
	p := plan(t, m, core.PlacementRequest{Action: core.PlaceWork, Item: item("bd-7"), Title: hostile,
		Template: []string{"claude", "--print", "Work on {task_id}: {title}", "{task_id}"}, Cwd: "/work/proj"})
	if len(p.Steps) != 1 {
		t.Fatalf("%q", p.Steps)
	}
	a := p.Steps[0]
	i := indexOf(a, "--")
	cmd := a[i+1:]
	if len(cmd) != 4 || cmd[0] != "claude" || cmd[1] != "--print" || cmd[3] != "bd-7" ||
		cmd[2] != "Work on bd-7: x'; $(touch /tmp/pwned); `id` \"q\" && rm -rf ~ next line" {
		t.Fatalf("each word is one argument, the title on one line: %q", cmd)
	}
	want := []string{"rex", "--autostart=false", "run", "-s", sessA, "--label", "bd-7", "-c", "/work/proj", "--keep-open", "--json", "--"}
	if strings.Join(a[:i+1], "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("\n got %q\nwant %q", a[:i+1], want)
	}
	if !strings.Contains(strings.Join(p.Lines, "\n"), "nothing is typed into any existing terminal") {
		t.Fatalf("%q", p.Lines)
	}
}

func TestWorkOnThisAsASplitUsesTheCurrentPaneAsAnchorWhenItIsValid(t *testing.T) {
	m := readyMod(t, paneFake(t), inSession(nil))
	r := core.PlacementRequest{Action: core.PlaceWork, Item: item("bd-7"), Title: "t", Template: []string{"agent"}, Split: true}
	a := plan(t, m, r).Steps[0]
	if a[2] != "split" || indexOf(a, "-b") < 0 || a[indexOf(a, "-b")+1] != blkAnch || indexOf(a, "--split=right") < 0 {
		t.Fatalf("%q", a)
	}
	m = readyMod(t, paneFake(t), inSession(map[string]string{"REX_BLOCK": "evil; x"}))
	if a := plan(t, m, r).Steps[0]; indexOf(a, "-b") >= 0 {
		t.Fatalf("an environment value that is not a pane id is not used: %q", a)
	}
}

func TestWorkNeedsASessionAndAnItemThatCanNameATerminal(t *testing.T) {
	r := core.PlacementRequest{Action: core.PlaceWork, Item: item("bd-7"), Title: "t", Template: []string{"agent"}}
	for _, env := range []map[string]string{nil, {"REX_SESSION": "not-a-session"}, {"REX_SESSION": "session:x; rm"}, {"REX_SESSION": "-s"}} {
		_, err := core.PlanPlacement(context.Background(), readyMod(t, paneFake(t), env), r)
		if err == nil || !strings.Contains(err.Error(), "inside a workspace session") {
			t.Errorf("%v: %v", env, err)
		}
	}
	long := r
	long.Item = item(strings.Repeat("a", 100))
	if _, err := core.PlanPlacement(context.Background(), readyMod(t, paneFake(t), inSession(nil)), long); err == nil {
		t.Error("an id too long to name a terminal is refused")
	}
	rel := r
	rel.Cwd = "relative/dir"
	if a := plan(t, readyMod(t, paneFake(t), inSession(nil)), rel).Steps[0]; indexOf(a, "-c") >= 0 {
		t.Errorf("a folder that is not absolute is left out: %q", a)
	}
}

func TestFocusIsOneScopedCommandOnlyForAConfiguredMachine(t *testing.T) {
	m := readyMod(t, paneFake(t), nil)
	pl := core.Placement{Item: item("bd-43"), Session: sessA, Block: blk43, Label: "demo2"}
	a := plan(t, m, core.PlacementRequest{Action: core.PlaceFocus, Item: item("bd-43"), Target: &pl}).Steps[0]
	if strings.Join(a, " ") != "rex --autostart=false focus -s "+sessA+" "+blk43 {
		t.Fatalf("%q", a)
	}
	pl.Host = "mbp"
	if a := plan(t, m, core.PlacementRequest{Action: core.PlaceFocus, Item: core.WorkspaceItem{Kind: core.WorkspaceTask, ID: "bd-43", Host: "mbp"}, Target: &pl}).Steps[0]; a[1] != "-S" || a[2] != "mbp" {
		t.Fatalf("%q", a)
	}
	pl.Host = "stranger"
	if _, err := core.PlanPlacement(context.Background(), m, core.PlacementRequest{Action: core.PlaceFocus, Item: core.WorkspaceItem{Kind: core.WorkspaceTask, ID: "bd-43", Host: "stranger"}, Target: &pl}); err == nil || !strings.Contains(err.Error(), "not in the configured list") {
		t.Fatalf("an unconfigured machine is never contacted: %v", err)
	}
	if _, err := core.PlanPlacement(context.Background(), m, core.PlacementRequest{Action: core.PlaceFocus, Item: item("bd-43"), Target: &core.Placement{Session: sessA}}); err == nil {
		t.Fatal("a placement with no pane has nothing to bring forward")
	}
}

func TestLayoutInTheCurrentSessionLeavesOpenTabsAloneAndFocusesTheFirstNew(t *testing.T) {
	f := paneFake(t)
	f.resp["--autostart=false window ls -s "+sessA+" --json"] = resp{out: `{"windows":[{"window_id":"w1","label":"Tasks"},{"window_id":"w2","label":"shell"}]}`}
	m := readyMod(t, f, inSession(nil))
	p := plan(t, m, core.PlacementRequest{Action: core.PlaceLayout, Exe: "/opt/abacus", Cwd: "/work"})
	if len(p.Steps) != 3 {
		t.Fatalf("Board, Leases and Queues are new, Tasks is open: %q", p.Steps)
	}
	var tabs []string
	for i, s := range p.Steps {
		tabs = append(tabs, s[indexOf(s, "-s")+2])
		if got := indexOf(s, "--focus=false") >= 0; got != (i > 0) {
			t.Errorf("step %d focus=false is %v: only the first new tab takes focus: %q", i, got, s)
		}
		if tail := s[indexOf(s, "--")+1:]; len(tail) != 4 || tail[0] != "/opt/abacus" || tail[1] != "tui" || tail[2] != "--tab" {
			t.Errorf("%q", tail)
		}
	}
	if strings.Join(tabs, ",") != "Board,Leases,Queues" {
		t.Fatalf("%v", tabs)
	}
	if !strings.Contains(strings.Join(p.Lines, "\n"), "already open, left alone: Tasks") {
		t.Fatalf("%q", p.Lines)
	}
	f.resp["--autostart=false window ls -s "+sessA+" --json"] = resp{out: `{"windows":[{"label":"board"},{"label":"Tasks"},{"label":"LEASES"},{"label":"Queues"}]}`}
	if _, err := core.PlanPlacement(context.Background(), m, core.PlacementRequest{Action: core.PlaceLayout, Exe: "/opt/abacus"}); err == nil || !strings.Contains(err.Error(), "already open") {
		t.Fatalf("every tab open (any case) is nothing to do: %v", err)
	}
}

func TestLayoutInANewSessionRefusesAnExistingNameAndNeverStartsAServer(t *testing.T) {
	m := readyMod(t, paneFake(t), nil)
	p := plan(t, m, core.PlacementRequest{Action: core.PlaceLayout, Exe: "/opt/abacus", NewSession: "abacus"})
	if len(p.Steps) != 4 || p.Steps[0][2] != "new" || p.Steps[1][2] != "window" || p.Steps[1][6] != "Tasks" {
		t.Fatalf("%q", p.Steps)
	}
	for _, s := range p.Steps {
		if s[1] != "--autostart=false" {
			t.Errorf("%q", s)
		}
	}
	if _, err := core.PlanPlacement(context.Background(), m, core.PlacementRequest{Action: core.PlaceLayout, Exe: "/x", NewSession: "DEMO2"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("%v", err)
	}
	for _, bad := range []string{"-x", "a b", "session:abcdefgh", strings.Repeat("a", 70), "x;y"} {
		if _, err := core.PlanPlacement(context.Background(), m, core.PlacementRequest{Action: core.PlaceLayout, Exe: "/x", NewSession: bad}); err == nil {
			t.Errorf("session name %q must be refused", bad)
		}
	}
	down := &fake{resp: map[string]resp{lsLocal: {exit: 4}, verLocal: {out: fixture(t, "version-14.json")}}}
	if _, err := core.PlanPlacement(context.Background(), readyMod(t, down, nil), core.PlacementRequest{Action: core.PlaceLayout, Exe: "/x", NewSession: "abacus"}); err == nil || !strings.Contains(err.Error(), "never starts one") {
		t.Fatalf("%v", err)
	}
}

// ---- running ----

func TestRunPlacementOutcomes(t *testing.T) {
	argv := append([]string{"rex"}, focusArgv("", sessA, blk43)...)
	key := strings.Join(argv[1:], " ")
	for name, tc := range map[string]struct {
		r       resp
		unknown bool
		ok      bool
		pane    string
	}{
		"ok with a pane": {r: resp{out: fixture(t, "run-json.json")}, ok: true, pane: blk43},
		"ok, no json":    {r: resp{out: "done"}, ok: true},
		"timeout":        {r: resp{err: core.ErrTimedOut}, unknown: true},
		"canceled":       {r: resp{err: core.ErrCanceled}, unknown: true},
		"signal":         {r: resp{exit: -1}, unknown: true},
		"exit 1":         {r: resp{exit: 1}, unknown: true},
		"exit 4":         {r: resp{exit: 4}},
		"exit 3":         {r: resp{exit: 3}},
		"exit 2":         {r: resp{exit: 2}},
		"cannot run":     {r: resp{err: errors.New("exec: denied")}},
	} {
		f := &fake{resp: map[string]resp{key: tc.r, verLocal: {out: fixture(t, "version-14.json")}}}
		sr, err := readyMod(t, f, inSession(nil)).RunPlacement(context.Background(), argv)
		switch {
		case tc.ok && (err != nil || sr.Pane != tc.pane):
			t.Errorf("%s: %+v %v", name, sr, err)
		case !tc.ok && err == nil:
			t.Errorf("%s: want an error", name)
		case !tc.ok && errors.Is(err, core.ErrOutcomeUnknown) != tc.unknown:
			t.Errorf("%s: unknown=%v but %v", name, tc.unknown, err)
		}
	}
	if paneOf([]byte(fixture(t, "new-json.json"))) != "block:01a20c2b-d49c-7c21-96ab-d4034be68bcc" || paneOf([]byte(fixture(t, "split-json.json"))) != blk44 || paneOf([]byte(`{"block_ids":["x y"]}`)) != "" {
		t.Error("the pane of a created terminal comes from the real --json answers, and only if it is a pane id")
	}
}

func TestAWriteTimeoutIsBoundedAndUnknown(t *testing.T) {
	f := &fake{hang: true}
	m := New(Options{LookPath: func(string) (string, error) { return "/fake/rex", nil }, Getenv: func(string) string { return "" }, Runner: f, WriteTimeout: 20 * time.Millisecond})
	m.Find("rex", "", func(string) (string, error) { return "/fake/rex", nil })
	start := time.Now()
	_, err := m.RunPlacement(context.Background(), append([]string{"rex"}, focusArgv("", sessA, blk43)...))
	if !errors.Is(err, core.ErrOutcomeUnknown) || time.Since(start) > time.Second {
		t.Fatalf("%v after %s", err, time.Since(start))
	}
}

// ---- terminal reads ----

func capPlacement() core.Placement {
	return core.Placement{Item: item("bd-43"), Session: sessA, Block: blk43}
}

func capKey(host string) string {
	return strings.Join(captureArgv(host, sessA, blk43), " ")
}

func TestCaptureReadsOnlyAndCleansWhatATerminalPrints(t *testing.T) {
	hostile := "ok\n\x1b]0;pwned\x07\x1b[2Jred\x1b[31m text\x00\n" + strings.Repeat("w", 1000) + "\n\n\n"
	f := &fake{resp: map[string]resp{capKey(""): {out: hostile}}}
	c, err := newMod(f, nil).Capture(context.Background(), capPlacement())
	if err != nil || len(c.Lines) != 3 || c.Lines[0] != "ok" || !c.Truncated {
		t.Fatalf("%+v %v", c, err)
	}
	for _, l := range c.Lines {
		if strings.ContainsAny(l, "\x1b\x00\x07") || len([]rune(l)) > core.CaptureMaxLineRunes {
			t.Errorf("%q", l)
		}
	}
	if got := f.called(); len(got) != 1 || got[0] != capKey("") || !strings.Contains(got[0], "capture -s") || strings.Contains(got[0], "send") {
		t.Fatalf("%v", got)
	}
	// A real capture.
	f = &fake{resp: map[string]resp{capKey(""): {out: fixture(t, "capture.txt")}}}
	if c, _ := newMod(f, nil).Capture(context.Background(), capPlacement()); len(c.Lines) != 1 || c.Lines[0] != "working on bd-43" {
		t.Fatalf("%+v", c)
	}
}

func TestCaptureOfAVeryLongTerminalKeepsTheEndAndSaysSo(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 1000; i++ {
		b.WriteString("line ")
		b.WriteString(strings.Repeat("x", i%7))
		b.WriteString("\n")
	}
	f := &fake{resp: map[string]resp{capKey(""): {out: b.String(), err: core.ErrOutputTruncated}}}
	c, err := newMod(f, nil).Capture(context.Background(), capPlacement())
	if err != nil || len(c.Lines) != core.CaptureMaxLines || !c.Truncated {
		t.Fatalf("%d lines %v %v", len(c.Lines), c.Truncated, err)
	}
}

func TestCaptureFailuresAndMachines(t *testing.T) {
	ctx := context.Background()
	for name, r := range map[string]resp{"exit 1": {exit: 1}, "exit 3": {exit: 3}, "error": {err: errors.New("x")}, "timeout": {err: core.ErrTimedOut}} {
		if _, err := newMod(&fake{resp: map[string]resp{capKey(""): r}}, nil).Capture(ctx, capPlacement()); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if _, err := newMod(&fake{resp: map[string]resp{capKey(""): {exit: 4}}}, nil).Capture(ctx, capPlacement()); !errors.Is(err, errNoServer) {
		t.Errorf("%v", err)
	}
	f := &fake{resp: map[string]resp{capKey("mbp"): {out: "hi\n"}}}
	p := capPlacement()
	p.Host = "mbp"
	if c, err := newMod(f, nil).Capture(ctx, p); err != nil || len(c.Lines) != 1 {
		t.Fatalf("a configured machine is read with -S: %+v %v", c, err)
	}
	p.Host = "stranger"
	if _, err := newMod(f, nil).Capture(ctx, p); err == nil || len(f.called()) != 1 {
		t.Fatalf("an unconfigured machine is never contacted: %v %v", err, f.called())
	}
	if _, err := newMod(f, nil).Capture(ctx, core.Placement{Session: sessA}); err == nil {
		t.Fatal("no pane, no read")
	}
	bad := capPlacement()
	bad.Block = "block:x y"
	if _, err := newMod(f, nil).Capture(ctx, bad); err == nil || len(f.called()) != 1 {
		t.Fatalf("a pane id that is not one is refused before rex runs: %v", err)
	}
}

// ---- typed state ----

func TestStatusCarriesTypedStateAndTheWindow(t *testing.T) {
	m := readyMod(t, paneFake(t), inSession(nil))
	st := m.Status(context.Background())
	w := st.Workspace
	if w == nil || w.Connection != core.Connected || w.APIRevision != 14 || w.SupportedMin != MinAPIRevision || w.SupportedMax != MaxAPIRevision || !w.Compatible || !w.InSession || w.Live || w.Server != "environment" {
		t.Fatalf("%+v", w)
	}
	down := readyMod(t, &fake{resp: map[string]resp{lsLocal: {exit: 4}, verLocal: {out: fixture(t, "version-14.json")}}}, nil)
	st = down.Status(context.Background())
	if st.Workspace == nil || st.Workspace.Connection != core.NotConnected || st.Workspace.InSession || !strings.Contains(st.HowToConnect, "optional") {
		t.Fatalf("%+v", st)
	}
	if c := m.Doctor(context.Background()); !strings.Contains(c[0].Detail, "checked against 10 to 14") {
		t.Fatalf("%+v", c)
	}
}

// ---- the change feed ----

type feed struct {
	mu    sync.Mutex
	runs  int
	lines [][]string // one script per connection
	args  [][]string
	hold  bool // the last connection stays up until canceled
}

func (f *feed) stream(ctx context.Context, name string, args []string, onLine func([]byte)) (int, error) {
	f.mu.Lock()
	i := f.runs
	f.runs++
	f.args = append(f.args, args)
	var script []string
	if i < len(f.lines) {
		script = f.lines[i]
	}
	last := i >= len(f.lines)-1
	hold := f.hold
	f.mu.Unlock()
	for _, l := range script {
		onLine([]byte(l))
	}
	if last && hold {
		<-ctx.Done()
		return -1, ctx.Err()
	}
	return 4, nil
}

func (f *feed) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.runs }

func fast(t *testing.T) {
	t.Helper()
	a, b, c, d := watchBackoffMin, watchBackoffMax, watchBackoffIdleMax, watchDebounce
	watchBackoffMin, watchBackoffMax, watchBackoffIdleMax, watchDebounce = 5*time.Millisecond, 20*time.Millisecond, 20*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { watchBackoffMin, watchBackoffMax, watchBackoffIdleMax, watchDebounce = a, b, c, d })
}

func watchMod(t *testing.T, fd *feed, f *fake) *Module {
	t.Helper()
	m := New(Options{LookPath: func(string) (string, error) { return "/fake/rex", nil }, Getenv: func(string) string { return "" }, Runner: f,
		Stream: fd.stream, EventSettle: time.Millisecond, CacheTTL: time.Hour})
	detect(m)
	return m
}

func TestOnlyEventsThatChangeAReadCountAndRealEventsAreClassifiedRight(t *testing.T) {
	lines := strings.Split(strings.TrimSpace(fixture(t, "events.jsonl")), "\n")
	var yes, no int
	for _, l := range lines {
		if relevantEvent([]byte(l)) {
			yes++
		} else {
			no++
		}
	}
	if yes == 0 || no == 0 {
		t.Fatalf("the captured events hold both kinds: %d %d", yes, no)
	}
	for _, l := range lines {
		if strings.Contains(l, `"client_connected"`) || strings.Contains(l, `"client_disconnected"`) || strings.Contains(l, `"block_owner_changed"`) {
			if relevantEvent([]byte(l)) {
				t.Errorf("a client connection (every rex command makes one, abacus's reads included) must not count: %s", l)
			}
		}
	}
	for _, l := range []string{"", "not json", "{}", `{"type":"session_event"}`, `{"type":"session_event","payload":{"event":{"type":"future_thing"}}}`, `[1]`} {
		if relevantEvent([]byte(l)) {
			t.Errorf("%q", l)
		}
	}
	for _, l := range []string{`{"type":"session_created","payload":{}}`, `{"type":"session_destroyed"}`, `{"type":"session_event","payload":{"event":{"type":"block_closed"}}}`} {
		if !relevantEvent([]byte(l)) {
			t.Errorf("%q", l)
		}
	}
}

func TestWatchInvalidatesAndCallsOnceForABurstAndStopsWhenCanceled(t *testing.T) {
	fast(t)
	fd := &feed{lines: [][]string{{
		`{"type":"session_event","payload":{"session_id":"x","event":{"type":"client_connected","payload":{}}}}`,
		`{"type":"session_created","payload":{}}`,
		`{"type":"session_destroyed","payload":{}}`,
		`{"type":"session_event","payload":{"event":{"type":"block_created"}}}`,
	}}, hold: true}
	f := paneFake(t)
	m := watchMod(t, fd, f)
	items := []core.WorkspaceItem{item("bd-43")}
	m.Workspace(context.Background(), items)
	m.Workspace(context.Background(), items)
	lsCalls := func() int {
		n := 0
		for _, c := range f.called() {
			if c == lsLocal {
				n++
			}
		}
		return n
	}
	if lsCalls() != 2 { // the probe, then one read: the second was cached
		t.Fatalf("ls calls = %d", lsCalls())
	}
	var mu sync.Mutex
	calls := 0
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Watch(ctx, func() { mu.Lock(); calls++; mu.Unlock() }); close(done) }()
	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		n := calls
		mu.Unlock()
		if n >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the feed never called back")
		case <-time.After(5 * time.Millisecond):
		}
	}
	time.Sleep(60 * time.Millisecond)
	mu.Lock()
	if calls != 1 {
		t.Errorf("a burst of events is one call, got %d", calls)
	}
	mu.Unlock()
	if !m.live.Load() {
		t.Error("the feed counts as connected once it has stayed up")
	}
	m.Workspace(context.Background(), items)
	if lsCalls() != 3 {
		t.Errorf("an event drops the cache: ls calls = %d", lsCalls())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Watch must return when its context ends")
	}
	if m.live.Load() {
		t.Error("a stopped feed is not live")
	}
	for _, a := range fd.args {
		if strings.Join(a, " ") != "--autostart=false events --json" {
			t.Errorf("the feed ran %q: it must never start a server or widen what it follows", a)
		}
	}
}

func TestWatchReconnectsWithBackoffAndIsQuietWhileThereIsNoServer(t *testing.T) {
	fast(t)
	fd := &feed{} // every connection exits at once, like no server running
	m := watchMod(t, fd, paneFake(t))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	m.Watch(ctx, func() { t.Error("no event, no call") })
	n := fd.count()
	if n < 3 || n > 40 {
		t.Fatalf("reconnects = %d: it retries, with back-off, and does not spin", n)
	}
	if m.live.Load() {
		t.Fatal("never connected, never live")
	}
}

func TestWatchWithoutARexBinaryRunsNothing(t *testing.T) {
	fd := &feed{}
	m := New(Options{LookPath: func(string) (string, error) { return "", errors.New("no") }, Getenv: func(string) string { return "" }, Runner: &fake{}, Stream: fd.stream})
	detect(m)
	done := make(chan struct{})
	go func() { m.Watch(context.Background(), func() {}); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Watch with nothing to run returns at once")
	}
	if fd.count() != 0 {
		t.Fatal("nothing may run")
	}
}

func TestLiveFeedLengthensTheCacheAndLosingItRestoresPolling(t *testing.T) {
	f := paneFake(t)
	now := time.Unix(1000, 0)
	m := New(Options{LookPath: func(string) (string, error) { return "/fake/rex", nil }, Getenv: func(string) string { return "" }, Runner: f,
		Hostname: func() string { return "me" }, Now: func() time.Time { return now }})
	detect(m)
	items := []core.WorkspaceItem{item("bd-43")}
	ls := func() int {
		n := 0
		for _, c := range f.called() {
			if c == lsLocal {
				n++
			}
		}
		return n
	}
	m.Workspace(context.Background(), items) // probe + read = 2
	now = now.Add(10 * time.Second)
	m.Workspace(context.Background(), items) // polling: 3 s has passed
	if ls() != 3 {
		t.Fatalf("polling: %d", ls())
	}
	m.live.Store(true)
	now = now.Add(30 * time.Second)
	m.Workspace(context.Background(), items)
	if ls() != 3 {
		t.Fatalf("live: still cached after 30 s: %d", ls())
	}
	now = now.Add(2 * time.Minute)
	m.Workspace(context.Background(), items)
	if ls() != 4 {
		t.Fatalf("live: the ceiling bounds staleness: %d", ls())
	}
}

func indexOf(a []string, s string) int {
	for i, x := range a {
		if x == s {
			return i
		}
	}
	return -1
}

func TestAReadThatStartedBeforeAnInvalidationIsNotStored(t *testing.T) {
	f := paneFake(t)
	now := time.Unix(1000, 0)
	var m *Module
	started := make(chan struct{})
	release := make(chan struct{})
	slow := &gate{inner: f, key: lsLocal, started: started, release: release}
	m = New(Options{LookPath: func(string) (string, error) { return "/fake/rex", nil }, Getenv: func(k string) string { return map[string]string{"REX_SESSION": sessA}[k] },
		Runner: slow, Hostname: func() string { return "me" }, CacheTTL: time.Hour, Now: func() time.Time { return now }})
	detect(m)
	slow.armed.Store(true)
	done := make(chan struct{})
	go func() { m.Workspace(context.Background(), []core.WorkspaceItem{item("bd-43")}); close(done) }()
	<-started
	m.Invalidate() // a change lands while the read is in flight
	close(release)
	<-done
	before := len(f.called())
	slow.armed.Store(false)
	m.Workspace(context.Background(), []core.WorkspaceItem{item("bd-43")})
	n := 0
	for _, c := range f.called()[before:] {
		if c == lsLocal {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the stale read must not have been cached: ls calls after = %d", n)
	}
}

// gate holds one call on key until released, so a test can act while it is in flight.
type gate struct {
	inner   *fake
	key     string
	started chan struct{}
	release chan struct{}
	armed   atomic.Bool
}

func (g *gate) Run(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	out, exit, err := g.inner.Run(ctx, name, args...)
	if g.armed.Swap(false) && strings.Join(args, " ") == g.key {
		close(g.started)
		<-g.release
	}
	return out, exit, err
}

func TestTheFeedInvalidatesOnConnectAndNeverLeavesLiveSetAfterItEnds(t *testing.T) {
	fast(t)
	fd := &feed{lines: [][]string{nil}, hold: true}
	f := paneFake(t)
	m := watchMod(t, fd, f)
	items := []core.WorkspaceItem{item("bd-43")}
	m.Workspace(context.Background(), items)
	m.Workspace(context.Background(), items)
	ls := func() int {
		n := 0
		for _, c := range f.called() {
			if c == lsLocal {
				n++
			}
		}
		return n
	}
	before := ls()
	ctx, cancel := context.WithCancel(context.Background())
	calls := make(chan struct{}, 8)
	done := make(chan struct{})
	go func() { m.Watch(ctx, func() { calls <- struct{}{} }); close(done) }()
	select {
	case <-calls:
	case <-time.After(3 * time.Second):
		t.Fatal("connecting must ask for a re-read")
	}
	m.Workspace(context.Background(), items)
	if ls() != before+1 {
		t.Fatalf("what was cached before the subscription is not trusted: %d -> %d", before, ls())
	}
	cancel()
	<-done
	if m.live.Load() {
		t.Fatal("live with no feed")
	}
}

func TestLiveIsNeverSetByASettleTimerThatFiresAfterTheStreamEnded(t *testing.T) {
	fast(t)
	for i := 0; i < 50; i++ {
		fd := &feed{} // exits at once
		m := New(Options{LookPath: func(string) (string, error) { return "/fake/rex", nil }, Getenv: func(string) string { return "" }, Runner: paneFake(t),
			Stream: fd.stream, EventSettle: time.Microsecond * time.Duration(1+i*20), CacheTTL: time.Hour})
		detect(m)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		m.Watch(ctx, func() {})
		cancel()
		if m.live.Load() {
			t.Fatalf("round %d: live after the feed ended", i)
		}
	}
}

func TestWritesAreHeldToTheConfiguredMachinesIncludingTheServerTheEnvironmentNames(t *testing.T) {
	r := core.PlacementRequest{Action: core.PlaceWork, Item: item("bd-7"), Title: "t", Template: []string{"agent"}}
	for server, ok := range map[string]bool{"": true, "unix:///tmp/x/s.sock": true, "/tmp/x.sock": true, "mbp": true, "me@MBP:7777": true, "tcp://mbp:9": true, "evil.example": false, "ssh://u@evil:22": false} {
		m := readyMod(t, paneFake(t), inSession(map[string]string{"REX_SERVER": server}))
		p, err := core.PlanPlacement(context.Background(), m, r)
		if (err == nil) != ok {
			t.Errorf("REX_SERVER=%q: %v", server, err)
			continue
		}
		if ok && !strings.Contains(strings.Join(p.Lines, "\n"), "server: ") {
			t.Errorf("the confirm screen names the server: %q", p.Lines)
		}
		if !ok {
			if err := m.CheckPlacement([][]string{append([]string{"rex"}, runArgv(sessA, "bd-7", "", []string{"agent"})...)}); err == nil {
				t.Errorf("REX_SERVER=%q: the check also refuses a hand-built step", server)
			}
		}
	}
	m := readyMod(t, paneFake(t), nil)
	focus := append([]string{"rex"}, focusArgv("stranger", sessA, blk43)...)
	if err := m.CheckPlacement([][]string{focus}); err == nil {
		t.Error("a hand-built focus for an unconfigured machine is refused")
	}
	if _, err := m.RunPlacement(context.Background(), focus); err == nil {
		t.Error("and RunPlacement refuses it too")
	}
	if err := m.CheckPlacement(nil); err == nil {
		t.Error("no steps")
	}
}
