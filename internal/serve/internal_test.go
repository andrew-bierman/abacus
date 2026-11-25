package serve

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

func TestMaxDepthIgnoresBracketsInStrings(t *testing.T) {
	for in, want := range map[string]int{
		`{}`: 1, `{"a":[1,{"b":2}]}`: 3, `{"a":"[[[[[[["}`: 1, `{"a":"\"[[["}`: 1, `{"a\\":[[1]]}`: 3, `[[[[1]]]]`: 4,
	} {
		if got := maxDepth([]byte(in)); got != want {
			t.Errorf("maxDepth(%s) = %d, want %d", in, got, want)
		}
	}
}

func TestAppliedIDsAreRememberedLongerThanOtherEndedOnes(t *testing.T) {
	st, _ := testStore(2)
	d := strings.Repeat("a", 64)
	st.add(held("first", d))
	if _, err := st.take("first", ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3*maxEnded; i++ { // far more than the ordinary bound of plans that expired
		id := newPlanID()
		st.add(held(id, d))
		_ = st.cancel(id)
	}
	if _, err := st.take("first", ""); codeOf(err) != core.CodeAlreadyApplied {
		t.Fatalf("an applied id must outlive %d cancelled ones: %v", 3*maxEnded, err)
	}
	var last, mid string
	for i := 0; i < maxApplied+5; i++ {
		last = newPlanID()
		st.add(held(last, d))
		_, _ = st.take(last, "")
		if i == 10 {
			mid = last
		}
	}
	if _, err := st.take(mid, ""); codeOf(err) != core.CodeAlreadyApplied {
		t.Fatalf("an id applied thousands of plans ago is still within the bound: %v", err)
	}
	if _, err := st.take(last, ""); codeOf(err) != core.CodeAlreadyApplied {
		t.Fatalf("a recently applied id must still be remembered: %v", err)
	}
	if len(st.appliedOrder) > maxApplied || len(st.ended) > maxApplied+maxEnded {
		t.Fatalf("applied memory is unbounded: %d", len(st.appliedOrder))
	}
}

func TestParseIDIsCanonical(t *testing.T) {
	pairs := [][2]string{{`"a"`, `"\u0061"`}, {`0`, `-0`}, {`"a/b"`, `"a\/b"`}}
	for _, p := range pairs {
		a, ok1 := parseID([]byte(p[0]))
		b, ok2 := parseID([]byte(p[1]))
		if !ok1 || !ok2 || a != b {
			t.Errorf("%s and %s must be one id: %q %q", p[0], p[1], a, b)
		}
	}
}

func TestParseID(t *testing.T) {
	for in, ok := range map[string]bool{
		`1`: true, `-7`: true, `"abc"`: true, `""`: true, `9007199254740992`: true, `9007199254740993`: false, `1.5`: false, `1e3`: false, `null`: false,
		`true`: false, `{}`: false, `[]`: false, `"` + strings.Repeat("x", 128) + `"`: true, `"` + strings.Repeat("x", 129) + `"`: false, ``: false,
	} {
		if _, got := parseID([]byte(in)); got != ok {
			t.Errorf("parseID(%.20s) = %v, want %v", in, got, ok)
		}
	}
	a, _ := parseID([]byte(`1`))
	b, _ := parseID([]byte(`"1"`))
	if a == b {
		t.Error("the number 1 and the string \"1\" are different ids")
	}
}

func testStore(max int) (*planStore, *time.Time) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return newPlanStore(max, time.Minute, func() time.Time { return now }), &now
}

func held(id, digest string) *heldPlan {
	return &heldPlan{id: id, kind: "task", digest: digest, apply: func(context.Context) (*core.ServeApplied, bool, error) { return nil, true, nil }}
}

func codeOf(err error) core.ErrorCode {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.code
	}
	return ""
}

func TestPlanStoreIsSingleUse(t *testing.T) {
	st, _ := testStore(4)
	d := strings.Repeat("a", 64)
	st.add(held("p1", d))
	if _, err := st.take("p1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.take("p1", ""); codeOf(err) != core.CodeAlreadyApplied {
		t.Fatalf("a second take: %v", err)
	}
	if _, err := st.take("nope", ""); codeOf(err) != core.CodeNotFound {
		t.Fatalf("unknown: %v", err)
	}
}

