package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// fakeRun answers by joined argv (the binary path is ignored): out, stderr and exit per key.
type fakeRun struct {
	out   map[string]string
	errs  map[string]string
	exit  map[string]int
	err   map[string]error
	calls []string
}

func (f *fakeRun) RunSplit(_ context.Context, bin string, args ...string) ([]byte, []byte, int, error) {
	k := bin + "|" + strings.Join(args, " ")
	f.calls = append(f.calls, k)
	for key, err := range f.err {
		if strings.Contains(k, key) {
			return nil, nil, -1, err
		}
	}
	for key, out := range f.out {
		if k == "/fake/board|"+key || k == "/fake/vb|"+key {
			return []byte(out), []byte(f.errs[key]), f.exit[key], nil
		}
	}
	return nil, nil, -1, fmt.Errorf("unexpected argv %q", k)
}

func fx(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func look(names ...string) func(string) (string, error) {
	return func(n string) (string, error) {
		for _, x := range names {
			if x == n {
				return "/fake/" + n, nil
			}
		}
		return "", errors.New("not found")
	}
}

// helpRuns serves the real --help on both candidate paths.
func helpRuns(t *testing.T) *fakeRun {
	return &fakeRun{out: map[string]string{"--help": fx(t, "help.txt")}, errs: map[string]string{}, exit: map[string]int{}, err: map[string]error{}}
}

func readyModule(t *testing.T, f *fakeRun, o Options) *Module {
	t.Helper()
	o.Runner, o.LookPath = f, look("board")
	m := New(o)
	if d := m.Detect(context.Background()); d.State != core.Ready {
		t.Fatalf("%+v", d)
	}
	return m
}

const url = "http://127.0.0.1:4100/api/board"

func boardRuns(t *testing.T, o ...string) *fakeRun {
	f := helpRuns(t)
	u := ""
	if len(o) > 0 {
		u = " --url " + o[0]
	}
	f.out["channels list --json --limit 50"+u] = fx(t, "channels.json")
	f.out["read dev/web --json --limit 5"+u] = fx(t, "read-channel.json")
	f.out["read work --json --limit 5"+u] = fx(t, "read-channel.json")
	f.out["read dev --json --limit 5"+u] = fx(t, "read-channel.json")
	f.out["tasks --json --limit 20"+u] = fx(t, "tasks.json")
	return f
}

func TestDetectTrustsOnlyAnIdentifiedVercelBoard(t *testing.T) {
	f := helpRuns(t)
	m := New(Options{Runner: f, LookPath: look("board")})
	d := m.Detect(context.Background())
	if d.State != core.Ready || d.Version != "0.54.0" || m.Bin() != "/fake/board" {
		t.Fatalf("%+v %s", d, m.Bin())
	}
}

func TestDetectRejectsTheUnrelatedVbPackageAndSaysSo(t *testing.T) {
	f := &fakeRun{out: map[string]string{"--help": "vb 1.0.2\nusage: vb [options] <file>\n"}, errs: map[string]string{}, exit: map[string]int{}, err: map[string]error{}}
	m := New(Options{Runner: f, LookPath: look("vb")})
	d := m.Detect(context.Background())
	if d.State != core.Incompatible || !strings.Contains(d.Detail, "not Vercel Board") || !strings.Contains(d.Detail, "@vercel/board") {
		t.Fatalf("%+v", d)
	}
	if m.Bin() != "" {
		t.Fatalf("a rejected binary must never be remembered: %q", m.Bin())
	}
	// A Board read refuses to run anything: there is no trusted binary.
	if _, err := m.Board(context.Background()); err == nil {
		t.Fatal("must not read through a rejected binary")
	}
}

func TestDetectNeedsTheUsageLineToo(t *testing.T) {
	f := &fakeRun{out: map[string]string{"--help": "board 0.54.0: your Vercel team's message board, for people and agents\n\nsomething else\n"}, errs: map[string]string{}, exit: map[string]int{}, err: map[string]error{}}
	if d := New(Options{Runner: f, LookPath: look("board")}).Detect(context.Background()); d.State != core.Incompatible {
		t.Fatalf("a look-alike first line is not enough: %+v", d)
	}
}

func TestDetectPrefersBoardThenFallsBackToVb(t *testing.T) {
	f := &fakeRun{out: map[string]string{"--help": fx(t, "help.txt")}, errs: map[string]string{}, exit: map[string]int{}, err: map[string]error{}}
	m := New(Options{Runner: f, LookPath: look("board", "vb")})
	if m.Detect(context.Background()); m.Bin() != "/fake/board" {
		t.Fatalf("board is preferred: %s", m.Bin())
	}
	// A `board` that is something else does not hide a real vb.
	g := &fakeRun{out: map[string]string{"--help": "board 2.0: some other tool\n"}, errs: map[string]string{}, exit: map[string]int{}, err: map[string]error{}}
	m = New(Options{Runner: g, LookPath: look("board", "vb")})
	if d := m.Detect(context.Background()); d.State != core.Incompatible {
		t.Fatalf("%+v", d)
	}
	h := &fakeRun{out: map[string]string{"--help": fx(t, "help.txt")}, errs: map[string]string{}, exit: map[string]int{}, err: map[string]error{}}
	m = New(Options{Runner: h, LookPath: look("vb")})
	if d := m.Detect(context.Background()); d.State != core.Ready || m.Bin() != "/fake/vb" {
		t.Fatalf("%+v %s", d, m.Bin())
	}
}

func TestDetectAbsentNamesTheRealPackage(t *testing.T) {
	m := New(Options{Runner: helpRuns(t), LookPath: look()})
	if d := m.Detect(context.Background()); d.State != core.Absent || !strings.Contains(d.Detail, "Vercel Board CLI") {
		t.Fatalf("%+v", d)
	}
	if !strings.Contains(core.HowToConnect[core.DomainBoard], "npm i -g @vercel/board") || strings.Contains(core.HowToConnect[core.DomainBoard], "npx vb") && !strings.Contains(core.HowToConnect[core.DomainBoard], "never") {
		t.Fatalf("the install hint must name @vercel/board and warn off vb: %s", core.HowToConnect[core.DomainBoard])
	}
}

func TestBoardDecodesTheRealOutputShape(t *testing.T) {
	m := readyModule(t, boardRuns(t), Options{})
	r, err := m.Board(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != core.SourceLive || r.Reason != "" {
		t.Fatalf("%+v", r.Header)
	}
	if len(r.Channels) != 3 || r.Channels[0].Path != "work" || r.Channels[1].Path != "dev/web" || r.Channels[1].Threads != 3 {
		t.Fatalf("channels are newest activity first: %+v", r.Channels)
	}
	var esc *core.BoardThread
	for i := range r.Threads {
		if r.Threads[i].Root.ID == "15" {
			esc = &r.Threads[i]
		}
	}
	if esc == nil || strings.ContainsRune(esc.Root.Text, 0x1b) || strings.ContainsRune(esc.Root.Text, 7) || esc.Root.Author != "ada" || esc.Root.Channel != "dev/web" {
		t.Fatalf("control characters in a message must never reach a screen: %+v", esc)
	}
	if len(r.Tasks) != 2 || r.Tasks[1].State != "claimed" || r.Tasks[1].Claimant != "codex via ada" || r.Tasks[0].State != "blocked" {
		t.Fatalf("%+v", r.Tasks)
	}
}

func TestBoardReadsAreBoundedAndUseOnlyAllowlistedArgv(t *testing.T) {
	f := boardRuns(t)
	m := readyModule(t, f, Options{})
	if _, err := m.Board(context.Background()); err != nil {
		t.Fatal(err)
	}
	reads := 0
	for _, c := range f.calls[1:] { // call 0 is the identity check
		args := strings.Fields(strings.TrimPrefix(c, "/fake/board|"))
		if !allowedRead(args) {
			t.Errorf("ran a non-allowlisted argv: %s", c)
		}
		if args[0] == "read" {
			reads++
		}
		for _, bad := range []string{"serve", "tail", "wait", "post", "reply", "inspect", "whoami"} {
			if args[0] == bad {
				t.Errorf("ran %s", bad)
			}
		}
	}
	if reads != readChannels {
		t.Fatalf("a Board read reads at most %d channels, read %d", readChannels, reads)
	}
}

func TestConfiguredURLIsPassedAsTheCliUrlFlag(t *testing.T) {
	f := boardRuns(t, url)
	m := readyModule(t, f, Options{URL: url})
	if _, err := m.Board(context.Background()); err != nil {
		t.Fatalf("%v (calls %v)", err, f.calls)
	}
	if !strings.HasSuffix(f.calls[1], " --url "+url) {
		t.Fatalf("%v", f.calls)
	}
}

func TestAnUnusableConfiguredURLNeverFallsBackToTheDefaultBoard(t *testing.T) {
	f := boardRuns(t)
	m := readyModule(t, f, Options{URLErr: "board_url must be an http(s) URL"})
	_, err := m.Board(context.Background())
	var nc *core.NotConnectedError
	if !errors.As(err, &nc) || !strings.Contains(nc.Reason, "board_url") || len(f.calls) != 1 {
		t.Fatalf("%v %v", err, f.calls)
	}
	if _, err := m.BuildBoardArgv(core.BoardChange{Op: core.BoardPost, Channel: "dev", Text: "x"}, "abacus-0123456789abcdef"); err == nil {
		t.Fatal("a write must refuse too")
	}
}

func TestNoBoardConfiguredIsNotConnectedWithTheExactNextStep(t *testing.T) {
	f := helpRuns(t)
	k := "channels list --json --limit 50"
	f.out[k], f.errs[k], f.exit[k] = "", fx(t, "err-no-board.stderr.json"), 2
	m := readyModule(t, f, Options{})
	_, err := m.Board(context.Background())
	var nc *core.NotConnectedError
	if !errors.As(err, &nc) || nc.Reason != core.BoardNotSetUpReason || !strings.Contains(nc.HowToConnect, "abacus config set board_url") || !strings.Contains(nc.HowToConnect, "vercel login") {
		t.Fatalf("%v", err)
	}
	st := m.Status(context.Background())
	if st.Unavailable == "" || st.HowToConnect == "" {
		t.Fatalf("%+v", st)
	}
}

func TestLoginRequiredAndUnreachableAreDifferentStates(t *testing.T) {
	f := helpRuns(t)
	k := "channels list --json --limit 50"
	f.out[k], f.errs[k], f.exit[k] = "", `{"status":"action_required","reason":"login_required","message":"Not signed in."}`, 1
	m := readyModule(t, f, Options{})
	var nc *core.NotConnectedError
	if _, err := m.Board(context.Background()); !errors.As(err, &nc) || nc.Reason != core.BoardNotSignedInReason || nc.HowToConnect != core.BoardNotSignedIn {
		t.Fatalf("%v", err)
	}
	f.errs[k] = fx(t, "err-unreachable.stderr.json")
	_, err := m.Board(context.Background())
	if errors.As(err, &nc) || err == nil || !strings.Contains(err.Error(), "could not reach the board") {
		t.Fatalf("an unreachable board is a read failure (degraded), not 'not connected': %v", err)
	}
}

func TestOneChannelFailingDegradesInsteadOfFailing(t *testing.T) {
	f := boardRuns(t)
	k := "read dev/web --json --limit 5"
	f.out[k], f.errs[k], f.exit[k] = "", fx(t, "err-channel-missing.stderr.json"), 1
	m := readyModule(t, f, Options{})
	r, err := m.Board(context.Background())
	if err != nil || !strings.Contains(r.Reason, "channel dev/web could not be read") || len(r.Threads) == 0 {
		t.Fatalf("%+v %v", r.Header, err)
	}
}

func TestUnreadableOutputIsAnErrorNotAnEmptyBoard(t *testing.T) {
	f := helpRuns(t)
	f.out["channels list --json --limit 50"] = `{"unexpected": true}`
	m := readyModule(t, f, Options{})
	if _, err := m.Board(context.Background()); err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("%v", err)
	}
	f.out["channels list --json --limit 50"] = `not json`
	if _, err := m.Board(context.Background()); err == nil {
		t.Fatal("garbage must be an error")
	}
}

func TestTimeoutIsAnErrorAndNeverHangs(t *testing.T) {
	f := helpRuns(t)
	f.err["channels"] = core.ErrTimedOut
	m := readyModule(t, f, Options{Timeout: time.Second})
	if _, err := m.Board(context.Background()); !errors.Is(err, core.ErrTimedOut) {
		t.Fatalf("%v", err)
	}
}

func TestSecretsInCliTextAreRedactedAndSanitized(t *testing.T) {
	f := helpRuns(t)
	k := "channels list --json --limit 50"
	f.out[k], f.errs[k], f.exit[k] = "", `{"status":"error","reason":"permission_denied","message":"token s3cret-token-value rejected\u001b[2J"}`, 1
	getenv := func(n string) string {
		if n == "BOARD_TOKEN" {
			return "s3cret-token-value"
		}
		return ""
	}
	m := readyModule(t, f, Options{Getenv: getenv})
	_, err := m.Board(context.Background())
	if err == nil || strings.Contains(err.Error(), "s3cret") || strings.ContainsRune(err.Error(), 0x1b) || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("%v", err)
	}
	if d := m.Doctor(context.Background()); strings.Contains(fmt.Sprint(d), "s3cret") {
		t.Fatalf("%v", d)
	}
}

