package core_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestInspectWorkspaceOnlyCallsAFailedInitRepairable(t *testing.T) {
	half := func(t *testing.T, dir string) { write(t, filepath.Join(dir, ".beads", ".gitignore"), "x: 1\n") }
	cases := []struct {
		name  string
		setup func(t *testing.T, dir string)
		want  core.WorkspaceHealth
	}{
		{"missing", func(t *testing.T, dir string) { os.RemoveAll(dir) }, core.WorkspaceClean},
		{"empty", func(*testing.T, string) {}, core.WorkspaceClean},
		{"ready", func(t *testing.T, dir string) { mkOwnTracker(t, dir) }, core.WorkspaceReady},
		{"half", half, core.WorkspaceHalfBuilt},
		{"half with git", func(t *testing.T, dir string) { half(t, dir); os.Mkdir(filepath.Join(dir, ".git"), 0o755) }, core.WorkspaceHalfBuilt},
		{"half with an earlier repair leftover", func(t *testing.T, dir string) {
			half(t, dir)
			os.Mkdir(filepath.Join(dir, ".beads.incomplete-20260101T000000Z"), 0o755)
		}, core.WorkspaceHalfBuilt},
		{"only leftovers", func(t *testing.T, dir string) { os.Mkdir(filepath.Join(dir, ".beads.incomplete-x"), 0o755) }, core.WorkspaceClean},
		{"init's own leftovers", func(t *testing.T, dir string) {
			half(t, dir)
			write(t, filepath.Join(dir, ".gitignore"), "x")
			write(t, filepath.Join(dir, ".beads.gate.lock"), "")
			write(t, filepath.Join(dir, ".beads.gate.lock.info"), "{}")
		}, core.WorkspaceHalfBuilt},
		{".gitignore is a link", func(t *testing.T, dir string) {
			half(t, dir)
			os.Symlink("/etc/hosts", filepath.Join(dir, ".gitignore"))
		}, core.WorkspaceUnsafe},
		{"config.yaml inside", func(t *testing.T, dir string) {
			half(t, dir)
			write(t, filepath.Join(dir, ".beads", "config.yaml"), "a: b")
		}, core.WorkspaceUnsafe},
		{"hooks folder inside", func(t *testing.T, dir string) {
			half(t, dir)
			write(t, filepath.Join(dir, ".beads", "hooks", "pre-commit"), "#!/bin/sh")
		}, core.WorkspaceUnsafe},
		{"empty hooks folder inside", func(t *testing.T, dir string) {
			half(t, dir)
			os.MkdirAll(filepath.Join(dir, ".beads", "hooks"), 0o755)
		}, core.WorkspaceUnsafe},
		{"database file inside", func(t *testing.T, dir string) {
			half(t, dir)
			write(t, filepath.Join(dir, ".beads", "beads.db"), "SQLite")
		}, core.WorkspaceUnsafe},
		{"backup inside", func(t *testing.T, dir string) {
			half(t, dir)
			write(t, filepath.Join(dir, ".beads", "backup", "x.json"), "{}")
		}, core.WorkspaceUnsafe},
		{"another database folder", func(t *testing.T, dir string) { half(t, dir); write(t, filepath.Join(dir, ".beads", "dolt", "x"), "d") }, core.WorkspaceUnsafe},
		{"a failed init after the database started", func(t *testing.T, dir string) { failedInitTree(t, dir, 3000) }, core.WorkspaceHalfBuilt},
		{"database with real data in it", func(t *testing.T, dir string) { failedInitTree(t, dir, 2<<20) }, core.WorkspaceUnsafe},
		{"unknown file in the database tree", func(t *testing.T, dir string) {
			failedInitTree(t, dir, 3000)
			write(t, filepath.Join(dir, ".beads", "embeddeddolt", "abacus", ".dolt", "noms", "other"), "x")
		}, core.WorkspaceUnsafe},
		{"user file beside it", func(t *testing.T, dir string) { half(t, dir); write(t, filepath.Join(dir, "notes.md"), "mine") }, core.WorkspaceUnsafe},
		{"user file, no .beads", func(t *testing.T, dir string) { write(t, filepath.Join(dir, "notes.md"), "mine") }, core.WorkspaceUnsafe},
		{"task export inside", func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, ".beads", "issues.jsonl"), `{"id":"a-1"}`+"\n")
		}, core.WorkspaceUnsafe},
		{"empty export inside", func(t *testing.T, dir string) { write(t, filepath.Join(dir, ".beads", "issues.jsonl"), "") }, core.WorkspaceUnsafe},
		{"nested export inside", func(t *testing.T, dir string) { write(t, filepath.Join(dir, ".beads", "a", "b", "x.JSONL"), "{}") }, core.WorkspaceUnsafe},
		{".beads is a link", func(t *testing.T, dir string) {
			os.Symlink(t.TempDir(), filepath.Join(dir, ".beads"))
		}, core.WorkspaceUnsafe},
		{"link inside .beads", func(t *testing.T, dir string) {
			half(t, dir)
			os.Symlink("/etc/hosts", filepath.Join(dir, ".beads", "link"))
		}, core.WorkspaceUnsafe},
		{".git is a file", func(t *testing.T, dir string) { half(t, dir); write(t, filepath.Join(dir, ".git"), "gitdir: x") }, core.WorkspaceUnsafe},
		{"workspace is a file", func(t *testing.T, dir string) { os.RemoveAll(dir); write(t, dir, "x") }, core.WorkspaceUnsafe},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "ws")
			os.Mkdir(dir, 0o755)
			c.setup(t, dir)
			if got, why := core.InspectWorkspace(dir); got != c.want || (got == core.WorkspaceUnsafe) != (why != "") {
				t.Fatalf("got %s (%q), want %s", got, why, c.want)
			}
		})
	}
}

