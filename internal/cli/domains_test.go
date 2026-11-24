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
func (m *taskMod) ReadTask(_ context.Context, id string) (core.Task, error) {
	return core.Task{ID: id, Title: "current", Status: "open"}, nil
}

// readFailMod is a Tasks writer whose current-status read fails (or that cannot read at all).
type readFailMod struct {
	*taskMod
	err error
}

func (m readFailMod) ReadTask(context.Context, string) (core.Task, error) { return core.Task{}, m.err }

// writeOnlyMod provides the writer but no reader.
type writeOnlyMod struct{ m *taskMod }

func (w writeOnlyMod) ID() string                              { return "bd" }
func (w writeOnlyMod) Name() string                            { return "bd" }
func (w writeOnlyMod) Detect(c context.Context) core.Detection { return w.m.Detect(c) }
func (w writeOnlyMod) Doctor(c context.Context) []core.Check   { return w.m.Doctor(c) }
func (w writeOnlyMod) Status(c context.Context) core.Status    { return w.m.Status(c) }
func (w writeOnlyMod) Domain() core.DomainID                   { return core.DomainTasks }
func (w writeOnlyMod) Tasks(c context.Context) (core.TasksReport, error) {
	return w.m.Tasks(c)
}
func (w writeOnlyMod) BuildTaskArgv(ch core.TaskChange) ([]string, error) {
	return w.m.BuildTaskArgv(ch)
}
func (w writeOnlyMod) RunTask(c context.Context, a []string) (core.TaskResult, error) {
	return w.m.RunTask(c, a)
}
func (w writeOnlyMod) TrackerDir() string { return "" }

type memAudit struct {
	n   int
	err error
}

func (a *memAudit) Record(core.AuditEntry) error {
	if a.err != nil {
		return a.err
	}
	a.n++
	return nil
}

func newTaskMod() *taskMod {
	return &taskMod{Fake: &modtest.Fake{IDStr: "bd", Det: core.Detection{State: core.Ready, Version: "1"}}}
}

func runIn(t *testing.T, stdin string, mods []core.Module, args ...string) (int, string, string, *memAudit) {
	t.Helper()
	return runAudit(t, stdin, mods, &memAudit{}, args...)
}

func runAudit(t *testing.T, stdin string, mods []core.Module, au *memAudit, args ...string) (int, string, string, *memAudit) {
	t.Helper()
	var o, e bytes.Buffer
	code := cli.Run(args, cli.Deps{Out: &o, Err: &e, In: strings.NewReader(stdin), Audit: au, Version: "t",
		Build: func(cli.Options) *core.Registry { return core.NewRegistry(mods...) }})
	return code, o.String(), e.String(), au
}

