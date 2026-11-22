package core_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

func TestScrubBoardEnvKeepsOnlyWhatTheCliNeeds(t *testing.T) {
	in := []string{"HOME=/h", "PATH=/bin", "BOARD_TOKEN=t", "BOARD_URL=u", "VERCEL_AUTH_TOKEN=v", "VERCEL_ORG_ID=o", "XDG_CONFIG_HOME=/c", "LC_ALL=C",
		"BEADS_DIR=/x", "BD_ACTOR=a", "AWS_SECRET_ACCESS_KEY=k", "NODE_OPTIONS=--require /evil.js", "VERCEL_TOKEN=never-sent", "GITHUB_TOKEN=g", "ANTHROPIC_API_KEY=a", "SSH_AUTH_SOCK=/s"}
	got := strings.Join(core.ScrubBoardEnv(in), " ")
	for _, want := range []string{"HOME=/h", "PATH=/bin", "BOARD_TOKEN=t", "BOARD_URL=u", "VERCEL_AUTH_TOKEN=v", "VERCEL_ORG_ID=o", "XDG_CONFIG_HOME=/c", "LC_ALL=C"} {
		if !strings.Contains(got, want) {
			t.Errorf("must keep %s", want)
		}
	}
	for _, bad := range []string{"BEADS_DIR", "BD_ACTOR", "AWS_", "NODE_OPTIONS", "VERCEL_TOKEN", "GITHUB_TOKEN", "ANTHROPIC", "SSH_AUTH_SOCK"} {
		if strings.Contains(got, bad) {
			t.Errorf("must drop %s: %s", bad, got)
		}
	}
}

