package cli_test

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/cli"
	"github.com/andrew-bierman/abacus/internal/core"
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
	for _, c := range cmds {
		assertHelpParity(t, []string{c[1]})
	}
	for _, v := range []string{"list", "show", "board", "graph", "history", "create", "update", "close", "reopen", "label", "assign", "priority", "dep", "comment", "bulk"} {
		assertHelpParity(t, []string{"tasks", v})
	}
}

func assertHelpParity(t *testing.T, command []string) {
	t.Helper()
	t.Run(strings.Join(command, " "), func(t *testing.T) {
		var want string
		for _, args := range [][]string{
			append(append([]string{}, command...), "--help"),
			append([]string{"help"}, command...),
		} {
			var out, errOut bytes.Buffer
			code := cli.Run(args, cli.Deps{
				Out: &out, Err: &errOut,
				Build: func(cli.Options) *core.Registry {
					t.Fatalf("%v initialized providers", args)
					return nil
				},
			})
			got := out.String()
			if code != 0 || errOut.Len() != 0 || !strings.Contains(strings.ToLower(got), "usage") {
				t.Errorf("%v: code=%d out=%q err=%q", args, code, got, errOut.String())
			}
			if command[0] == "search" && (!strings.Contains(got, "-domain") || !strings.Contains(got, "-limit")) {
				t.Errorf("%v: missing search flags in %q", args, got)
			}
			if want == "" {
				want = got
			} else if got != want {
				t.Errorf("%v: help differs from --help:\ngot %q\nwant %q", args, got, want)
			}
		}
	})
}

func TestHelpTopLevelAndUnknownCommand(t *testing.T) {
	_, usage, _ := run(t, nil, "--help")
	for _, tc := range []struct {
		args   []string
		prefix string
	}{
		{[]string{"help"}, ""},
		{[]string{"-h"}, ""},
		{[]string{"help", "unknown-command"}, "unknown command \"help\"\n"},
		{[]string{"unknown-command", "--help"}, "unknown command \"unknown-command\"\n"},
	} {
		code, out, errOut := run(t, nil, tc.args...)
		if code != 0 || errOut != "" || out != tc.prefix+usage {
			t.Errorf("%v: code=%d out=%q err=%q", tc.args, code, out, errOut)
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
