package board

import (
	"context"
	neturl "net/url"
	"os"
	"strings"
	"testing"

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
