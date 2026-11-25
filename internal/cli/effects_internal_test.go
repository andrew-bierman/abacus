package cli

import (
	"strings"
	"testing"
)

// Every registered command, verb and subverb has a classification, so a new one cannot slip past demo mode.
func TestEveryRegisteredCommandIsClassified(t *testing.T) {
	var want []string
	for _, c := range topCommands {
		if !verbCommands[c] {
			want = append(want, c)
		}
	}
	for c, verbs := range map[string][]string{"tasks": taskVerbs, "board": boardVerbs, "config": configVerbs, "rex": rexVerbs} {
		for _, v := range verbs {
			want = append(want, c+" "+v)
		}
	}
	for _, k := range want {
		if _, ok := effects[k]; !ok {
			t.Errorf("%q has no entry in the effects table", k)
		}
		args := strings.Fields(k)
		if _, mutates := mutation(args); mutates {
			if eff, _ := mutation(args); eff == reads {
				t.Errorf("%q: inconsistent", k)
			}
		}
	}
	for _, c := range topCommands {
		if verbCommands[c] != (c == "tasks" || c == "board" || c == "config" || c == "rex") {
			t.Errorf("verbCommands disagrees about %q", c)
		}
	}
	// the table has nothing the registry does not know
	known := map[string]bool{}
	for _, k := range want {
		known[k] = true
	}
	for k := range effects {
		if !known[k] && !verbCommands[k] {
			t.Errorf("effects has %q, which is not a registered command", k)
		}
	}
	// a verb or command nobody classified is treated as mutating
	for _, a := range [][]string{{"tasks", "newverb"}, {"newcommand"}, {"board", "newverb"}} {
		if eff, mutates := mutation(a); !mutates || eff != changes {
			t.Errorf("%v must fail closed", a)
		}
	}
}

// serve is long-lived: the gate lets `serve --demo` start (it reads), and the server itself refuses every
// plan and apply in demo mode (see internal/serve TestDemoModeServesReadsAndRefusesEveryWrite).
func TestServeIsClassifiedAsReads(t *testing.T) {
	if r, ok := effects["serve"]; !ok || r.effect != reads {
		t.Fatalf("serve: %+v", r)
	}
	if _, mutates := mutation([]string{"serve", "--stdio", "--demo"}); mutates {
		t.Error("the gate must not refuse serve --demo itself")
	}
}