func TestPlanStoreDigestMismatchDoesNotConsume(t *testing.T) {
	st, _ := testStore(4)
	d := strings.Repeat("a", 64)
	st.add(held("p1", d))
	if _, err := st.take("p1", strings.Repeat("b", 64)); codeOf(err) != core.CodeChanged {
		t.Fatalf("mismatch: %v", err)
	}
	if _, err := st.take("p1", d); err != nil {
		t.Fatalf("the plan must survive a wrong digest: %v", err)
	}
}

func TestPlanStoreExpiryAndEviction(t *testing.T) {
	st, now := testStore(2)
	d := strings.Repeat("a", 64)
	st.add(held("p1", d))
	*now = now.Add(time.Minute) // exactly the TTL: expired
	_, err := st.take("p1", "")
	var ae *apiError
	if codeOf(err) != core.CodeExpired || !errors.As(err, &ae) || ae.expiresAt != "2026-01-01T00:01:00Z" {
		t.Fatalf("expired: %v", err)
	}
	st.add(held("p2", d))
	st.add(held("p3", d))
	st.add(held("p4", d)) // pushes p2 out
	if st.size() != 2 {
		t.Fatalf("size %d", st.size())
	}
	if _, err := st.take("p2", ""); codeOf(err) != core.CodeChanged {
		t.Fatalf("evicted: %v", err)
	}
	if _, err := st.take("p4", ""); err != nil {
		t.Fatal(err)
	}
	// a plan expired by a later add is also gone
	*now = now.Add(2 * time.Minute)
	st.add(held("p5", d))
	if st.size() != 1 {
		t.Fatalf("expired plans were kept: %d", st.size())
	}
}

func TestPlanStoreCancelAndUnwritten(t *testing.T) {
	st, _ := testStore(4)
	d := strings.Repeat("a", 64)
	st.add(held("p1", d))
	if err := st.cancel("p1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.take("p1", ""); codeOf(err) != core.CodeNotFound {
		t.Fatalf("cancelled: %v", err)
	}
	st.add(held("p2", d))
	if _, err := st.take("p2", ""); err != nil {
		t.Fatal(err)
	}
	st.unwritten("p2")
	if _, err := st.take("p2", ""); codeOf(err) != core.CodeChanged {
		t.Fatalf("refused before it wrote must not say already applied: %v", err)
	}
}

func TestPlanStoreMemoryIsBounded(t *testing.T) {
	st, _ := testStore(3)
	for i := 0; i < 5000; i++ {
		id := newPlanID()
		st.add(held(id, strings.Repeat("a", 64)))
		if i%2 == 0 {
			_, _ = st.take(id, "")
		}
	}
	if st.size() > 3 || len(st.ended) > maxEnded+maxApplied || len(st.endedOrder) > maxEnded || len(st.appliedOrder) > maxApplied {
		t.Fatalf("live %d ended %d/%d", st.size(), len(st.ended), len(st.endedOrder))
	}
}

func TestPlanStoreConcurrentTakeClaimsOnce(t *testing.T) {
	st, _ := testStore(4)
	st.add(held("p1", strings.Repeat("a", 64)))
	var wg sync.WaitGroup
	var won atomic.Int32
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := st.take("p1", ""); err == nil {
				won.Add(1)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("%d claims", won.Load())
	}
}

func TestPlanIDsAreUnguessable(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := newPlanID()
		if !planIDRE.MatchString(id) || seen[id] {
			t.Fatalf("bad or repeated id %q", id)
		}
		seen[id] = true
	}
}

