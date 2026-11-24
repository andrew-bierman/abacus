package taskview

import (
	"testing"
	"time"
)

func TestExpediteMakesACheckDueAtOnceWithoutChangingTheWait(t *testing.T) {
	p := NewPoller(5*time.Second, time.Minute, 2*time.Second)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	p.Start(now)
	p.Done(now, 3*time.Second, true) // slow: the wait doubles and the next check is 10 s away
	if p.Due(now.Add(time.Second)) {
		t.Fatal("not due yet")
	}
	wait := p.Wait()
	p.Expedite()
	if !p.Due(now.Add(time.Second)) || p.Wait() != wait {
		t.Fatalf("due=%v wait=%v want %v", p.Due(now.Add(time.Second)), p.Wait(), wait)
	}
	if !p.Start(now.Add(time.Second)) || p.Start(now.Add(time.Second)) {
		t.Fatal("one check at a time, still")
	}
}
