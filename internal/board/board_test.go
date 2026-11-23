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
	if !strings.Contains(core.HowToConnectFor(core.DomainBoard), "npm i -g @vercel/board") || strings.Contains(core.HowToConnectFor(core.DomainBoard), "npx vb") && !strings.Contains(core.HowToConnectFor(core.DomainBoard), "never") {
		t.Fatalf("the install hint must name @vercel/board and warn off vb: %s", core.HowToConnectFor(core.DomainBoard))
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

func writeRig(t *testing.T, post, read string, exit int, o ...Options) (*Module, core.BoardPlan, *fakeRun) {
	t.Helper()
	f := helpRuns(t)
	opt := Options{}
	if len(o) > 0 {
		opt = o[0]
	}
	m := readyModule(t, f, opt)
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

// ---- review fixes ----

func TestAmbiguousWriteFailuresAreUnknownOutcome(t *testing.T) {
	cases := map[string]func(f *fakeRun, k string){
		"network_error": func(f *fakeRun, k string) {
			f.errs[k], f.exit[k] = `{"status":"error","reason":"network_error","message":"Couldn't reach the board."}`, 1
		},
		"timeout reason": func(f *fakeRun, k string) {
			f.errs[k], f.exit[k] = `{"status":"error","reason":"timeout","message":"Timed out."}`, 1
		},
		"exit 1, no failure": func(f *fakeRun, k string) { f.exit[k] = 1 },
		"exit 1, not json":   func(f *fakeRun, k string) { f.errs[k], f.exit[k] = "segfault", 1 },
		"exit 0, garbage":    func(f *fakeRun, k string) { f.out[k] = "not json" },
		"exit 0, empty":      func(f *fakeRun, k string) { f.out[k] = "" },
		"runner error":       func(f *fakeRun, _ string) { f.err["post dev"] = errors.New("pipe closed") },
		"canceled":           func(f *fakeRun, _ string) { f.err["post dev"] = core.ErrCanceled },
		"timed out":          func(f *fakeRun, _ string) { f.err["post dev"] = core.ErrTimedOut },
	}
	for name, set := range cases {
		m, plan, f := writeRig(t, "", "", 0)
		set(f, strings.Join(plan.Argv, " "))
		a := &memAudit{}
		_, err := core.NewBoardApplier(m, a).Apply(context.Background(), plan.Confirm())
		if !errors.Is(err, core.ErrOutcomeUnknown) || !strings.Contains(strings.ToLower(err.Error()), "refresh") {
			t.Errorf("%s: must be an unknown outcome that says to refresh: %v", name, err)
		}
		if a.lines[len(a.lines)-1].Phase != "unknown" {
			t.Errorf("%s: audit %+v", name, a.lines)
		}
	}
}

func TestDefiniteWriteRefusalsStayNotApplied(t *testing.T) {
	for _, reason := range []string{"permission_denied", "login_required", "missing_scope", "channel_not_found", "message_not_found", "invalid_arguments"} {
		m, plan, f := writeRig(t, "", "", 1)
		f.errs[strings.Join(plan.Argv, " ")] = `{"status":"error","reason":"` + reason + `","message":"refused: ` + reason + `"}`
		_, err := core.NewBoardApplier(m, &memAudit{}).Apply(context.Background(), plan.Confirm())
		if err == nil || errors.Is(err, core.ErrOutcomeUnknown) || !strings.HasPrefix(core.DescribeWriteError(err), "not applied") {
			t.Errorf("%s is a definite refusal: %v", reason, err)
		}
	}
}

func TestWritesCarryTheBoardURLExactlyWhenOneIsConfigured(t *testing.T) {
	const k = "abacus-0123456789abcdef"
	for _, c := range []struct {
		url  string
		post []string
		repl []string
	}{
		{"", []string{"post", "dev", "--json", "--idempotency-key", k, "--", "hi"}, []string{"reply", "17", "--json", "--idempotency-key", k, "--", "hi"}},
		{url, []string{"post", "dev", "--json", "--url", url, "--idempotency-key", k, "--", "hi"}, []string{"reply", "17", "--json", "--url", url, "--idempotency-key", k, "--", "hi"}},
	} {
		m := readyModule(t, helpRuns(t), Options{URL: c.url})
		got, err := m.BuildBoardArgv(core.BoardChange{Op: core.BoardPost, Channel: "dev", Text: "hi"}, k)
		if err != nil || strings.Join(got, "\x00") != strings.Join(c.post, "\x00") {
			t.Errorf("url=%q post: %v %v", c.url, got, err)
		}
		got, err = m.BuildBoardArgv(core.BoardChange{Op: core.BoardReply, ID: "17", Text: "hi"}, k)
		if err != nil || strings.Join(got, "\x00") != strings.Join(c.repl, "\x00") {
			t.Errorf("url=%q reply: %v %v", c.url, got, err)
		}
	}
	// And what actually runs is that argv, with the url.
	m, plan, f := writeRig(t, postDoc("17", "dev", "hello"), readDoc("17", "dev", "hello"), 0, Options{URL: url})
	f.out["read 17 --json --url "+url] = readDoc("17", "dev", "hello")
	if _, err := core.NewBoardApplier(m, &memAudit{}).Apply(context.Background(), plan.Confirm()); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls[1:] {
		if !strings.HasSuffix(c, "--url "+url) && !strings.Contains(c, "--url "+url+" --idempotency-key") {
			t.Errorf("a call without the pinned --url: %s", c)
		}
	}
}

func TestReplyIsVerifiedByIdInTheThread(t *testing.T) {
	thread := func(id, text string) string {
		return fmt.Sprintf(`{"message":{"id":"3","channel":"dev/web","text":"root"},"replies":[{"id":%q,"channel":"dev/web","text":%q}],"cursor":%q}`, id, text, id)
	}
	for name, c := range map[string]struct {
		read    string
		unknown bool
	}{"matches": {thread("21", "ack"), false}, "different text": {thread("21", "other"), true}, "id missing from the thread": {thread("22", "ack"), true}} {
		f := helpRuns(t)
		m := readyModule(t, f, Options{})
		plan, err := core.PlanBoardChange(m, core.BoardChange{Op: core.BoardReply, ID: "3", Text: "ack"})
		if err != nil {
			t.Fatal(err)
		}
		f.out[strings.Join(plan.Argv, " ")] = `{"message":{"id":"21"},"outcome":"posted"}`
		f.out["read 21 --json"] = c.read
		res, err := core.NewBoardApplier(m, &memAudit{}).Apply(context.Background(), plan.Confirm())
		if c.unknown != errors.Is(err, core.ErrOutcomeUnknown) || (!c.unknown && (err != nil || res.ID != "21")) {
			t.Errorf("%s: %+v %v", name, res, err)
		}
	}
}

func TestTasksReadAllowlistPinsJsonAndTheExactArgv(t *testing.T) {
	if !allowedRead([]string{"tasks", "--json", "--limit", "20"}) {
		t.Fatal("the exact tasks read is allowed")
	}
	for _, a := range [][]string{
		{"tasks", "--limit", "20", "--json"}, {"tasks", "--limit", "20", "--json", "--limit", "20"}, {"tasks", "--jsonx", "--limit", "20"},
		{"tasks", "--state", "all", "--limit", "20"}, {"tasks", "dev", "--json", "--limit", "20"}, {"tasks", "--json", "--limit"},
		{"tasks", "--json", "--limit", "20", "--url"},
	} {
		if allowedRead(a) {
			t.Errorf("must refuse %v", a)
		}
	}
}

func scriptBoard(t *testing.T) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "board")
	body := "#!/bin/sh\ncase \"$1\" in\n--help) env > '" + dir + "/help.env'; printf '%s\\n' \"board 0.54.0: your Vercel team's message board, for people and agents\" '' 'usage: board <command> [options]' ;;\n*) env > '" + dir + "/read.env'; echo '{}' ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

func envNames(t *testing.T, path string) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, l := range strings.Split(string(b), "\n") {
		n, _, _ := strings.Cut(l, "=")
		out[n] = true
	}
	return out
}

