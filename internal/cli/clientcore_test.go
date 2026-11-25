package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/cli"
	"github.com/andrew-bierman/abacus/internal/core"
)

// runOpts is contractRun that also records the Options the CLI asked Build for, and can set ABACUS_DEMO.
func runOpts(t *testing.T, env map[string]string, au core.Audit, args ...string) (code int, out, errOut string, got []cli.Options) {
	t.Helper()
	var o, e bytes.Buffer
	code = cli.Run(args, cli.Deps{Out: &o, Err: &e, Version: "t", Audit: au, Now: func() time.Time { return contractNow },
		Getenv: func(k string) string { return env[k] },
		Build:  func(op cli.Options) *core.Registry { got = append(got, op); return core.NewRegistry(contractMods()...) }})
	return code, o.String(), e.String(), got
}

var readCommands = [][]string{
	{"board"}, {"tasks"}, {"tasks", "list"}, {"tasks", "list", "--tree"}, {"tasks", "show", "ac-12"}, {"tasks", "graph"}, {"tasks", "board"},
	{"tasks", "history", "ac-12"}, {"leases"}, {"queues"}, {"agents"}, {"status"}, {"doctor"}, {"setup"}, {"search", "lease"}, {"version"},
}

func hasDemo(t *testing.T, out string) bool {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	return string(m["demo"]) == "true"
}

func TestDemoFlagAndEnvMarkEveryReadAndBuildDemo(t *testing.T) {
	for _, c := range readCommands {
		args := append(append([]string{}, c...), "--json")
		for name, tc := range map[string]struct {
			args []string
			env  map[string]string
			demo bool
		}{
			"flag": {append(args, "--demo"), nil, true},
			"env":  {args, map[string]string{"ABACUS_DEMO": "1"}, true},
			"off":  {args, map[string]string{"ABACUS_DEMO": "0"}, false},
		} {
			code, out, errOut, opts := runOpts(t, tc.env, &memAudit{}, tc.args...)
			if code != 0 {
				t.Fatalf("%v %s: exit %d %q", c, name, code, errOut)
			}
			if hasDemo(t, out) != tc.demo {
				t.Errorf("%v %s: demo marker = %v, want %v", c, name, !tc.demo, tc.demo)
			}
			if len(opts) == 0 || opts[len(opts)-1].Demo != tc.demo {
				t.Errorf("%v %s: Build got %+v, want Demo=%v", c, name, opts, tc.demo)
			}
		}
	}
}

// Every write verb in demo mode: only a --dry-run plan is allowed; anything else is refused, runs nothing
// and records nothing. The demo providers would happily "apply" (they change nothing and say so), so only
// the guard stops these.
func TestDemoModeRefusesEveryWriteButDryRunPlans(t *testing.T) {
	writes := [][]string{
		{"tasks", "create", "--title", "x"}, {"tasks", "update", "ac-12", "--priority", "1"}, {"tasks", "close", "ac-12"},
		{"tasks", "reopen", "ac-03"}, {"tasks", "label", "add", "ac-12", "ui"}, {"tasks", "assign", "ac-12", "mei"},
		{"tasks", "priority", "ac-12", "3"}, {"tasks", "dep", "add", "ac-15", "--blocked-by", "ac-30"},
		{"tasks", "comment", "ac-12", "hello"}, {"tasks", "bulk", "--ids", "ac-12,ac-15", "--op", "label-add", "--value", "x"},
		{"board", "post", "dev", "hello"}, {"board", "reply", "15", "hello"}, {"setup"}, {"setup", "--repair"},
	}
	for _, w := range writes {
		for _, how := range []struct {
			name string
			env  map[string]string
			flag []string
		}{{"flag", nil, []string{"--demo"}}, {"env", map[string]string{"ABACUS_DEMO": "1"}, nil}} {
			au := &memAudit{}
			args := append(append(append([]string{}, w...), how.flag...), "--yes", "--json")
			code, out, _, _ := runOpts(t, how.env, au, args...)
			var doc core.ErrorDoc
			if code != 1 || json.Unmarshal([]byte(out), &doc) != nil || doc.Error.Code != core.CodeRefused || !hasDemo(t, out) {
				t.Errorf("%v %s --yes: exit %d, doc %s", w, how.name, code, out)
			}
			if au.n != 0 {
				t.Errorf("%v %s: the audit log was written (%d records)", w, how.name, au.n)
			}
			if w[0] == "setup" {
				continue // a setup plan is its report; the per-verb dry-run plans are checked below
			}
			dry := append(append(append([]string{}, w...), how.flag...), "--dry-run", "--json")
			code, out, errOut, _ := runOpts(t, how.env, &memAudit{}, dry...)
			if code != 0 || !hasDemo(t, out) || !strings.Contains(out, `"plan_digest"`) {
				t.Errorf("%v %s --dry-run: exit %d %q\n%s", w, how.name, code, errOut, out)
			}
		}
	}
	// a repair asked for without --yes is refused by the demo guard, not by the "not confirmed" fallback
	if code, _, errOut, _ := runOpts(t, nil, &memAudit{}, "setup", "--repair", "--demo"); code != 1 || !strings.Contains(errOut, "demo data cannot be changed") {
		t.Errorf("setup --repair --demo: %d %q", code, errOut)
	}
	// the interactive path cannot apply either: a "y" at the prompt is not reached
	au := &memAudit{}
	var o, e bytes.Buffer
	code := cli.Run([]string{"tasks", "close", "ac-12", "--demo"}, cli.Deps{Out: &o, Err: &e, In: strings.NewReader("y\n"), Audit: au, Version: "t",
		Build: func(cli.Options) *core.Registry { return core.NewRegistry(contractMods()...) }})
	if code != 1 || au.n != 0 || strings.Contains(o.String(), "done:") {
		t.Errorf("a y at the prompt applied demo data: %d %s", code, o.String())
	}
}

