package board

import (
	"context"
	"errors"
	"net"
	neturl "net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// Runs the real Vercel Board CLI against a local board the caller started in isolation:
//
//	npm i --ignore-scripts @vercel/board@0.54.0   (in a temp prefix)
//	board serve --port N --dir TMP --no-inspect --no-open   (isolated HOME/XDG)
//	HOME=... XDG_CONFIG_HOME=... ABACUS_BOARD_REAL_BIN=.../board ABACUS_BOARD_REAL_URL=http://127.0.0.1:N/api/board go test ./internal/board -run Real
//
// It posts to that board, so only a loopback URL is accepted: never a team board.
func TestRealBoardCliAgainstAnIsolatedLocalBoard(t *testing.T) {
	bin, raw := os.Getenv("ABACUS_BOARD_REAL_BIN"), os.Getenv("ABACUS_BOARD_REAL_URL")
	if bin == "" || raw == "" {
		t.Skip("set ABACUS_BOARD_REAL_BIN and ABACUS_BOARD_REAL_URL (a loopback local board) to run this")
	}
	if u, err := neturl.Parse(raw); err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
		t.Fatalf("refusing %q: only a loopback local board may be written to", raw)
	}
	m := New(Options{Bin: bin, URL: raw})
	ctx := context.Background()
	if d := m.Detect(ctx); d.State != core.Ready || d.Version == "" {
		t.Fatalf("%+v", d)
	}
	plan, err := core.PlanBoardChange(m, core.BoardChange{Op: core.BoardPost, Channel: "real/test", Text: "real run, posted by the abacus test"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := core.NewBoardApplier(m, &memAudit{}).Apply(ctx, plan.Confirm())
	if err != nil || res.ID == "" {
		t.Fatalf("%+v %v", res, err)
	}
	rp, err := core.PlanBoardChange(m, core.BoardChange{Op: core.BoardReply, ID: res.ID, Text: "--file /etc/hostname"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.NewBoardApplier(m, &memAudit{}).Apply(ctx, rp.Confirm()); err != nil {
		t.Fatal(err)
	}
	r, err := m.Board(ctx)
	if err != nil || r.Reason != "" {
		t.Fatalf("%+v %v", r, err)
	}
	found := false
	for _, th := range r.Threads {
		found = found || (th.Root.ID == res.ID && th.Replies == 1 && strings.Contains(th.Last.Text, "--file"))
	}
	if !found {
		t.Fatalf("the post and its reply are on the board: %+v", r.Threads)
	}
}

// A board that accepts the connection and never answers: the real CLI has no client deadline (it
// was left running for 100 s), so the only timeout is abacus's own. It must end the read, and a
// post must be reported as an unknown result. The "board" is a listener in this process on a
// loopback port; nothing leaves the machine.
func TestRealBoardCliAStalledBoardIsCutByAbacusDeadline(t *testing.T) {
	bin := os.Getenv("ABACUS_BOARD_REAL_BIN")
	if bin == "" {
		t.Skip("set ABACUS_BOARD_REAL_BIN to run this")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var held []net.Conn
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			held = append(held, c) // never answered, never closed
		}
	}()
	m := New(Options{Bin: bin, URL: "http://" + l.Addr().String() + "/api/board", Timeout: 3 * time.Second})
	ctx := context.Background()
	if d := m.Detect(ctx); d.State != core.Ready {
		t.Fatalf("%+v", d)
	}
	start := time.Now()
	if _, err := m.Board(ctx); !errors.Is(err, core.ErrTimedOut) || time.Since(start) > 20*time.Second {
		t.Fatalf("a stalled board is cut by the deadline: %v after %v", err, time.Since(start))
	}
	plan, err := core.PlanBoardChange(m, core.BoardChange{Op: core.BoardPost, Channel: "stall/test", Text: "never lands"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.NewBoardApplier(m, &memAudit{}).Apply(ctx, plan.Confirm()); !errors.Is(err, core.ErrOutcomeUnknown) {
		t.Fatalf("a post to a stalled board is an unknown result, never 'not applied': %v", err)
	}
}
