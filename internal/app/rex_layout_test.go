package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/cli"
	"github.com/andrew-bierman/abacus/internal/core"
)

const (
	layoutSession = "session:01a20c1b-ae95-745d-ba13-9eaee76983fa"
	layoutPane    = "block:01a20c2b-ed0a-7802-a0b6-b446d8779a01"
)

// stubWorkspace is a script that is not Rex: it answers the reads from fixed text and appends every
// command that would change anything to a log, so nothing real is ever involved. open lists the
// tabs the session already has.
func stubWorkspace(t *testing.T, open string) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "writes.log")
	body := `#!/bin/sh
case "$*" in
*version*) echo '{"api_revision":14,"rex_version":"1"}' ;;
*"window ls"*) echo '{"windows":[` + open + `]}' ;;
*"block ls"*) echo '{"blocks":[]}' ;;
*" ls --json"*) echo '{"sessions":[{"session_id":"` + layoutSession + `","label":"General"}]}' ;;
*" run "*|*" split "*|*" new "*|*" focus "*|*"window new"*) echo "$*" >> '` + log + `'; echo '{"block_ids":["` + layoutPane + `"]}' ;;
*) echo "unexpected: $*" >> '` + log + `'; exit 9 ;;
esac
`
	bin = filepath.Join(dir, "stub-not-rex")
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func runRexLayout(t *testing.T, e Env, in string, args ...string) (code int, out, errOut string, audit *memAuditLog) {
	t.Helper()
	var o, er bytes.Buffer
	audit = &memAuditLog{}
	code = cli.Run(append([]string{"rex"}, args...), cli.Deps{Out: &o, Err: &er, In: strings.NewReader(in), Version: "1.2.3", Config: e.Config, Audit: audit,
		Exe:   func() (string, error) { return "/opt/abacus/bin/abacus", nil },
		Build: func(op cli.Options) *core.Registry { return Build(op, e) }})
	return code, o.String(), er.String(), audit
}

type memAuditLog struct{ lines []core.AuditEntry }

func (a *memAuditLog) Record(e core.AuditEntry) error { a.lines = append(a.lines, e); return nil }

func TestRexLayoutPlansTheTabsAndRunsNothingOnADryRun(t *testing.T) {
	bin, log := stubWorkspace(t, `{"label":"Tasks"}`)
	e := env(t, map[string]string{"ABACUS_REX_BIN": bin, "REX_SESSION": layoutSession})
	code, out, errOut, au := runRexLayout(t, e, "", "layout", "--dry-run")
	if code != core.ExitOK || errOut != "" {
		t.Fatalf("%d %q", code, errOut)
	}
	for _, want := range []string{"add the cockpit tabs to session " + layoutSession, "already open, left alone: Tasks", "nothing is typed into any existing terminal",
		"--tab board", "--tab leases", "--tab queues", "/opt/abacus/bin/abacus tui --tab"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "--tab tasks") {
		t.Errorf("an open tab is not opened again:\n%s", out)
	}
	if n := strings.Count(out, "command: rex --autostart=false window new"); n != 3 {
		t.Errorf("one command per new tab: %d\n%s", n, out)
	}
	if _, err := os.Stat(log); err == nil || len(au.lines) != 0 {
		t.Fatalf("a dry run writes nothing and audits nothing: %v %v", err, au.lines)
	}
	// JSON dry run carries every step.
	_, jout, _, _ := runRexLayout(t, e, "", "layout", "--dry-run", "--json")
	var v struct {
		DryRun bool       `json:"dry_run"`
		Argv   []string   `json:"argv"`
		Steps  [][]string `json:"steps"`
	}
	if err := json.Unmarshal([]byte(jout), &v); err != nil || !v.DryRun || len(v.Steps) != 2 || v.Argv[0] != "rex" {
		t.Fatalf("%v %s", err, jout)
	}
}

