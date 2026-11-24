package core_test

import (
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// Every part of the plan is in the digest, and the encoding cannot be made to collide by moving
// text between fields.
func TestPlanDigestCoversEveryPartOfAPlan(t *testing.T) {
	base := func() string {
		return core.PlanDigest("tasks", "close", []string{"t-1"}, [][]string{{"bd", "close", "t-1"}}, map[string]string{"if_status": "open"})
	}
	if base() != base() || len(base()) != 64 {
		t.Fatal("a digest is deterministic sha256 hex")
	}
	for name, got := range map[string]string{
		"domain":        core.PlanDigest("board", "close", []string{"t-1"}, [][]string{{"bd", "close", "t-1"}}, map[string]string{"if_status": "open"}),
		"op":            core.PlanDigest("tasks", "reopen", []string{"t-1"}, [][]string{{"bd", "close", "t-1"}}, map[string]string{"if_status": "open"}),
		"target":        core.PlanDigest("tasks", "close", []string{"t-2"}, [][]string{{"bd", "close", "t-1"}}, map[string]string{"if_status": "open"}),
		"extra target":  core.PlanDigest("tasks", "close", []string{"t-1", "t-2"}, [][]string{{"bd", "close", "t-1"}}, map[string]string{"if_status": "open"}),
		"argv element":  core.PlanDigest("tasks", "close", []string{"t-1"}, [][]string{{"bd", "close", "t-1", "--reason=x"}}, map[string]string{"if_status": "open"}),
		"argv changed":  core.PlanDigest("tasks", "close", []string{"t-1"}, [][]string{{"bd", "close", "t-2"}}, map[string]string{"if_status": "open"}),
		"argv joined":   core.PlanDigest("tasks", "close", []string{"t-1"}, [][]string{{"bd", "close t-1"}}, map[string]string{"if_status": "open"}),
		"extra step":    core.PlanDigest("tasks", "close", []string{"t-1"}, [][]string{{"bd", "close", "t-1"}, {"x"}}, map[string]string{"if_status": "open"}),
		"guard value":   core.PlanDigest("tasks", "close", []string{"t-1"}, [][]string{{"bd", "close", "t-1"}}, map[string]string{"if_status": "blocked"}),
		"guard key":     core.PlanDigest("tasks", "close", []string{"t-1"}, [][]string{{"bd", "close", "t-1"}}, map[string]string{"expect": "open"}),
		"guard removed": core.PlanDigest("tasks", "close", []string{"t-1"}, [][]string{{"bd", "close", "t-1"}}, nil),
	} {
		if got == base() {
			t.Errorf("a change to the %s does not change the digest", name)
		}
	}
	// a split moved across the argv boundary is a different plan
	a := core.PlanDigest("d", "o", nil, [][]string{{"a", "b"}, {"c"}}, nil)
	b := core.PlanDigest("d", "o", nil, [][]string{{"a"}, {"b", "c"}}, nil)
	if a == b {
		t.Error("argv boundaries are part of the digest")
	}
	// map order is not
	g1 := core.PlanDigest("d", "o", nil, nil, map[string]string{"a": "1", "b": "2", "c": "3"})
	for i := 0; i < 20; i++ {
		if core.PlanDigest("d", "o", nil, nil, map[string]string{"c": "3", "b": "2", "a": "1"}) != g1 {
			t.Fatal("guard order must not matter")
		}
	}
}

func TestWritePlanDigestFollowsArgvTargetsAndGuardsNotDisplayText(t *testing.T) {
	w := &fakeWriter{}
	ch := core.TaskChange{Op: core.TaskUpdate, ID: "t-1", Title: "a", Status: "blocked", IfStatus: "open", Before: core.Task{ID: "t-1", Title: "old", Status: "open"}}
	d0 := plan(t, w, ch).Digest()
	if plan(t, w, ch).Digest() != d0 {
		t.Fatal("two plans of the same change share a digest (the plan id is not in it)")
	}
	same := ch
	same.Before.Title, same.Warning = "renamed meanwhile", "a warning"
	if plan(t, w, same).Digest() != d0 {
		t.Error("display-only text is not part of the digest")
	}
	for name, mod := range map[string]func(*core.TaskChange){
		"argv (title)": func(c *core.TaskChange) { c.Title = "b" },
		"guard":        func(c *core.TaskChange) { c.IfStatus = "in_progress" },
		"target":       func(c *core.TaskChange) { c.ID = "t-2" },
		"op":           func(c *core.TaskChange) { c.Op = core.TaskClose; c.Status = ""; c.IfStatus = "" },
	} {
		c := ch
		mod(&c)
		if plan(t, w, c).Digest() == d0 {
			t.Errorf("%s does not change the digest", name)
		}
	}
	// the expected status and the assignee guard are guards too
	e1 := plan(t, w, core.TaskChange{Op: core.TaskClose, ID: "t-1", Expect: "open"}).Digest()
	if e1 == plan(t, w, core.TaskChange{Op: core.TaskClose, ID: "t-1", Expect: "blocked"}).Digest() {
		t.Error("the expected status is a guard")
	}
	empty, ada := "", "ada"
	a1 := plan(t, w, core.TaskChange{Op: core.TaskAssign, ID: "t-1", Assignee: &ada, IfAssignee: ""}).Digest()
	if a1 == plan(t, w, core.TaskChange{Op: core.TaskAssign, ID: "t-1", Assignee: &ada, IfAssignee: "bob"}).Digest() {
		t.Error("the assignee guard is a guard")
	}
	_ = empty
	p3, p4 := 3, 3
	if plan(t, w, core.TaskChange{Op: core.TaskPriority, ID: "t-1", Priority: &p3, Before: core.Task{ID: "t-1", Priority: 1}}).Digest() ==
		plan(t, w, core.TaskChange{Op: core.TaskPriority, ID: "t-1", Priority: &p4, Before: core.Task{ID: "t-1", Priority: 2}}).Digest() {
		t.Error("the priority the task had is a guard")
	}
}

func TestBulkAndBoardAndSetupDigests(t *testing.T) {
	w := &fakeWriter{}
	spec := core.BulkSpec{Op: core.TaskClose}
	tasks := func(status string) []core.Task {
		return []core.Task{{ID: "t-1", Title: "one", Status: status}, {ID: "t-2", Title: "two", Status: "open"}}
	}
	bp, err := core.PlanBulk(w, spec, tasks("open"))
	if err != nil {
		t.Fatal(err)
	}
	d0 := bp.Digest()
	bp2, _ := core.PlanBulk(w, spec, tasks("open"))
	if bp2.Digest() != d0 {
		t.Error("deterministic")
	}
	if p, _ := core.PlanBulk(w, spec, tasks("blocked")); p.Digest() == d0 {
		t.Error("a target that moved changes the digest (its expected status is a guard)")
	}
	if p, _ := core.PlanBulk(w, spec, []core.Task{{ID: "t-2", Title: "two", Status: "open"}}); p.Digest() == d0 {
		t.Error("the targets are in the digest")
	}
	if p, _ := core.PlanBulk(w, spec, []core.Task{{ID: "t-1", Title: "one", Status: "closed"}, {ID: "t-2", Title: "two", Status: "open"}}); p.Digest() == d0 {
		t.Error("a task that dropped out of the batch changes the digest")
	}

	bw := &fakeBoardWriter{}
	post := func(text string) core.BoardPlan {
		p, err := core.PlanBoardChange(bw, core.BoardChange{Op: core.BoardPost, Channel: "dev", Text: text, Nonce: "n1"})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if post("hi").Digest() != post("hi").Digest() || post("hi").Digest() == post("hello").Digest() {
		t.Error("a board digest follows the text (through the argv and the key)")
	}
	reply, _ := core.PlanBoardChange(bw, core.BoardChange{Op: core.BoardReply, ID: "15", Text: "hi", Nonce: "n1"})
	if reply.Digest() == post("hi").Digest() {
		t.Error("op and destination are in the digest")
	}

	steps := []core.StepInfo{{Step: core.StepActor, State: "needed", Command: "abacus config set actor me"}, {Step: core.StepQueues, State: "done"}}
	s0 := core.SetupDigest(steps)
	if core.SetupDigest(steps) != s0 || core.SetupDigest([]core.StepInfo{{Step: core.StepActor, State: "needed", Command: "abacus config set actor you"}}) == s0 {
		t.Error("the setup digest follows the commands of the steps still to do")
	}
	if core.SetupDigest([]core.StepInfo{{Step: core.StepActor, State: "done"}}) == s0 {
		t.Error("a step that is done is no longer part of the plan")
	}
}

func TestParseDigest(t *testing.T) {
	d := core.PlanDigest("a", "b", nil, nil, nil)
	for in, ok := range map[string]bool{d: true, " " + strings.ToUpper(d) + "\n": true, d[:63]: false, d + "0": false, "": false, strings.Repeat("z", 64): false} {
		got, valid := core.ParseDigest(in)
		if valid != ok || (ok && got != d) {
			t.Errorf("%q: %q %v", in, got, valid)
		}
	}
}

func TestFileAuditReadsBackAndBoardCLINonceUsesIt(t *testing.T) {
	a := core.FileAudit(t.TempDir() + "/audit.jsonl")
	r := a.(core.AuditReader)
	if es, err := r.Entries(); err != nil || len(es) != 0 {
		t.Fatalf("a missing log is empty: %v %v", es, err)
	}
	ch := core.BoardChange{Op: core.BoardPost, Channel: "dev", Text: "hi"}
	n0, rep, err := core.BoardCLINonce(a, (&fakeBoardWriter{}).Target(), ch, 10*time.Minute, time.Now())
	if err != nil || rep || n0 != "cli-0" {
		t.Fatalf("%s %v %v", n0, rep, err)
	}
	ch.Nonce = n0
	p, _ := core.PlanBoardChange(&fakeBoardWriter{}, ch)
	_ = a.Record(core.AuditEntry{Time: time.Now(), Domain: "board", Verb: "post", Phase: "attempt", Argv: p.Argv})
	n1, rep, _ := core.BoardCLINonce(a, (&fakeBoardWriter{}).Target(), core.BoardChange{Op: core.BoardPost, Channel: "dev", Text: "hi"}, 10*time.Minute, time.Now())
	if n1 != "cli-1" || !rep {
		t.Errorf("a recorded key is a repeat and the next key is fresh: %s %v", n1, rep)
	}
}
