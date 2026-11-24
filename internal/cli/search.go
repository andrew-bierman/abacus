package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/query"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// domainList is a repeatable, comma-separated --domain flag.
type domainList []core.DomainID

func (l *domainList) String() string { return fmt.Sprint([]core.DomainID(*l)) }
func (l *domainList) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		d := core.DomainID(strings.ToLower(strings.TrimSpace(p)))
		if !validSearchDomain(d) {
			return fmt.Errorf("unknown domain %q (tasks, board, leases, queues, agents)", p)
		}
		*l = append(*l, d)
	}
	return nil
}

func validSearchDomain(d core.DomainID) bool {
	for _, k := range core.SearchOrder {
		if d == k {
			return true
		}
	}
	return false
}

// runSearch handles `search QUERY [--json] [--domain D] [--limit N]`: the same core search the TUI
// overlay uses, read-only, over one read of each domain. Exit 0 with answers (even none), 2 for a
// bad invocation or query, 1 when no domain could be searched. Under --json nothing reaches stdout
// on a failure.
func runSearch(rest []string, d Deps) int {
	var literal []string
	for i, a := range rest { // after -- everything is query text, so a leading dash can be a negation
		if a == "--" {
			rest, literal = rest[:i], rest[i+1:]
			break
		}
	}
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	fs.SetOutput(d.Err)
	var o Options
	var asJSON bool
	var doms domainList
	limit := core.DefaultSearchLimit
	fs.BoolVar(&asJSON, "json", false, "print JSON")
	fs.Var(&doms, "domain", "search only these domains (tasks, board, leases, queues, agents); repeat or comma-separate")
	fs.IntVar(&limit, "limit", core.DefaultSearchLimit, fmt.Sprintf("results per domain (1 to %d)", core.MaxSearchLimit))
	fs.StringVar(&o.LambeConfig, "lambe-config", "", "config file selecting the queues daemon")
	fs.BoolVar(&o.Demo, "demo", false, "search fixtures instead of the real domains")
	var words []string
	for len(rest) > 0 { // flags and words may be mixed: the flag package stops at the first word
		if fs.Parse(rest) != nil {
			return core.ExitUsage
		}
		if fs.NArg() == 0 {
			break
		}
		words = append(words, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	words = append(words, literal...)
	text := strings.TrimSpace(strings.Join(words, " "))
	if text == "" {
		fmt.Fprintln(d.Err, `search needs a query, for example: search "status:open lease"`)
		return core.ExitUsage
	}
	if limit < 1 || limit > core.MaxSearchLimit {
		fmt.Fprintf(d.Err, "search: --limit must be 1 to %d\n", core.MaxSearchLimit)
		return core.ExitUsage
	}
	if utf8.RuneCountInString(text) > query.MaxLen {
		fmt.Fprintf(d.Err, "search: bad query: query is longer than %d characters\n", query.MaxLen)
		return core.ExitUsage
	}
	if _, err := query.Parse(text); err != nil {
		fmt.Fprintf(d.Err, "search: bad query: %v\n", err)
		return core.ExitUsage
	}
	now := time.Now()
	if d.Now != nil {
		now = d.Now()
	}
	ctx, cancel := context.WithTimeout(context.Background(), cliTimeout)
	defer cancel()
	feeds := readSearchFeeds(ctx, d.Build(o).Detect(ctx), now, doms)
	rep, err := core.NewSearchIndex(feeds...).Search(text, core.SearchOptions{Now: now, Limit: limit, Domains: doms})
	if err != nil {
		fmt.Fprintf(d.Err, "search: bad query: %v\n", err)
		return core.ExitUsage
	}
	searched := 0
	for _, s := range rep.Domains {
		if s.Searched() {
			searched++
		}
	}
	if searched == 0 {
		fmt.Fprintf(d.Err, "search: no domain could be searched (%s)\n", strings.Join(unsearched(rep), "; "))
		return core.ExitFail
	}
	if asJSON {
		jsonOut(d.Out, rep)
	} else {
		renderSearch(d.Out, rep)
	}
	return core.ExitOK
}

func unsearched(r core.SearchReport) []string {
	var out []string
	for _, s := range r.Domains {
		out = append(out, s.Title+": "+searchStateWords(s))
	}
	return out
}

func searchStateWords(s core.SearchDomain) string {
	w := strings.ReplaceAll(string(s.State), "_", " ")
	if s.Reason != "" && s.State == core.SearchDegraded {
		w += " (" + s.Reason + ")"
	}
	return w
}

// readSearchFeeds reads each wanted domain once, concurrently, under the command's deadline. A
// domain that cannot be read becomes a feed that says so; it never fails the others.
func readSearchFeeds(ctx context.Context, entries []core.Entry, now time.Time, only []core.DomainID) []core.SearchFeed {
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
				f.Docs = taskview.Build(snap, now).SearchDocs()
				return f
			}
		}
	}
	dom := core.ReadDomain(ctx, entries, id)
	switch id {
	case core.DomainTasks:
		f.State, f.Reason = feedState(dom.Tasks.Header), dom.Tasks.Reason
		f.Docs = taskview.Build(taskview.FromRecent(dom.Tasks.Recent, now), now).SearchDocs()
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

func renderSearch(w io.Writer, r core.SearchReport) {
	fmt.Fprintf(w, "Search  %s  %d hits\n", r.Query, r.Total)
	for _, g := range r.Groups {
		fmt.Fprintf(w, "\n%s (%d)\n", g.Title, g.Total)
		for _, x := range g.Results {
			line := fmt.Sprintf("  %-12s %s", x.Label, x.Title)
			if x.Snippet != "" {
				line += "  " + x.Snippet
			}
			if x.Meta != "" {
				line += "  [" + x.Meta + "]"
			}
			if x.Stale {
				line += "  (stale, read " + x.Age + " ago)"
			}
			fmt.Fprintln(w, core.Sanitize(line))
		}
		if g.Truncated > 0 {
			fmt.Fprintf(w, "  +%d more\n", g.Truncated)
		}
	}
	if r.Total == 0 {
		fmt.Fprintln(w, "\nno matches")
	}
	for _, s := range r.Domains {
		if s.Reduced() {
			fmt.Fprintf(w, "\nnote: %s: %s\n", s.Title, core.Sanitize(s.Reason))
		}
	}
	var notes []string
	for _, s := range r.Domains {
		if !s.Searched() {
			notes = append(notes, s.Title+": "+searchStateWords(s))
		}
	}
	if len(notes) > 0 {
		fmt.Fprintf(w, "\nnot searched: %s\n", strings.Join(notes, ", "))
	}
}
