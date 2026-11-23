package core

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/andrew-bierman/abacus/internal/query"
)

// The `:` command prompt's command table. The prompt only navigates: every command resolves to a
// typed Action of a navigation kind (go to a tab or a Tasks view, open search, apply a list
// filter, show the context, help, quit). There is no kind that writes or runs a tool, and
// validateRegistry (run when the package loads) refuses a registry that has one, so the table is the
// whole of what the prompt can ask for. This file is pure: no UI, no I/O, no clock.

// ActionKind is what a command asks the front-end to do.
type ActionKind string

const (
	ActGoTab   ActionKind = "tab"     // select a tab
	ActGoView  ActionKind = "view"    // select a tab, then one of its views
	ActSearch  ActionKind = "search"  // open global search, optionally with text
	ActFilter  ActionKind = "filter"  // apply (or clear) the Tasks list filter
	ActContext ActionKind = "context" // say which tracker and board this is
	ActHelp    ActionKind = "help"    // open the help overlay
	ActQuit    ActionKind = "quit"
)

// navigationKinds is the closed set of kinds a command may have; none changes the tracker, the board,
// a lease or a queue, and none runs a tool (the front-end may keep its own UI layout state, such as
// which Tasks view or fold state was last shown). A new kind is added here on purpose, with its test, or not at all.
var navigationKinds = map[ActionKind]bool{
	ActGoTab: true, ActGoView: true, ActSearch: true, ActFilter: true, ActContext: true, ActHelp: true, ActQuit: true,
}

// ArgSpec is a command's optional argument.
type ArgSpec struct {
	Name     string // "" for a command without one
	Required bool
}

// Command is one entry of the table.
type Command struct {
	Name    string
	Aliases []string
	Arg     ArgSpec
	Desc    string
	Group   string // "tabs", "views" or "other": how help lists it
	Kind    ActionKind
	Tab     string // the tab title the action targets (ActGoTab, ActGoView, ActFilter)
	View    string // the Tasks view (ActGoView): list, tree, kanban, graph, insights
}

var commands = []Command{
	{Name: "tasks", Aliases: []string{"task"}, Group: "tabs", Kind: ActGoTab, Tab: "Tasks", Desc: "go to the Tasks tab"},
	{Name: "board", Group: "tabs", Kind: ActGoTab, Tab: "Board", Desc: "go to the Board tab"},
	{Name: "leases", Aliases: []string{"lease"}, Group: "tabs", Kind: ActGoTab, Tab: "Leases", Desc: "go to the Leases tab"},
	{Name: "queues", Aliases: []string{"queue"}, Group: "tabs", Kind: ActGoTab, Tab: "Queues", Desc: "go to the Queues tab"},
	{Name: "doctor", Group: "tabs", Kind: ActGoTab, Tab: "Doctor", Desc: "go to the Doctor tab"},
	{Name: "list", Aliases: []string{"ls"}, Group: "views", Kind: ActGoView, Tab: "Tasks", View: "list", Desc: "Tasks: the flat list"},
	{Name: "tree", Group: "views", Kind: ActGoView, Tab: "Tasks", View: "tree", Desc: "Tasks: epics and their children"},
	{Name: "kanban", Group: "views", Kind: ActGoView, Tab: "Tasks", View: "kanban", Desc: "Tasks: columns by status"},
	{Name: "graph", Group: "views", Kind: ActGoView, Tab: "Tasks", View: "graph", Desc: "Tasks: the dependency graph"},
	{Name: "insights", Group: "views", Kind: ActGoView, Tab: "Tasks", View: "insights", Desc: "Tasks: next picks, blockers, critical path"},
	{Name: "search", Aliases: []string{"find"}, Arg: ArgSpec{Name: "TEXT"}, Group: "other", Kind: ActSearch, Desc: "search every tab, optionally for TEXT"},
	{Name: "filter", Arg: ArgSpec{Name: "QUERY"}, Group: "other", Kind: ActFilter, Tab: "Tasks", Desc: "filter the Tasks list (no query clears it)"},
	{Name: "ctx", Aliases: []string{"context"}, Arg: ArgSpec{Name: "NAME"}, Group: "other", Kind: ActContext, Desc: "show the tracker and board in use"},
	{Name: "help", Aliases: []string{"?"}, Group: "other", Kind: ActHelp, Desc: "keys and commands"},
	{Name: "q", Aliases: []string{"quit", "exit"}, Group: "other", Kind: ActQuit, Desc: "quit"},
}

func init() {
	if err := validateRegistry(commands); err != nil {
		panic("command registry: " + err.Error())
	}
}

// Commands returns a copy of the table, in help order.
func Commands() []Command {
	out := make([]Command, len(commands))
	for i, c := range commands {
		c.Aliases = append([]string(nil), c.Aliases...)
		out[i] = c
	}
	return out
}

// CommandGroup is a titled run of commands, as help lists them.
type CommandGroup struct {
	Title    string
	Commands []Command
}