func TestValidateBoardURL(t *testing.T) {
	for _, ok := range []string{"http://127.0.0.1:4100/api/board", "https://api.vercel.com/v1/messageboard/boards/brd_x?teamId=team_y"} {
		if err := core.ValidateBoardURL(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "ftp://h/x", "file:///etc/passwd", "http://", "not a url", "http://user:pw@host/x", "http://h/\x1b[2J", "javascript:alert(1)", "http://h/a b"} {
		if core.ValidateBoardURL(bad) == nil {
			t.Errorf("must refuse %q", bad)
		}
	}
}

func TestConfigBoardURLRoundTripsAndValidates(t *testing.T) {
	var c core.Config
	if err := c.Set("board_url", "http://127.0.0.1:4100/api/board"); err != nil {
		t.Fatal(err)
	}
	if v, _ := c.Get("board_url"); v != "http://127.0.0.1:4100/api/board" {
		t.Fatal(v)
	}
	if err := c.Set("board_url", "ftp://x"); err == nil || c.BoardURL != "http://127.0.0.1:4100/api/board" {
		t.Fatalf("a bad value leaves the config unchanged: %v %q", err, c.BoardURL)
	}
	if err := c.Set("board_url", ""); err != nil || c.BoardURL != "" {
		t.Fatal("empty clears it")
	}
}

type fakeBoardWriter struct {
	ran  [][]string
	fail error
}

func (f *fakeBoardWriter) BuildBoardArgv(ch core.BoardChange, key string) ([]string, error) {
	return []string{"board", string(ch.Op), "--idempotency-key", key, "--", ch.Text}, nil
}
func (f *fakeBoardWriter) RunBoard(_ context.Context, argv []string, ch core.BoardChange) (core.BoardResult, error) {
	f.ran = append(f.ran, argv)
	return core.BoardResult{ID: "7", Text: ch.Text}, f.fail
}
func (f *fakeBoardWriter) Target() string { return "http://127.0.0.1:4100/api/board" }

func TestBoardChangeValidation(t *testing.T) {
	w := &fakeBoardWriter{}
	long := strings.Repeat("x", core.BoardMaxText+1)
	for name, ch := range map[string]core.BoardChange{
		"empty text":       {Op: core.BoardPost, Channel: "dev", Text: "  \n"},
		"too long":         {Op: core.BoardPost, Channel: "dev", Text: long},
		"control char":     {Op: core.BoardPost, Channel: "dev", Text: "a\x1b[2Jb"},
		"bad channel":      {Op: core.BoardPost, Channel: "-rf", Text: "x"},
		"channel with sp":  {Op: core.BoardPost, Channel: "dev web", Text: "x"},
		"reply to channel": {Op: core.BoardReply, ID: "dev", Text: "x"},
		"reply no id":      {Op: core.BoardReply, Text: "x"},
		"unknown op":       {Op: "delete", ID: "1", Text: "x"},
	} {
		if _, err := core.PlanBoardChange(w, ch); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	p, err := core.PlanBoardChange(w, core.BoardChange{Op: core.BoardReply, ID: "msg_abc-1", Text: "multi\nline\ttext"})
	if err != nil || len(w.ran) != 0 {
		t.Fatalf("planning has no side effects: %v %v", err, w.ran)
	}
	if got := strings.Join(p.Lines, "\n"); !strings.Contains(got, "msg_abc-1") || !strings.Contains(got, "multi") || !strings.Contains(got, "127.0.0.1:4100") {
		t.Fatalf("the plan shows the target and the text:\n%s", got)
	}
}

func TestBoardWriteGate(t *testing.T) {
	w, a := &fakeBoardWriter{}, &memAudit{}
	plan, _ := core.PlanBoardChange(w, core.BoardChange{Op: core.BoardPost, Channel: "dev", Text: "hi"})
	ap := core.NewBoardApplier(w, a)
	if _, err := ap.Apply(context.Background(), plan); !errors.Is(err, core.ErrNotConfirmed) || len(w.ran) != 0 || len(a.lines) != 0 {
		t.Fatalf("unconfirmed never runs: %v", err)
	}
	if res, err := ap.Apply(context.Background(), plan.Confirm()); err != nil || res.ID != "7" || len(w.ran) != 1 {
		t.Fatalf("%v %v", res, err)
	}
	if _, err := ap.Apply(context.Background(), plan.Confirm()); !errors.Is(err, core.ErrAlreadyApplied) || len(w.ran) != 1 {
		t.Fatalf("a plan is single use: %v", err)
	}
	if len(a.lines) != 2 || a.lines[0].Phase != "attempt" || a.lines[1].Phase != "ok" || a.lines[0].Domain != "board" || a.lines[0].Verb != "post" || a.lines[0].Tracker == "" {
		t.Fatalf("attempt then outcome are audited: %+v", a.lines)
	}
}

func TestBoardWriteFailsClosedWithoutAnAudit(t *testing.T) {
	w, a := &fakeBoardWriter{}, &memAudit{err: errors.New("disk full")}
	plan, _ := core.PlanBoardChange(w, core.BoardChange{Op: core.BoardPost, Channel: "dev", Text: "hi"})
	if _, err := core.NewBoardApplier(w, a).Apply(context.Background(), plan.Confirm()); err == nil || len(w.ran) != 0 {
		t.Fatalf("a failed audit blocks the write: %v %v", err, w.ran)
	}
}

func TestBoardUnknownOutcomeIsAuditedAsUnknown(t *testing.T) {
	w, a := &fakeBoardWriter{fail: core.UnknownOutcome("timed out")}, &memAudit{}
	plan, _ := core.PlanBoardChange(w, core.BoardChange{Op: core.BoardPost, Channel: "dev", Text: "hi"})
	_, err := core.NewBoardApplier(w, a).Apply(context.Background(), plan.Confirm())
	if !errors.Is(err, core.ErrOutcomeUnknown) || a.lines[1].Phase != "unknown" {
		t.Fatalf("%v %+v", err, a.lines)
	}
}

func TestEveryPlanGetsItsOwnIdempotencyKey(t *testing.T) {
	w := &fakeBoardWriter{}
	a, _ := core.PlanBoardChange(w, core.BoardChange{Op: core.BoardPost, Channel: "dev", Text: "hi"})
	b, _ := core.PlanBoardChange(w, core.BoardChange{Op: core.BoardPost, Channel: "dev", Text: "hi"})
	if !strings.HasPrefix(a.Argv[3], "abacus-") || a.Argv[3] == b.Argv[3] {
		t.Fatalf("%v %v", a.Argv, b.Argv)
	}
}