func TestRexLayoutAppliesOnlyAfterAYesAndAuditsEveryStep(t *testing.T) {
	bin, log := stubWorkspace(t, "")
	e := env(t, map[string]string{"ABACUS_REX_BIN": bin, "REX_SESSION": layoutSession})
	code, _, errOut, au := runRexLayout(t, e, "n\n", "layout")
	if code != core.ExitFail || !strings.Contains(errOut, "not applied: the change was not confirmed") {
		t.Fatalf("%d %q", code, errOut)
	}
	if _, err := os.Stat(log); err == nil || len(au.lines) != 0 {
		t.Fatalf("declining runs nothing: %v %v", err, au.lines)
	}
	code, out, errOut, au := runRexLayout(t, e, "y\n", "layout")
	if code != core.ExitOK || !strings.Contains(out, "done: opened 4 tabs") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	b, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 4 || !strings.Contains(lines[0], "window new -s "+layoutSession+" Board") || strings.Contains(lines[0], "--focus=false") || !strings.Contains(lines[1], "Tasks") || !strings.Contains(lines[1], "--focus=false") {
		t.Fatalf("%q", lines)
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "--autostart=false ") || !strings.Contains(l, "-- /opt/abacus/bin/abacus tui --tab ") {
			t.Errorf("%q", l)
		}
	}
	// Attempt for the first step, then an attempt per later step, then the plan's outcome.
	if len(au.lines) != 5 || au.lines[0].Phase != "attempt" || au.lines[4].Phase != "ok" || au.lines[0].Domain != "workspace" || au.lines[0].Verb != "layout" {
		t.Fatalf("%+v", au.lines)
	}
}

func TestRexLayoutInANewSessionAndItsRefusals(t *testing.T) {
	bin, log := stubWorkspace(t, "")
	e := env(t, map[string]string{"ABACUS_REX_BIN": bin})
	code, out, _, _ := runRexLayout(t, e, "", "layout", "--new", "cockpit", "--dry-run")
	if code != core.ExitOK || !strings.Contains(out, "create the session cockpit") || !strings.Contains(out, "command: rex --autostart=false new cockpit --json --window Board") {
		t.Fatalf("%d\n%s", code, out)
	}
	// Not inside a session and no --new: there is nowhere to put the tabs.
	code, _, errOut, _ := runRexLayout(t, e, "", "layout", "--dry-run")
	if code != core.ExitUsage || !strings.Contains(errOut, "not running inside a workspace session") {
		t.Fatalf("%d %q", code, errOut)
	}
	for _, args := range [][]string{{}, {"nope"}, {"layout", "extra"}, {"layout", "--json"}, {"layout", "--new", "a b", "--dry-run"}, {"layout", "--new", "General", "--dry-run"}} {
		if code, _, _, _ := runRexLayout(t, e, "", args...); code == core.ExitOK {
			t.Errorf("%q must fail", args)
		}
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("nothing was written")
	}
}

func TestRexLayoutWithoutRexSaysNotConnectedAndNeverAsksToInstall(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	e := env(t, nil)
	code, _, errOut, _ := runRexLayout(t, e, "", "layout", "--dry-run")
	if code != core.ExitFail || !strings.Contains(errOut, "not connected to a terminal workspace") || !strings.Contains(errOut, "Rex is optional") {
		t.Fatalf("%d %q", code, errOut)
	}
	assertNoAdvice(t, "rex layout", errOut, false)
}

func TestStatusAndDoctorJSONCarryTheTypedWorkspaceState(t *testing.T) {
	bin, _ := stubWorkspace(t, "")
	e := env(t, map[string]string{"ABACUS_REX_BIN": bin, "REX_SESSION": layoutSession})
	var o, er bytes.Buffer
	cli.Run([]string{"status", "--json"}, cli.Deps{Out: &o, Err: &er, Version: "t", Config: e.Config, Build: func(op cli.Options) *core.Registry { return Build(op, e) }})
	var rep core.Report
	if err := json.Unmarshal(o.Bytes(), &rep); err != nil {
		t.Fatalf("%v %s", err, o.String())
	}
	var w *core.WorkspaceInfo
	for _, m := range rep.Modules {
		if m.ID == "rex" && m.Status != nil {
			w = m.Status.Workspace
		}
	}
	if w == nil || w.Connection != core.Connected || w.APIRevision != 14 || w.SupportedMin != 10 || w.SupportedMax != 14 || !w.Compatible || !w.InSession || w.Server != "environment" {
		t.Fatalf("%+v\n%s", w, o.String())
	}
	if rep.SchemaVersion != 1 {
		t.Fatalf("schema_version stays 1: %d", rep.SchemaVersion)
	}
}
