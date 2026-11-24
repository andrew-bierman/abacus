package lambe

import (
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

// The CLI and JSON read the same normalized state as the TUI: padding and case never change the tone.
func TestRunStatesAreNormalizedForTheLineAndTheTypedReport(t *testing.T) {
	r := Run{Issue: "A-1", State: " running "}
	if got := r.Line(); got != "A-1  running  ?" {
		t.Fatalf("%q", got)
	}
	v := View{Snapshot: Snapshot{Runs: []Run{{Issue: "A-1", State: " running "}, {Issue: "A-2", State: "Failed"}}}}
	q := QueuesReportOf(v, false)
	if q.Runs[0].State != core.RunRunning || q.Runs[0].State.Tone() != core.ToneOK || q.Runs[1].State != core.RunFailed || q.Runs[1].State.Tone() != core.ToneFail {
		t.Fatalf("%+v", q.Runs)
	}
}