func TestIdentityProbeRunsWithoutAnySecretButReadsKeepTheTokens(t *testing.T) {
	for k, v := range map[string]string{"BOARD_TOKEN": "tok-12345678", "VERCEL_AUTH_TOKEN": "vat-12345678", "VERCEL_AUTOMATION_BYPASS_SECRET": "byp-12345678", "VERCEL_ORG_ID": "team_x", "BOARD_URL": "http://evil.example/x", "BOARD_NAME": "n"} {
		t.Setenv(k, v)
	}
	bin, dir := scriptBoard(t)
	m := New(Options{Bin: bin, URL: "https://example.com/api/board"})
	if d := m.Detect(context.Background()); d.State != core.Ready {
		t.Fatalf("%+v", d)
	}
	_, _ = m.Board(context.Background())
	probe, read := envNames(t, dir+"/help.env"), envNames(t, dir+"/read.env")
	for _, n := range []string{"BOARD_TOKEN", "VERCEL_AUTH_TOKEN", "VERCEL_AUTOMATION_BYPASS_SECRET", "VERCEL_ORG_ID", "BOARD_URL", "BOARD_NAME"} {
		if probe[n] {
			t.Errorf("the identity probe must not see %s", n)
		}
	}
	if !probe["PATH"] || !probe["HOME"] {
		t.Error("the probe still has PATH and HOME")
	}
	if !read["BOARD_TOKEN"] || !read["VERCEL_AUTH_TOKEN"] {
		t.Error("reads keep the credentials")
	}
	if read["BOARD_URL"] || read["BOARD_NAME"] {
		t.Error("BOARD_URL never passes, and BOARD_NAME not beside --url")
	}
}

