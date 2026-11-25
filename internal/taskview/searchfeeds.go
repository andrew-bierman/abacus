package taskview

import (
	"context"
	"sync"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// ReadSearchFeeds reads each wanted domain once, concurrently, under the command's deadline. A
// domain that cannot be read becomes a feed that says so; it never fails the others.
func ReadSearchFeeds(ctx context.Context, entries []core.Entry, now time.Time, only []core.DomainID) []core.SearchFeed {
	want := func(d core.DomainID) bool { return len(only) == 0 || containsDomain(only, d) }
	var mu sync.Mutex
	var feeds []core.SearchFeed
	var wg sync.WaitGroup
	for _, id := range core.SearchOrder {
		if !want(id) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			f := readSearchFeed(ctx, entries, now, id)
			mu.Lock()
			feeds = append(feeds, f)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return feeds
}

func containsDomain(l []core.DomainID, d core.DomainID) bool {
	for _, x := range l {
		if x == d {
			return true
		}
	}
	return false
}

// feedState maps a domain header to what search can do with it: a read that failed has no
// documents, a reduced source still does (its reason is kept as a note).
func feedState(h core.Header) core.SearchState {
	switch {
	case h.State == core.Connected:
		return core.SearchSearched
	case h.State == core.Degraded && h.HowToConnect != "":
		return core.SearchDegraded
	case h.State == core.Degraded:
		return core.SearchSearched
	}
	return core.SearchNotConnected
}

func readSearchFeed(ctx context.Context, entries []core.Entry, now time.Time, id core.DomainID) core.SearchFeed {
	f := core.SearchFeed{Domain: id, ReadAt: now}
	if id == core.DomainTasks { // the whole tracker, as the list reads it; the summary only when that is unavailable
		if src := core.ResolveProviders(entries).Source; src != nil {
			if snap, err := src.Snapshot(ctx); err == nil {
				f.Docs = Build(snap, now).SearchDocs()
				return f
			}
		}
	}
	dom := core.ReadDomain(ctx, entries, id)
	switch id {
	case core.DomainTasks:
		f.State, f.Reason = feedState(dom.Tasks.Header), dom.Tasks.Reason
		f.Docs = Build(FromRecent(dom.Tasks.Recent, now), now).SearchDocs()
		if f.State == core.SearchSearched { // searched, but only the summary's recent changes
			f.State, f.Reason = core.SearchDegraded, core.JoinSearchReasons(f.Reason, core.SearchReasonRecentTasks)
		}
	case core.DomainBoard:
		f.State, f.Reason = feedState(dom.Board.Header), dom.Board.Reason
		f.Docs = core.BoardSearchDocs(dom.Board)
		if dom.Board.Truncated != "" {
			f.Reason = core.JoinSearchReasons(f.Reason, core.SearchReasonBoardCapped)
		}
	case core.DomainLeases:
		f.State, f.Reason = feedState(dom.Leases.Header), dom.Leases.Reason
		f.Docs = core.LeasesSearchDocs(dom.Leases)
	case core.DomainQueues:
		f.State, f.Reason = feedState(dom.Queues.Header), dom.Queues.Reason
		f.Docs = core.QueuesSearchDocs(dom.Queues)
	case core.DomainAgents:
		f.State, f.Reason = feedState(dom.Agents.Header), dom.Agents.Reason
		f.Docs = core.AgentsSearchDocs(dom.Agents)
	}
	if f.State == core.SearchNotConnected || (f.State == core.SearchDegraded && id != core.DomainTasks) {
		f.Docs = nil
	}
	return f
}