// ---- the argv allowlists, element by element ----

func TestReadAllowlist(t *testing.T) {
	ok := [][]string{
		{"--help"}, {"--version"},
		{"channels", "list", "--json", "--limit", "50"},
		{"channels", "list", "--json", "--limit", "50", "--url", url},
		{"read", "dev/web", "--json", "--limit", "5"},
		{"read", "17", "--json"},
		{"read", "msg_8d9ca1cd-4c78_1", "--json"},
		{"tasks", "--json", "--limit", "20"},
	}
	for _, a := range ok {
		if !allowedRead(a) {
			t.Errorf("must allow %v", a)
		}
	}
	bad := [][]string{
		nil, {},
		{"serve"}, {"tail", "dev"}, {"wait", "dev"}, {"inspect"}, {"whoami", "--json"}, {"hooks", "uninstall"}, {"conformance"},
		{"post", "dev", "--json", "--limit", "5"}, {"reply", "1", "--json"}, {"tasks", "claim", "1"}, {"tasks", "run", "1"},
		{"channels", "create", "x", "--json", "--limit", "5"},
		{"channels", "create", "--json", "--limit", "5"},
		{"channels", "list", "--json", "--limit", "5", "x"},
		{"channels", "list", "--json", "--limit", "0"}, {"channels", "list", "--json", "--limit", "101"}, {"channels", "list", "--json", "--limit", "5x"},
		{"channels", "list", "--limit", "5", "--json"},                          // order is part of the match
		{"channels", "list", "--json", "--limit", "5", "extra"},                 // nothing trailing
		{"channels", "list", "--json", "--limit", "5", "--url", "ftp://x"},      // a bad url is not stripped
		{"channels", "list", "--json", "--limit", "5", "--url", "http://u:p@h"}, // credentials in the url
		{"channels", "list", "--json", "--limit", "5", "--scope", "team"},
		{"read", "-x", "--json", "--limit", "5"}, {"read", "dev;rm", "--json", "--limit", "5"}, {"read", "dev", "--json"}, // a channel read needs --limit
		{"tasks", "--json"}, {"tasks", "--json", "--limit", "20", "--state", "all"}, {"tasks", "--json", "--limit", "500"},
		{"read", "dev", "--json", "--limit", "500"},
		{"--help", "serve"}, {"--version", "x"},
	}
	for _, a := range bad {
		if allowedRead(a) {
			t.Errorf("must refuse %v", a)
		}
	}
}

