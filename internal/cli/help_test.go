package cli_test

import (
	"regexp"
	"strings"
	"testing"
)

// Every command the usage lists answers --help and `help CMD` on stdout with exit 0 and nothing on stderr,
// and the tasks verbs each have their own usage line.
func TestHelpForEveryCommandAndVerb(t *testing.T) {
	code, usageOut, errOut := run(t, nil, "--help")
	if code != 0 || errOut != "" || !strings.Contains(usageOut, "exit codes:") {
		t.Fatalf("--help: code=%d err=%q", code, errOut)
	}
	cmds := regexp.MustCompile(`(?m)^  ([a-z]+) {2,}`).FindAllStringSubmatch(usageOut, -1)
	if len(cmds) < 14 {
		t.Fatalf("usage lists %d commands:\n%s", len(cmds), usageOut)
	}
	var args [][]string
	for _, c := range cmds {
		args = append(args, []string{c[1], "--help"}, []string{"help", c[1]})
	}
	for _, v := range []string{"list", "show", "board", "graph", "history", "create", "update", "close", "label", "assign", "priority", "dep", "comment", "bulk"} {
		args = append(args, []string{"tasks", v, "-h"})
	}
	for _, a := range args {
		code, out, errOut := run(t, nil, a...)
		if code != 0 || errOut != "" || !strings.Contains(strings.ToLower(out), "usage") {
			t.Errorf("%v: code=%d out=%q err=%q", a, code, out, errOut)
		}
	}
}

func TestCompletionScripts(t *testing.T) {
	for _, sh := range []string{"bash", "zsh", "fish"} {
		code, out, _ := run(t, nil, "completion", sh)
		if code != 0 || !strings.Contains(out, "tasks") || !strings.Contains(out, "bulk") {
			t.Errorf("%s: code=%d out=%q", sh, code, out)
		}
	}
	for _, a := range [][]string{{"completion"}, {"completion", "powershell"}} {
		if code, out, _ := run(t, nil, a...); code != 2 || out != "" {
			t.Errorf("%v: code=%d out=%q", a, code, out)
		}
	}
}