func halfEnv(t *testing.T) setupEnv {
	t.Helper()
	e := newSetupEnv(t)
	write(t, filepath.Join(e.ws, ".beads", ".gitignore"), "x: 1\n")
	return e
}

func TestRepairPlanShowsTheExactMoveAndRefusesAnythingElse(t *testing.T) {
	e := halfEnv(t)
	p, err := e.s.Plan(core.StepRepair)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(p.Lines, "\n")
	for _, want := range []string{filepath.Join(e.ws, ".beads"), filepath.Join(e.ws, ".beads.incomplete-"), "nothing is deleted", "bd init"} {
		if !strings.Contains(all, want) {
			t.Errorf("plan is missing %q:\n%s", want, all)
		}
	}
	// the plain workspace step points at the repair rather than failing at apply
	if _, err := e.s.Plan(core.StepWorkspace); err == nil || !strings.Contains(err.Error(), "--repair") {
		t.Fatalf("workspace plan on a half-built folder: %v", err)
	}
	write(t, filepath.Join(e.ws, "mine.txt"), "x")
	if _, err := e.s.Plan(core.StepRepair); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("a folder with user files must be refused: %v", err)
	}
	os.Remove(filepath.Join(e.ws, "mine.txt"))
	os.RemoveAll(filepath.Join(e.ws, ".beads"))
	if _, err := e.s.Plan(core.StepRepair); err == nil {
		t.Fatal("nothing half-created: nothing to repair")
	}
}

