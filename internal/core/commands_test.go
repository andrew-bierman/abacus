package core

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseCommandTable(t *testing.T) {
	cases := []struct {
		in   string
		want Action
	}{
		{"tasks", Action{Kind: ActGoTab, Tab: "Tasks"}},
		{":tasks", Action{Kind: ActGoTab, Tab: "Tasks"}},
		{"  TASKS  ", Action{Kind: ActGoTab, Tab: "Tasks"}},
		{"task", Action{Kind: ActGoTab, Tab: "Tasks"}},
		{"board", Action{Kind: ActGoTab, Tab: "Board"}},
		{"leases", Action{Kind: ActGoTab, Tab: "Leases"}},
		{"lease", Action{Kind: ActGoTab, Tab: "Leases"}},
		{"queues", Action{Kind: ActGoTab, Tab: "Queues"}},
		{"queue", Action{Kind: ActGoTab, Tab: "Queues"}},
		{"doctor", Action{Kind: ActGoTab, Tab: "Doctor"}},
		{"agents", Action{Kind: ActGoTab, Tab: "Agents"}},
		{"agent", Action{Kind: ActGoTab, Tab: "Agents"}},
		{"list", Action{Kind: ActGoView, Tab: "Tasks", View: "list"}},
		{"ls", Action{Kind: ActGoView, Tab: "Tasks", View: "list"}},
		{"tree", Action{Kind: ActGoView, Tab: "Tasks", View: "tree"}},
		{"kanban", Action{Kind: ActGoView, Tab: "Tasks", View: "kanban"}},
		{"graph", Action{Kind: ActGoView, Tab: "Tasks", View: "graph"}},
		{"insights", Action{Kind: ActGoView, Tab: "Tasks", View: "insights"}},
		{"search", Action{Kind: ActSearch}},
		{"search lease renewal", Action{Kind: ActSearch, Arg: "lease renewal"}},
		{"find   a:b  ", Action{Kind: ActSearch, Arg: "a:b"}},
		{"filter status:open label:ui", Action{Kind: ActFilter, Tab: "Tasks", Arg: "status:open label:ui"}},
		{"filter", Action{Kind: ActFilter, Tab: "Tasks"}},
		{"ctx", Action{Kind: ActContext}},
		{"ctx other", Action{Kind: ActContext, Arg: "other"}},
		{"help", Action{Kind: ActHelp}},
		{"?", Action{Kind: ActHelp}},
		{"q", Action{Kind: ActQuit}},
		{"quit", Action{Kind: ActQuit}},
		{"exit", Action{Kind: ActQuit}},
		{"context", Action{Kind: ActContext}},
		{"tasks\x07", Action{Kind: ActGoTab, Tab: "Tasks"}}, // control characters are dropped, not run
	}
	for _, c := range cases {
		got, err := ParseCommand(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseCommand(%q) = %+v, %v; want %+v", c.in, got, err, c.want)
		}
	}
}

func TestParseCommandErrors(t *testing.T) {
	cases := []struct {
		in      string
		msg     string // substring of the error
		suggest string
	}{
		{"x", "unknown command: x (try :help)", ""},
		{"bord", "unknown command: bord (try :help)", "board"},
		{"taks", "unknown command: taks (try :help)", "tasks"},
		{"kanbn", "unknown command: kanbn", "kanban"},
		{"insight", "unknown command: insight", "insights"},
		{"zzzzzzzz", "unknown command: zzzzzzzz", ""},
		{"tasks now", "tasks takes no argument", ""},
		{"filter priority:abc", "bad filter", ""},
		{"filter nosuchfield:x", "bad filter", ""},
		{"search " + strings.Repeat("a", 270), "at most 256 characters", ""},
		{strings.Repeat("a", 400), "too long", ""},
		{"", "empty", ""},
		{"   ", "empty", ""},
		{"close ac-12", "unknown command: close", ""}, // there is no write alias to suggest
	}
	for _, c := range cases {
		_, err := ParseCommand(c.in)
		ce, ok := err.(*CommandError)
		if !ok || !strings.Contains(ce.Error(), c.msg) {
			t.Errorf("ParseCommand(%q) error = %v, want %q", c.in, err, c.msg)
			continue
		}
		if ce.Suggest != c.suggest {
			t.Errorf("ParseCommand(%q) suggests %q, want %q", c.in, ce.Suggest, c.suggest)
		}
		if c.suggest != "" && !strings.Contains(ce.Error(), "Did you mean :"+c.suggest+"?") {
			t.Errorf("error %q does not name the suggestion", ce.Error())
		}
	}
}