// With the guard gone the audit backstop still stops an apply in demo mode (fail closed): a record that
// cannot be written blocks the write.
func TestDemoModeAuditCannotRecord(t *testing.T) {
	code, out, _, _ := runOpts(t, map[string]string{"ABACUS_DEMO": "1"}, nil, "tasks", "close", "ac-12", "--yes", "--json")
	if code == 0 || strings.Contains(out, `"applied"`) {
		t.Errorf("%d %s", code, out)
	}
}

func trackerDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(d, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, ".beads", "metadata.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestTrackerFlagIsValidatedAndReachesBuildOnEveryTasksVerbAndTheReads(t *testing.T) {
	dir := trackerDir(t)
	for _, c := range [][]string{
		{"tasks"}, {"tasks", "list"}, {"tasks", "show", "ac-12"}, {"tasks", "graph"}, {"tasks", "board"}, {"tasks", "history", "ac-12"},
		{"tasks", "create", "--title", "x", "--dry-run"}, {"tasks", "update", "ac-12", "--priority", "1", "--dry-run"}, {"tasks", "close", "ac-12", "--dry-run"},
		{"tasks", "reopen", "ac-03", "--dry-run"}, {"tasks", "label", "add", "ac-12", "ui", "--dry-run"}, {"tasks", "assign", "ac-12", "mei", "--dry-run"},
		{"tasks", "priority", "ac-12", "3", "--dry-run"}, {"tasks", "dep", "add", "ac-15", "--blocked-by", "ac-30", "--dry-run"},
		{"tasks", "comment", "ac-12", "hi", "--dry-run"}, {"tasks", "bulk", "--ids", "ac-12", "--op", "close", "--dry-run"},
		{"search", "lease"}, {"status"}, {"doctor"},
	} {
		code, _, errOut, opts := runOpts(t, nil, &memAudit{}, append(append([]string{}, c...), "--tracker", dir, "--json")...)
		if code != 0 || len(opts) == 0 || opts[len(opts)-1].Tracker != dir {
			t.Errorf("%v: exit %d %q, Build got %+v, want Tracker %s", c, code, errOut, opts, dir)
		}
	}
	// --tracker=DIR spelling, and a path with a trailing slash is cleaned
	if _, _, _, opts := runOpts(t, nil, &memAudit{}, "tasks", "--tracker="+dir+"/", "--json"); len(opts) == 0 || opts[0].Tracker != dir {
		t.Errorf("cleaned path: %+v", opts)
	}
	// without the flag the options carry none (ABACUS_BD_DIR and the rest stay app.Build's business)
	if _, _, _, opts := runOpts(t, nil, &memAudit{}, "tasks", "--json"); len(opts) == 0 || opts[0].Tracker != "" {
		t.Errorf("no flag: %+v", opts)
	}
}