// CommandGroups is the table by group, in help order.
func CommandGroups() []CommandGroup {
	titles := []struct{ id, title string }{{"tabs", "Go to a tab"}, {"views", "Tasks views"}, {"other", "Other"}}
	var out []CommandGroup
	for _, g := range titles {
		cg := CommandGroup{Title: g.title}
		for _, c := range Commands() {
			if c.Group == g.id {
				cg.Commands = append(cg.Commands, c)
			}
		}
		out = append(out, cg)
	}
	return out
}

// forbiddenWords are tool names: a command, alias, argument name or description never names a tool
// (navigation says "Tasks", never what is behind it).
var forbiddenWords = []string{"bd", "bv", "lambe", "beads", "beads-lease", "b9s", "rex", "bd-ready"}

func mentionsTool(s string) (string, bool) {
	words := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z')
	})
	for _, w := range words {
		for _, f := range forbiddenWords {
			if w == f {
				return f, true
			}
		}
	}
	return "", false
}

// validateRegistry refuses a table the prompt must not have: a kind that is not navigation, a name
// or alias that is not one lower-case word or is used twice, a tool named anywhere, or a target
// missing.
func validateRegistry(cs []Command) error {
	seen := map[string]string{}
	for _, c := range cs {
		if !navigationKinds[c.Kind] {
			return fmt.Errorf("command %q has kind %q, which is not navigation", c.Name, c.Kind)
		}
		if c.Desc == "" {
			return fmt.Errorf("command %q has no description", c.Name)
		}
		if c.Arg.Required && c.Arg.Name == "" {
			return fmt.Errorf("command %q requires an argument it does not name", c.Name)
		}
		switch c.Kind {
		case ActGoTab, ActGoView, ActFilter:
			if c.Tab == "" {
				return fmt.Errorf("command %q has no tab to go to", c.Name)
			}
		}
		if c.Kind == ActGoView && c.View == "" {
			return fmt.Errorf("command %q has no view", c.Name)
		}
		for _, w := range append(append([]string{c.Name}, c.Aliases...), c.Arg.Name, c.Desc, c.Tab, c.View) {
			if f, bad := mentionsTool(w); bad {
				return fmt.Errorf("command %q names the tool %q in %q", c.Name, f, w)
			}
		}
		for _, w := range append([]string{c.Name}, c.Aliases...) {
			if w == "" || w != strings.ToLower(w) || strings.ContainsAny(w, " \t:") {
				return fmt.Errorf("%q is not one lower-case word", w)
			}
			if prev, dup := seen[w]; dup {
				return fmt.Errorf("%q names both %q and %q", w, prev, c.Name)
			}
			seen[w] = c.Name
		}
	}
	return nil
}

// Action is a parsed command: what the front-end should do. It is typed data; the front-end maps it
// to its own navigation and nothing in it names a tool or a write.
type Action struct {
	Kind ActionKind
	Tab  string // tab title
	View string // Tasks view name
	Arg  string // search text, filter query or context name, as typed (cleaned)
}

// CommandError is why an input is not a command. Suggest is the closest command name, when one is
// near enough.
type CommandError struct {
	Msg     string
	Suggest string
}

func (e *CommandError) Error() string {
	if e.Suggest != "" {
		return e.Msg + ". Did you mean :" + e.Suggest + "?"
	}
	return e.Msg
}

// MaxCommandLen caps what is read of an input line, in runes.
const MaxCommandLen = 300

// echoLen caps how much of a mistyped word an error repeats.
const echoLen = 24

func lookup(word string) (Command, bool) {
	for _, c := range commands {
		if c.Name == word {
			return c, true
		}
		for _, a := range c.Aliases {
			if a == word {
				return c, true
			}
		}
	}
	return Command{}, false
}

// cleanInput drops control and invisible characters and the leading colon a person may retype.
func cleanInput(s string) string {
	s = strings.TrimSpace(Sanitize(s))
	return strings.TrimSpace(strings.TrimPrefix(s, ":"))
}

// ParseCommand turns a typed line (with or without its leading colon) into an Action. A search or
// filter argument is validated here with the shared query grammar, so the front-end never has to
// handle a bad one.
func ParseCommand(input string) (Action, error) {
	if utf8.RuneCountInString(input) > MaxCommandLen {
		return Action{}, &CommandError{Msg: fmt.Sprintf("command too long (more than %d characters)", MaxCommandLen)}
	}
	s := cleanInput(input)
	if s == "" {
		return Action{}, &CommandError{Msg: "empty command (try :help)"}
	}
	word, arg, _ := strings.Cut(s, " ")
	word, arg = strings.ToLower(word), strings.TrimSpace(arg)
	c, ok := lookup(word)
	if !ok {
		shown := word
		if r := []rune(shown); len(r) > echoLen {
			shown = string(r[:echoLen]) + "…"
		}
		near, _ := ClosestCommand(word)
		return Action{}, &CommandError{Msg: "unknown command: " + shown + " (try :help)", Suggest: near}
	}
	if arg != "" && c.Arg.Name == "" {
		return Action{}, &CommandError{Msg: c.Name + " takes no argument (try :help)"}
	}
	if arg == "" && c.Arg.Required {
		return Action{}, &CommandError{Msg: c.Name + " needs " + c.Arg.Name + " (try :help)"}
	}
	switch c.Kind {
	case ActSearch:
		if utf8.RuneCountInString(arg) > query.MaxLen {
			return Action{}, &CommandError{Msg: fmt.Sprintf("search text is limited to at most %d characters", query.MaxLen)}
		}
	case ActFilter:
		if _, err := query.Parse(arg); err != nil {
			return Action{}, &CommandError{Msg: "bad filter: " + Sanitize(err.Error())}
		}
	}
	return Action{Kind: c.Kind, Tab: c.Tab, View: c.View, Arg: arg}, nil
}

