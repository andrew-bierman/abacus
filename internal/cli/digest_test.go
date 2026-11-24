package cli_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

func digestOf(t *testing.T, out string) string {
	t.Helper()
	var v struct {
		PlanDigest string `json:"plan_digest"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || len(v.PlanDigest) != 64 {
		t.Fatalf("no plan_digest (%v) in %s", err, out)
	}
	return v.PlanDigest
}

// For every write verb: the dry run prints a digest, applying with that digest works and echoes it, and
// applying with any other digest writes nothing and says "changed".
func TestEveryWriteVerbPrintsAPlanDigestAndApplyChecksIt(t *testing.T) {
	restore := core.SetBoardClockForTest(func() time.Time { return contractNow })
	defer restore()
	edit := func() ([]core.Module, func() int) {
		m := newEditMod()
		return []core.Module{m}, func() int { return len(m.ran) }
	}
	closed := func() ([]core.Module, func() int) {
		m := closedMod()
		return []core.Module{m}, func() int { return len(m.ran) }
	}
	board := func() ([]core.Module, func() int) {
		m := newBoardMod()
		return []core.Module{m}, func() int { return len(m.ran) }
	}
	for name, c := range map[string]struct {
		mods func() ([]core.Module, func() int)
		args []string
	}{
		"create":   {edit, []string{"tasks", "create", "--title", "x"}},
		"update":   {edit, []string{"tasks", "update", "t-1", "--priority", "1"}},
		"close":    {edit, []string{"tasks", "close", "t-1"}},
		"reopen":   {closed, []string{"tasks", "reopen", "t-1"}},
		"label":    {edit, []string{"tasks", "label", "add", "t-1", "bug"}},
		"assign":   {edit, []string{"tasks", "assign", "t-1", "ada"}},
		"priority": {edit, []string{"tasks", "priority", "t-1", "0"}},
		"dep":      {edit, []string{"tasks", "dep", "add", "t-3", "--blocked-by", "t-2"}},
		"comment":  {edit, []string{"tasks", "comment", "t-1", "hello"}},
		"bulk":     {edit, []string{"tasks", "bulk", "--ids", "t-1,t-3", "--op", "label-add", "--value", "sprint"}},
		"post":     {board, []string{"board", "post", "dev", "hi"}},
		"reply":    {board, []string{"board", "reply", "15", "hi"}},
	} {
		mods, ran := c.mods()
		_, out, errOut, _ := runIn(t, "", mods, append(append([]string{}, c.args...), "--dry-run", "--json")...)
		if ran() != 0 {
			t.Fatalf("%s: a dry run ran something", name)
		}
		d := digestOf(t, out)
		if again := func() string {
			_, o, _, _ := runIn(t, "", mods, append(append([]string{}, c.args...), "--dry-run", "--json")...)
			return digestOf(t, o)
		}(); again != d {
			t.Errorf("%s: the same plan made twice has two digests", name)
		}
		// the wrong digest: refused, nothing run, nothing audited, in both modes
		wrong := strings.Repeat("0", 64)
		for _, mode := range []string{"--yes", "--dry-run"} {
			code, out, errOut, au := runIn(t, "", mods, append(append([]string{}, c.args...), mode, "--expect-digest", wrong, "--json")...)
			if code != 1 || errorCode(out) != "changed" || ran() != 0 || au.n != 0 || !strings.Contains(errOut, "plan changed") {
				t.Errorf("%s %s with a wrong digest: code=%d out=%s err=%s ran=%d audit=%d", name, mode, code, out, errOut, ran(), au.n)
			}
		}
		// the right digest: applied once, and the result says which plan it was
		code, out, errOut, au := runIn(t, "", mods, append(append([]string{}, c.args...), "--yes", "--expect-digest", strings.ToUpper(d), "--json")...)
		if code != 0 || ran() < 1 || au.n < 2 || digestOf(t, out) != d {
			t.Errorf("%s with the right digest: code=%d out=%s err=%s ran=%d audit=%d", name, code, out, errOut, ran(), au.n)
		}
	}
}

// The digest follows the argv: the same command line with a different value is a different plan, and a
// digest of one is refused for the other.
func TestPlanDigestFollowsTheExactArgv(t *testing.T) {
	m := newTaskMod()
	_, o1, _, _ := runIn(t, "", []core.Module{m}, "tasks", "create", "--title", "first", "--dry-run", "--json")
	_, o2, _, _ := runIn(t, "", []core.Module{m}, "tasks", "create", "--title", "second", "--dry-run", "--json")
	d1, d2 := digestOf(t, o1), digestOf(t, o2)
	if d1 == d2 {
		t.Fatal("two different argvs share a digest")
	}
	code, out, _, _ := runIn(t, "", []core.Module{m}, "tasks", "create", "--title", "second", "--yes", "--expect-digest", d1, "--json")
	if code != 1 || errorCode(out) != "changed" || len(m.ran) != 0 {
		t.Fatalf("the digest of one plan applied another: code=%d out=%s ran=%v", code, out, m.ran)
	}
}

// The task moved between the review and the apply: a different guard, so a different digest.
func TestPlanDigestChangesWhenTheTaskChangedUnderThePlan(t *testing.T) {
	m := newEditMod()
	_, out, _, _ := runIn(t, "", []core.Module{m}, "tasks", "assign", "t-1", "ada", "--dry-run", "--json")
	d := digestOf(t, out)
	m.cur.Assignee = "eve" // someone else took it
	code, out, _, au := runIn(t, "", []core.Module{m}, "tasks", "assign", "t-1", "ada", "--yes", "--expect-digest", d, "--json")
	if code != 1 || errorCode(out) != "changed" || len(m.ran) != 0 || au.n != 0 {
		t.Fatalf("code=%d out=%s ran=%v audit=%d", code, out, m.ran, au.n)
	}
	// and a task that is now already as asked no longer even plans: still "changed", not a usage error
	m.cur.Assignee = "ada"
	code, out, _, _ = runIn(t, "", []core.Module{m}, "tasks", "assign", "t-1", "ada", "--yes", "--expect-digest", d, "--json")
	if code != 1 || errorCode(out) != "changed" || len(m.ran) != 0 {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestBulkDigestAndResultReport(t *testing.T) {
	m := newEditMod()
	args := []string{"tasks", "bulk", "--ids", "t-1,t-3", "--op", "label-add", "--value", "sprint"}
	_, out, _, _ := runIn(t, "", []core.Module{m}, append(append([]string{}, args...), "--dry-run", "--json")...)
	d := digestOf(t, out)
	m.cur.Status = "blocked" // a target moved: the batch is a different plan
	code, out, _, _ := runIn(t, "", []core.Module{m}, append(append([]string{}, args...), "--yes", "--expect-digest", d, "--json")...)
	if code != 1 || errorCode(out) != "changed" || len(m.ran) != 0 {
		t.Fatalf("code=%d out=%s ran=%v", code, out, m.ran)
	}
	m.cur.Status = "open"
	code, out, _, _ = runIn(t, "", []core.Module{m}, append(append([]string{}, args...), "--yes", "--expect-digest", d, "--json")...)
	if code != 0 || len(m.ran) != 2 || digestOf(t, out) != d {
		t.Fatalf("code=%d out=%s ran=%v", code, out, m.ran)
	}
}

func TestExpectDigestMustBeADigest(t *testing.T) {
	for _, args := range [][]string{
		{"tasks", "close", "t-1", "--yes", "--expect-digest", "abc"},
		{"tasks", "label", "add", "t-1", "x", "--yes", "--expect-digest", "abc"},
		{"tasks", "bulk", "--ids", "t-1", "--op", "close", "--yes", "--expect-digest", "abc"},
		{"board", "post", "dev", "hi", "--yes", "--expect-digest", "abc"},
		{"setup", "--yes", "--expect-digest", "abc"},
	} {
		m := newEditMod()
		if code, _, errOut, _ := runIn(t, "", []core.Module{m, newBoardMod()}, args...); code != 2 || len(m.ran) != 0 || !strings.Contains(errOut, "64 hex digits") {
			t.Errorf("%v: code=%d err=%s", args, code, errOut)
		}
	}
}

func TestSetupDigestGuardsTheSteps(t *testing.T) {
	r := runSetupCmd(t, "", "setup", "--json", "--dry-run")
	d := digestOf(t, r.out)
	wrong := runSetupCmd(t, "", "setup", "--yes", "--json", "--expect-digest", strings.Repeat("1", 64))
	if c, _ := wrong.cfg.Load(); wrong.code != 1 || errorCode(wrong.out) != "changed" || c.Actor != "" || wrong.audit.n != 0 {
		t.Fatalf("code=%d out=%s actor=%q audit=%d", wrong.code, wrong.out, c.Actor, wrong.audit.n)
	}
	ok := runSetupCmd(t, "", "setup", "--yes", "--json", "--expect-digest", d)
	if c, _ := ok.cfg.Load(); ok.code != 0 || c.Actor != "me-mac" || digestOf(t, ok.out) != d {
		t.Fatalf("code=%d out=%s actor=%q", ok.code, ok.out, c.Actor)
	}
}

// Only the argv tells two batches of the same tasks apart when the value differs.
func TestBulkDigestFollowsTheValueInTheArgv(t *testing.T) {
	m := newEditMod()
	dry := func(value string) string {
		_, out, _, _ := runIn(t, "", []core.Module{m}, "tasks", "bulk", "--ids", "t-1,t-3", "--op", "label-add", "--value", value, "--dry-run", "--json")
		return digestOf(t, out)
	}
	if dry("sprint") == dry("backlog") {
		t.Fatal("two batches that run different commands share a digest")
	}
}
