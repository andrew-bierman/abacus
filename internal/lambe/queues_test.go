package lambe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestDemoAndTrackerSubsetCarryAReasonSoTheyAreNeverLiveLooking(t *testing.T) {
	d := queuesReport(View{}, true)
	if d.Source != core.SourceSnapshot || d.Reason == "" || !strings.Contains(d.Reason, "demo") {
		t.Fatalf("demo %+v", d.Header)
	}
	tr := queuesReport(View{Source: SourceTracker}, false)
	if tr.Reason == "" || !strings.Contains(tr.Reason, "no live daemon") {
		t.Fatalf("tracker %+v", tr.Header)
	}
	live := queuesReport(View{}, false)
	if live.Source != core.SourceLive || live.Reason != "" {
		t.Fatalf("live %+v", live.Header)
	}
	// Through the registry a demo or a subset is degraded, never connected.
	m := NewDemo()
	if got := core.ReadDomains(context.Background(), []core.Entry{{Module: m, Detection: m.Detect(context.Background())}}).Queues; got.State != core.Degraded {
		t.Fatalf("demo domain state = %s", got.State)
	}
}

func TestAFailedHistoryFetchIsReportedNotSwallowed(t *testing.T) {
	r := queuesReport(View{HistoryErr: "state API GET /api/v1/history failed"}, false)
	if !strings.Contains(r.Reason, "history unavailable: state API GET /api/v1/history failed") {
		t.Fatalf("%+v", r.Header)
	}
	if !strings.Contains(queuesReport(View{HistoryErr: "x", Source: SourceTracker}, false).Reason, "history unavailable") {
		t.Fatal("reasons must combine")
	}
}

func TestStatusSummaryForTheTrackerSubsetDropsTheQueuedCount(t *testing.T) {
	r := queuesReport(View{Source: SourceTracker}, false)
	if s := r.Summarize(); strings.Contains(s, "queued") || !strings.Contains(s, "tracker subset") {
		t.Fatal(s)
	}
}

func TestLiveReadRecordsASanitizedHistoryFailure(t *testing.T) {
	mux := http.NewServeMux()
	ok := func(file string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(fixture(t, file)) }
	}
	mux.HandleFunc("/api/v1/state", ok("snapshot.json"))
	mux.HandleFunc("/api/v1/queue", ok("queue.json"))
	mux.HandleFunc("/api/v1/history", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"schema_version":1,"error":"boom","message":"disk\u001b[2J gone"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	dir := t.TempDir()
	d := baseDesc("lambe-live", "")
	d.Addr = strings.TrimPrefix(srv.URL, "http://")
	d.BearerToken = secret
	writeDescriptor(t, dir, "stateapi-live.json", d)
	m := New(Options{StateDir: dir, PidAlive: func(int) bool { return true }, Identity: func(int) (string, error) { return "id-ok", nil }})
	r, err := m.Queues(context.Background())
	if err != nil || len(r.Runs) != 3 || !strings.Contains(r.Reason, "history unavailable") || strings.ContainsRune(r.Reason, 0x1b) {
		t.Fatalf("%+v %v", r.Header, err)
	}
}