func TestDomainCommandsShowNotConnectedWithHowToConnect(t *testing.T) {
	for _, d := range []string{"tasks", "leases", "queues", "board", "sessions"} {
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
		// Sessions is this machine, not a tool: there is nothing to install, so it carries no
		// how-to-connect text, while every tool-backed domain names what to set up.
		wantHow := d != "sessions"
		if err := json.Unmarshal([]byte(js), &got); err != nil || got.SchemaVersion != 1 || got.Domain != d || got.State != "not-connected" || (got.How != "") != wantHow {
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
		code, out, errOut, _ := runIn(t, stdin, []core.Module{m}, "tasks", "create", "--title", "hello")
		if len(m.ran) != wantRan {
			t.Errorf("stdin %q: ran %d, want %d", stdin, len(m.ran), wantRan)
		}
		if strings.Contains(out, "[y/N]") || !strings.Contains(errOut, "[y/N]") {
			t.Errorf("stdin %q: the prompt goes to stderr so stdout stays the result:\nout=%s\nerr=%s", stdin, out, errOut)
		}
		if wantRan == 0 && code == 0 {
			t.Errorf("stdin %q: declining must exit non-zero", stdin)
		}
	}
}

func TestYesSkipsThePromptAndAudits(t *testing.T) {
	m := newTaskMod()
	code, out, _, au := runIn(t, "", []core.Module{m}, "tasks", "close", "t-1", "--reason", "done", "--yes")
	if code != 0 || len(m.ran) != 1 || au.n != 2 || !strings.Contains(out, "t-9") {
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

func TestTasksShowsInsightsOrSaysWhatIsWrong(t *testing.T) {
	_, out, _, _ := runIn(t, "", []core.Module{newTaskMod(), insMod{&modtest.Fake{IDStr: "g", Det: core.Detection{State: core.Ready}}}}, "tasks")
	for _, want := range []string{"graph analysis", "next fx-1", "cycle a -> b -> a"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	_, out, _, _ = runIn(t, "", []core.Module{newTaskMod()}, "tasks")
	if !strings.Contains(out, "bd is not available") || strings.Contains(out, "install") {
		t.Errorf("a missing bundled bd is abacus's problem, not an install task:\n%s", out)
	}
}

// ---- --json contract: one JSON document on stdout, nothing else ----

func oneJSON(t *testing.T, out string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(out))
	var v map[string]any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%q", err, out)
	}
	if dec.More() {
		t.Fatalf("stdout carries more than one document: %q", out)
	}
	if v["schema_version"] != float64(1) {
		t.Fatalf("no schema_version: %v", v)
	}
	return v
}

func TestJSONDryRunPrintsOnlyTheDocument(t *testing.T) {
	m := newTaskMod()
	code, out, _, au := runIn(t, "", []core.Module{m}, "tasks", "create", "--title", "hello", "--dry-run", "--json")
	v := oneJSON(t, out)
	if code != 0 || len(m.ran) != 0 || au.n != 0 || v["dry_run"] != true || v["argv"] == nil || v["change"] == nil {
		t.Fatalf("code=%d v=%v", code, v)
	}
}

func TestJSONConfirmedPrintsOnlyTheResult(t *testing.T) {
	m := newTaskMod()
	code, out, _, _ := runIn(t, "", []core.Module{m}, "tasks", "close", "t-1", "--yes", "--json")
	v := oneJSON(t, out)
	if code != 0 || v["applied"] != true || v["result"] == nil || len(m.ran) != 1 {
		t.Fatalf("code=%d v=%v", code, v)
	}
}

func TestJSONWithoutYesOrDryRunIsAUsageErrorWithEmptyStdout(t *testing.T) {
	m := newTaskMod()
	code, out, errOut, _ := runIn(t, "y\n", []core.Module{m}, "tasks", "create", "--title", "hello", "--json")
	if code != 2 || out != "" || len(m.ran) != 0 || !strings.Contains(errOut, "--yes") {
		t.Fatalf("code=%d out=%q err=%q ran=%v", code, out, errOut, m.ran)
	}
}

func TestJSONFailureLeavesStdoutEmpty(t *testing.T) {
	m := newTaskMod()
	m.fail = errors.New("bd: boom")
	code, out, errOut, _ := runIn(t, "", []core.Module{m}, "tasks", "close", "t-1", "--yes", "--json")
	if code != 1 || out != "" || !strings.Contains(errOut, "boom") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, _, _ = runIn(t, "", nil, "tasks", "create", "--title", "x", "--yes", "--json")
	if code != 1 || out != "" {
		t.Fatalf("not connected under --json: code=%d out=%q", code, out)
	}
}

func TestUnknownOutcomeIsNeverReportedAsNotApplied(t *testing.T) {
	m := newTaskMod()
	m.fail = core.UnknownOutcome("bd timed out; refresh before retrying")
	code, out, errOut, au := runIn(t, "", []core.Module{m}, "tasks", "close", "t-1", "--yes")
	if code != 1 || !strings.Contains(errOut, "result unknown: bd timed out; refresh before retrying") || strings.Contains(errOut+out, "not applied") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	if au.n != 2 {
		t.Fatalf("audit %d", au.n)
	}
}

func TestAuditFailureBlocksTheWriteAndExitsOne(t *testing.T) {
	m := newTaskMod()
	code, _, errOut, _ := runAudit(t, "", []core.Module{m}, &memAudit{err: errors.New("disk full")}, "tasks", "close", "t-1", "--yes")
	if code != 1 || len(m.ran) != 0 || !strings.Contains(errOut, "disk full") {
		t.Fatalf("code=%d ran=%v err=%q", code, m.ran, errOut)
	}
}

// ---- stray positionals ----

func TestStrayPositionalsAreUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"tasks", "create", "--yes", "--title", "Fix", "the", "bug"},
		{"tasks", "--json", "create"},
		{"tasks", "extra"},
		{"tasks", "close", "t-1", "extra", "--yes"},
		{"leases", "extra"},
		{"queues", "--json", "extra"},
		{"board", "x"},
	} {
		m := newTaskMod()
		code, out, _, _ := runIn(t, "", []core.Module{m}, args...)
		if code != 2 || len(m.ran) != 0 || out != "" && strings.Contains(out, "done") {
			t.Errorf("%v: code=%d ran=%v", args, code, m.ran)
		}
	}
}

