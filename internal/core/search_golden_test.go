package core_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

var updateSearchGolden = flag.Bool("update-search-golden", false, "rewrite internal/core/testdata/search-*.golden.json")

// The report JSON with the keys the CLI goldens cannot reach: a stale domain (stale, age on a
// hit), a reduced one (reason), a degraded one and a truncated group.
func TestSearchReportJSONContractStaleAndReduced(t *testing.T) {
	feeds := searchFixtures()
	feeds[2].ReadAt, feeds[2].PollEvery = searchNow.Add(-5*time.Minute), 30*time.Second
	feeds[1].Reason = core.SearchReasonBoardCapped
	feeds[3] = core.SearchFeed{Domain: core.DomainQueues, State: core.SearchDegraded, Reason: "the read failed"}
	r, err := core.NewSearchIndex(feeds...).Search("e", core.SearchOptions{Now: searchNow, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.MarshalIndent(r, "", "  ")
	got := string(b) + "\n"
	path := filepath.Join("testdata", "search-stale.golden.json")
	if *updateSearchGolden {
		_ = os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil || string(want) != got {
		t.Errorf("%s differs (-update-search-golden accepts): %v\n%s", path, err, got)
	}
	for _, key := range []string{`"stale": true`, `"age": "5m"`, `"truncated"`, `"reason": "only recent threads`, `"state": "degraded"`, `"schema_version": 1`} {
		if !strings.Contains(got, key) {
			t.Errorf("golden lacks %s", key)
		}
	}
}
