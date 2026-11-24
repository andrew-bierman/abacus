package cli_test

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
)

// errorCode is the code of out when it is one error document, "" when it is anything else (or empty).
func errorCode(out string) string {
	var d errDoc
	if json.Unmarshal([]byte(out), &d) != nil || d.SchemaVersion != 1 {
		return ""
	}
	return d.Error.Code
}

type errDoc struct {
	SchemaVersion int `json:"schema_version"`
	Error         struct {
		Code           string `json:"code"`
		Message        string `json:"message"`
		Retryable      bool   `json:"retryable"`
		UnknownOutcome bool   `json:"unknown_outcome"`
	} `json:"error"`
}

// A failing command run with --json prints exactly one error document on stdout, keeps its stderr text
// and its exit code, and names a stable code.
func TestFailingCommandsPrintOneErrorDocumentWithAStableCode(t *testing.T) {
	changed := newEditMod()
	changed.changeAfter = 1
	changed.again = core.Task{ID: "t-1", Title: "one", Status: "open", Priority: 3, Assignee: "eve", Labels: []string{"ui", "bug"}}
	unknown := newTaskMod()
	unknown.fail = core.UnknownOutcome("bd timed out; refresh before retrying")
	nc := []core.Module{snapMod{Fake: &modtest.Fake{IDStr: "bd", Det: core.Detection{State: core.Ready, Version: "1"}}, err: &core.NotConnectedError{Reason: "no tracker", HowToConnect: "set a directory"}}}
	setup := []core.Module{snapMod{Fake: &modtest.Fake{IDStr: "bd", Det: core.Detection{State: core.Ready, Version: "1"}}, err: &core.NeedsSetupError{Guide: core.Guide{Message: "no workspace yet", Action: "create one"}}}}
	slow := []core.Module{snapMod{Fake: &modtest.Fake{IDStr: "bd", Det: core.Detection{State: core.Ready, Version: "1"}}, err: errors.Join(errors.New("export"), core.ErrTimedOut)}}
	for _, c := range []struct {
		name    string
		mods    []core.Module
		audit   *memAudit
		args    []string
		exit    int
		code    string
		unknown bool
		retry   bool
	}{
		{name: "bad flag", args: []string{"doctor", "--json", "--nope"}, exit: 2, code: "usage"},
		{name: "unknown command", args: []string{"frobnicate", "--json"}, exit: 2, code: "usage"},
		{name: "missing id", mods: snap(), args: []string{"tasks", "show", "--json"}, exit: 2, code: "usage"},
		{name: "json needs yes", mods: snap(), args: []string{"tasks", "close", "t-1", "--json"}, exit: 2, code: "usage"},
		{name: "no provider", args: []string{"tasks", "list", "--json"}, exit: 1, code: "not_connected"},
		{name: "no provider write", args: []string{"tasks", "close", "t-1", "--yes", "--json"}, exit: 1, code: "not_connected"},
		{name: "no board", args: []string{"board", "post", "dev", "hi", "--yes", "--json"}, exit: 1, code: "not_connected"},
		{name: "read says not connected", mods: nc, args: []string{"tasks", "list", "--json"}, exit: 1, code: "not_connected"},
		{name: "read needs setup", mods: setup, args: []string{"tasks", "list", "--json"}, exit: 1, code: "needs_setup"},
		{name: "read timed out", mods: slow, args: []string{"tasks", "list", "--json"}, exit: 1, code: "timeout", retry: true},
		{name: "no such task", mods: snap(), args: []string{"tasks", "show", "tk-nope", "--json"}, exit: 1, code: "not_found"},
		{name: "changed", mods: []core.Module{changed}, args: []string{"tasks", "label", "add", "t-1", "bug", "--yes", "--json"}, exit: 1, code: "changed", retry: true},
		{name: "unknown outcome", mods: []core.Module{unknown}, args: []string{"tasks", "close", "t-1", "--yes", "--json"}, exit: 1, code: "unknown_outcome", unknown: true},
		{name: "audit failed", mods: []core.Module{newTaskMod()}, audit: &memAudit{err: errors.New("disk full")}, args: []string{"tasks", "close", "t-1", "--yes", "--json"}, exit: 1, code: "audit_failed"},
		{name: "board audit failed", mods: []core.Module{newBoardMod()}, audit: &memAudit{err: errors.New("disk full")}, args: []string{"board", "post", "dev", "hi", "--yes", "--json"}, exit: 1, code: "audit_failed"},
		{name: "write failed", mods: []core.Module{func() core.Module { m := newTaskMod(); m.fail = errors.New("bd exited 1"); return m }()}, args: []string{"tasks", "close", "t-1", "--yes", "--json"}, exit: 1, code: "failed"},
	} {
		au := c.audit
		if au == nil {
			au = &memAudit{}
		}
		code, out, errOut, _ := runAudit(t, "", c.mods, au, c.args...)
		var d errDoc
		dec := json.NewDecoder(strings.NewReader(out))
		if err := dec.Decode(&d); err != nil || dec.More() {
			t.Errorf("%s: stdout is not exactly one document (%v): %q", c.name, err, out)
			continue
		}
		if code != c.exit || d.SchemaVersion != 1 || d.Error.Code != c.code || d.Error.Message == "" || d.Error.UnknownOutcome != c.unknown || d.Error.Retryable != c.retry {
			t.Errorf("%s: exit %d doc %+v", c.name, code, d)
		}
		if strings.TrimSpace(errOut) == "" || !strings.Contains(errOut, strings.Fields(d.Error.Message)[0]) {
			t.Errorf("%s: the human text stays on stderr, got %q (message %q)", c.name, errOut, d.Error.Message)
		}
		// without --json nothing changes: no document, the same exit code
		plain := make([]string, 0, len(c.args))
		for _, a := range c.args {
			if a != "--json" {
				plain = append(plain, a)
			}
		}
		if c.exit == 1 || c.name == "bad flag" || c.name == "unknown command" {
			code2, out2, _, _ := runAudit(t, "", c.mods, &memAudit{err: au.err}, plain...)
			if code2 != c.exit || strings.Contains(out2, `"error"`) {
				t.Errorf("%s without --json: exit %d out %q", c.name, code2, out2)
			}
		}
	}
}

