package rex

import (
	"context"
	"strings"
	"testing"
)

const (
	sid  = "session:01a20c37-b18f-74b9-88de-38a5e9f1dc56"
	bid  = "block:01a20c37-b18f-74b9-88de-38a5e9f1dc57"
	auto = "--autostart=false"
)

// agentOK are the three shapes the Agents view adds, each with and without the -S prefix. The
// list is spelled out literally (not built with the argv helpers) so a helper that drifts is caught.
func agentOK() [][]string {
	base := [][]string{
		{auto, "block", "ls", "-s", sid, "--json"},
		{auto, "block", "call", "-s", sid, "-b", bid, "program_status"},
		{auto, "block", "call", "-s", sid, "-b", bid, "process"},
	}
	var out [][]string
	for _, b := range base {
		out = append(out, b, append([]string{"-S", "mbp"}, b...), append([]string{"-S", "me@mbp.local"}, b...))
	}
	return out
}

// agentRefused are the neighbours of the allowed shapes that must never run: every other method
// of the generic `block call` verb, every extra word, and every malformed id or guard. It is
// deliberately longer than the allowed list.
func agentRefused() [][]string {
	call := func(method ...string) []string {
		return append([]string{auto, "block", "call", "-s", sid, "-b", bid}, method...)
	}
	r := [][]string{
		// other methods of the generic call verb: some of these type into or change a terminal
		call("write"), call("reset"), call("clear"), call("resize"), call("set_theme"), call("title"), call("pwd"),
		call("screen"), call("capture"), call("clipboard_read"), call("download_begin"), call("modes"), call("ports"),
		call("Program_Status"), call("program_status "), call("process "), call("program-status"), call(""),
		call("program_status", "process"), call(),
		// an argument word after an allowed method (the JSON args of a call)
		call("program_status", `{"x":1}`), call("process", "--json"), call("program_status", ""), call("process", "x"),
		call("write", "rm -rf /\n"), call("process", "extra", "words"),
		// block ls that is not exactly one session as JSON
		{auto, "block", "ls", "-A", "--json"}, {auto, "block", "ls", "--json"}, {auto, "block", "ls"}, {auto, "block", "ls", "-s", sid},
		{auto, "block", "ls", "-s", sid, "--json", "-A"}, {auto, "block", "ls", "-A"}, {auto, "block", "ls", "-q"},
		{auto, "block", "ls", "--json", "-s", sid}, {auto, "block", "ls", "-S", sid, "--json"},
		// other block verbs
		{auto, "block", "inspect", "-s", sid, "-b", bid, "--json"}, {auto, "block", "new", "-s", sid},
		{auto, "block", "close", "-s", sid, "-b", bid}, {auto, "block", "send", "-s", sid, "-b", bid, "x"},
		{auto, "block", "capture", "-s", sid, "-b", bid}, {auto, "block", "focus", "-s", sid, "-b", bid},
		{auto, "block", "kill", "-s", sid, "-b", bid}, {auto, "block"}, {auto, "block", "call"},
		// block call without both ids, or flags out of place
		{auto, "block", "call", "-b", bid, "program_status"}, {auto, "block", "call", "-s", sid, "program_status"},
		{auto, "block", "call", "-b", bid, "-s", sid, "program_status"}, {auto, "block", "call", "-s", sid, "-b", bid},
		{auto, "block", "call", "-s", bid, "-b", sid, "program_status"}, // the ids swapped
		{auto, "block", "call", "-S", sid, "-b", bid, "program_status"},
		// the whole rest of the CLI
		{auto, "api", "call", "session.view"}, {auto, "api", "call", "block.call"}, {auto, "events", "-s", sid},
		{auto, "events"}, {auto, "send", "-s", sid, "x"}, {auto, "capture", "-s", sid}, {auto, "kill", sid}, {auto, "new"},
	}
	// ids that are not in the grammar, in either position
	badIDs := []string{"", "-" + sid, "session:", "session:-1", "session: 1", "session:1 2", "session:1\n2", "session:\n", "Session:01a2",
		"sess:01a2", "session:01a2;ls", "session:01A2", "session:01a2/../x", "session:$(x)", "session:01a2\x00", "session:" + strings.Repeat("a", 200),
		"01a20c37-b18f", "session:g1", "session:01a2'", "session:01a2 --json"}
	for _, id := range badIDs {
		r = append(r,
			[]string{auto, "block", "ls", "-s", id, "--json"},
			[]string{auto, "block", "call", "-s", id, "-b", bid, "program_status"},
			[]string{auto, "block", "call", "-s", sid, "-b", id, "process"},
		)
	}
	r = append(r,
		// a block id where a session id belongs (and the reverse) is a different grammar
		[]string{auto, "block", "ls", "-s", bid, "--json"}, []string{auto, "block", "call", "-s", sid, "-b", sid, "process"},
		// the autostart guard: absent, altered or in any other position
		[]string{"block", "ls", "-s", sid, "--json"}, []string{"block", "call", "-s", sid, "-b", bid, "process"},
		[]string{"--autostart=true", "block", "ls", "-s", sid, "--json"}, []string{"--autostart", "block", "call", "-s", sid, "-b", bid, "process"},
		[]string{"--autostart=", "block", "ls", "-s", sid, "--json"}, []string{"-autostart=false", "block", "ls", "-s", sid, "--json"},
		[]string{"block", auto, "ls", "-s", sid, "--json"}, []string{"block", "ls", auto, "-s", sid, "--json"},
		[]string{"block", "call", "-s", sid, "-b", bid, auto, "process"}, []string{"block", "call", "-s", sid, "-b", bid, "process", auto},
		[]string{auto, auto, "block", "ls", "-s", sid, "--json"}, []string{"-S", "mbp", "block", "ls", "-s", sid, "--json"},
		[]string{auto, "-S", "mbp", "block", "ls", "-s", sid, "--json"},
		// the host prefix: invalid, empty, option-like, doubled or in a later position
		[]string{"-S", auto, "block", "ls", "-s", sid, "--json"}, []string{"-S", "", auto, "block", "ls", "-s", sid, "--json"},
		[]string{"-S", "-oProxyCommand=x", auto, "block", "call", "-s", sid, "-b", bid, "process"},
		[]string{"-S", "a b", auto, "block", "call", "-s", sid, "-b", bid, "process"}, []string{"-S", "a;b", auto, "block", "ls", "-s", sid, "--json"},
		[]string{"-S", "a\nb", auto, "block", "ls", "-s", sid, "--json"}, []string{"-S", "mbp", "-S", "other", auto, "block", "ls", "-s", sid, "--json"},
		[]string{"-S", strings.Repeat("a", 300), auto, "block", "ls", "-s", sid, "--json"}, []string{"-s", "mbp", auto, "block", "ls", "-s", sid, "--json"},
		[]string{"--server", "unix:///x", auto, "block", "ls", "-s", sid, "--json"},
		// the prefix of an allowed argv, and an allowed argv with an extension
		[]string{auto, "block", "ls", "-s"}, []string{auto, "block", "call", "-s", sid, "-b"}, []string{auto, "block", "call", "-s", sid, "-b", bid, "program_status", "x"},
		[]string{auto, "block", "ls", "-s", sid, "--json", ""}, []string{auto, "block", "ls", "-s", sid, "--json", "--json"},
		// version stays local-only, as before
		[]string{"-S", "mbp", auto, "version", "--json"},
	)
	return r
}

