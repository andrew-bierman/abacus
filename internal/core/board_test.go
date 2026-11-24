package core_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

func TestScrubBoardEnvKeepsOnlyWhatTheCliNeeds(t *testing.T) {
	in := []string{"HOME=/h", "PATH=/bin", "BOARD_TOKEN=t", "VERCEL_AUTH_TOKEN=v", "VERCEL_ORG_ID=o", "XDG_CONFIG_HOME=/c", "LC_ALL=C",
		"BEADS_DIR=/x", "BD_ACTOR=a", "AWS_SECRET_ACCESS_KEY=k", "NODE_OPTIONS=--require /evil.js", "VERCEL_TOKEN=never-sent", "GITHUB_TOKEN=g", "ANTHROPIC_API_KEY=a", "SSH_AUTH_SOCK=/s"}
	got := strings.Join(core.ScrubBoardEnvFor("")(in), " ")
	for _, want := range []string{"HOME=/h", "PATH=/bin", "BOARD_TOKEN=t", "VERCEL_AUTH_TOKEN=v", "VERCEL_ORG_ID=o", "XDG_CONFIG_HOME=/c", "LC_ALL=C"} {
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
	for _, ok := range []string{"http://127.0.0.1:4100/api/board", "http://localhost:4100/api/board", "http://127.0.0.5/x", "http://[::1]:4100/api/board", "https://example.com/api/board", "https://api.vercel.com/v1/messageboard/boards/brd_x"} {
		if err := core.ValidateBoardURL(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "ftp://h/x", "file:///etc/passwd", "http://", "not a url", "http://user:pw@host/x", "http://h/\x1b[2J", "javascript:alert(1)", "http://h/a b",
		"https://h/x?token=abc", "https://h/x?", "https://h/x#frag", "http://127.0.0.1:4100/api/board?x=1",
		"http://example.com/api/board", "http://10.0.0.5/api/board", "http://localhost.evil.com/x", "http://127.0.0.1.evil.com/x"} {
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
	ran    [][]string
	fail   error
	target string
}

func (f *fakeBoardWriter) BuildBoardArgv(ch core.BoardChange, key string) ([]string, error) {
	return []string{"board", string(ch.Op), "--idempotency-key", key, "--", ch.Text}, nil
}
func (f *fakeBoardWriter) RunBoard(_ context.Context, argv []string, ch core.BoardChange) (core.BoardResult, error) {
	f.ran = append(f.ran, argv)
	return core.BoardResult{ID: "7", Text: ch.Text}, f.fail
}
func (f *fakeBoardWriter) Target() string {
	if f.target != "" {
		return f.target
	}
	return "http://127.0.0.1:4100/api/board"
}
func (f *fakeBoardWriter) TargetSource() string { return "board_url (config)" }
func (f *fakeBoardWriter) Describe(context.Context, core.BoardChange) []string {
	return []string{"channel dev: new"}
}

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

func TestBoardTargetIsSchemeHostPathOnly(t *testing.T) {
	for in, want := range map[string]string{
		"http://127.0.0.1:4100/api/board": "http://127.0.0.1:4100/api/board",
		"https://example.com/a/b":         "https://example.com/a/b",
		"https://example.com":             "https://example.com",
		"":                                "the CLI's default board",
	} {
		if got := core.BoardTarget(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
	// Even a value that would not validate never leaks its query, fragment or credentials.
	if got := core.BoardTarget("https://u:p@h/x?token=s#f"); strings.ContainsAny(got, "?#@") || strings.Contains(got, "token") {
		t.Errorf("%q", got)
	}
}

func TestScrubBoardEnvForDropsBoardURLAndTokensOnAnUntrustedTransport(t *testing.T) {
	in := []string{"HOME=/h", "PATH=/p", "BOARD_URL=http://evil", "BOARD_NAME=n", "BOARD_TOKEN=t", "VERCEL_AUTH_TOKEN=v", "VERCEL_AUTOMATION_BYPASS_SECRET=b", "VERCEL_ORG_ID=o"}
	has := func(env []string, name string) bool {
		for _, e := range env {
			if strings.HasPrefix(e, name+"=") {
				return true
			}
		}
		return false
	}
	for _, url := range []string{"", "https://example.com/api/board", "http://127.0.0.1:4100/api/board"} {
		got := core.ScrubBoardEnvFor(url)(in)
		if has(got, "BOARD_URL") {
			t.Errorf("%q: BOARD_URL must never pass through (abacus passes --url itself)", url)
		}
		if !has(got, "BOARD_TOKEN") || !has(got, "VERCEL_AUTH_TOKEN") {
			t.Errorf("%q: a trusted target keeps the credentials", url)
		}
		if (url != "") == has(got, "BOARD_NAME") {
			t.Errorf("%q: BOARD_NAME is misuse beside --url and only kept without one", url)
		}
	}
	bad := core.ScrubBoardEnvFor("http://example.com/api/board")(in)
	for _, n := range []string{"BOARD_TOKEN", "VERCEL_AUTH_TOKEN", "VERCEL_AUTOMATION_BYPASS_SECRET", "VERCEL_ORG_ID", "BOARD_URL"} {
		if has(bad, n) {
			t.Errorf("tokens are only passed when the target is https or loopback: %s leaked to http://example.com", n)
		}
	}
	if !has(bad, "HOME") || !has(bad, "PATH") {
		t.Error("HOME and PATH still pass")
	}
}

func TestProbeEnvHasNoSecretsAndNoBoardSettings(t *testing.T) {
	in := []string{"HOME=/h", "PATH=/p", "XDG_CONFIG_HOME=/c", "LC_ALL=C", "BOARD_TOKEN=t", "VERCEL_AUTH_TOKEN=v", "VERCEL_AUTOMATION_BYPASS_SECRET=b", "VERCEL_ORG_ID=o", "BOARD_URL=u", "BOARD_NAME=n", "HTTPS_PROXY=p"}
	got := strings.Join(core.ScrubBoardProbeEnv(in), " ")
	for _, bad := range []string{"BOARD_", "VERCEL_", "PROXY"} {
		if strings.Contains(got, bad) {
			t.Errorf("the identity probe must not see %s: %s", bad, got)
		}
	}
	for _, want := range []string{"HOME=/h", "PATH=/p", "XDG_CONFIG_HOME=/c", "LC_ALL=C"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s", want)
		}
	}
}

func TestIdempotencyKeyIsDerivedSoARetryOfAnUnknownOutcomeReusesIt(t *testing.T) {
	now := time.Date(2026, 11, 22, 20, 3, 0, 0, time.UTC)
	defer core.SetBoardClockForTest(func() time.Time { return now })()
	w := &fakeBoardWriter{}
	key := func(ch core.BoardChange) string {
		p, err := core.PlanBoardChange(w, ch)
		if err != nil {
			t.Fatal(err)
		}
		return p.Argv[3]
	}
	post := core.BoardChange{Op: core.BoardPost, Channel: "dev", Text: "hello"}
	k := key(post)
	if !strings.HasPrefix(k, "abacus-") || len(k) != len("abacus-")+16 {
		t.Fatalf("shape: %s", k)
	}
	if key(post) != k {
		t.Fatal("re-planning the same post (a retry after an unknown outcome) must reuse the key")
	}
	now = now.Add(5 * time.Minute) // same ten-minute bucket
	if key(post) != k {
		t.Fatal("a retry a few minutes later is the same post")
	}
	for name, other := range map[string]core.BoardChange{
		"text":    {Op: core.BoardPost, Channel: "dev", Text: "hello!"},
		"channel": {Op: core.BoardPost, Channel: "dev2", Text: "hello"},
		"op":      {Op: core.BoardReply, ID: "7", Text: "hello"},
	} {
		if key(other) == k {
			t.Errorf("a different %s must get a different key", name)
		}
	}
	if key(core.BoardChange{Op: core.BoardReply, ID: "8", Text: "hello"}) == key(core.BoardChange{Op: core.BoardReply, ID: "7", Text: "hello"}) {
		t.Error("a different reply target must get a different key")
	}
	other := &fakeBoardWriter{target: "https://example.com/api/board"}
	if p, _ := core.PlanBoardChange(other, post); p.Argv[3] == k {
		t.Error("the same text to another board must get another key")
	}
	now = now.Add(time.Hour)
	if key(post) == k {
		t.Fatal("a much later identical post is a new post: the key changes with the time bucket")
	}
}

func TestExecRunnerCapsStdoutAndSaysSo(t *testing.T) {
	r := core.ExecRunner{MaxOut: 1024}
	out, _, _, err := r.RunSplit(context.Background(), "sh", "-c", "head -c 100000 /dev/zero | tr '\\0' x")
	if !errors.Is(err, core.ErrOutputTooLarge) || len(out) > 1024 {
		t.Fatalf("len=%d err=%v", len(out), err)
	}
	out, _, exit, err := r.RunSplit(context.Background(), "sh", "-c", "echo small")
	if err != nil || exit != 0 || string(out) != "small\n" {
		t.Fatalf("%q %d %v", out, exit, err)
	}
}

func TestSanitizeTextKeepsNewlinesAndDropsTheRest(t *testing.T) {
	got := core.SanitizeText("a\r\nb\tc\x1b[2Jd\u200be\rf")
	if got != "a\nb c[2Jde\nf" {
		t.Fatalf("%q", got)
	}
}

func TestDraftKeyIsStableAcrossTheClockAndChangesWithTheDraft(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 9, 59, 0, time.UTC) // two seconds before a ten-minute boundary
	defer core.SetBoardClockForTest(func() time.Time { return now })()
	w := &fakeBoardWriter{}
	key := func(ch core.BoardChange) string {
		p, err := core.PlanBoardChange(w, ch)
		if err != nil {
			t.Fatal(err)
		}
		if p.Key != p.Argv[3] {
			t.Fatalf("the plan names the key that is in the argv: %q vs %q", p.Key, p.Argv[3])
		}
		return p.Key
	}
	draft := core.BoardChange{Op: core.BoardPost, Channel: "dev", Text: "hello", Nonce: "n1"}
	k := key(draft)
	now = now.Add(2 * time.Second) // across the boundary
	if key(draft) != k {
		t.Fatal("the same draft retried across a clock boundary must reuse the key")
	}
	now = now.Add(48 * time.Hour)
	if key(draft) != k {
		t.Fatal("and at any later time")
	}
	for name, other := range map[string]core.BoardChange{
		"text":    {Op: core.BoardPost, Channel: "dev", Text: "hello!", Nonce: "n1"},
		"channel": {Op: core.BoardPost, Channel: "dev2", Text: "hello", Nonce: "n1"},
		"nonce":   {Op: core.BoardPost, Channel: "dev", Text: "hello", Nonce: "n2"},
		"target":  {Op: core.BoardReply, ID: "7", Text: "hello", Nonce: "n1"},
	} {
		if key(other) == k {
			t.Errorf("a different %s must get a different key", name)
		}
	}
	// without a nonce the key is clock-bucketed and the screen does not promise more than that
	cli := core.BoardChange{Op: core.BoardPost, Channel: "dev", Text: "hello"}
	p, _ := core.PlanBoardChange(w, cli)
	if got := strings.Join(p.Lines, "\n"); !strings.Contains(got, "not across a ten-minute clock boundary") || strings.Contains(got, "at most once") {
		t.Fatalf("the command line's key promise is the weaker one:\n%s", got)
	}
	p, _ = core.PlanBoardChange(w, draft)
	if got := strings.Join(p.Lines, "\n"); !strings.Contains(got, "retry of this same draft reuses it") {
		t.Fatalf("%s", got)
	}
}

func TestBoardChannelAndCursorPathsRefuseWhatTheBoardTreatsAsAPath(t *testing.T) {
	good := []string{"dev", "dev/web", "a-b", "a_b", "a/b/c", "work.2", "Team/Ch-1", "a-/b", "x/y.z/w", strings.Repeat("a", 128)}
	bad := []string{"", "a//b", "a/../b", "..", "../a", "a/..", "/a", "a/", "./a", "a/.", "a..b", "a/-b", "-a", "_a", ".a", "a.", "a./b", "a b", "a\nb", "a;b", strings.Repeat("a", 129), "dev/", "//"}
	for _, c := range good {
		if !core.BoardChannelRE.MatchString(c) {
			t.Errorf("channel %q must be accepted", c)
		}
	}
	for _, c := range bad {
		if core.BoardChannelRE.MatchString(c) {
			t.Errorf("channel %q must be refused", c)
		}
	}
	for _, c := range []string{"team/ch05", "c.8f2a.2", "a=b+c:d", strings.Repeat("a", 160)} {
		if !core.BoardCursorRE.MatchString(c) {
			t.Errorf("cursor %q must be accepted", c)
		}
	}
	for _, c := range []string{"", "-x", "a//b", "a/../b", "/a", "a/", "a b", "a..b", strings.Repeat("a", 161)} {
		if core.BoardCursorRE.MatchString(c) {
			t.Errorf("cursor %q must be refused", c)
		}
	}
	if _, err := core.PlanBoardChange(&fakeBoardWriter{}, core.BoardChange{Op: core.BoardPost, Channel: "a//b", Text: "x"}); err == nil {
		t.Fatal("a post to a path the board refuses is stopped before the confirm screen")
	}
}

// The local board 0.54.0 refuses a message over 10,000 UTF-16 code units (checked: 10,000 CJK
// characters and 5,000 emoji pass, 5,001 emoji do not; it is not a byte limit). A rune is at most two
// units, so the rune cap must stay at or under half of that.
func TestTheMessageCapIsWithinTheBoardsLimitInItsOwnUnits(t *testing.T) {
	const boardLimitUTF16 = 10000
	if 2*core.BoardMaxText > boardLimitUTF16 {
		t.Fatalf("%d runes can be %d UTF-16 units, over the board's %d", core.BoardMaxText, 2*core.BoardMaxText, boardLimitUTF16)
	}
}