func TestNothingRunsAtAllAgainstPlainHttpToARemoteHost(t *testing.T) {
	t.Setenv("BOARD_TOKEN", "tok-12345678")
	bin, dir := scriptBoard(t)
	m := New(Options{Bin: bin, URL: "http://example.com/api/board"}) // not reachable through ValidateBoardURL; defense in depth
	m.Detect(context.Background())
	if _, err := m.Board(context.Background()); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("the argv allowlist refuses it: %v", err)
	}
	if _, err := os.Stat(dir + "/read.env"); err == nil {
		t.Fatal("the CLI must not have been run with a token for an http non-loopback target")
	}
}

func TestShapeDriftIsUnreadableNotAnEmptyOrBlankRow(t *testing.T) {
	f := helpRuns(t)
	f.out["channels list --json --limit 50"] = `{"channels":[{"path":""}],"next":null}`
	m := readyModule(t, f, Options{})
	if _, err := m.Board(context.Background()); err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("an empty channel path: %v", err)
	}
	g := boardRuns(t)
	g.out["read dev/web --json --limit 5"] = `{"threads":[{"message":{"id":"","channel":"dev/web"},"replies":0,"last":null}],"cursor":"1"}`
	g.out["tasks --json --limit 20"] = `{"tasks":[{"message":{"id":"","channel":"work","task":{"state":"ready"}}}],"cursor":"1"}`
	r, err := readyModule(t, g, Options{}).Board(context.Background())
	if err != nil || !strings.Contains(r.Reason, "dev/web") || !strings.Contains(r.Reason, "tasks could not be read") {
		t.Fatalf("an empty message or task id degrades that part: %+v %v", r.Header, err)
	}
	for _, th := range r.Threads {
		if th.Root.ID == "" {
			t.Fatal("a blank thread row must not be shown")
		}
	}
	if len(r.Tasks) != 0 {
		t.Fatalf("%+v", r.Tasks)
	}
}