func TestWriteAllowlist(t *testing.T) {
	k := "abacus-0123456789abcdef"
	ok := [][]string{
		{"post", "dev", "--json", "--idempotency-key", k, "--", "hello"},
		{"post", "dev/web", "--json", "--url", url, "--idempotency-key", k, "--", "--file /etc/passwd"},
		{"reply", "17", "--json", "--idempotency-key", k, "--", "-x"},
	}
	for _, a := range ok {
		if !allowedWrite(a) {
			t.Errorf("must allow %v", a)
		}
	}
	bad := [][]string{
		nil,
		{"post", "dev", "--json", "--idempotency-key", k, "hello"},                     // no `--`: text could be an option
		{"post", "dev", "--json", "--idempotency-key", k, "hello", "more"},             // seven elements but no `--`
		{"post", "dev", "--json", "--idempotency-key", k, "--", ""},                    // empty text
		{"post", "dev", "--json", "--idempotency-key", "x", "--", "hi"},                // key shape
		{"post", "dev", "--json", "--idempotency-key", k, "--", "hi", "more"},          // one text
		{"post", "dev", "--json", "--idempotency-key", k, "--attach", "f", "--", "hi"}, // no attachments
		{"post", "dev", "--json", "--sync", "slack:#x", "--idempotency-key", k, "--", "hi"},
		{"post", "-dev", "--json", "--idempotency-key", k, "--", "hi"},
		{"reply", "dev", "--json", "--idempotency-key", k, "--", "hi"}, // reply wants an id
		{"edit", "17", "--json", "--idempotency-key", k, "--", "hi"},
		{"delete", "17", "--json", "--idempotency-key", k, "--", "hi"},
		{"react", "17", "--json", "--idempotency-key", k, "--", "eyes"},
		{"label", "17", "--json", "--idempotency-key", k, "--", "k=v"},
		{"tasks", "claim", "--json", "--idempotency-key", k, "--", "17"},
		{"post", "dev", "--json", "--url", "ftp://h", "--idempotency-key", k, "--", "hi"},
	}
	for _, a := range bad {
		if allowedWrite(a) {
			t.Errorf("must refuse %v", a)
		}
	}
}