func TestTrackerFlagRefusesWhatIsNotATrustedTracker(t *testing.T) {
	loose := trackerDir(t)
	if err := os.Chmod(filepath.Join(loose, ".beads"), 0o777); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{"relative missing": "no/such/dir", "not a tracker": t.TempDir(), "bare .beads": filepath.Join(trackerDir(t), ".beads"), "control chars": "/tmp/a\x1bb"}
	if runtime.GOOS != "windows" { // there is no ownership or mode model off unix (core.UntrustedDir)
		cases["world-writable"] = loose
	}
	for name, bad := range cases {
		var built bool
		var o, e bytes.Buffer
		code := cli.Run([]string{"tasks", "--tracker", bad, "--json"}, cli.Deps{Out: &o, Err: &e, Build: func(cli.Options) *core.Registry { built = true; return core.NewRegistry() }})
		var doc core.ErrorDoc
		if code != 2 || built || json.Unmarshal(o.Bytes(), &doc) != nil || doc.Error.Code != core.CodeUsage || !strings.Contains(e.String(), "--tracker") {
			t.Errorf("%s: exit %d built=%v out=%s err=%s", name, code, built, o.String(), e.String())
		}
	}
	if code, _, errOut, _ := runOpts(t, nil, &memAudit{}, "tasks", "--demo", "--tracker", trackerDir(t)); code != 2 || !strings.Contains(errOut, "demo") {
		t.Errorf("demo with a tracker: %d %q", code, errOut)
	}
}

