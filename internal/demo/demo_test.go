package demo

import (
	"context"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

// The demo providers plug into the domains they stand in for.
var (
	_ core.TaskProvider     = Tasks{}
	_ core.InsightsProvider = Tasks{}
	_ core.TaskWriter       = Tasks{}
	_ core.TaskReader       = Tasks{}
	_ core.LeaseProvider    = Leases{}
	_ core.BoardProvider    = Board{}
	_ core.Module           = Tasks{}
)

func TestEveryDemoProviderIsReadyAndSaysItIsDemoData(t *testing.T) {
	for _, m := range []interface {
		core.Module
		IsDemo() bool
	}{NewTasks(), NewLeases(), NewBoard()} {
		if d := m.Detect(context.Background()); d.State != core.Ready {
			t.Errorf("%s: detection %v", m.ID(), d.State)
		}
		if !m.IsDemo() {
			t.Errorf("%s must report itself as demo data", m.ID())
		}
		if cs := m.Doctor(context.Background()); len(cs) == 0 || cs[0].Status != core.StatusOK {
			t.Errorf("%s: doctor %v", m.ID(), cs)
		}
	}
}

// A demo write changes nothing and says so: there is no path from here to a tracker.
func TestDemoWritesApplyToNothing(t *testing.T) {
	w := NewTasks()
	argv, err := w.BuildTaskArgv(core.TaskChange{Op: core.TaskCreate, Title: "ship it"})
	if err != nil || strings.Join(argv, " ") != "bd create --title=ship it" {
		t.Fatalf("argv = %v, %v", argv, err)
	}
	res, err := w.RunTask(context.Background(), argv)
	if err != nil || res.ID != "demo-1" || !strings.Contains(res.Title, "nothing was written") {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if w.TrackerDir() != "" {
		t.Fatal("a demo has no tracker directory")
	}
}

func TestDemoReadTaskFindsFixturesAndRejectsOthers(t *testing.T) {
	if got, err := NewTasks().ReadTask(context.Background(), "ac-12"); err != nil || got.Title != "wire the lease heartbeat" {
		t.Fatalf("ReadTask(ac-12) = %+v, %v", got, err)
	}
	if _, err := NewTasks().ReadTask(context.Background(), "nope"); err == nil {
		t.Fatal("an unknown id must be an error")
	}
}

func TestDemoReportsCoverEveryStateTheTabsDraw(t *testing.T) {
	states := map[core.LeaseState]bool{}
	for _, l := range LeasesReport().Leases {
		states[l.State] = true
	}
	if !states[core.LeaseHeld] || !states[core.LeaseStale] || !states[core.LeaseFree] {
		t.Fatalf("lease fixtures must show held, stale and free: %v", states)
	}
	if b := BoardReport(); len(b.Channels) == 0 || len(b.Threads) == 0 || len(b.Tasks) == 0 {
		t.Fatalf("board fixtures must fill every section: %+v", b)
	}
	if i := InsightsReport(); len(i.TopPicks) == 0 || len(i.CriticalPath) == 0 || len(i.Cycles) == 0 {
		t.Fatalf("insight fixtures must fill every section: %+v", i)
	}
}
