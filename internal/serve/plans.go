package serve

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// planIDRE is the only shape of plan id the server accepts.
var planIDRE = regexp.MustCompile(`^plan_[0-9a-f]{32}$`)

type ended int

const (
	endedApplied   ended = iota + 1 // claimed by an apply (single use)
	endedUnwritten                  // claimed, then refused before anything was written
	endedExpired                    // too old (past its time to live)
	endedEvicted                    // pushed out by newer plans
	endedCancelled                  // plan.cancel
)

// heldPlan is a plan the server holds for one apply. The client never sends an argv: it names this plan.
type heldPlan struct {
	id, kind, digest string
	expires          time.Time
	// apply runs the write. wrote is false when it was refused before the provider was asked (a stale
	// plan, an audit log that cannot be written): nothing happened.
	apply func(ctx context.Context) (res *core.ServeApplied, wrote bool, err error)
}

// planStore holds at most max live plans and remembers (bounded) how the ended ones ended, so a second
// apply, an expired plan and an unknown id are told apart. It is the single-use guard of the protocol.
type planStore struct {
	mu           sync.Mutex
	live         map[string]*heldPlan
	order        []string
	ended        map[string]endedPlan
	endedOrder   []string
	appliedOrder []string
	max          int
	ttl          time.Duration
	now          func() time.Time
}

// endedPlan is how a plan ended and, for an expired one, when it stopped being appliable.
type endedPlan struct {
	why     ended
	expires time.Time
}

// maxEnded bounds the memory of plans that ended otherwise (expired, cancelled, refused before a write);
// maxApplied bounds the applied ones, which are kept longer so "already applied" stays honest for a long
// session (about 16k small entries). Past either bound an old id is reported not_found.
const (
	maxEnded   = 1024
	maxApplied = 16384
)

func newPlanStore(max int, ttl time.Duration, now func() time.Time) *planStore {
	return &planStore{live: map[string]*heldPlan{}, ended: map[string]endedPlan{}, max: max, ttl: ttl, now: now}
}

func newPlanID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("serve: no random source: " + err.Error()) // a plan id that could be guessed is worse than no server
	}
	return "plan_" + hex.EncodeToString(b[:])
}

// end moves a plan out of the live set. Callers hold mu.
func (p *planStore) end(id string, why ended) {
	var expires time.Time
	if h := p.live[id]; h != nil {
		expires = h.expires
	}
	delete(p.live, id)
	for i, o := range p.order {
		if o == id {
			p.order = append(p.order[:i], p.order[i+1:]...)
			break
		}
	}
	if _, seen := p.ended[id]; !seen {
		if why == endedApplied {
			p.appliedOrder = append(p.appliedOrder, id)
			if len(p.appliedOrder) > maxApplied {
				delete(p.ended, p.appliedOrder[0])
				p.appliedOrder = p.appliedOrder[1:]
			}
		} else {
			p.endedOrder = append(p.endedOrder, id)
			if len(p.endedOrder) > maxEnded {
				delete(p.ended, p.endedOrder[0])
				p.endedOrder = p.endedOrder[1:]
			}
		}
	}
	p.ended[id] = endedPlan{why, expires}
}

// add holds a plan, expiring the old ones and pushing out the oldest when full.
func (p *planStore) add(h *heldPlan) (expires time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	for _, id := range append([]string(nil), p.order...) {
		if !now.Before(p.live[id].expires) {
			p.end(id, endedExpired)
		}
	}
	for len(p.live) >= p.max {
		p.end(p.order[0], endedEvicted)
	}
	h.expires = now.Add(p.ttl)
	p.live[h.id] = h
	p.order = append(p.order, h.id)
	return h.expires
}

// take claims a plan for one apply and removes it, so no second apply can reach it. A digest that is
// not the plan's is refused without consuming the plan (nothing was written: the client had the wrong one).
func (p *planStore) take(id, expect string) (*heldPlan, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	h := p.live[id]
	if h == nil {
		e := p.ended[id]
		switch e.why {
		case endedApplied:
			return nil, &apiError{rpc: rpcServer, code: core.CodeAlreadyApplied, msg: "this plan was already applied (a plan is single use)", planID: id, outcome: "already_applied"}
		case endedUnwritten:
			return nil, &apiError{rpc: rpcServer, code: core.CodeChanged, msg: "this plan was refused when it was applied and nothing was written: plan again", planID: id, outcome: "changed"}
		case endedEvicted:
			return nil, &apiError{rpc: rpcServer, code: core.CodeChanged, msg: "this plan is no longer held (newer plans pushed it out): plan again", planID: id, outcome: "changed"}
		case endedExpired:
			return nil, p.expired(id, "", e.expires)
		}
		return nil, &apiError{rpc: rpcServer, code: core.CodeNotFound, msg: "no such plan", planID: id, outcome: "not_applied"}
	}
	if !p.now().Before(h.expires) {
		p.end(id, endedExpired)
		return nil, p.expired(id, h.digest, h.expires)
	}
	if expect != "" && expect != h.digest {
		return nil, &apiError{rpc: rpcServer, code: core.CodeChanged, msg: fmt.Sprintf("the plan you reviewed (%s) is not the plan held under this id (%s): nothing was written", expect[:12], h.digest[:12]), planID: id, digest: h.digest, outcome: "changed"}
	}
	p.end(id, endedApplied)
	return h, nil
}

// expired is the answer for a plan past its time to live: its own code, so a client can say "this review
// timed out" and plan again, with when it stopped being appliable.
func (p *planStore) expired(id, digest string, at time.Time) *apiError {
	e := &apiError{rpc: rpcServer, code: core.CodeExpired, msg: fmt.Sprintf("this plan expired after %s: nothing was written, plan again", p.ttl), planID: id, digest: digest, outcome: "expired"}
	if !at.IsZero() {
		e.expiresAt = at.UTC().Format(time.RFC3339)
	}
	return e
}

// unwritten records that a claimed plan was refused before anything was written.
func (p *planStore) unwritten(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, o := range p.appliedOrder { // it was claimed as applied; it was not
		if o == id {
			p.appliedOrder = append(p.appliedOrder[:i], p.appliedOrder[i+1:]...)
			break
		}
	}
	delete(p.ended, id)
	p.end(id, endedUnwritten)
}

// cancel discards a plan the client no longer wants.
func (p *planStore) cancel(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.live[id] == nil {
		return &apiError{rpc: rpcServer, code: core.CodeNotFound, msg: "no such plan", planID: id}
	}
	p.end(id, endedCancelled)
	return nil
}

func (p *planStore) size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.live)
}
