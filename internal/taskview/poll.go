package taskview

import "time"

// Poller decides when the live refresh may look at the tracker again. It never reads anything: the
// caller asks Due, starts a check, and reports how it went. It encodes the rules that keep the
// refresh from hammering a shared tracker: one check at a time, none while the tab is hidden, and a
// longer wait after a slow or failed check.
type Poller struct {
	// Base is the wait after a quick, successful check; Max caps the back-off; Slow is how long a
	// check may take before it counts as slow (the wait then doubles).
	Base, Max, Slow time.Duration

	wait     time.Duration
	next     time.Time
	inflight bool
	hidden   bool
}

// NewPoller is a poller that checks every base, backs off to at most max after a check slower than slow.
func NewPoller(base, max, slow time.Duration) *Poller {
	return &Poller{Base: base, Max: max, Slow: slow, wait: base}
}

// Wait is the current wait between checks.
func (p *Poller) Wait() time.Duration { return p.wait }

// Busy is whether a check is in flight.
func (p *Poller) Busy() bool { return p.inflight }

// Hidden is whether the tab is hidden.
func (p *Poller) Hidden() bool { return p.hidden }

// Due is whether a check may start at now: the tab is shown, no check is in flight and the wait is over.
func (p *Poller) Due(now time.Time) bool {
	return !p.hidden && !p.inflight && !now.Before(p.next)
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
// than Slow doubles the wait up to Max; a quick one returns it to Base.
func (p *Poller) Done(now time.Time, took time.Duration, ok bool) {
	p.inflight = false
	if !ok || took > p.Slow {
		p.wait = min(p.wait*2, p.Max)
	} else {
		p.wait = p.Base
	}
	p.next = now.Add(p.wait)
}

// Abort ends a check that will never report (its read was dropped), without changing the wait.
func (p *Poller) Abort() { p.inflight = false }

// SetHidden pauses or resumes the checks. Showing the tab again makes a check due at once, since
// the tracker may have changed while it was hidden.
func (p *Poller) SetHidden(hidden bool) {
	if p.hidden && !hidden {
		p.next = time.Time{}
	}
	p.hidden = hidden
}

// Defer pushes the next check to at least until, without touching the wait (a read is already on
// its way, or the user just asked for one).
func (p *Poller) Defer(until time.Time) {
	if until.After(p.next) {
		p.next = until
	}
}