func TestRepairMovesTheUnfinishedFolderAsideAuditsAndCreatesAgain(t *testing.T) {
	e := halfEnv(t)
	fixed := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	e.s = core.NewSetup(entriesOf(e), core.SetupOptions{Config: e.cfg, Workspace: e.ws, LambeConfig: e.lc, Audit: e.audit, Now: func() time.Time { return fixed }})
	p, err := e.s.Plan(core.StepRepair)
	if err != nil {
		t.Fatal(err)
	}
	aside := filepath.Join(e.ws, ".beads.incomplete-20261123T100000Z")
	if _, err := e.s.Apply(context.Background(), p); !errors.Is(err, core.ErrNotConfirmed) {
		t.Fatalf("unconfirmed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.ws, ".beads", ".gitignore")); err != nil {
		t.Fatal("an unconfirmed repair touched the folder")
	}
	msg, err := e.s.Apply(context.Background(), p.Confirm())
	if err != nil || !strings.Contains(msg, aside) {
		t.Fatalf("%q %v", msg, err)
	}
	if b, err := os.ReadFile(filepath.Join(aside, ".gitignore")); err != nil || string(b) != "x: 1\n" {
		t.Fatalf("the user's files must be kept intact: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(e.ws, ".beads")); err == nil {
		t.Fatal("the unfinished .beads should have been moved")
	}
	if len(e.bd.ran) != 1 {
		t.Fatalf("init should run once: %v", e.bd.ran)
	}
	var verbs []string
	for _, a := range e.audit.entries {
		verbs = append(verbs, a.Verb+"/"+a.Phase)
	}
	if got := strings.Join(verbs, " "); !strings.HasPrefix(got, "setup-repair/attempt setup-repair/ok init/") {
		t.Fatalf("audit: %s", got)
	}
	if _, err := e.s.Apply(context.Background(), p.Confirm()); !errors.Is(err, core.ErrAlreadyApplied) {
		t.Fatalf("single use: %v", err)
	}
}

func entriesOf(e setupEnv) []core.Entry {
	return []core.Entry{ready(e.bd), ready(e.lambe), ready(e.leases)}
}

func TestRepairFailsClosedWhenTheFolderChangedOrTheAuditFails(t *testing.T) {
	e := halfEnv(t)
	p, _ := e.s.Plan(core.StepRepair)
	write(t, filepath.Join(e.ws, "late.txt"), "added after planning")
	if _, err := e.s.Apply(context.Background(), p.Confirm()); err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Fatalf("a file added after the plan must stop the repair: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.ws, ".beads", ".gitignore")); err != nil || len(e.bd.ran) != 0 {
		t.Fatal("nothing may move or run")
	}

	e = halfEnv(t)
	e.s = core.NewSetup(entriesOf(e), core.SetupOptions{Config: e.cfg, Workspace: e.ws, LambeConfig: e.lc, Audit: failAudit{}})
	p, _ = e.s.Plan(core.StepRepair)
	if _, err := e.s.Apply(context.Background(), p.Confirm()); err == nil {
		t.Fatal("a failed audit must block the repair")
	}
	if _, err := os.Stat(filepath.Join(e.ws, ".beads", ".gitignore")); err != nil {
		t.Fatal("the folder moved without an audit line")
	}
}

func TestStepsReportARepairThatSetupYesNeverApplies(t *testing.T) {
	e := halfEnv(t)
	e.bd.guide = &core.Guide{Domain: core.DomainTasks, Step: core.StepRepair, Message: "Your fleet workspace was only partly created", Action: "repair it"}
	var tasks core.StepInfo
	for _, st := range e.s.Steps(context.Background()) {
		if st.Domain == core.DomainTasks {
			tasks = st
		}
	}
	if tasks.Step != core.StepRepair || tasks.State != "repair" || !strings.Contains(tasks.Command, "--repair") {
		t.Fatalf("%+v", tasks)
	}
}

func TestRepairNeverOverwritesAnExistingAsideFolder(t *testing.T) {
	e := halfEnv(t)
	fixed := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	e.s = core.NewSetup(entriesOf(e), core.SetupOptions{Config: e.cfg, Workspace: e.ws, LambeConfig: e.lc, Audit: e.audit, Now: func() time.Time { return fixed }})
	p, _ := e.s.Plan(core.StepRepair)
	write(t, filepath.Join(e.ws, ".beads.incomplete-20261123T100000Z", "keep.txt"), "an earlier leftover")
	if _, err := e.s.Apply(context.Background(), p.Confirm()); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(filepath.Join(e.ws, ".beads", ".gitignore")); err != nil {
		t.Fatal("the unfinished folder must stay where it was")
	}
}

func TestARepairGuideNamesTheRepairCommandEverywhereItIsShown(t *testing.T) {
	g := core.Guide{Step: core.StepRepair, Message: "Your fleet workspace was only partly created", Action: "repair it"}
	if got := g.CLILine(); !strings.Contains(got, "setup --repair") {
		t.Fatal(got)
	}
	if got := (core.Guide{Step: core.StepWorkspace, Message: "m", Action: "a"}).CLILine(); strings.Contains(got, "--repair") {
		t.Fatal(got)
	}
}

// failedInitTree is what a real `bd init` killed after starting its database leaves (seen with
// bd 1.3.4), with the big table file at the given size.
func failedInitTree(t *testing.T, dir string, tableBytes int) {
	t.Helper()
	b := filepath.Join(dir, ".beads")
	db := filepath.Join(b, "embeddeddolt", "abacus", ".dolt")
	write(t, filepath.Join(b, ".gitignore"), strings.Repeat("x", 1941))
	write(t, filepath.Join(b, "embeddeddolt", ".lock"), "")
	write(t, filepath.Join(b, "embeddeddolt", "abacus", ".dolt_safe_to_ignore"), "")
	write(t, filepath.Join(db, "config.json"), "{}")
	write(t, filepath.Join(db, "noms", "manifest"), "5:__DOLT__")
	write(t, filepath.Join(db, "noms", "LOCK"), "")
	write(t, filepath.Join(db, "noms", "journal.idx"), "")
	write(t, filepath.Join(db, "noms", strings.Repeat("v", 32)), strings.Repeat("d", tableBytes))
	os.MkdirAll(filepath.Join(db, "noms", "oldgen"), 0o755)
	os.MkdirAll(filepath.Join(db, "temptf"), 0o755)
}

func TestRepairNeedsInitToBeAbleToRunAndMovesNothingOtherwise(t *testing.T) {
	e := halfEnv(t)
	p, err := e.s.Plan(core.StepRepair)
	if err != nil {
		t.Fatal(err)
	}
	e.bd.preflight = errors.New("the bundled bd was rejected as unsafe")
	if _, err := e.s.Plan(core.StepRepair); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("plan must refuse when init cannot run: %v", err)
	}
	if _, err := e.s.Apply(context.Background(), p.Confirm()); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("apply must refuse too: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.ws, ".beads", ".gitignore")); err != nil || len(e.bd.ran) != 0 {
		t.Fatal("nothing may move when init cannot run")
	}
}

func TestRepairWordsAnUnknownInitOutcomeAsUnknownWithWhereTheFilesWent(t *testing.T) {
	e := halfEnv(t)
	e.bd.runErr = core.UnknownOutcome("bd init timed out")
	p, _ := e.s.Plan(core.StepRepair)
	_, err := e.s.Apply(context.Background(), p.Confirm())
	if !errors.Is(err, core.ErrOutcomeUnknown) || !strings.Contains(err.Error(), ".beads.incomplete-") || !strings.Contains(err.Error(), "is unknown: check") {
		t.Fatalf("%v", err)
	}
	e = halfEnv(t)
	e.bd.runErr = errors.New("bd init exited 1")
	p, _ = e.s.Plan(core.StepRepair)
	if _, err := e.s.Apply(context.Background(), p.Confirm()); err == nil || errors.Is(err, core.ErrOutcomeUnknown) || !strings.Contains(err.Error(), ".beads.incomplete-") {
		t.Fatalf("%v", err)
	}
}
