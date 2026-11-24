package cli_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/cli"
	"github.com/andrew-bierman/abacus/internal/core"
)

// logAudit is an audit log that can be read back, with a movable clock for the entries it stamps.
type logAudit struct {
	entries []core.AuditEntry
	age     time.Duration // how long ago every entry recorded from now on is stamped
}

func (a *logAudit) Record(e core.AuditEntry) error {
	e.Time = time.Now().Add(-a.age)
	a.entries = append(a.entries, e)
	return nil
}
func (a *logAudit) Entries() ([]core.AuditEntry, error) { return a.entries, nil }

func runBoard(t *testing.T, m *boardMod, au core.Audit, args ...string) (int, string, string) {
	t.Helper()
	var o, e bytes.Buffer
	code := cli.Run(args, cli.Deps{Out: &o, Err: &e, Audit: au, Version: "t",
		Build: func(cli.Options) *core.Registry { return core.NewRegistry(m) }})
	return code, o.String(), e.String()
}

// A dry run reviewed long before the apply, or either side of a ten-minute boundary, still applies:
// the key has no clock in it.
func TestBoardDigestDoesNotDependOnTheClock(t *testing.T) {
	var d string
	for _, at := range []time.Time{contractNow, contractNow.Add(9*time.Minute + 59*time.Second), contractNow.Add(37 * time.Minute)} {
		restore := core.SetBoardClockForTest(func() time.Time { return at })
		_, out, _ := runBoard(t, newBoardMod(), &logAudit{}, "board", "post", "dev", "hi", "--dry-run", "--json")
		restore()
		if d2 := digestOf(t, out); d != "" && d2 != d {
			t.Fatalf("the digest moved with the clock: %s vs %s", d, d2)
		} else {
			d = d2
		}
	}
	restore := core.SetBoardClockForTest(func() time.Time { return contractNow.Add(3 * time.Hour) })
	defer restore()
	m := newBoardMod()
	code, out, errOut := runBoard(t, m, &logAudit{}, "board", "post", "dev", "hi", "--yes", "--json", "--expect-digest", d)
	if code != 0 || len(m.ran) != 1 {
		t.Fatalf("code=%d ran=%v out=%s err=%s", code, m.ran, out, errOut)
	}
}

// The clock no longer guards a double post, the audit log does.
func TestBoardRepeatIsRefusedFromTheAuditLogUnlessAgain(t *testing.T) {
	au := &logAudit{}
	m := newBoardMod()
	post := func(extra ...string) (int, string) {
		code, out, errOut := runBoard(t, m, au, append([]string{"board", "post", "dev", "hi", "--yes", "--json"}, extra...)...)
		_ = errOut
		return code, out
	}
	if code, out := post(); code != 0 {
		t.Fatalf("first: %d %s", code, out)
	}
	first := strings.Join(m.ran[0], " ")
	// the same message again, right away: refused, nothing sent (a dry run says so too)
	code, out := post()
	if code != 1 || errorCode(out) != "refused" || len(m.ran) != 1 {
		t.Fatalf("repeat: %d %s ran=%d", code, out, len(m.ran))
	}
	if code, out, _ := runBoard(t, m, au, "board", "post", "dev", "hi", "--dry-run", "--json"); code != 1 || errorCode(out) != "refused" {
		t.Errorf("a dry run of a repeat: %d %s", code, out)
	}
	// --again posts it, under a key the board has not seen
	if code, out := post("--again"); code != 0 || len(m.ran) != 2 || strings.Join(m.ran[1], " ") == first {
		t.Fatalf("--again: %d %s ran=%v", code, out, m.ran)
	}
	// different text is not a repeat
	if code, _, _ := runBoard(t, m, au, "board", "post", "dev", "other", "--yes"); code != 0 || len(m.ran) != 3 {
		t.Errorf("other text: %d", code)
	}
}

// Once the window has passed it is a new post, with a fresh key (the board would drop a reused one).
func TestBoardRepeatAfterTheWindowGetsAFreshKey(t *testing.T) {
	au := &logAudit{age: 30 * time.Minute}
	m := newBoardMod()
	for i := 0; i < 2; i++ {
		if code, out, e := runBoard(t, m, au, "board", "post", "dev", "hi", "--yes", "--json"); code != 0 {
			t.Fatalf("post %d: %d %s %s", i, code, out, e)
		}
	}
	if strings.Join(m.ran[0], " ") == strings.Join(m.ran[1], " ") {
		t.Fatal("the second post reused the first one's key")
	}
}

func TestBoardCLINonceFailsClosedOnAnUnreadableLog(t *testing.T) {
	if _, _, err := core.BoardCLINonce(badLog{}, "t", core.BoardChange{Op: core.BoardPost, Channel: "dev", Text: "x"}, time.Minute, time.Now()); err == nil {
		t.Fatal("an audit log that cannot be read back must stop the write")
	}
}

type badLog struct{}

func (badLog) Record(core.AuditEntry) error        { return nil }
func (badLog) Entries() ([]core.AuditEntry, error) { return nil, errTest }

var errTest = errString("disk on fire")

type errString string

func (e errString) Error() string { return string(e) }