func TestVersionJSONHasTheContractSection(t *testing.T) {
	code, out, _ := contractRun(t, contractMods(), "version", "--json")
	var v struct {
		Contract struct {
			SchemaVersion int      `json:"schema_version"`
			ErrorCodes    int      `json:"error_codes"`
			PlanDigest    bool     `json:"plan_digest"`
			Serve         bool     `json:"serve"`
			Capabilities  []string `json:"capabilities"`
		} `json:"contract"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &v) != nil {
		t.Fatalf("%d %s", code, out)
	}
	c := v.Contract
	if c.SchemaVersion != core.SchemaVersion || c.ErrorCodes != core.ErrorCodesVersion || !c.PlanDigest || c.Serve != core.ServeSupported || len(c.Capabilities) == 0 {
		t.Errorf("%+v", c)
	}
	for _, want := range []string{"demo", "tracker", "history"} {
		found := false
		for _, x := range c.Capabilities {
			found = found || x == want
		}
		if !found {
			t.Errorf("capability %q missing: %v", want, c.Capabilities)
		}
	}
}

// Demo mode refuses every command that changes something, through the central table, with nothing written.
func TestDemoModeRefusesEveryMutatingCommandCentrally(t *testing.T) {
	cfgDir := t.TempDir()
	cfg := filepath.Join(cfgDir, "config.json")
	out := filepath.Join(t.TempDir(), "schemas")
	mutating := [][]string{
		{"config", "set", "actor", "bob"}, {"schema", "--out", out}, {"rex", "layout", "--yes"}, {"rex", "layout", "--dry-run"},
		{"setup", "--yes"}, {"setup", "--repair", "--yes"}, {"tasks", "close", "ac-12", "--yes"}, {"tasks", "create", "--title", "x", "--yes"},
		{"tasks", "bulk", "--ids", "ac-12", "--op", "close", "--yes"}, {"board", "post", "dev", "x", "--yes"}, {"board", "reply", "15", "x", "--yes"},
		{"tasks", "nosuchverb"}, {"config", "nosuchverb"},
	}
	for _, how := range []string{"flag", "env"} {
		for _, c := range mutating {
			args := append([]string{}, c...)
			env := map[string]string{}
			if how == "flag" {
				args = append(args, "--demo")
			} else {
				env["ABACUS_DEMO"] = "1"
			}
			au := &memAudit{}
			var o, e bytes.Buffer
			code := cli.Run(append(args, "--json"), cli.Deps{Out: &o, Err: &e, Audit: au, Version: "t", Config: core.ConfigStore{Path: cfg},
				Getenv: func(k string) string { return env[k] },
				Build:  func(cli.Options) *core.Registry { return core.NewRegistry(contractMods()...) }})
			var doc core.ErrorDoc
			if code != 1 || json.Unmarshal(o.Bytes(), &doc) != nil || doc.Error.Code != core.CodeRefused {
				t.Errorf("%v %s: exit %d out %s err %s", c, how, code, o.String(), e.String())
			}
			if au.n != 0 {
				t.Errorf("%v %s: audited", c, how)
			}
		}
	}
	if _, err := os.Stat(cfg); err == nil {
		t.Error("demo mode wrote the config")
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("demo mode wrote schema files")
	}
	// reads and dry-run plans still work in demo mode
	for _, c := range [][]string{{"config", "show"}, {"config", "path"}, {"schema"}, {"tasks", "close", "ac-12", "--dry-run"}, {"setup"}, {"licenses"}} {
		args := append(append([]string{}, c...), "--json")
		if c[0] == "licenses" {
			args = c
		}
		var o, e bytes.Buffer
		if code := cli.Run(args, cli.Deps{Out: &o, Err: &e, Version: "t", Config: core.ConfigStore{Path: cfg}, Getenv: func(k string) string { return map[string]string{"ABACUS_DEMO": "1"}[k] },
			Build: func(cli.Options) *core.Registry { return core.NewRegistry(contractMods()...) }}); code != 0 {
			t.Errorf("%v in demo mode: exit %d %s", c, code, e.String())
		}
	}
}

func TestDemoDigestNeverEqualsARealOne(t *testing.T) {
	digest := func(env map[string]string, extra ...string) string {
		_, out, _, _ := runOpts(t, env, &memAudit{}, append([]string{"tasks", "close", "ac-12", "--dry-run", "--json"}, extra...)...)
		var d struct {
			PlanDigest string `json:"plan_digest"`
		}
		if err := json.Unmarshal([]byte(out), &d); err != nil || len(d.PlanDigest) != 64 {
			t.Fatalf("%v %s", err, out)
		}
		return d.PlanDigest
	}
	real, demo := digest(nil), digest(nil, "--demo")
	if real == demo || demo != digest(map[string]string{"ABACUS_DEMO": "1"}) {
		t.Errorf("real %s demo %s", real, demo)
	}
	// and --expect-digest takes the demo digest in demo mode, the real one only outside it
	if code, _, _, _ := runOpts(t, nil, &memAudit{}, "tasks", "close", "ac-12", "--dry-run", "--demo", "--expect-digest", real); code != 1 {
		t.Errorf("a real digest must not match a demo plan: %d", code)
	}
	if code, _, _, _ := runOpts(t, nil, &memAudit{}, "tasks", "close", "ac-12", "--dry-run", "--demo", "--expect-digest", demo); code != 0 {
		t.Errorf("the demo digest matches its own plan: %d", code)
	}
}

func TestEmptyTrackerFlagIsAUsageErrorOnEveryVerb(t *testing.T) {
	for _, c := range [][]string{{"tasks"}, {"tasks", "list"}, {"tasks", "show", "ac-12"}, {"tasks", "graph"}, {"tasks", "board"}, {"tasks", "history", "ac-12"},
		{"tasks", "create", "--title", "x", "--dry-run"}, {"tasks", "close", "ac-12", "--dry-run"}, {"tasks", "label", "add", "ac-12", "ui", "--dry-run"},
		{"tasks", "bulk", "--ids", "ac-12", "--op", "close", "--dry-run"}, {"search", "x"}, {"status"}, {"doctor"}} {
		for _, form := range [][]string{{"--tracker", ""}, {"--tracker="}} {
			var built bool
			var o, e bytes.Buffer
			code := cli.Run(append(append([]string{}, c...), append(form, "--json")...), cli.Deps{Out: &o, Err: &e, Build: func(cli.Options) *core.Registry { built = true; return core.NewRegistry() }})
			if code != 2 || built || !strings.Contains(e.String(), "--tracker") {
				t.Errorf("%v %v: exit %d built=%v %s", c, form, code, built, e.String())
			}
		}
	}
}

// A bulk batch with an unknown result carries the standard error object next to its own per-task results.
func TestBulkWithAnUnknownResultCarriesTheErrorObject(t *testing.T) {
	m := newTaskMod()
	m.fail = core.UnknownOutcome("timed out")
	var o, e bytes.Buffer
	code := cli.Run([]string{"tasks", "bulk", "--ids", "ac-12", "--op", "close", "--yes", "--json"}, cli.Deps{Out: &o, Err: &e, Audit: &memAudit{}, Version: "t",
		Build: func(cli.Options) *core.Registry { return core.NewRegistry(m) }})
	var rep core.BulkReport
	if code != 1 || json.Unmarshal(o.Bytes(), &rep) != nil || rep.Unknown != 1 || rep.Error == nil || rep.Error.Code != core.CodeUnknownOutcome || !rep.Error.UnknownOutcome {
		t.Errorf("exit %d %s", code, o.String())
	}
}
