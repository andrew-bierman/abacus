package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/andrew-bierman/abacus/internal/core"
)

// ---- the agent command ----

func TestAgentCommandKeepsEveryWordOneArgumentWhateverTheTitleHolds(t *testing.T) {
	tpl := []string{"claude", "-p", "Work on {task_id}: {title}", "{title}", "--id={task_id}"}
	for _, title := range []string{
		"plain", "", "a b  c", "x'; rm -rf ~", "$(touch /tmp/x) `id`", `"; echo "`, "line one\nline two\r\nthree", "tab\there",
		"\x1b[2J\x1b]0;pwn\x07 bidi ‮ evil", strings.Repeat("é", 500), "-- --flag", "😀 emoji",
	} {
		got, err := core.ExpandAgentCommand(tpl, "bd-12", title)
		if title == "-- --flag" {
			if err == nil {
				t.Errorf("a title that would make the whole word an option must be refused: %q", got)
			}
			continue
		}
		if err != nil || len(got) != len(tpl) || got[0] != "claude" || got[1] != "-p" || got[4] != "--id=bd-12" {
			t.Errorf("%q: %q %v", title, got, err)
			continue
		}
		for _, w := range got {
			if strings.ContainsAny(w, "\n\r\t\x1b\x07‮") {
				t.Errorf("%q: control or invisible text in %q", title, w)
			}
		}
		if n := len([]rune(got[3])); n > 200 {
			t.Errorf("the title is bounded: %d", n)
		}
		if title == "" && got[3] != "bd-12" {
			t.Errorf("an empty title falls back to the id: %q", got)
		}
	}
	got, _ := core.ExpandAgentCommand([]string{"agent", "{title}"}, "bd-1", "  spaced   out  ")
	if got[1] != "spaced out" {
		t.Errorf("%q", got)
	}
}