func TestMinimumCliVersion(t *testing.T) {
	for ver, ok := range map[string]bool{"0.53.9": false, "0.9.0": false, "0.54.0": true, "0.54.1": true, "0.60.0": true, "1.0.0": true, "0.54.0-beta.1": true} {
		help := strings.ReplaceAll(fx(t, "help.txt"), "board 0.54.0:", "board "+ver+":")
		f := &fakeRun{out: map[string]string{"--help": help}, errs: map[string]string{}, exit: map[string]int{}, err: map[string]error{}}
		d := New(Options{Runner: f, LookPath: look("board")}).Detect(context.Background())
		if ok != (d.State == core.Ready) {
			t.Errorf("%s: %+v", ver, d)
		}
		if !ok && (d.State != core.Incompatible || !strings.Contains(d.Detail, "0.54.0") || !strings.Contains(d.Detail, "npm i -g @vercel/board")) {
			t.Errorf("%s: an old CLI says what to install: %+v", ver, d)
		}
	}
}

func TestTruncationIsNamedButDoesNotDegrade(t *testing.T) {
	r, err := readyModule(t, boardRuns(t), Options{}).Board(context.Background())
	if err != nil || r.Truncated != "" {
		t.Fatalf("3 channels and 2 tasks are all there is: %q %v", r.Truncated, err)
	}
	many := `{"channels":[{"path":"a","lastIndex":5},{"path":"b","lastIndex":4},{"path":"c","lastIndex":3},{"path":"d","lastIndex":2}],"next":"e"}`
	f := boardRuns(t)
	f.out["channels list --json --limit 50"] = many
	for _, c := range []string{"a", "b", "c"} {
		f.out["read "+c+" --json --limit 5"] = `{"threads":[],"cursor":"1"}`
	}
	var tasks []string
	for i := 0; i < maxTasks; i++ {
		tasks = append(tasks, fmt.Sprintf(`{"message":{"id":"%d","channel":"a","text":"t","task":{"state":"ready"}},"replies":0,"last":null}`, i+1))
	}
	f.out["tasks --json --limit 20"] = `{"tasks":[` + strings.Join(tasks, ",") + `],"cursor":"20"}`
	r, err = readyModule(t, f, Options{}).Board(context.Background())
	if err != nil || r.Reason != "" {
		t.Fatalf("truncation is not a failure: %+v %v", r.Header, err)
	}
	for _, want := range []string{"first 50 channels", "3 most recently active", "first 20 tasks"} {
		if !strings.Contains(r.Truncated, want) {
			t.Errorf("Truncated %q misses %q", r.Truncated, want)
		}
	}
}

