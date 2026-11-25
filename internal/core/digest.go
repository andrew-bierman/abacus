package core

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// A plan digest is the identity of a plan: sha256 over its operation, its targets, every exact argv
// it would run and the guards that must still hold when it runs. A front end that showed a plan
// (`--dry-run --json` carries plan_digest) passes it back as --expect-digest when it applies, and the
// apply is refused (code "changed", nothing written) if the plan made now has a different digest. So
// "what the user confirmed" and "what ran" are provably the same plan, across two processes.
//
// The digest is over values, not text: nothing a person reads (the lines, titles, ages) is in it.
// The encoding is length-prefixed, so no two different plans share bytes.

// digestVersion versions the encoding; a change to what is hashed changes it.
const digestVersion = "abacus-plan-digest-v1"

var digestRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ParseDigest normalizes a digest a caller passed (case, surrounding space) and says whether it is one.
func ParseDigest(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	return s, digestRE.MatchString(s)
}

type digestWriter struct{ h hash.Hash }

func (w digestWriter) str(s string) {
	var b [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(b[:], uint64(len(s)))
	w.h.Write(b[:n])
	_, _ = io.WriteString(w.h, s)
}

func (w digestWriter) list(l []string) {
	w.str(strconv.Itoa(len(l)))
	for _, s := range l {
		w.str(s)
	}
}

// PlanDigest hashes a plan: the domain and operation, the targets it names, each argv it runs in
// order, and its guards (the state it was planned against, as name -> value, in sorted order).
func PlanDigest(domain, op string, targets []string, argv [][]string, guards map[string]string) string {
	w := digestWriter{sha256.New()}
	w.str(digestVersion)
	w.str(domain)
	w.str(op)
	w.list(targets)
	w.str(strconv.Itoa(len(argv)))
	for _, a := range argv {
		w.list(a)
	}
	keys := make([]string, 0, len(guards))
	for k := range guards {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	w.str(strconv.Itoa(len(keys)))
	for _, k := range keys {
		w.str(k)
		w.str(guards[k])
	}
	return hex.EncodeToString(w.h.Sum(nil))
}

// guards are the state a task change was planned against and that is checked again before it runs
// (bd's --if-status and --if-assignee, the status a close or edit expects, the priority a re-prioritize saw).
func (ch TaskChange) guards(prefix string) map[string]string {
	g := map[string]string{}
	if ch.IfStatus != "" {
		g[prefix+"if_status"] = ch.IfStatus
	}
	if ch.Expect != "" {
		g[prefix+"expect_status"] = ch.Expect
	}
	if ch.Op == TaskAssign {
		g[prefix+"if_assignee"] = ch.IfAssignee
	}
	if ch.Op == TaskPriority && ch.Before.ID != "" {
		g[prefix+"before_priority"] = strconv.Itoa(ch.Before.Priority)
	}
	return g
}

func (ch TaskChange) targets() []string {
	var t []string
	if ch.ID != "" {
		t = append(t, ch.ID)
	}
	if ch.Blocker != "" {
		t = append(t, ch.Blocker)
	}
	return t
}

// Digest is the plan digest of one task change.
func (p WritePlan) Digest() string {
	return PlanDigest(string(DomainTasks), string(p.Change.Op), p.Change.targets(), [][]string{p.Argv}, p.Change.guards(""))
}

// Digest is the plan digest of a batch: every task, in order, with its own argv and guards, and the
// tasks left out (a task that changed since the plan moves into or out of the batch, so it changes).
func (p BulkPlan) Digest() string {
	var targets []string
	var argv [][]string
	guards := map[string]string{}
	for _, it := range p.Items {
		targets = append(targets, it.Change.ID)
		argv = append(argv, it.Argv)
		for k, v := range it.Change.guards(it.Change.ID + ".") {
			guards[k] = v
		}
	}
	for _, s := range p.Skipped {
		guards["skipped."+s.ID] = ""
	}
	return PlanDigest(string(DomainTasks), "bulk:"+string(p.Spec.Op), targets, argv, guards)
}

// Digest is the plan digest of a board post or reply: where it goes, the exact argv (which carries
// the idempotency key, so a retry of the same draft has the same digest) and the key.
func (p BoardPlan) Digest() string {
	dest := p.Change.Channel
	if p.Change.Op == BoardReply {
		dest = p.Change.ID
	}
	return PlanDigest(string(DomainBoard), string(p.Change.Op), []string{dest}, [][]string{p.Argv}, map[string]string{"idempotency_key": p.Key})
}

// Digest is the plan digest of one setup step. The folder a repair sets aside to is named from the
// clock, so it is shown to a person but is not part of the digest.
func (p SetupPlan) Digest() string {
	argv := [][]string{p.Argv}
	if p.wp.Argv != nil {
		argv = append(argv, p.wp.Argv)
	}
	return PlanDigest("setup", string(p.Step), []string{p.dir}, argv, nil)
}

// SetupStepPlan is one step of `setup` as it was reviewed: its state and message (for a blocked step, the
// reason), and the exact argv its plan would run (nil when it has no plan: done, blocked or unavailable).
type SetupStepPlan struct {
	Step    StepID
	State   StepState
	Message string
	Argv    []string
}

// SetupStepPlans reads every step and plans the ones that can be planned now. Planning runs nothing.
func (s *Setup) SetupStepPlans(ctx context.Context) []SetupStepPlan {
	var out []SetupStepPlan
	for _, st := range s.Steps(ctx) {
		sp := SetupStepPlan{Step: st.Step, State: st.State, Message: st.Message}
		if st.State == StepNeeded || st.State == StepRepairing {
			if p, err := s.Plan(st.Step); err == nil {
				sp.Argv = p.Argv
			}
		}
		out = append(out, sp)
	}
	return out
}

// SetupDigest is the digest of everything `setup` reports and would run: every step in order (blocked ones
// with their reason), its state, and the argv its plan runs.
func SetupDigest(steps []SetupStepPlan) string {
	var targets []string
	var argv [][]string
	guards := map[string]string{}
	for _, st := range steps {
		targets = append(targets, string(st.Step))
		argv = append(argv, append([]string{string(st.Step)}, st.Argv...))
		guards[string(st.Step)+".state"] = string(st.State)
		guards[string(st.Step)+".message"] = st.Message
	}
	return PlanDigest("setup", "setup", targets, argv, guards)
}

// DemoDigest is the plan digest a client sees in demo mode: never equal to the digest of the same plan
// made against a real tracker, so a demo review cannot stand in for a real one (--expect-digest).
func DemoDigest(digest string) string {
	sum := sha256.Sum256([]byte("abacus-demo-plan\x00" + digest))
	return hex.EncodeToString(sum[:])
}