func TestAgentCommandRefusesWhatCannotBeTrusted(t *testing.T) {
	ok := []string{"agent", "{task_id}"}
	for _, id := range []string{"", "-x", "a b", "a;b", "a$(x)", "../x", "bd-1\n", strings.Repeat("a", 200), "é"} {
		if _, err := core.ExpandAgentCommand(ok, id, "t"); err == nil {
			t.Errorf("id %q must be refused", id)
		}
	}
	for _, id := range []string{"bd-1", "bd-1.2", "LAM-7", "msg_abc.1", "team/proj:12"} {
		if _, err := core.ExpandAgentCommand(ok, id, "t"); err != nil {
			t.Errorf("id %q: %v", id, err)
		}
	}
	for name, tpl := range map[string][]string{
		"empty": nil, "empty program": {"", "x"}, "placeholder program": {"{title}"}, "unknown placeholder": {"a", "{path}"},
		"typo": {"a", "{task_id"}, "empty braces": {"a", "{}"}, "control": {"a", "x\x1b[0m"}, "too many": make([]string, 17), "bidi": {"a", "x‮y"},
		"long": {"a", strings.Repeat("x", 513)},
	} {
		if name == "typo" { // an unclosed brace is just text
			if err := core.ValidateAgentCommand(tpl); err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if tpl != nil && len(tpl) == 17 {
			for i := range tpl {
				tpl[i] = "a"
			}
		}
		if err := core.ValidateAgentCommand(tpl); err == nil {
			t.Errorf("%s must be refused: %q", name, tpl)
		}
	}
	if _, err := core.ExpandAgentCommand([]string{"agent", "{title}"}, "bd-1", "-rf"); err == nil {
		t.Error("a title that starts with a dash would be read as an option")
	}
	if got, err := core.ExpandAgentCommand([]string{"agent", "--note={title}", "-n", "x -{title}"}, "bd-1", "-rf"); err != nil || got[1] != "--note=-rf" {
		t.Errorf("text before the placeholder keeps it from being an option: %q %v", got, err)
	}
}

func TestAgentCommandIsAConfigKeyThatRoundTripsAndValidates(t *testing.T) {
	var c core.Config
	if err := c.Set("agent_command", `["claude","Work on {task_id}: {title}"]`); err != nil {
		t.Fatal(err)
	}
	v, _ := c.Get("agent_command")
	if v != `["claude","Work on {task_id}: {title}"]` || len(c.AgentCommand) != 2 {
		t.Fatalf("%q %q", v, c.AgentCommand)
	}
	b, _ := json.Marshal(c)
	if !strings.Contains(string(b), `"agent_command":["claude"`) {
		t.Fatalf("%s", b)
	}
	for _, bad := range []string{"claude", `"claude"`, `["a","{nope}"]`, `[]`, `[1]`, `{"a":1}`, `["a"`} {
		before := c.AgentCommand
		if err := c.Set("agent_command", bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
		if strings.Join(c.AgentCommand, "|") != strings.Join(before, "|") {
			t.Errorf("a refused set changed the value")
		}
	}
	if err := c.Set("agent_command", ""); err != nil || c.AgentCommand != nil {
		t.Errorf("empty clears: %v %q", err, c.AgentCommand)
	}
}

// ---- the placement gate ----

type fakeActor struct {
	built    []core.PlacementRequest
	ran      [][]string
	steps    [][]string
	failAt   int // 1-based step that fails, 0 none
	failErr  error
	target   string
	checkErr error
}

func (f *fakeActor) BuildPlacement(_ context.Context, r core.PlacementRequest) (core.PlacementBuild, error) {
	f.built = append(f.built, r)
	steps := f.steps
	if steps == nil {
		steps = [][]string{{"rex", "--autostart=false", "run", "x"}}
	}
	return core.PlacementBuild{Steps: steps, Lines: []string{"open a thing"}}, nil
}

func (f *fakeActor) RunPlacement(_ context.Context, argv []string) (core.StepResult, error) {
	f.ran = append(f.ran, argv)
	if f.failAt == len(f.ran) {
		return core.StepResult{}, f.failErr
	}
	return core.StepResult{Pane: "block:" + string(rune('a'+len(f.ran)))}, nil
}
func (f *fakeActor) PlacementTarget() string { return "workspace (test)" }
func (f *fakeActor) CheckPlacement(steps [][]string) error {
	if f.checkErr != nil {
		return f.checkErr
	}
	return nil
}

func workReq() core.PlacementRequest {
	return core.PlacementRequest{Action: core.PlaceWork, Item: core.WorkspaceItem{Kind: core.WorkspaceTask, ID: "bd-1"}, Title: "t", Template: []string{"agent", "{title}"}}
}

func TestAPlacementIsPlannedConfirmedAppliedOnceAndAudited(t *testing.T) {
	w, a := &fakeActor{}, &memAudit{}
	p, err := core.PlanPlacement(context.Background(), w, workReq())
	if err != nil || len(w.built) != 1 || strings.Join(w.built[0].Command, "|") != "agent|t" {
		t.Fatalf("%v %+v", err, w.built)
	}
	ap := core.NewPlacementApplier(w, a)
	if _, err := ap.Apply(context.Background(), p); !errors.Is(err, core.ErrNotConfirmed) || len(w.ran) != 0 || len(a.lines) != 0 {
		t.Fatalf("an unconfirmed plan runs nothing and records nothing: %v %v %v", err, w.ran, a.lines)
	}
	res, err := ap.Apply(context.Background(), p.Confirm())
	if err != nil || res.Steps != 1 || len(res.Panes) != 1 || len(w.ran) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := ap.Apply(context.Background(), p.Confirm()); !errors.Is(err, core.ErrAlreadyApplied) || len(w.ran) != 1 {
		t.Fatalf("single use: %v %v", err, w.ran)
	}
	if len(a.lines) != 2 || a.lines[0].Phase != "attempt" || a.lines[1].Phase != "ok" || a.lines[0].Domain != "workspace" || a.lines[0].Verb != "work" ||
		a.lines[0].Tracker != "workspace (test)" || strings.Join(a.lines[0].Argv, " ") != "rex --autostart=false run x" {
		t.Fatalf("%+v", a.lines)
	}
}

func TestAPlacementAuditFailureBlocksTheWrite(t *testing.T) {
	w := &fakeActor{}
	p, _ := core.PlanPlacement(context.Background(), w, workReq())
	_, err := core.NewPlacementApplier(w, &memAudit{err: errors.New("disk full")}).Apply(context.Background(), p.Confirm())
	if err == nil || len(w.ran) != 0 {
		t.Fatalf("fail closed: %v %v", err, w.ran)
	}
}

func TestAPlacementTimeoutIsUnknownNeverNotApplied(t *testing.T) {
	w, a := &fakeActor{failAt: 1, failErr: core.UnknownOutcome("did not confirm")}, &memAudit{}
	p, _ := core.PlanPlacement(context.Background(), w, workReq())
	_, err := core.NewPlacementApplier(w, a).Apply(context.Background(), p.Confirm())
	if !errors.Is(err, core.ErrOutcomeUnknown) || strings.Contains(core.DescribeWriteError(err), "not applied") {
		t.Fatalf("%v", err)
	}
	if last := a.lines[len(a.lines)-1]; last.Phase != "unknown" {
		t.Fatalf("%+v", a.lines)
	}
}

func TestAMultiStepPlacementAuditsEveryStepBeforeItRunsAndStopsOnAFailure(t *testing.T) {
	steps := [][]string{{"rex", "a"}, {"rex", "b"}, {"rex", "c"}}
	w, a := &fakeActor{steps: steps, failAt: 2, failErr: errors.New("boom")}, &memAudit{}
	p, _ := core.PlanPlacement(context.Background(), w, core.PlacementRequest{Action: core.PlaceLayout, Exe: "/x"})
	res, err := core.NewPlacementApplier(w, a).Apply(context.Background(), p.Confirm())
	if err == nil || !strings.Contains(err.Error(), "step 2 of 3") || len(w.ran) != 2 || res.Steps != 1 {
		t.Fatalf("it stops at the failing step and says which: %v %v %+v", err, w.ran, res)
	}
	var phases []string
	for _, l := range a.lines {
		phases = append(phases, l.Phase+":"+l.Argv[1])
	}
	// attempt a, attempt b (before b runs), then the failure line for the whole plan; c never started.
	if strings.Join(phases, ",") != "attempt:a,attempt:b,error:a" {
		t.Fatalf("%v", phases)
	}
	// A failed audit of a later step stops the rest.
	w2 := &fakeActor{steps: steps}
	au := &countAudit{failAfter: 1}
	p2, _ := core.PlanPlacement(context.Background(), w2, core.PlacementRequest{Action: core.PlaceLayout, Exe: "/x"})
	if _, err := core.NewPlacementApplier(w2, au).Apply(context.Background(), p2.Confirm()); err == nil || len(w2.ran) != 1 {
		t.Fatalf("nothing runs unrecorded: %v %v", err, w2.ran)
	}
	// An unknown outcome in a later step stays unknown.
	w3 := &fakeActor{steps: steps, failAt: 3, failErr: core.UnknownOutcome("x")}
	p3, _ := core.PlanPlacement(context.Background(), w3, core.PlacementRequest{Action: core.PlaceLayout, Exe: "/x"})
	if _, err := core.NewPlacementApplier(w3, &memAudit{}).Apply(context.Background(), p3.Confirm()); !errors.Is(err, core.ErrOutcomeUnknown) {
		t.Fatalf("%v", err)
	}
}

type countAudit struct {
	n         atomic.Int32
	failAfter int32
}

func (c *countAudit) Record(core.AuditEntry) error {
	if c.n.Add(1) > c.failAfter {
		return errors.New("audit down")
	}
	return nil
}

func TestPlacementRequestsAreValidatedBeforeTheProviderIsAsked(t *testing.T) {
	w := &fakeActor{}
	ctx := context.Background()
	for name, r := range map[string]core.PlacementRequest{
		"unknown action":    {Action: "kill"},
		"focus no target":   {Action: core.PlaceFocus, Item: core.WorkspaceItem{ID: "bd-1"}},
		"focus no pane":     {Action: core.PlaceFocus, Item: core.WorkspaceItem{ID: "bd-1"}, Target: &core.Placement{Session: "s"}},
		"focus bad id":      {Action: core.PlaceFocus, Item: core.WorkspaceItem{ID: "a b"}, Target: &core.Placement{Block: "b"}},
		"work no command":   {Action: core.PlaceWork, Item: core.WorkspaceItem{ID: "bd-1"}},
		"work bad id":       {Action: core.PlaceWork, Item: core.WorkspaceItem{ID: "$(x)"}, Template: []string{"a"}},
		"work bad host":     {Action: core.PlaceWork, Item: core.WorkspaceItem{ID: "bd-1", Host: "a b"}, Template: []string{"a"}},
		"layout no program": {Action: core.PlaceLayout},
		"work dash title":   {Action: core.PlaceWork, Item: core.WorkspaceItem{ID: "bd-1"}, Title: "-x", Template: []string{"a", "{title}"}},
	} {
		if _, err := core.PlanPlacement(ctx, w, r); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	if len(w.built) != 0 {
		t.Fatalf("the provider must not be asked: %v", w.built)
	}
	if core.ValidTab("Tasks") != true || core.ValidTab("doctor") != true || core.ValidTab("nope") || core.ValidTab("") {
		t.Fatal("ValidTab")
	}
}

// ---- reading a terminal ----

func TestCleanCaptureIsSafeAndBounded(t *testing.T) {
	c := core.CleanCapture([]byte("a\r\nb\x1b[31m\x00c\n\n  \n"), false)
	if strings.Join(c.Lines, "|") != "a|b[31mc" || c.Truncated {
		t.Fatalf("%q %v", c.Lines, c.Truncated)
	}
	var b strings.Builder
	for i := 0; i < 500; i++ {
		b.WriteString("l\n")
	}
	c = core.CleanCapture([]byte(b.String()), false)
	if len(c.Lines) != core.CaptureMaxLines || !c.Truncated {
		t.Fatalf("%d %v", len(c.Lines), c.Truncated)
	}
	c = core.CleanCapture([]byte("partial fragment\nwhole\n"), true)
	if strings.Join(c.Lines, "|") != "whole" || !c.Truncated {
		t.Fatalf("a source cut at its start drops the first, partial, line: %q", c.Lines)
	}
	if c := core.CleanCapture(nil, false); c.Lines == nil || len(c.Lines) != 0 {
		t.Fatalf("empty is an empty list, never null: %+v", c)
	}
	if c := core.CleanCapture([]byte("\xff\xfebad \u202e utf8\n"), false); strings.Contains(c.Lines[0], "\u202e") || !utf8.ValidString(c.Lines[0]) {
		t.Fatalf("%q", c.Lines)
	}
}

func TestExecRunnerKeepTailKeepsTheEndAndSaysSo(t *testing.T) {
	r := core.ExecRunner{MaxOut: 64, KeepTail: true}
	out, exit, err := r.Run(context.Background(), "sh", "-c", "i=0; while [ $i -lt 200 ]; do echo line$i; i=$((i+1)); done")
	if !errors.Is(err, core.ErrOutputTruncated) || exit != 0 || len(out) != 64 || !strings.HasSuffix(string(out), "line199\n") {
		t.Fatalf("%q %d %v", out, exit, err)
	}
	out, _, err = r.Run(context.Background(), "sh", "-c", "echo short")
	if err != nil || string(out) != "short\n" {
		t.Fatalf("%q %v", out, err)
	}
	_, _, err = core.ExecRunner{MaxOut: 64}.Run(context.Background(), "sh", "-c", "head -c 1000 /dev/zero")
	if !errors.Is(err, core.ErrOutputTooLarge) {
		t.Fatalf("without KeepTail the old behavior stands: %v", err)
	}
}

// ---- the stream ----

func TestExecStreamDeliversLinesSkipsHugeOnesAndKillsTheGroupOnCancel(t *testing.T) {
	var lines []string
	ctx := context.Background()
	exit, err := core.ExecStream(nil)(ctx, "sh", []string{"-c", "echo one; head -c 600000 /dev/zero | tr '\\0' x; echo; echo two; exit 3"}, func(l []byte) { lines = append(lines, string(l)) })
	if err != nil || exit != 3 || strings.Join(lines, "|") != "one|two" {
		t.Fatalf("a line over the bound is skipped whole: %d %v %q", exit, err, lines)
	}

	// Cancel kills the command and what it started, and returns promptly.
	pidFile := t.TempDir() + "/pid"
	cctx, cancel := context.WithCancel(ctx)
	started := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		_, err := core.ExecStream(nil)(cctx, "sh", []string{"-c", "sleep 300 & echo $! > " + pidFile + "; echo ready; wait"}, func([]byte) {
			select {
			case started <- struct{}{}:
			default:
			}
		})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("never started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel must stop the stream")
	}
	pid := strings.TrimSpace(readFile(t, pidFile))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if out, _ := runOut("kill", "-0", pid); !strings.Contains(out, "ok") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the child's own child %s is still running after cancel", pid)
}

func TestExecStreamScrubsTheEnvironment(t *testing.T) {
	t.Setenv("ABACUS_SECRET_TOKEN", "hunter2")
	var got []string
	scrub := func(env []string) []string {
		var out []string
		for _, e := range env {
			if strings.HasPrefix(e, "PATH=") {
				out = append(out, e)
			}
		}
		return out
	}
	core.ExecStream(scrub)(context.Background(), "sh", []string{"-c", "env"}, func(l []byte) { got = append(got, string(l)) })
	for _, l := range got {
		if strings.Contains(l, "hunter2") {
			t.Fatalf("the scrub must apply: %q", got)
		}
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// runOut runs a command and returns "ok" in its output when it succeeded.
func runOut(name string, args ...string) (string, error) {
	if err := exec.Command(name, args...).Run(); err != nil {
		return "fail", err
	}
	return "ok", nil
}

func TestApplyRefusesPlansNotMadeByPlanPlacementAndRechecksTheProvider(t *testing.T) {
	w, a := &fakeActor{}, &memAudit{}
	ap := core.NewPlacementApplier(w, a)
	ctx := context.Background()
	// A zero plan neither panics nor runs anything, confirmed or not.
	for _, p := range []core.PlacementPlan{{}, core.PlacementPlan{}.Confirm()} {
		if _, err := ap.Apply(ctx, p); err == nil {
			t.Fatal("a zero plan must be refused")
		}
	}
	if len(w.ran) != 0 || len(a.lines) != 0 {
		t.Fatalf("%v %v", w.ran, a.lines)
	}
	// A plan whose commands the provider no longer accepts runs nothing and records nothing.
	p, _ := core.PlanPlacement(ctx, w, workReq())
	w.checkErr = errors.New("machine not allowed")
	if _, err := ap.Apply(ctx, p.Confirm()); err == nil || len(w.ran) != 0 || len(a.lines) != 0 {
		t.Fatalf("%v %v %v", err, w.ran, a.lines)
	}
}

func TestAnExpandedWordThatIsTooLongSaysSo(t *testing.T) {
	tpl := []string{"agent", strings.Repeat("{title}", 8)}
	_, err := core.ExpandAgentCommand(tpl, "bd-1", strings.Repeat("x", 190))
	if err == nil || !strings.Contains(err.Error(), "once filled in") || strings.Contains(err.Error(), "option") {
		t.Fatalf("%v", err)
	}
}