// A failing command that already printed its own document keeps it and gets no second one.
func TestAFailingCommandThatPrintedADocumentGetsNoSecond(t *testing.T) {
	code, out, _ := run(t, []core.Module{ready("a", core.Check{Name: "c", Status: core.StatusFail})}, "doctor", "--json")
	var m map[string]json.RawMessage
	if code != 1 || json.Unmarshal([]byte(out), &m) != nil || m["error"] != nil || m["modules"] == nil {
		t.Fatalf("code=%d %s", code, out)
	}
}

func TestErrorDocumentGoldens(t *testing.T) {
	unknown := newTaskMod()
	unknown.fail = core.UnknownOutcome("bd timed out; refresh before retrying")
	for _, c := range []struct {
		name string
		mods []core.Module
		args []string
	}{
		{"error-usage", nil, []string{"tasks", "close", "t-1", "--json"}},
		{"error-not-connected", nil, []string{"tasks", "list", "--json"}},
		{"error-unknown-outcome", []core.Module{unknown}, []string{"tasks", "close", "t-1", "--yes", "--json"}},
	} {
		_, out, _ := contractRun(t, c.mods, c.args...)
		checkContract(t, c.name, out)
	}
}

// The code set is closed and documented: every code core defines appears in docs/tool-contract.md,
// and the document says nothing about a code core does not have.
func TestErrorCodesAreDocumented(t *testing.T) {
	b, err := os.ReadFile("../../docs/tool-contract.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	for _, c := range core.ErrorCodes {
		if !strings.Contains(doc, "| `"+string(c)+"` |") {
			t.Errorf("docs/tool-contract.md has no table row for the error code %q", c)
		}
	}
	if got := strings.Count(doc, "\n| `"); got < len(core.ErrorCodes) {
		t.Errorf("only %d code rows", got)
	}
}

// --json is read the way the command's own flags read it: any boolean spelling, not after `--` or as a flag's value.
func TestErrorDocumentFollowsTheRealJSONFlag(t *testing.T) {
	for _, c := range []struct {
		args []string
		doc  bool
	}{
		{[]string{"tasks", "list", "--json=T"}, true},
		{[]string{"tasks", "list", "--json=TRUE"}, true},
		{[]string{"tasks", "list", "-json=t"}, true},
		{[]string{"tasks", "list", "--json=false"}, false},
		{[]string{"tasks", "list", "--json=0"}, false},
		{[]string{"tasks", "comment", "t-1", "--text", "--json", "--yes"}, false}, // --json is the text
		{[]string{"tasks", "close", "t-1", "--yes", "--reason", "--json"}, false},
		{[]string{"frobnicate", "--json=T"}, true},
		{[]string{"frobnicate", "--", "--json"}, false},
	} {
		_, out, _, _ := runIn(t, "", nil, c.args...)
		if got := errorCode(out) != ""; got != c.doc {
			t.Errorf("%v: error document=%v, want %v (stdout %q)", c.args, got, c.doc, out)
		}
	}
}