func TestMessageTextKeepsItsNewlinesAndDeletedMessagesSayTheyAre(t *testing.T) {
	f := boardRuns(t)
	f.out["read dev/web --json --limit 5"] = `{"threads":[{"message":{"id":"1","channel":"dev/web","text":"line one\nline two\u001b[2J","createdAt":"2026-11-22T20:00:00Z","author":{"handle":"ada"}},"replies":1,"last":{"id":"2","channel":"dev/web","text":"secret text","deleted":true,"author":{"handle":"ada"}}}],"cursor":"2"}`
	r, err := readyModule(t, f, Options{}).Board(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got *core.BoardThread
	for i := range r.Threads {
		if r.Threads[i].Root.ID == "1" {
			got = &r.Threads[i]
		}
	}
	if got == nil || got.Root.Text != "line one\nline two[2J" {
		t.Fatalf("newlines are data, controls are not: %+v", got)
	}
	if got.Last.Text != "(deleted)" || strings.Contains(got.Last.Text, "secret") {
		t.Fatalf("a deleted message never shows its old text: %+v", got.Last)
	}
}

func TestRedactionHappensAfterSanitizingSoInvisibleCharsCannotSplitASecret(t *testing.T) {
	f := helpRuns(t)
	k := "channels list --json --limit 50"
	f.errs[k], f.exit[k] = `{"status":"error","reason":"permission_denied","message":"bad token tok-\u200b12345678 here"}`, 1
	m := readyModule(t, f, Options{Getenv: func(n string) string {
		if n == "BOARD_TOKEN" {
			return "tok-12345678"
		}
		return ""
	}})
	_, err := m.Board(context.Background())
	if err == nil || strings.Contains(err.Error(), "12345678") {
		t.Fatalf("%v", err)
	}
}

func TestAnOversizedAnswerIsNamedInTheReason(t *testing.T) {
	f := boardRuns(t)
	delete(f.out, "read dev/web --json --limit 5")
	f.err["read dev/web"] = core.ErrOutputTooLarge
	r, err := readyModule(t, f, Options{}).Board(context.Background())
	if err != nil || !strings.Contains(r.Reason, "too large") {
		t.Fatalf("%+v %v", r.Header, err)
	}
}

func TestDescribeForAReplyShowsWhatItAnswersAndSurvivesAFailedRead(t *testing.T) {
	f := helpRuns(t)
	f.out["read 3 --json"] = fx(t, "read-thread.json")
	m := readyModule(t, f, Options{})
	got := strings.Join(m.Describe(context.Background(), core.BoardChange{Op: core.BoardReply, ID: "3", Text: "x"}), "\n")
	for _, want := range []string{"dev/web", "ada", "checkout times out"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	f.errs["read 4 --json"], f.exit["read 4 --json"], f.out["read 4 --json"] = `{"status":"error","reason":"message_not_found","message":"Couldn't find message #4."}`, 1, ""
	got = strings.Join(m.Describe(context.Background(), core.BoardChange{Op: core.BoardReply, ID: "4", Text: "x"}), "\n")
	if !strings.Contains(got, "could not read message 4") || !strings.Contains(got, "still confirm") {
		t.Errorf("a failed read is said on the confirm screen: %q", got)
	}
}

func TestDescribeForAPostFlagsANewChannel(t *testing.T) {
	f := boardRuns(t)
	m := readyModule(t, f, Options{})
	if got := strings.Join(m.Describe(context.Background(), core.BoardChange{Op: core.BoardPost, Channel: "dev/web", Text: "x"}), "\n"); !strings.Contains(got, "existing channel dev/web") || strings.Contains(got, "NEW") {
		t.Errorf("%q", got)
	}
	if got := strings.Join(m.Describe(context.Background(), core.BoardChange{Op: core.BoardPost, Channel: "brand/new", Text: "x"}), "\n"); !strings.Contains(got, "NEW channel brand/new") {
		t.Errorf("%q", got)
	}
	g := helpRuns(t)
	g.errs["channels list --json --limit 50"], g.exit["channels list --json --limit 50"], g.out["channels list --json --limit 50"] = `{"status":"error","reason":"network_error","message":"x"}`, 1, ""
	if got := strings.Join(readyModule(t, g, Options{}).Describe(context.Background(), core.BoardChange{Op: core.BoardPost, Channel: "dev", Text: "x"}), "\n"); !strings.Contains(got, "could not check") || !strings.Contains(got, "still confirm") {
		t.Errorf("%q", got)
	}
}

func TestTargetIsCredentialFreeAndSaysWhereItCameFrom(t *testing.T) {
	m := readyModule(t, helpRuns(t), Options{URL: "https://h.example/api/board?token=s3cret#f", URLSource: "board_url (config)"})
	if tg := m.Target(); strings.ContainsAny(tg, "?#") || strings.Contains(tg, "s3cret") || tg != "https://h.example/api/board" {
		t.Errorf("%q", tg)
	}
	if m.TargetSource() != "board_url (config)" {
		t.Errorf("%q", m.TargetSource())
	}
	d := readyModule(t, helpRuns(t), Options{})
	if d.Target() != "the CLI's default board" || !strings.Contains(d.TargetSource(), "default") {
		t.Errorf("%q %q", d.Target(), d.TargetSource())
	}
}

func TestDoctorSaysWhenTheConfiguredURLIsUnusable(t *testing.T) {
	m := readyModule(t, helpRuns(t), Options{URLErr: "BOARD_URL must use https"})
	var warn bool
	for _, c := range m.Doctor(context.Background()) {
		warn = warn || (c.Status == core.StatusWarn && strings.Contains(c.Detail, "BOARD_URL must use https"))
	}
	if !warn {
		t.Fatal("Doctor must say why the URL is not used")
	}
}