func TestUnknownCommandEchoIsBounded(t *testing.T) {
	_, err := ParseCommand("a‮" + strings.Repeat("é", 100) + " rest")
	if err == nil || len([]rune(err.Error())) > 80 || strings.ContainsRune(err.Error(), '‮') {
		t.Errorf("echo is long or carries a format character: %v", err)
	}
}

func TestEditDistance(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"", "", 0}, {"a", "", 1}, {"", "abc", 3}, {"board", "board", 0}, {"bord", "board", 1},
		{"tsak", "task", 1}, {"taks", "tasks", 1}, {"kitten", "sitting", 3}, {"é", "e", 1},
	} {
		if got := editDistance(c.a, c.b); got != c.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestClosestThreshold(t *testing.T) {
	for _, c := range []struct {
		word, want string
	}{
		{"lq", "list"},                       // a tie (ls and q are one edit away): the table's first command wins
		{"bord", "board"}, {"tsak", "tasks"}, // one swap from the alias "task"
		{"leas", "leases"}, {"x", ""}, {"xy", ""}, {"abc", ""}, {"grap", "graph"}, {"dctor", "doctor"},
		{"treee", "tree"}, {"helps", "help"}, {"queus", "queues"},
	} {
		got, _ := ClosestCommand(c.word) // an alias that matches is reported by its command's name
		if got != c.want {
			t.Errorf("ClosestCommand(%q) = %q, want %q", c.word, got, c.want)
		}
	}
}

func TestCompleteCommandTable(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", []string{"tasks", "board", "leases", "queues", "doctor", "agents", "list", "tree", "kanban", "graph", "insights", "search ", "filter ", "ctx ", "help", "q"}},
		{"ta", []string{"tasks", "task"}},
		{"tas", []string{"tasks", "task"}},
		{"tasks", []string{"tasks"}},
		{"task", []string{"task", "tasks"}},
		{"search", []string{"search "}},
		{"ka", []string{"kanban"}},
		{"se", []string{"search "}},
		{"fil", []string{"filter "}},
		{"q", []string{"q", "queues", "queue", "quit"}},
		{"zz", nil},
		{"TA", []string{"tasks", "task"}},
		{"tasks ", nil},
		{"filter st", []string{"filter status:"}},
		{"filter status:o", nil}, // status takes free text: nothing to offer
		{"filter is:", []string{"filter is:blocked", "filter is:ready", "filter is:stale"}},
		{"filter is:b", []string{"filter is:blocked"}},
		{"filter !is:r", []string{"filter !is:ready"}},
		{"filter -pri", []string{"filter -priority:"}},
		{"search in:", []string{"search in:tasks", "search in:board", "search in:leases", "search in:queues"}},
		{"filter label:ui st", []string{"filter label:ui status:"}},
		{"filter ar", nil}, // "parent" contains "ar" but does not start with it
		{"ctx ", nil},
		{"filter is:blocked,re", []string{"filter is:blocked,ready"}},
		{"filter has:comments,la", []string{"filter has:comments,labels"}},
		{"search is:b,r", []string{"search is:b,ready"}},
		{"filter is:blocked,", []string{"filter is:blocked,ready", "filter is:blocked,stale"}},
		{"filter is:x,s", []string{"filter is:x,stale"}},
		{"filter is:blocked,b", nil}, // already chosen
	}
	for _, c := range cases {
		got := CompleteCommand(c.in)
		if len(got) == 0 && len(c.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("CompleteCommand(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCommonPrefix(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{{nil, ""}, {[]string{"tasks"}, "tasks"}, {[]string{"tasks", "task"}, "task"}, {[]string{"task", "tasks"}, "task"}, {[]string{"ab", "ab"}, "ab"}, {[]string{"a", "b"}, ""}, {[]string{"é1", "é2"}, "é"}} {
		if got := CommonPrefix(c.in); got != c.want {
			t.Errorf("CommonPrefix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRegistryIsValid(t *testing.T) {
	if err := validateRegistry(Commands()); err != nil {
		t.Fatal(err)
	}
}

// The guard that keeps the prompt navigation-only: every way a registry can go wrong is refused.
func TestRegistryGuardRefuses(t *testing.T) {
	base := func() []Command { return Commands() }
	mut := map[string]func([]Command) []Command{
		"a write kind": func(c []Command) []Command {
			return append(c, Command{Name: "close", Kind: "write", Desc: "close the task"})
		},
		"an empty kind":        func(c []Command) []Command { c[0].Kind = ""; return c },
		"a duplicate alias":    func(c []Command) []Command { c[1].Aliases = append(c[1].Aliases, "task"); return c },
		"a duplicate name":     func(c []Command) []Command { c[1].Name = c[0].Name; return c },
		"an upper-case alias":  func(c []Command) []Command { c[0].Aliases = []string{"Task"}; return c },
		"a spaced name":        func(c []Command) []Command { c[0].Name = "my tasks"; return c },
		"a tool in the name":   func(c []Command) []Command { c[0].Name = "bd"; return c },
		"a tool in an alias":   func(c []Command) []Command { c[0].Aliases = []string{"beads-lease"}; return c },
		"a tool in the text":   func(c []Command) []Command { c[0].Desc = "go to Tasks (bd)"; return c },
		"a tool in an arg":     func(c []Command) []Command { c[11].Arg.Name = "lambe"; return c },
		"a tab with no title":  func(c []Command) []Command { c[0].Tab = ""; return c },
		"a view with no name":  func(c []Command) []Command { c[6].View = ""; return c },
		"no description":       func(c []Command) []Command { c[0].Desc = ""; return c },
		"an alias of a name":   func(c []Command) []Command { c[0].Aliases = []string{c[1].Name}; return c },
		"a required empty arg": func(c []Command) []Command { c[0].Arg = ArgSpec{Required: true}; return c },
	}
	for name, f := range mut {
		if err := validateRegistry(f(base())); err == nil {
			t.Errorf("the guard accepted %s", name)
		}
	}
}

// Whatever is typed, the action that comes back is one of the navigation kinds: the prompt has no
// way to ask for a write or a tool.
func TestParseNeverYieldsAKindOutsideNavigation(t *testing.T) {
	inputs := []string{"tasks", "search x", "filter a", "close ac-1", "rm -rf /", "bd close", "ctx x", "help", "q", "x;y", "tasks && quit", "$(id)", "`id`", "\x00", "claim ac-12", "delete", "y", "yes", "apply"}
	for _, in := range inputs {
		a, err := ParseCommand(in)
		if err == nil && !navigationKinds[a.Kind] {
			t.Errorf("%q yields the kind %q, which is not navigation", in, a.Kind)
		}
	}
	for _, c := range Commands() {
		if !navigationKinds[c.Kind] {
			t.Errorf("command %q has the kind %q", c.Name, c.Kind)
		}
	}
	for k := range navigationKinds {
		switch k {
		case ActGoTab, ActGoView, ActSearch, ActFilter, ActContext, ActHelp, ActQuit:
		default:
			t.Errorf("navigationKinds holds %q", k)
		}
	}
}

func TestCommandGroupsCoverEveryCommand(t *testing.T) {
	seen := map[string]bool{}
	for _, g := range CommandGroups() {
		for _, c := range g.Commands {
			seen[c.Name] = true
		}
	}
	for _, c := range Commands() {
		if !seen[c.Name] {
			t.Errorf("%q is in no group", c.Name)
		}
	}
}

func TestContextLine(t *testing.T) {
	full := ContextLine{Tracker: "api", Board: "https://board.example/x", HaveTasks: true, Tasks: 41, Ready: 9, ReadyKnown: true}
	if got := full.Parts(); !reflect.DeepEqual(got, []string{"api", "41 tasks · 9 ready", "board https://board.example/x"}) {
		t.Errorf("Parts = %q", got)
	}
	if got := (ContextLine{HaveTasks: true, Tasks: 1}).Parts(); !reflect.DeepEqual(got, []string{"1 task"}) {
		t.Errorf("one task, deps unknown = %q", got)
	}
	if got := (ContextLine{Tracker: "a\x1b[31m‮b", Board: " "}).Parts(); !reflect.DeepEqual(got, []string{"a[31mb"}) {
		t.Errorf("hostile = %q", got)
	}
	if got := full.String(); got != "api · 41 tasks · 9 ready · board https://board.example/x" {
		t.Errorf("String = %q", got)
	}
	if (ContextLine{}).String() != "" {
		t.Error("empty context has text")
	}
}