func TestUpdateRejectsABlankTitle(t *testing.T) {
	m := newTaskMod()
	code, _, _, _ := runIn(t, "", []core.Module{m}, "tasks", "update", "t-1", "--title", " ", "--yes")
	if code != 2 || len(m.ran) != 0 {
		t.Fatalf("code=%d", code)
	}
}

// ---- one domain, one provider ----

type countMod struct {
	*modtest.Fake
	tasks, leases, queues, board, insights int
}

func (m *countMod) Tasks(context.Context) (core.TasksReport, error) {
	m.tasks++
	return core.TasksReport{Header: core.Header{Source: core.SourceLive}, Counts: core.TaskCounts{Total: 3}}, nil
}
func (m *countMod) Leases(context.Context) (core.LeasesReport, error) {
	m.leases++
	return core.LeasesReport{Header: core.Header{Source: core.SourceLive}}, nil
}
func (m *countMod) Queues(context.Context) (core.QueuesReport, error) {
	m.queues++
	return core.QueuesReport{Header: core.Header{Source: core.SourceLive}}, nil
}
func (m *countMod) Board(context.Context) (core.BoardReport, error) {
	m.board++
	return core.BoardReport{Header: core.Header{Source: core.SourceLive}}, nil
}
func (m *countMod) Insights(context.Context) (core.InsightsReport, error) {
	m.insights++
	return core.InsightsReport{Header: core.Header{Source: core.SourceGraph}}, nil
}

// Status reads every provider the way the real modules do, so a status run that also asks the
// module for its Status reads twice.
func (m *countMod) Status(ctx context.Context) core.Status {
	m.tasks++
	m.leases++
	m.queues++
	m.board++
	m.insights++
	return core.Status{Summary: "own"}
}

func newCount() *countMod {
	return &countMod{Fake: &modtest.Fake{IDStr: "c", Det: core.Detection{State: core.Ready}}}
}

func TestSingleDomainCommandsReadOnlyThatProvider(t *testing.T) {
	want := map[string][5]int{"leases": {0, 1, 0, 0, 0}, "queues": {0, 0, 1, 0, 0}, "tasks": {1, 0, 0, 0, 1}, "board": {0, 0, 0, 1, 0}}
	for cmd, w := range want {
		m := newCount()
		runIn(t, "", []core.Module{m}, cmd)
		if got := [5]int{m.tasks, m.leases, m.queues, m.board, m.insights}; got != w {
			t.Errorf("%s read %v, want %v", cmd, got, w)
		}
	}
}

func TestStatusReadsEachProviderOnce(t *testing.T) {
	m := newCount()
	_, out, _, _ := runIn(t, "", []core.Module{m}, "status", "--json")
	if [5]int{m.tasks, m.leases, m.queues, m.board, m.insights} != [5]int{1, 1, 1, 1, 1} {
		t.Fatalf("status read a provider more than once: %+v", m)
	}
	var d struct {
		Modules []struct {
			Status *core.Status `json:"status"`
		} `json:"modules"`
	}
	if err := json.Unmarshal([]byte(out), &d); err != nil || d.Modules[0].Status == nil {
		t.Fatalf("%v %s", err, out)
	}
}