// A notification never waits and never piles up: one per topic is queued, and a full queue says so.
func TestNotifyNeverBlocksAndCoalesces(t *testing.T) {
	s := New(Config{})
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100000; i++ {
			if !s.notify(core.ServeNotice{Topic: "tasks", Reason: "x"}) {
				t.Error("a first notification was refused")
			}
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("notify blocked")
	}
	if n := len(s.out); n != 1 {
		t.Fatalf("%d queued for one topic", n)
	}
	if !s.notify(core.ServeNotice{Topic: "workspace", Reason: "x"}) || len(s.out) != 2 {
		t.Fatal("a second topic gets its own slot")
	}
	for len(s.out) < cap(s.out) { // fill the queue with responses
		s.out <- outMsg{b: []byte("{}")}
	}
	if s.notify(core.ServeNotice{Topic: "engine", Reason: "x"}) {
		t.Fatal("a notification that did not fit must say so (the watcher retries), not vanish")
	}
	if s.seq["engine"] != 0 {
		t.Fatal("a refused notification used a sequence number")
	}
}

// Changes that arrive while a notification waits are merged into it, so a client that refetches only the
// listed ids misses none; anything unknown, or too many, makes it "full".
func TestNotifyMergesWhatArrivesWhileOneIsQueued(t *testing.T) {
	s := New(Config{})
	ids := func(a ...string) core.ServeNotice {
		return core.ServeNotice{Topic: "tasks", Reason: "x", Kind: "ids", ChangedIDs: a, Fingerprint: "f" + strings.Join(a, "")}
	}
	s.notify(ids("b"))
	s.notify(ids("a", "b"))
	n := *s.pendingN["tasks"]
	if n.Kind != "ids" || !slices.Equal(n.ChangedIDs, []string{"a", "b"}) || n.Fingerprint != "fab" || n.Seq != 1 || len(s.out) != 1 {
		t.Fatalf("merged: %+v (%d queued)", n, len(s.out))
	}
	s.notify(core.ServeNotice{Topic: "tasks", Reason: "x"}) // ids unknown: refetch everything
	if n := *s.pendingN["tasks"]; n.Kind != "full" || n.ChangedIDs != nil || n.Seq != 1 {
		t.Fatalf("full: %+v", n)
	}
	s.notify(ids("c")) // and it stays full
	if n := *s.pendingN["tasks"]; n.Kind != "full" || n.ChangedIDs != nil {
		t.Fatalf("full again: %+v", n)
	}
	// too many ids is "full" too, whether it arrives alone or by merging
	many := func(from, n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("t-%04d", from+i)
		}
		return out
	}
	s.notify(core.ServeNotice{Topic: "workspace", Kind: "ids", ChangedIDs: many(0, maxNoticeIDs+1)})
	if n := *s.pendingN["workspace"]; n.Kind != "full" || n.ChangedIDs != nil {
		t.Fatalf("a single oversized notice: %+v", n)
	}
	s.notify(core.ServeNotice{Topic: "agents", Kind: "ids", ChangedIDs: many(0, maxNoticeIDs)})
	s.notify(core.ServeNotice{Topic: "agents", Kind: "ids", ChangedIDs: many(maxNoticeIDs, 1)})
	if n := *s.pendingN["agents"]; n.Kind != "full" || n.ChangedIDs != nil {
		t.Fatalf("a merge past the bound: %+v", n)
	}
	// what is written is what was merged, and the next change opens a new notification
	b := noticeBytes(core.ServeNotice{Topic: "agents", Seq: 7, Kind: "ids", ChangedIDs: []string{"a"}, Fingerprint: "f"})
	if string(b) != `{"jsonrpc":"2.0","method":"agents.changed","params":{"topic":"agents","seq":7,"kind":"ids","fingerprint":"f","changed_ids":["a"]}}` {
		t.Errorf("%s", b)
	}
}

func TestChangedIDsCollectsNothingPastTheCap(t *testing.T) {
	many := make([]string, maxNoticeIDs+1)
	for i := range many {
		many[i] = fmt.Sprintf("t-%d", i)
	}
	if ids, ok := changedIDs(taskview.Change{Added: many}); ok || ids != nil {
		t.Errorf("past the cap: %d ids, %v", len(ids), ok)
	}
	ids, ok := changedIDs(taskview.Change{Added: []string{"b"}, Changed: []string{"c"}, Removed: []string{"a"}})
	if !ok || !slices.Equal(ids, []string{"a", "b", "c"}) {
		t.Errorf("%v %v", ids, ok)
	}
}
