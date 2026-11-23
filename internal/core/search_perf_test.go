package core_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// The path a front-end takes when a domain is re-read: derive the documents (SearchDocs) and index
// them. 20,000 tasks, plus as many documents again in each other domain, stay inside a budget that
// leaves the overlay responsive, and re-preparing one domain costs a fraction of all four.
func TestSearchBuildPathAtTwentyThousandDocuments(t *testing.T) {
	snap := core.TaskSnapshot{ReadAt: searchNow}
	for i := 0; i < 20000; i++ {
		snap.Issues = append(snap.Issues, core.TaskIssue{ID: fmt.Sprintf("ac-%05d", i), Title: fmt.Sprintf("Task %d about renewal", i), Description: "some longer description text for the body of the task",
			Status: "open", Priority: i % 5, Labels: []string{"core"}, UpdatedAt: searchNow.Add(-time.Duration(i) * time.Minute), CreatedAt: searchNow.Add(-time.Duration(i) * time.Hour)})
	}
	filler := func(d core.DomainID, k core.SearchKind) []core.SearchDoc {
		out := make([]core.SearchDoc, 5000)
		for i := range out {
			out[i] = core.SearchDoc{Domain: d, Kind: k, ID: fmt.Sprint(i), Ref: fmt.Sprint(i), Title: fmt.Sprint("item ", i, " renewal"), Body: "body text of an item"}
		}
		return out
	}
	start := time.Now()
	tasks := taskview.Build(snap, searchNow).SearchDocs()
	derive := time.Since(start)
	start = time.Now()
	ix := core.NewSearchIndex(core.SearchFeed{Domain: core.DomainTasks, Docs: tasks}, core.SearchFeed{Domain: core.DomainBoard, Docs: filler(core.DomainBoard, core.KindThread)},
		core.SearchFeed{Domain: core.DomainLeases, Docs: filler(core.DomainLeases, core.KindLease)}, core.SearchFeed{Domain: core.DomainQueues, Docs: filler(core.DomainQueues, core.KindRun)})
	all := time.Since(start)
	start = time.Now()
	ix.Replace(core.SearchFeed{Domain: core.DomainBoard, Docs: filler(core.DomainBoard, core.KindThread)})
	one := time.Since(start)
	t.Logf("derive %v, index four domains %v, replace one small domain %v", derive, all, one)
	if derive+all > 1500*time.Millisecond {
		t.Errorf("derive + index took %v", derive+all)
	}
	if one > all/3 {
		t.Errorf("replacing one small domain took %v of %v for all four", one, all)
	}
	s := time.Now()
	if _, err := ix.Search("renewal status:open", core.SearchOptions{Now: searchNow}); err != nil || time.Since(s) > 200*time.Millisecond {
		t.Errorf("search took %v (%v)", time.Since(s), err)
	}
}