// maxEdit is how far a mistyped word may be from a command and still be suggested: up to two edits,
// and never more than half the word, so a one-letter word suggests nothing.
func maxEdit(word string) int { return min(2, utf8.RuneCountInString(word)/2) }

// ClosestCommand is the command name nearest a mistyped word (matching aliases too, reported by their
// command's name), and false when nothing is near enough.
func ClosestCommand(word string) (string, bool) {
	word = strings.ToLower(word)
	limit, best, name := maxEdit(word), -1, ""
	for _, c := range commands {
		for _, w := range append([]string{c.Name}, c.Aliases...) {
			if d := editDistance(word, w); d <= limit && (best < 0 || d < best) {
				best, name = d, c.Name
			}
		}
	}
	return name, best >= 0
}

// editDistance is the optimal-string-alignment distance (insert, delete, substitute, swap two
// neighbors) between a and b, over runes.
func editDistance(a, b string) int {
	x, y := []rune(a), []rune(b)
	d := make([][]int, len(x)+1)
	for i := range d {
		d[i] = make([]int, len(y)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(x); i++ {
		for j := 1; j <= len(y); j++ {
			cost := 1
			if x[i-1] == y[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && x[i-1] == y[j-2] && x[i-2] == y[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(x)][len(y)]
}

// CompleteCommand lists the full-line completions of input, best first: the command words that
// start with it (an exact match first, then table order, names before their aliases; a command that
// takes an argument ends in a space), or, inside a search or filter, the field names and member values
// that complete the word being typed. No candidates means nothing to complete.
func CompleteCommand(input string) []string {
	s := strings.TrimLeft(Sanitize(input), " :")
	word, rest, hasArg := strings.Cut(s, " ")
	if !hasArg {
		return completeWord(strings.ToLower(word))
	}
	c, ok := lookup(strings.ToLower(word))
	if !ok || (c.Kind != ActFilter && c.Kind != ActSearch) {
		return nil
	}
	start := strings.LastIndex(rest, " ") + 1
	head, tok := rest[:start], rest[start:]
	if tok == "" || strings.Contains(tok, `"`) {
		return nil
	}
	prefix := ""
	for strings.HasPrefix(tok, "!") || strings.HasPrefix(tok, "-") {
		prefix, tok = prefix+tok[:1], tok[1:]
	}
	lead := word + " " + head + prefix
	var out []string
	if name, val, ok := strings.Cut(tok, ":"); ok {
		// a member field takes a comma list (is:blocked,ready): complete the last segment, skipping
		// the values already chosen
		cut := strings.LastIndex(val, ",") + 1
		done, last := val[:cut], strings.ToLower(val[cut:])
		chosen := strings.Split(strings.TrimSuffix(done, ","), ",")
		for _, v := range query.Values(name) {
			if strings.HasPrefix(v, last) && !slices.Contains(chosen, v) {
				out = append(out, lead+name+":"+done+v)
			}
		}
		return out
	}
	for _, f := range query.Fields() {
		if strings.HasPrefix(f, strings.ToLower(tok)) {
			out = append(out, lead+f+":")
		}
	}
	return out
}

func completeWord(word string) []string {
	var out []string
	add := func(w string, c Command) {
		if w != "" && strings.HasPrefix(w, word) {
			if c.Arg.Name != "" {
				w += " "
			}
			out = append(out, w)
		}
	}
	if word == "" { // the whole table by name
		for _, c := range commands {
			add(c.Name, c)
		}
		return out
	}
	if c, ok := lookup(word); ok {
		add(word, c)
	}
	for _, c := range commands { // names first, then aliases, each in table order
		if c.Name != word {
			add(c.Name, c)
		}
	}
	for _, c := range commands {
		for _, a := range c.Aliases {
			if a != word {
				add(a, c)
			}
		}
	}
	return out
}

// CommonPrefix is the longest prefix every string shares (by rune).
func CommonPrefix(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	p := []rune(ss[0])
	for _, s := range ss[1:] {
		r := []rune(s)
		n := 0
		for n < len(p) && n < len(r) && p[n] == r[n] {
			n++
		}
		p = p[:n]
	}
	return string(p)
}