func TestRunRefusesAnArgvOutsideTheAllowlistBeforeRunningAnything(t *testing.T) {
	f := helpRuns(t)
	m := readyModule(t, f, Options{})
	before := len(f.calls)
	if err := m.run(context.Background(), []string{"serve"}, nil, allowedRead); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("%v", err)
	}
	if _, err := m.RunBoard(context.Background(), []string{"post", "dev", "--json", "hi"}, core.BoardChange{}); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("%v", err)
	}
	if len(f.calls) != before {
		t.Fatal("nothing may run")
	}
}

func TestInspectHandoffIsOnlyBuiltNeverRunAndCarriesTheScrubbedEnv(t *testing.T) {
	t.Setenv("BEADS_DIR", "/x")
	t.Setenv("BOARD_TOKEN", "tok-12345678")
	m := readyModule(t, helpRuns(t), Options{URL: url})
	c := m.InspectCmd()
	if got := strings.Join(c.Args, " "); got != "/fake/board inspect --url "+url {
		t.Fatalf("%s", got)
	}
	env := strings.Join(c.Env, "\n")
	if strings.Contains(env, "BEADS_DIR") || !strings.Contains(env, "BOARD_TOKEN=") {
		t.Fatalf("scrubbed env: %s", env)
	}
}

// ---- writes ----

