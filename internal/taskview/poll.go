package taskview

import "time"

// Poller decides when the live refresh may look at the tracker again. It never reads anything: the
// caller asks Due, starts a check, and reports how it went. It encodes the rules that keep the
// refresh from hammering a shared tracker: one check at a time, a slow rhythm while the tab is hidden,
// and a longer wait after a slow or failed check.
type Poller struct {
	// Base is the wait after quick, successful checks; Max caps the back-off; Slow is how long a
	// check may take before it counts as slow (the wait then doubles). HiddenWait is the least wait
	// between checks while the tab is hidden: a hidden tab still looks, just rarely.
	Base, Max, Slow, HiddenWait time.Duration

	wait     time.Duration
	quick    int // consecutive quick, successful checks since the last slow or failed one
	next     time.Time
	inflight bool
	hidden   bool
}

// NewPoller is a poller that checks every base, backs off to at most max after a check slower than slow.
func NewPoller(base, max, slow time.Duration) *Poller {
	return &Poller{Base: base, Max: max, Slow: slow, HiddenWait: 30 * time.Second, wait: base}
}

// Wait is the current wait between checks.
func (p *Poller) Wait() time.Duration { return p.wait }

// Busy is whether a check is in flight.
func (p *Poller) Busy() bool { return p.inflight }

// Hidden is whether the tab is hidden.
func (p *Poller) Hidden() bool { return p.hidden }

// quickToRecover is how many quick checks in a row return a backed-off wait to Base.
const quickToRecover = 3

// Due is whether a check may start at now: no check is in flight and the wait is over (a hidden tab
// waits at least HiddenWait between checks).
func (p *Poller) Due(now time.Time) bool {
	return !p.inflight && !now.Before(p.next)
}

// Start records that a check began. It reports false (and records nothing) when one may not start
// now, so a caller that skips Due still cannot overlap two checks.
func (p *Poller) Start(now time.Time) bool {
	if !p.Due(now) {
		return false
	}
	p.inflight = true
	return true
}

// Done records the end of a check that began at start and ended at now. A failed check or one slower
// than Slow doubles the wait (never below Base, up to Max); quickToRecover quick ones in a row return
// it to Base. The next check is a wait away, and at least HiddenWait away while the tab is hidden.
func (p *Poller) Done(now time.Time, took time.Duration, ok bool) {
	p.inflight = false
	p.wait = max(p.wait, p.Base)
	if !ok || took > p.Slow {
		p.quick = 0
		p.wait = max(p.Base, min(p.wait*2, p.Max))
	} else if p.quick++; p.quick >= quickToRecover {
		p.wait = p.Base
	}
	next := p.wait
	if p.hidden {
		next = max(next, p.HiddenWait)
	}
	p.next = now.Add(next)
}

// Abort ends a check that will never report (its read was dropped), without changing the wait.
func (p *Poller) Abort() { p.inflight = false }

// SetHidden marks the tab hidden or shown. Showing it again makes a check due at once, since the
// tracker may have changed while it was hidden.
func (p *Poller) SetHidden(hidden bool) {
	if p.hidden && !hidden {
		p.next = time.Time{}
	}
	p.hidden = hidden
}

// Expedite makes a check due at once (a person asked for one), without touching the wait.
func (p *Poller) Expedite() { p.next = time.Time{} }

// Defer pushes the next check to at least until, without touching the wait (a read is already on
// its way, or the user just asked for one).
func (p *Poller) Defer(until time.Time) {
	if until.After(p.next) {
		p.next = until
	}
}
