package lambe

import (
	"context"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

func TestDemoQueuesIsASnapshotWithRunsQueueAttentionAndHistory(t *testing.T) {
	var p core.QueueProvider = NewDemo()
	r, err := p.Queues(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != core.SourceSnapshot || len(r.Runs) != 3 || len(r.Queue) != 2 || len(r.Attention) != 1 || len(r.History) == 0 {
		t.Fatalf("%+v", r)
	}
	if !r.Queue[1].Blocked || !r.Runs[2].Stalled {
		t.Fatalf("flags lost: %+v", r)
	}
}

func TestTrackerViewIsLabelledAsTheNeutralTrackerSource(t *testing.T) {
	if SourceTracker != core.SourceTracker {
		t.Fatalf("lambe %q != core %q", SourceTracker, core.SourceTracker)
	}
	r := queuesReport(View{Source: SourceTracker}, false)
	if r.Source != core.SourceTracker || len(r.Queue) != 0 {
		t.Fatalf("%+v", r)
	}
}