func postDoc(id, channel, text string) string {
	return fmt.Sprintf(`{"message":{"id":%q,"channel":%q,"text":%q},"outcome":"posted"}`, id, channel, text)
}

func readDoc(id, channel, text string) string {
	return fmt.Sprintf(`{"message":{"id":%q,"channel":%q,"text":%q,"replies":[]},"replies":[],"cursor":%q}`, id, channel, text, id)
}

func writeRig(t *testing.T, post, read string, exit int) (*Module, core.BoardPlan, *fakeRun) {
	t.Helper()
	f := helpRuns(t)
	m := readyModule(t, f, Options{})
	plan, err := core.PlanBoardChange(m, core.BoardChange{Op: core.BoardPost, Channel: "dev", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	f.out[strings.Join(plan.Argv, " ")], f.exit[strings.Join(plan.Argv, " ")] = post, exit
	f.out["read 17 --json"] = read
	return m, plan, f
}

func TestPostIsVerifiedByIdAfterItRuns(t *testing.T) {
	m, plan, f := writeRig(t, postDoc("17", "dev", "hello"), readDoc("17", "dev", "hello"), 0)
	if plan.Argv[len(plan.Argv)-2] != "--" || plan.Argv[len(plan.Argv)-1] != "hello" || plan.Argv[0] != "post" {
		t.Fatalf("%v", plan.Argv)
	}
	res, err := core.NewBoardApplier(m, &memAudit{}).Apply(context.Background(), plan.Confirm())
	if err != nil || res.ID != "17" || res.Text != "hello" {
		t.Fatalf("%+v %v", res, err)
	}
	if last := f.calls[len(f.calls)-1]; !strings.HasSuffix(last, "read 17 --json") {
		t.Fatalf("the result must be re-read by id: %v", f.calls)
	}
}

func TestPostThatTheBoardDoesNotHaveBackIsOutcomeUnknownNotSuccess(t *testing.T) {
	for name, read := range map[string]string{
		"different text":    readDoc("17", "dev", "tampered"),
		"different channel": readDoc("17", "other", "hello"),
		"different id":      readDoc("18", "dev", "hello"),
	} {
		m, plan, _ := writeRig(t, postDoc("17", "dev", "hello"), read, 0)
		_, err := core.NewBoardApplier(m, &memAudit{}).Apply(context.Background(), plan.Confirm())
		if !errors.Is(err, core.ErrOutcomeUnknown) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestPostTimeoutIsOutcomeUnknown(t *testing.T) {
	m, plan, f := writeRig(t, "", "", 0)
	f.err["post dev"] = core.ErrTimedOut
	a := &memAudit{}
	_, err := core.NewBoardApplier(m, a).Apply(context.Background(), plan.Confirm())
	if !errors.Is(err, core.ErrOutcomeUnknown) || a.lines[len(a.lines)-1].Phase != "unknown" {
		t.Fatalf("%v %+v", err, a.lines)
	}
	if got := core.DescribeWriteError(err); strings.HasPrefix(got, "not applied") {
		t.Fatalf("an unknown outcome is never 'not applied': %s", got)
	}
}

func TestPostFailureIsAPlainError(t *testing.T) {
	m, plan, f := writeRig(t, "", "", 1)
	k := strings.Join(plan.Argv, " ")
	f.errs[k] = `{"status":"error","reason":"permission_denied","message":"You can't post to dev."}`
	_, err := core.NewBoardApplier(m, &memAudit{}).Apply(context.Background(), plan.Confirm())
	if err == nil || errors.Is(err, core.ErrOutcomeUnknown) || !strings.Contains(err.Error(), "can't post") {
		t.Fatalf("%v", err)
	}
}

type memAudit struct{ lines []core.AuditEntry }

func (a *memAudit) Record(e core.AuditEntry) error { a.lines = append(a.lines, e); return nil }

func TestRealPostOutputShapeDecodes(t *testing.T) {
	var w writeDoc
	if err := json.Unmarshal([]byte(fx(t, "post.json")), &w); err != nil || w.Message == nil || !core.BoardIDRE.MatchString(w.Message.ID) || w.Outcome != "posted" {
		t.Fatalf("%+v %v", w, err)
	}
}
