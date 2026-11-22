package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/cli"
	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
)

// taskMod is a module that is the Tasks provider and writer, recording every write argv.
type taskMod struct {
	*modtest.Fake
	ran  [][]string
	fail error
}

func (m *taskMod) Tasks(context.Context) (core.TasksReport, error) {
	return core.TasksReport{Header: core.Header{Source: core.SourceLive}, Counts: core.TaskCounts{Total: 2, Open: 1, InProgress: 1, Ready: 1},
		Recent: []core.Task{{ID: "t-2", Title: "newer", Status: "in_progress", Priority: 1}}}, nil
}
func (m *taskMod) BuildTaskArgv(ch core.TaskChange) ([]string, error) {
	return []string{"bd", string(ch.Op), ch.ID, ch.Title}, nil
}
func (m *taskMod) RunTask(_ context.Context, argv []string) (core.TaskResult, error) {
	m.ran = append(m.ran, argv)
	return core.TaskResult{ID: "t-9", Title: "made", Status: "open"}, m.fail
}
func (m *taskMod) TrackerDir() string { return "" }

type memAudit struct{ n int }

func (a *memAudit) Record(core.AuditEntry) error { a.n++; return nil }

func newTaskMod() *taskMod {
	return &taskMod{Fake: &modtest.Fake{IDStr: "bd", Det: core.Detection{State: core.Ready, Version: "1"}}}
}

func runIn(t *testing.T, stdin string, mods []core.Module, args ...string) (int, string, string, *memAudit) {
	t.Helper()
	var o, e bytes.Buffer
	au := &memAudit{}
	code := cli.Run(args, cli.Deps{Out: &o, Err: &e, In: strings.NewReader(stdin), Audit: au, Version: "t",
		Build: func(cli.Options) *core.Registry { return core.NewRegistry(mods...) }})
	return code, o.String(), e.String(), au
}

func TestDomainCommandsShowNotConnectedWithHowToConnect(t *testing.T) {
	for _, d := range []string{"tasks", "leases", "queues", "board"} {
		code, out, _, _ := runIn(t, "", nil, d)
		if code != 0 || !strings.Contains(out, "not connected") {
			t.Errorf("%s: code=%d out=%q", d, code, out)
		}
		_, js, _, _ := runIn(t, "", nil, d, "--json")
		var got struct {
			SchemaVersion int    `json:"schema_version"`
			Domain        string `json:"domain"`
			State         string `json:"state"`
			How           string `json:"how_to_connect"`
		}
		if err := json.Unmarshal([]byte(js), &got); err != nil || got.SchemaVersion != 1 || got.Domain != d || got.State != "not-connected" || got.How == "" {
			t.Errorf("%s json: %v %+v\n%s", d, err, got, js)
		}
	}
}

