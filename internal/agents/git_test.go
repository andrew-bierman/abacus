package agents

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fakeGitRunner supplies fixed output and a call log, so no test reads this machine or shells
// out to git.
type fakeGitRunner struct {
	out   []byte
	exit  int
	err   error
	calls []string
}

func (f *fakeGitRunner) Run(_ context.Context, name string, args ...string) ([]byte, int, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	return f.out, f.exit, f.err
}

type deadlineRunner struct {
	hasDeadline bool
}

func (r *deadlineRunner) Run(ctx context.Context, _ string, _ ...string) ([]byte, int, error) {
	_, r.hasDeadline = ctx.Deadline()
	return []byte("## main\n"), 0, nil
}

func TestGitStatusParsesBranchAndCounts(t *testing.T) {
	r := &fakeGitRunner{out: []byte("## feat/x...origin/feat/x [ahead 2]\n" +
		"?? new.txt\n" +
		"A  staged.txt\n" +
		" M edited.txt\n" +
		"M  staged-edit.txt\n" +
		"AM both.txt\n" +
		"R  old.txt -> new.txt\n" +
		" D gone.txt\n" +
		"UU conflict.txt\n")}
	g := &Git{Runner: r}
	got := g.Status(context.Background(), "/repo")
	want := GitStatus{Branch: "feat/x", Added: 3, Modified: 2}
	if got != want {
		t.Errorf("status = %+v, want %+v", got, want)
	}
	wantCall := "git --no-optional-locks -C /repo status --porcelain --branch"
	if len(r.calls) != 1 || r.calls[0] != wantCall {
		t.Errorf("calls = %q, want one %q", r.calls, wantCall)
	}
}

func TestGitStatusParsesBranchHeaders(t *testing.T) {
	cases := []struct {
		out  string
		want string
	}{
		{"## main\n", "main"},
		{"## feat/payments\n", "feat/payments"},
		{"## main...origin/main [ahead 1, behind 2]\n", "main"},
		{"## No commits yet on trunk\n", "trunk"},
		{"## HEAD (no branch)\n", ""},
		{"## HEAD\n", ""},
	}
	for _, c := range cases {
		if got := parseGitStatus([]byte(c.out)).Branch; got != c.want {
			t.Errorf("branch from %q = %q, want %q", c.out, got, c.want)
		}
	}
}

func TestGitStatusCacheReusesUntilTTL(t *testing.T) {
	now := time.Unix(1_000, 0)
	r := &fakeGitRunner{out: []byte("## main\n")}
	g := &Git{Runner: r, TTL: 10 * time.Second, Now: func() time.Time { return now }}

	if got := g.Status(context.Background(), "/repo"); got.Branch != "main" {
		t.Fatalf("first status = %+v, want main", got)
	}
	g.Status(context.Background(), "/repo")
	if len(r.calls) != 1 {
		t.Fatalf("calls within TTL = %d, want 1", len(r.calls))
	}

	now = now.Add(10 * time.Second)
	g.Status(context.Background(), "/repo")
	if len(r.calls) != 2 {
		t.Errorf("calls after TTL = %d, want 2", len(r.calls))
	}
}

func TestGitStatusCachesPerDirectory(t *testing.T) {
	r := &fakeGitRunner{out: []byte("## main\n")}
	g := &Git{Runner: r, Now: func() time.Time { return time.Unix(1_000, 0) }}

	g.Status(context.Background(), "/a")
	g.Status(context.Background(), "/a")
	g.Status(context.Background(), "/b")
	if len(r.calls) != 2 {
		t.Fatalf("calls = %q, want one per directory", r.calls)
	}
	want := []string{
		"git --no-optional-locks -C /a status --porcelain --branch",
		"git --no-optional-locks -C /b status --porcelain --branch",
	}
	if !reflect.DeepEqual(r.calls, want) {
		t.Errorf("calls = %q, want %q", r.calls, want)
	}
}

func TestGitStatusCachesFailureAndIgnoresIt(t *testing.T) {
	r := &fakeGitRunner{exit: 128}
	g := &Git{Runner: r, Now: func() time.Time { return time.Unix(1_000, 0) }}

	if got := g.Status(context.Background(), "/not-a-repo"); got != (GitStatus{}) {
		t.Errorf("status = %+v, want zero", got)
	}
	g.Status(context.Background(), "/not-a-repo")
	if len(r.calls) != 1 {
		t.Errorf("calls = %d, want the failure cached", len(r.calls))
	}

	// A runner can fail with a zero exit, for example capped output.
	r.exit, r.err = 0, errors.New("git missing")
	if got := g.Status(context.Background(), "/broken"); got != (GitStatus{}) {
		t.Errorf("status on runner error = %+v, want zero", got)
	}
}

func TestGitStatusEmptyDirectoryIsNotRead(t *testing.T) {
	r := &fakeGitRunner{out: []byte("## main\n")}
	g := &Git{Runner: r}
	if got := g.Status(context.Background(), ""); got != (GitStatus{}) {
		t.Errorf("status without directory = %+v, want zero", got)
	}
	if len(r.calls) != 0 {
		t.Errorf("calls for an empty directory = %q, want none", r.calls)
	}
}

func TestGitStatusWithoutRunnerIsZero(t *testing.T) {
	g := &Git{}
	if got := g.Status(context.Background(), "/repo"); got != (GitStatus{}) {
		t.Errorf("status without runner = %+v, want zero", got)
	}
}

func TestGitStatusBoundsRead(t *testing.T) {
	r := &deadlineRunner{}
	g := &Git{Runner: r}
	g.Status(context.Background(), "/repo")
	if !r.hasDeadline {
		t.Error("a read without a caller deadline should get one")
	}
}
