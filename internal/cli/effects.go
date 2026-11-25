package cli

import (
	"strconv"
	"strings"

	"github.com/andrew-bierman/abacus/internal/core"
)

// What each command does to the world, in one table. dispatch checks it before any command runs, so
// demo mode (--demo, ABACUS_DEMO=1) cannot reach a command that changes something by forgetting a guard in
// that command. A command that is not in the table is treated as mutating (fail closed), and a test
// enumerates every registered command and verb against it.
type effect int

const (
	reads   effect = iota // changes nothing
	planned               // changes something, but --dry-run only plans: allowed in demo mode
	changes               // changes something, no plan to show: refused in demo mode
)

type effectRule struct {
	effect effect
	// when narrows the effect to the command lines it applies to (nil: always). A command line it does
	// not match reads.
	when func(args []string) bool
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		n, v, hasV := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if n != name || !strings.HasPrefix(a, "-") || strings.HasPrefix(a, "---") {
			continue
		}
		if !hasV {
			return true
		}
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return false
}

func anyFlag(names ...string) func([]string) bool {
	return func(args []string) bool {
		for _, n := range names {
			if hasFlag(args, n) {
				return true
			}
		}
		return false
	}
}

// verbCommands are the commands whose first word is a verb. An unlisted verb of one of them is unclassified.
var verbCommands = map[string]bool{"tasks": true, "board": true, "config": true, "rex": true}

// boardVerbs, configVerbs and rexVerbs are the verbs of those commands, besides the tasks verbs (taskVerbs).
var (
	boardVerbs  = []string{"post", "reply"}
	configVerbs = []string{"show", "get", "set", "path"}
	rexVerbs    = []string{"layout"}
)

var effects = func() map[string]effectRule {
	m := map[string]effectRule{}
	for _, c := range []string{"version", "doctor", "status", "tui", "leases", "queues", "agents", "search", "licenses", "completion", "help", "tasks", "board"} {
		m[c] = effectRule{reads, nil}
	}
	m["schema"] = effectRule{changes, anyFlag("out")} // `schema --out DIR` writes files
	m["setup"] = effectRule{planned, anyFlag("yes", "repair")}
	for _, v := range []string{"list", "show", "board", "graph", "history"} {
		m["tasks "+v] = effectRule{reads, nil}
	}
	for _, v := range []string{"create", "update", "close", "reopen", "label", "assign", "priority", "dep", "comment", "bulk"} {
		m["tasks "+v] = effectRule{planned, nil}
	}
	for _, v := range boardVerbs {
		m["board "+v] = effectRule{planned, nil}
	}
	for _, v := range configVerbs {
		m["config "+v] = effectRule{reads, nil}
	}
	m["config set"] = effectRule{changes, nil}
	m["rex layout"] = effectRule{changes, nil} // there is no demo terminal workspace to plan against
	return m
}()

// mutation says how a command line changes things: the rule that applies, or false when it reads.
func mutation(args []string) (effect, bool) {
	cmd, rest := args[0], args[1:]
	key := cmd
	if verbCommands[cmd] {
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			key = cmd + " " + rest[0]
			if _, ok := effects[key]; !ok {
				return changes, true // a verb nobody classified
			}
		} else if cmd == "rex" || cmd == "config" {
			return reads, false // usage error: no verb
		}
	}
	r, ok := effects[key]
	if !ok {
		return changes, true // a command nobody classified
	}
	if r.effect == reads || (r.when != nil && !r.when(rest)) {
		return reads, false
	}
	return r.effect, true
}

// demoRequested reads --demo and ABACUS_DEMO the way the commands do, before any of them parses its flags.
func demoRequested(d Deps, args []string) bool {
	return d.getenv(envDemo) == "1" || hasFlag(args, "demo")
}

// demoGate refuses, in demo mode, every command line that changes something unless it is a --dry-run plan.
func demoGate(d Deps, args []string) (int, bool) {
	if len(args) == 0 || !demoRequested(d, args) {
		return 0, false
	}
	eff, mutates := mutation(args)
	if !mutates || (eff == planned && hasFlag(args, "dry-run")) {
		return 0, false
	}
	if c, ok := d.Out.(*counter); ok {
		c.demo = true
	}
	label := args[0]
	if len(args) > 1 && !strings.HasPrefix(args[1], "-") && verbCommands[args[0]] {
		label += " " + args[1]
	}
	if eff == changes {
		return failf(d, core.CodeRefused, "%s: not available in demo mode (nothing was written)\n", label), true
	}
	return failf(d, core.CodeRefused, "%s: demo data cannot be changed (nothing was written). --dry-run shows the plan\n", label), true
}