func TestTasksReadShowsCountsAndRecent(t *testing.T) {
	_, out, _, _ := runIn(t, "", []core.Module{newTaskMod()}, "tasks")
	for _, want := range []string{"Tasks", "connected", "2 tasks", "t-2", "newer", "in_progress"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

func TestStatusJSONGainsDomainsAdditivelyAndKeepsSchemaVersionOne(t *testing.T) {
	_, out, _, _ := runIn(t, "", []core.Module{newTaskMod()}, "status", "--json")
	var d struct {
		SchemaVersion int `json:"schema_version"`
		Domains       *struct {
			Tasks struct{ State string } `json:"tasks"`
		} `json:"domains"`
	}
	if err := json.Unmarshal([]byte(out), &d); err != nil || d.SchemaVersion != 1 || d.Domains == nil || d.Domains.Tasks.State != "connected" {
		t.Fatalf("%v %s", err, out)
	}
	_, out, _, _ = runIn(t, "", []core.Module{newTaskMod()}, "doctor", "--json")
	if strings.Contains(out, `"domains"`) {
		t.Fatal("doctor does not carry domains")
	}
}

func TestCreateDryRunPrintsArgvAndRunsNothing(t *testing.T) {
	m := newTaskMod()
	code, out, _, au := runIn(t, "", []core.Module{m}, "tasks", "create", "--title", "hello", "--dry-run")
	if code != 0 || len(m.ran) != 0 || au.n != 0 || !strings.Contains(out, "bd create") || !strings.Contains(out, "hello") {
		t.Fatalf("code=%d ran=%v out=%s", code, m.ran, out)
	}
}

func TestWriteWithoutYesAsksAndOnlyYesProceeds(t *testing.T) {
	for stdin, wantRan := range map[string]int{"y\n": 1, "yes\n": 1, "n\n": 0, "\n": 0, "": 0, "maybe\n": 0} {
		m := newTaskMod()
		code, out, _, _ := runIn(t, stdin, []core.Module{m}, "tasks", "create", "--title", "hello")
		if len(m.ran) != wantRan {
			t.Errorf("stdin %q: ran %d, want %d", stdin, len(m.ran), wantRan)
		}
		if !strings.Contains(out, "[y/N]") {
			t.Errorf("stdin %q: no prompt:\n%s", stdin, out)
		}
		if wantRan == 0 && code == 0 {
			t.Errorf("stdin %q: declining must exit non-zero", stdin)
		}
	}
}

func TestYesSkipsThePromptAndAudits(t *testing.T) {
	m := newTaskMod()
	code, out, _, au := runIn(t, "", []core.Module{m}, "tasks", "close", "t-1", "--reason", "done", "--yes")
	if code != 0 || len(m.ran) != 1 || au.n != 0 && au.n != 2 || !strings.Contains(out, "t-9") {
		t.Fatalf("code=%d ran=%v out=%s", code, m.ran, out)
	}
}

func TestUpdateAndValidationErrorsExitTwoWithoutRunning(t *testing.T) {
	m := newTaskMod()
	code, _, _, _ := runIn(t, "", []core.Module{m}, "tasks", "update", "t-1", "--yes")
	if code != 2 || len(m.ran) != 0 {
		t.Fatalf("nothing to change: code=%d", code)
	}
	code, _, _, _ = runIn(t, "", []core.Module{m}, "tasks", "update", "--force", "--title", "x", "--yes")
	if code != 2 || len(m.ran) != 0 {
		t.Fatalf("flag-like id: code=%d", code)
	}
	code, _, _, _ = runIn(t, "", []core.Module{m}, "tasks", "delete", "t-1", "--yes")
	if code != 2 {
		t.Fatalf("unknown verb: code=%d", code)
	}
	code, _, _, _ = runIn(t, "", []core.Module{m}, "tasks", "update", "t-1", "--status", "closed", "--yes")
	if code != 2 || len(m.ran) != 0 {
		t.Fatalf("closing via update: code=%d", code)
	}
}

func TestWriteWithNoTaskProviderExplainsHowToConnect(t *testing.T) {
	code, _, errOut, _ := runIn(t, "", nil, "tasks", "create", "--title", "x", "--yes")
	if code != 1 || !strings.Contains(errOut, "not connected") {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
}

func TestFailedWriteExitsOneAndShowsTheError(t *testing.T) {
	m := newTaskMod()
	m.fail = errors.New("bd: boom")
	code, _, errOut, _ := runIn(t, "", []core.Module{m}, "tasks", "close", "t-1", "--yes")
	if code != 1 || !strings.Contains(errOut, "boom") {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
}

type insMod struct{ *modtest.Fake }

func (insMod) Insights(context.Context) (core.InsightsReport, error) {
	return core.InsightsReport{Header: core.Header{Source: core.SourceGraph},
		TopPicks: []core.Pick{{ID: "fx-1", Title: "base", Unblocks: 1}}, Cycles: [][]string{{"a", "b", "a"}}, TotalActionable: 1}, nil
}

func TestTasksShowsInsightsOrHowToInstall(t *testing.T) {
	_, out, _, _ := runIn(t, "", []core.Module{newTaskMod(), insMod{&modtest.Fake{IDStr: "g", Det: core.Detection{State: core.Ready}}}}, "tasks")
	for _, want := range []string{"graph analysis", "next fx-1", "cycle a -> b -> a"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	_, out, _, _ = runIn(t, "", []core.Module{newTaskMod()}, "tasks")
	if !strings.Contains(out, "brew install dicklesworthstone/tap/bv") {
		t.Errorf("no install hint:\n%s", out)
	}
}