func TestAgentArgvShapesAreAllowedWithAndWithoutAHost(t *testing.T) {
	for _, a := range agentOK() {
		if !allowedArgv(a) {
			t.Errorf("%q should be allowed", a)
		}
	}
}

func TestAgentArgvHelpersBuildExactlyTheAllowedShapes(t *testing.T) {
	for _, host := range []string{"", "mbp"} {
		got := [][]string{blockLsArgv(host, sid), blockCallArgv(host, sid, bid, methodProgramStatus), blockCallArgv(host, sid, bid, methodProcess)}
		for i, a := range got {
			if !allowedArgv(a) {
				t.Errorf("helper %d (host %q) built a refused argv %q", i, host, a)
			}
		}
	}
	if a := blockCallArgv("", sid, bid, "write"); allowedArgv(a) {
		t.Errorf("a helper must never make another method allowed: %q", a)
	}
}

func TestAgentArgvNeighboursAreRefusedAndNeverReachTheRunner(t *testing.T) {
	refused := agentRefused()
	if len(refused) <= len(agentOK()) {
		t.Fatalf("the refused list (%d) must be longer than the allowed one (%d)", len(refused), len(agentOK()))
	}
	for _, a := range refused {
		if allowedArgv(a) {
			t.Errorf("%q must be refused", a)
		}
		var ran bool
		r := readOnly{inner: runnerFunc(func() { ran = true })}
		if _, exit, err := r.Run(context.Background(), "rex", a...); err == nil || exit != -1 || ran {
			t.Errorf("%q reached the runner (err=%v ran=%v)", a, err, ran)
		}
	}
}

// The method pin is the whole safety of the generic `block call` verb. This is the mutant check:
// the tests above must fail if the matcher accepted any method, so a method list that only
// happens to hold today cannot pass.
func TestTheMethodPinIsWhatRefusesWrite(t *testing.T) {
	loose := func(a []string) bool { // allowedArgv with the method pin removed
		if len(a) >= 2 && a[0] == "-S" {
			a = a[2:]
		}
		return len(a) == 8 && a[0] == auto && a[1] == "block" && a[2] == "call" && validID("session", a[4]) && validID("block", a[6])
	}
	dangerous := []string{"write", "reset", "clear", "resize", "set_theme"}
	for _, m := range dangerous {
		a := []string{auto, "block", "call", "-s", sid, "-b", bid, m}
		if !loose(a) {
			t.Fatalf("the mutant should have accepted %s", m)
		}
		if allowedArgv(a) {
			t.Errorf("method %s is allowed", m)
		}
	}
}
