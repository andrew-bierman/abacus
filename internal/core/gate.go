package core

import (
	"errors"
	"sync"
	"time"
)

// gate is the one write gate shared by the Tasks Applier and the Setup steps: a plan id can be
// claimed once, the attempt is audited before anything runs (a failed audit blocks it), and the
// outcome is audited after. It does not know what is being written.
type gate struct {
	audit   Audit
	mu      sync.Mutex
	applied map[uint64]bool
}

func newGate(a Audit) *gate { return &gate{audit: a, applied: map[uint64]bool{}} }

// claim marks id used without running anything (ErrAlreadyApplied if it was): a bulk plan is single use
// as a whole, on top of each of its items being single use.
func (g *gate) claim(id uint64) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.applied[id] {
		return ErrAlreadyApplied
	}
	g.applied[id] = true
	return nil
}

// do claims id (ErrAlreadyApplied if it was used), records the attempt e (fail closed), runs
// the write, and records its outcome. An outcome that cannot be known is recorded as "unknown".
func (g *gate) do(id uint64, e AuditEntry, run func() error) error {
	g.mu.Lock()
	if g.applied[id] {
		g.mu.Unlock()
		return ErrAlreadyApplied
	}
	g.applied[id] = true
	g.mu.Unlock()
	e.Time, e.Phase = time.Now().UTC(), "attempt"
	if err := g.audit.Record(e); err != nil {
		return &AuditError{err}
	}
	err := runRecovered(run)
	e.Time, e.Phase = time.Now().UTC(), "ok"
	if err != nil {
		e.Phase, e.Error = "error", Sanitize(err.Error())
		switch {
		case errors.Is(err, ErrOutcomeUnknown):
			e.Phase = "unknown"
		case errors.Is(err, ErrAppliedMismatch):
			e.Phase = "applied-mismatch" // it landed; the result is not the one asked for
		}
	}
	_ = g.audit.Record(e) // the attempt line already exists; a failed result line must not mask the outcome
	return err
}

// runRecovered runs a write; a panic in it (a provider bug) is a result unknown, not a crash and not a
// lost outcome: the write may have reached the tracker before it panicked, and the attempt line is
// already in the audit log, so the outcome must be recorded too.
func runRecovered(run func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = UnknownOutcome("the write failed unexpectedly after it was sent; refresh before retrying")
		}
	}()
	return run()
}