type leaseMod struct{ *modtest.Fake }

func (leaseMod) Leases(context.Context) (core.LeasesReport, error) {
	return core.LeasesReport{Header: core.Header{Source: core.SourceLive}, Leases: []core.Lease{
		{Resource: "a", State: core.LeaseHeld, Holder: "h", ExpiresAt: "T"}, {Resource: "b", State: core.LeaseStale, Holder: "old"}, {Resource: "c", State: core.LeaseFree}}}, nil
}

func TestLeasesTextUsesTheSharedLeaseDetail(t *testing.T) {
	_, out, _, _ := runIn(t, "", []core.Module{leaseMod{&modtest.Fake{IDStr: "l", Det: core.Detection{State: core.Ready}}}}, "leases")
	for _, want := range []string{"1 held, 1 stale, 1 free", "a  held by h (expires T)", "b  stale (last holder old)", "c  free\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

// boardMod is a module that provides the Board domain and its writer, recording every write.
type boardMod struct {
	*modtest.Fake
	ran     [][]string
	fail    error
	readErr error
	trunc   string
}

func (m *boardMod) TargetSource() string { return "board_url (config)" }
func (m *boardMod) Describe(_ context.Context, ch core.BoardChange) []string {
	return []string{"existing channel " + ch.Channel + ch.ID}
}

func (m *boardMod) Board(context.Context) (core.BoardReport, error) {
	if m.readErr != nil {
		return core.BoardReport{}, m.readErr
	}
	return core.BoardReport{Header: core.Header{Source: core.SourceLive},
		Truncated: m.trunc,
		Channels:  []core.BoardChannel{{Path: "dev", Threads: 1}, {Path: "work"}},
		Threads:   []core.BoardThread{{Root: core.BoardMessage{ID: "2", Channel: "dev", Author: "ada", Text: "deploys are green\nagain"}, Replies: 1, Last: &core.BoardMessage{ID: "3", Author: "claude", Text: "ack"}}},
		Tasks:     []core.BoardTask{{ID: "4", State: "claimed", Claimant: "codex via ada", Text: "fix it"}}}, nil
}
func (m *boardMod) BuildBoardArgv(ch core.BoardChange, key string) ([]string, error) {
	return []string{"board", string(ch.Op), ch.Channel + ch.ID, "--idempotency-key", key, "--", ch.Text}, nil
}
func (m *boardMod) RunBoard(_ context.Context, argv []string, ch core.BoardChange) (core.BoardResult, error) {
	m.ran = append(m.ran, argv)
	return core.BoardResult{ID: "9", Text: ch.Text}, m.fail
}
func (m *boardMod) Target() string { return "the default board" }

func newBoardMod() *boardMod {
	return &boardMod{Fake: &modtest.Fake{IDStr: "board", Det: core.Detection{State: core.Ready, Version: "0.54.0"}}}
}

func TestBoardCommandRendersChannelsThreadsAndTasks(t *testing.T) {
	_, out, _, _ := runIn(t, "", []core.Module{newBoardMod()}, "board")
	for _, want := range []string{"channel dev", "thread #2", "deploys are green", "latest #3", "task #4  claimed  held by codex via ada"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	_, js, _, _ := runIn(t, "", []core.Module{newBoardMod()}, "board", "--json")
	var got struct {
		SchemaVersion int `json:"schema_version"`
		Channels      []struct{ Path string }
		Threads       []struct{ Replies int }
		Tasks         []struct{ State, Claimant string }
	}
	if err := json.Unmarshal([]byte(js), &got); err != nil || got.SchemaVersion != 1 || len(got.Channels) != 2 || got.Threads[0].Replies != 1 || got.Tasks[0].Claimant != "codex via ada" {
		t.Fatalf("%v %s", err, js)
	}
}

func TestBoardPostNeedsAConfirmAndRunsOnce(t *testing.T) {
	m := newBoardMod()
	code, out, _, au := runIn(t, "n\n", []core.Module{m}, "board", "post", "dev", "hello")
	if code == 0 || len(m.ran) != 0 || au.n != 0 || !strings.Contains(out, "channel dev") || !strings.Contains(out, `"hello"`) {
		t.Fatalf("a declined confirm runs nothing: code=%d ran=%v out=%s", code, m.ran, out)
	}
	code, out, _, au = runIn(t, "y\n", []core.Module{m}, "board", "post", "dev", "hello")
	if code != 0 || len(m.ran) != 1 || au.n != 2 || !strings.Contains(out, "done: posted #9") {
		t.Fatalf("code=%d ran=%v audit=%d out=%s", code, m.ran, au.n, out)
	}
	code, _, _, _ = runIn(t, "y\n", []core.Module{m}, "board", "reply", "17", "ack", "--yes")
	if code != 0 || len(m.ran) != 2 || m.ran[1][1] != "reply" || m.ran[1][2] != "17" {
		t.Fatalf("%v", m.ran)
	}
}

func TestBoardWriteDryRunAndJSONRules(t *testing.T) {
	m := newBoardMod()
	if code, out, _, _ := runIn(t, "", []core.Module{m}, "board", "post", "dev", "hi", "--dry-run"); code != 0 || len(m.ran) != 0 || !strings.Contains(out, "command: board post dev") {
		t.Fatalf("%d %s", code, out)
	}
	if code, _, e, _ := runIn(t, "", []core.Module{m}, "board", "post", "dev", "hi", "--json"); code == 0 || !strings.Contains(e, "--yes or --dry-run") {
		t.Fatalf("--json cannot prompt: %d %s", code, e)
	}
	// Text that starts with a dash goes through --text, never as an option.
	if code, _, _, _ := runIn(t, "", []core.Module{m}, "board", "post", "dev", "--text", "--file x", "--yes"); code != 0 || m.ran[0][len(m.ran[0])-1] != "--file x" {
		t.Fatalf("%v", m.ran)
	}
	for _, bad := range [][]string{{"board", "post"}, {"board", "post", "dev"}, {"board", "post", "bad channel", "x", "--yes"}, {"board", "reply", "dev", "x", "--yes"}, {"board", "delete", "1"}, {"board", "post", "dev", "a", "b", "--yes"}} {
		if code, _, _, _ := runIn(t, "", []core.Module{m}, bad...); code == 0 {
			t.Errorf("must refuse %v", bad)
		}
	}
	if len(m.ran) != 1 {
		t.Fatalf("only the --text write ran: %v", m.ran)
	}
}

func TestBoardWriteFailsClosedAndSaysUnknownOutcomeHonestly(t *testing.T) {
	m := newBoardMod()
	if code, _, _, _ := runAudit(t, "", []core.Module{m}, &memAudit{err: errors.New("disk full")}, "board", "post", "dev", "hi", "--yes"); code == 0 || len(m.ran) != 0 {
		t.Fatal("a failed audit blocks the write")
	}
	m.fail = core.UnknownOutcome("timed out")
	_, _, e, _ := runIn(t, "", []core.Module{m}, "board", "post", "dev", "hi", "--yes")
	if strings.Contains(e, "not applied") || !strings.Contains(e, "result unknown") {
		t.Fatalf("%s", e)
	}
	if code, _, e, _ := runIn(t, "", nil, "board", "post", "dev", "hi", "--yes"); code == 0 || !strings.Contains(e, "board is not available") || strings.Contains(e, "npm") {
		t.Fatalf("not connected says how: %d %s", code, e)
	}
}

func TestBoardConfirmShowsTheTargetAndTheContextBeforeThePrompt(t *testing.T) {
	m := newBoardMod()
	_, out, errOut, _ := runIn(t, "n\n", []core.Module{m}, "board", "post", "dev", "hello")
	for _, want := range []string{"board: the default board (board_url (config))", "existing channel dev"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(errOut, "[y/N]") {
		t.Error("the prompt follows the context")
	}
	_, js, _, _ := runIn(t, "", []core.Module{m}, "board", "reply", "5", "x", "--dry-run", "--json")
	if !strings.Contains(js, `"context"`) || !strings.Contains(js, "existing channel 5") {
		t.Fatalf("%s", js)
	}
}

func TestBoardReadFailureIsDegradedWithoutAnInstallHint(t *testing.T) {
	m := newBoardMod()
	m.readErr = errors.New("could not reach the board")
	_, js, _, _ := runIn(t, "", []core.Module{m}, "status", "--json")
	if !strings.Contains(js, `"state": "degraded"`) || strings.Contains(js, "npm i -g") {
		t.Fatalf("a present but failing CLI is degraded and is not told to install: %s", js)
	}
	_, out, _, _ := runIn(t, "", []core.Module{m}, "board")
	if strings.Contains(out, "npm i") || !strings.Contains(out, "board_url") {
		t.Fatalf("%s", out)
	}
}

func TestBoardRenderHidesUnknownCountsAndShowsTruncation(t *testing.T) {
	m := newBoardMod()
	m.trunc = "only the first 50 channels are listed"
	_, out, _, _ := runIn(t, "", []core.Module{m}, "board")
	if !strings.Contains(out, "channel dev  (1 threads)") || strings.Contains(out, "(0 threads)") || !strings.Contains(out, "channel work\n") {
		t.Fatalf("%s", out)
	}
	if !strings.Contains(out, "only the first 50 channels are listed") {
		t.Fatalf("%s", out)
	}
	_, js, _, _ := runIn(t, "", []core.Module{m}, "board", "--json")
	if !strings.Contains(js, `"truncated": "only the first 50`) || strings.Contains(js, `"state": "degraded"`) {
		t.Fatalf("truncation is not degradation: %s", js)
	}
}

// A status change reads the task first and sends its status as the guard, so a change made by
// someone else in between is refused by the tracker, not overwritten.
func TestUpdateStatusReadsTheCurrentStatusAsTheGuard(t *testing.T) {
	m := newTaskMod()
	code, out, _, _ := runIn(t, "", []core.Module{m}, "tasks", "update", "t-1", "--status", "blocked", "--dry-run")
	if code != 0 || len(m.ran) != 0 || !strings.Contains(out, `status: "open" -> "blocked"`) {
		t.Fatalf("code=%d ran=%v out=%s", code, m.ran, out)
	}
}

// A status update that cannot read the task first never runs: each failure exits 1 with the reason.
func TestUpdateStatusReadFailuresExitOneAndRunNothing(t *testing.T) {
	base := newTaskMod()
	for name, mod := range map[string]core.Module{
		"read error": readFailMod{taskMod: base, err: errors.New("bd show timed out")},
		"no reader":  writeOnlyMod{m: base},
	} {
		code, _, errOut, au := runIn(t, "", []core.Module{mod}, "tasks", "update", "t-1", "--status", "blocked", "--yes")
		if code != 1 || len(base.ran) != 0 || au.n != 0 || errOut == "" {
			t.Errorf("%s: code=%d ran=%v audit=%d err=%q", name, code, base.ran, au.n, errOut)
		}
		if name == "read error" && !strings.Contains(errOut, "could not read t-1 first: bd show timed out") {
			t.Errorf("%s: %q", name, errOut)
		}
		if name == "no reader" && !strings.Contains(errOut, "cannot read the task's current status") {
			t.Errorf("%s: %q", name, errOut)
		}
	}
}

// --title X --status <the current status> is a title change; the status alone is "already X".
func TestUpdateWithTheCurrentStatusAndOtherFieldsDropsTheStatus(t *testing.T) {
	m := newTaskMod()
	code, out, _, _ := runIn(t, "", []core.Module{m}, "tasks", "update", "t-1", "--title", "X", "--status", "open", "--dry-run")
	if code != 0 || strings.Contains(out, "status") {
		t.Fatalf("code=%d out=%s", code, out)
	}
	code, _, errOut, _ := runIn(t, "", []core.Module{m}, "tasks", "update", "t-1", "--status", "open", "--dry-run")
	if code != 2 || !strings.Contains(errOut, "already open") {
		t.Fatalf("status alone: code=%d err=%q", code, errOut)
	}
}
