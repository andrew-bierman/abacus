package cli_test

import (
	"context"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
)

type queuesMod struct {
	*modtest.Fake
	r core.QueuesReport
}

func (m queuesMod) Queues(context.Context) (core.QueuesReport, error) { return m.r, nil }

// The plain Queues report prints every section the typed report has, with "?" for a run whose
// state or phase is not known and a marker for a stalled run or a blocked queued task.
func TestQueuesPrintsRunsQueueAttentionAndHistory(t *testing.T) {
	m := queuesMod{Fake: &modtest.Fake{IDStr: "q", Det: core.Detection{State: core.Ready}}, r: core.QueuesReport{
		Header:    core.Header{Title: "Queues", State: core.Connected, Source: core.SourceLive},
		Runs:      []core.QueueRun{{Issue: "T-1", State: "running", Phase: "build"}, {Issue: "T-2", Stalled: true}},
		Queue:     []core.QueueItem{{ID: "T-3", Title: "ready one"}, {ID: "T-4", Title: "waits", Blocked: true}},
		Attention: []core.Attention{{Issue: "T-5", Reason: "needs-human", Detail: "pick a plan"}},
		History:   []core.HistoryItem{{Issue: "T-6", Outcome: "settled", Title: "done one"}},
	}}
	code, out, _, _ := runIn(t, "", []core.Module{m}, "queues")
	if code != 0 {
		t.Fatalf("code %d\n%s", code, out)
	}
	for _, want := range []string{
		"    run T-1  running  build\n",
		"    run T-2  ?  ?  (stalled)\n",
		"    queued T-3  ready  ready one\n",
		"    queued T-4  blocked  waits\n",
		"    attention T-5  needs-human  pick a plan\n",
		"    history T-6  settled  done one\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}
