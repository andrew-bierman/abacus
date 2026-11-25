package core_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

func TestConfigSetPlanValidatesDigestsAndAppliesOnce(t *testing.T) {
	st := core.ConfigStore{Path: filepath.Join(t.TempDir(), "c", "config.json")}
	for _, bad := range [][2]string{{"nosuch", "x"}, {"actor", "has space"}, {"tracker_dir", "relative"}, {"search_history", "maybe"}} {
		if _, err := core.PlanConfigSet(st, bad[0], bad[1]); err == nil {
			t.Errorf("%v was planned", bad)
		}
	}
	p, err := core.PlanConfigSet(st, "actor", "mei")
	if err != nil {
		t.Fatal(err)
	}
	if q, _ := core.PlanConfigSet(st, "actor", "mei"); q.Digest() != p.Digest() {
		t.Error("the same change has two digests")
	}
	if q, _ := core.PlanConfigSet(st, "actor", "kai"); q.Digest() == p.Digest() {
		t.Error("two values share a digest")
	}
	if c, _ := st.Load(); c.Actor != "" {
		t.Fatal("planning wrote")
	}
	rec := &recAudit{}
	if _, wrote, err := core.ApplyConfigSet(context.Background(), st, rec, p); !errors.Is(err, core.ErrNotConfirmed) || wrote || len(rec.entries) != 0 {
		t.Fatalf("an unconfirmed plan ran: %v %v", err, wrote)
	}
	if _, wrote, err := core.ApplyConfigSet(context.Background(), st, rec, p.Confirm()); err != nil || !wrote {
		t.Fatalf("%v %v", err, wrote)
	}
	if c, _ := st.Load(); c.Actor != "mei" || len(rec.entries) != 2 || rec.entries[0].Phase != "attempt" || rec.entries[1].Phase != "ok" {
		t.Errorf("%+v %+v", c, rec.entries)
	}
	// the key moved since the plan: changed, nothing audited, nothing written
	rec = &recAudit{}
	if _, wrote, err := core.ApplyConfigSet(context.Background(), st, rec, p.Confirm()); !errors.Is(err, core.ErrChanged) || wrote || len(rec.entries) != 0 {
		t.Errorf("a stale plan: %v %v %v", err, wrote, rec.entries)
	}
	// a cancelled request does not start the write
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	q, _ := core.PlanConfigSet(st, "actor", "zed")
	if _, wrote, err := core.ApplyConfigSet(ctx, st, rec, q.Confirm()); err == nil || wrote {
		t.Errorf("a cancelled apply: %v %v", err, wrote)
	}
	// an audit log that cannot be written blocks the write
	if _, wrote, err := core.ApplyConfigSet(context.Background(), st, failAudit{}, q.Confirm()); wrote || core.CodeFor(err) != core.CodeAuditFailed {
		t.Errorf("fail closed: %v %v", err, wrote)
	}
	if c, _ := st.Load(); c.Actor != "mei" {
		t.Errorf("the config changed past a failed audit: %+v", c)
	}
}

func TestNewErrorCodesAreRetryableAndDemoDigestsDiffer(t *testing.T) {
	for _, c := range []core.ErrorCode{core.CodeExpired, core.CodeBusy} {
		if !core.NewErrorDoc(c, "x").Error.Retryable {
			t.Errorf("%s is not retryable", c)
		}
	}
	if d := core.PlanDigest("a", "b", nil, nil, nil); core.DemoDigest(d) == d || core.DemoDigest(d) != core.DemoDigest(d) {
		t.Error("a demo digest must differ from the real one and be stable")
	}
}

type hookAudit struct {
	entries []core.AuditEntry
	onAtt   func()
}

func (h *hookAudit) Record(e core.AuditEntry) error {
	h.entries = append(h.entries, e)
	if e.Phase == "attempt" && h.onAtt != nil {
		h.onAtt()
	}
	return nil
}

// A change made after the check but before the write (here, while the attempt is being recorded) is not
// overwritten: the comparison is made again under the file's lock.
func TestConfigSetIsAtomicAgainstAConcurrentChange(t *testing.T) {
	st := core.ConfigStore{Path: filepath.Join(t.TempDir(), "c", "config.json")}
	p, _ := core.PlanConfigSet(st, "actor", "mei")
	au := &hookAudit{onAtt: func() {
		_ = st.Update(func(c *core.Config) error { return c.Set("actor", "other") })
	}}
	_, _, err := core.ApplyConfigSet(context.Background(), st, au, p.Confirm())
	if !errors.Is(err, core.ErrChanged) {
		t.Fatalf("err %v", err)
	}
	if c, _ := st.Load(); c.Actor != "other" {
		t.Errorf("the concurrent change was overwritten: %+v", c)
	}
	if last := au.entries[len(au.entries)-1]; last.Phase != "error" {
		t.Errorf("outcome %+v", last)
	}
}

func TestConfigSetAuditRecordsTheKeyAndABoundedValueForTraceableKeys(t *testing.T) {
	st := core.ConfigStore{Path: filepath.Join(t.TempDir(), "c", "config.json")}
	for _, tc := range []struct{ key, val, want string }{
		{"actor", "mei", "mei"},
		{"board_url", "https://board.example/x", "https://board.example/x"},
		{"search_history", "off", ""},
	} {
		p, err := core.PlanConfigSet(st, tc.key, tc.val)
		if err != nil {
			t.Fatal(err)
		}
		au := &hookAudit{}
		if _, _, err := core.ApplyConfigSet(context.Background(), st, au, p.Confirm()); err != nil {
			t.Fatal(err)
		}
		argv := au.entries[0].Argv
		if argv[0] != tc.key || (tc.want == "" && len(argv) != 1) || (tc.want != "" && (len(argv) != 2 || argv[1] != tc.want)) {
			t.Errorf("%s: audit argv %v", tc.key, argv)
		}
	}
	long := "/" + strings.Repeat("d", 300)
	p, _ := core.PlanConfigSet(st, "tracker_dir", long)
	au := &hookAudit{}
	if _, _, err := core.ApplyConfigSet(context.Background(), st, au, p.Confirm()); err != nil {
		t.Fatal(err)
	}
	if v := au.entries[0].Argv[1]; len(v) > 130 {
		t.Errorf("an unbounded value in the audit: %d bytes", len(v))
	}
}
