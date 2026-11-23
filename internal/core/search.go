package core

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/andrew-bierman/abacus/internal/query"
)

// Global search is a linear scan over documents built from the typed domain reports abacus
// already reads (Tasks snapshot, Board, Leases, Queues). It never runs a tool: a front-end hands
// each domain's last successful read to NewSearchIndex as a SearchFeed. The grammar is the shared
// internal/query one; SearchDoc is the Doc accessor that maps each domain's own fields onto it.

// Search bounds. The query cap is query.MaxLen (256 characters).
const (
	// DefaultSearchLimit is the number of results kept per domain unless asked otherwise.
	DefaultSearchLimit = 20
	// MaxSearchLimit is the largest per-domain limit a caller may ask for.
	MaxSearchLimit = 200
	// MaxSearchDocs is how many documents of one domain a scan covers.
	MaxSearchDocs = 20000
	// MaxSearchTitle is how many characters of a title, id or label are searched and shown.
	MaxSearchTitle = 500
	// MaxSearchBody is how many characters of a document's body are searched.
	MaxSearchBody = 2000
	// TaskStaleAfter is how long an unfinished task may go untouched before `is:stale` holds.
	TaskStaleAfter = 14 * 24 * time.Hour
)

// SearchOrder is the order of the groups in a report.
var SearchOrder = []DomainID{DomainTasks, DomainBoard, DomainLeases, DomainQueues}

// SearchKind is what a document is.
type SearchKind string

const (
	KindTask      SearchKind = "task"
	KindThread    SearchKind = "thread"
	KindMessage   SearchKind = "message"
	KindBoardTask SearchKind = "board_task"
	KindLease     SearchKind = "lease"
	KindRun       SearchKind = "run"
	KindQueued    SearchKind = "queued"
	KindAttention SearchKind = "attention"
	KindHistory   SearchKind = "history"
)

// searchFields is the set of grammar fields each kind has (the field table in the plan). A
// predicate on a field a kind lacks excludes that kind.
var searchFields = func() map[SearchKind]map[string]bool {
	set := func(names ...string) map[string]bool {
		m := map[string]bool{"in": true, "id": true, "title": true}
		for _, n := range names {
			m[n] = true
		}
		return m
	}
	return map[SearchKind]map[string]bool{
		KindTask:      set("status", "who", "label", "type", "priority", "parent", "is", "has", "blocked", "ready", "updated", "created"),
		KindThread:    set("who", "channel", "updated"),
		KindMessage:   set("who", "channel", "updated"),
		KindBoardTask: set("status", "who", "channel"),
		KindLease:     set("status", "who", "is", "expires"),
		KindRun:       set("status", "who", "phase", "workflow", "is"),
		KindQueued:    set("status"),
		KindAttention: set(),
		KindHistory:   set("status", "updated"),
	}
}()

// SearchDoc is one searchable item. ID is what the owning section selects on a jump (Target, when
// set, is the row that shows it: a reply is shown by its thread). Ref is the id as the domain
// names it.
type SearchDoc struct {
	Domain DomainID
	Kind   SearchKind
	ID     string
	Target string
	Ref    string
	Title  string
	Body   string
	Meta   string // the short secondary text of a result row

	Status, Who, Type, Parent, Channel, Phase, Workflow string
	Labels                                              []string
	Priority                                            int
	HasPriority                                         bool
	Blocked, Ready, HasDerived                          bool
	Stale                                               bool
	Has                                                 []string
	Updated, Created, Expires                           time.Time

	lower []string // id, title, labels, body: lower-cased once, what free words search
}

// cutRunes is s cut to at most n characters (a string no longer than n bytes needs no counting).
func cutRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	count := 0
	for i := range s { // stops at the n-th character: a huge string is never converted whole
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

// prepare lower-cases the free-text fields once, so a scan does not allocate per word. Every
// field is cut first: a multi-megabyte one-line message is not lower-cased or scanned whole.
func (d *SearchDoc) prepare() {
	d.lower = append(d.lower[:0], strings.ToLower(cutRunes(d.Ref, MaxSearchTitle)), strings.ToLower(cutRunes(d.Title, MaxSearchTitle)))
	for _, l := range d.Labels {
		d.lower = append(d.lower, strings.ToLower(cutRunes(l, MaxSearchTitle)))
	}
	d.lower = append(d.lower, strings.ToLower(cutRunes(d.Body, MaxSearchBody)))
}

// Text is what free words search: id, title, labels and body.
func (d *SearchDoc) Text() []string { return d.lower }

func strs(s string) query.Value {
	if s == "" {
		return query.Value{}
	}
	return query.Value{Strs: []string{s}}
}

func tval(t time.Time) query.Value { return query.Value{Time: t, HasTime: !t.IsZero()} }

// Field maps the grammar's canonical fields onto the document.
func (d *SearchDoc) Field(name string) (query.Value, bool) {
	if !searchFields[d.Kind][name] {
		return query.Value{}, false
	}
	switch name {
	case "in":
		return query.Value{Strs: []string{string(d.Domain)}}, true
	case "id":
		return strs(d.Ref), true
	case "title":
		if d.Kind == KindRun {
			return query.Value{Strs: []string{d.Ref, d.Title}}, true
		}
		return strs(d.Title), true
	case "status":
		return strs(d.Status), true
	case "who":
		return strs(d.Who), true
	case "label":
		return query.Value{Strs: d.Labels}, true
	case "type":
		return strs(d.Type), true
	case "parent":
		return strs(d.Parent), true
	case "channel":
		return strs(d.Channel), true
	case "phase":
		return strs(d.Phase), true
	case "workflow":
		return strs(d.Workflow), true
	case "priority":
		return query.Value{Num: d.Priority, HasNum: d.HasPriority}, true
	case "blocked":
		return query.Value{Bool: d.Blocked, HasBool: d.HasDerived}, true
	case "ready":
		return query.Value{Bool: d.Ready, HasBool: d.HasDerived}, true
	case "is":
		var s []string
		if d.Blocked && d.HasDerived {
			s = append(s, "blocked")
		}
		if d.Ready && d.HasDerived {
			s = append(s, "ready")
		}
		if d.Stale {
			s = append(s, "stale")
		}
		return query.Value{Strs: s}, true
	case "has":
		return query.Value{Strs: d.Has}, true
	case "updated":
		return tval(d.Updated), true
	case "created":
		return tval(d.Created), true
	case "expires":
		return tval(d.Expires), true
	}
	return query.Value{}, false
}

// SearchState is whether a domain could be searched.
type SearchState string

const (
	SearchSearched     SearchState = "searched"
	SearchLoading      SearchState = "loading"
	SearchNotConnected SearchState = "not_connected"
	SearchDegraded     SearchState = "degraded"
)

// SearchFeed is one domain's last successful read, as documents, with how fresh it is. A feed
// that could not be read carries State and Reason instead of documents.
type SearchFeed struct {
	Domain    DomainID
	State     SearchState // empty means searched
	Reason    string
	ReadAt    time.Time
	PollEvery time.Duration // how often the domain is re-read; 0 means a fresh read, never stale
	Docs      []SearchDoc
}

// SearchOptions bound one search.
type SearchOptions struct {
	Now     time.Time
	Limit   int        // per domain; 0 means DefaultSearchLimit
	Domains []DomainID // nil means every domain
}

// SearchResult is one hit.
type SearchResult struct {
	Domain  DomainID   `json:"domain"`
	Kind    SearchKind `json:"kind"`
	ID      string     `json:"id"`
	Target  string     `json:"target,omitempty"`
	Label   string     `json:"label"`
	Title   string     `json:"title"`
	Meta    string     `json:"meta,omitempty"`
	Status  string     `json:"status,omitempty"`
	Snippet string     `json:"snippet,omitempty"`
	Matched []string   `json:"matched,omitempty"`
	Rank    int        `json:"rank"`
	Stale   bool       `json:"stale,omitempty"`
	Age     string     `json:"age,omitempty"`
}

// Jump is the row id a section selects for this result.
func (r SearchResult) Jump() string {
	if r.Target != "" {
		return r.Target
	}
	return r.ID
}

// SearchGroup is one domain's hits: Total is every match, Results the first Limit of them.
type SearchGroup struct {
	Domain    DomainID       `json:"domain"`
	Title     string         `json:"title"`
	Total     int            `json:"total"`
	Truncated int            `json:"truncated,omitempty"`
	Results   []SearchResult `json:"results"`
}

// SearchDomain says what happened to one domain: searched, loading, not connected or degraded.
type SearchDomain struct {
	Domain DomainID    `json:"domain"`
	Title  string      `json:"title"`
	State  SearchState `json:"state"`
	Reason string      `json:"reason,omitempty"`
	Docs   int         `json:"docs"`
	ReadAt string      `json:"read_at,omitempty"`
	Age    string      `json:"age,omitempty"`
	Stale  bool        `json:"stale,omitempty"`
}

// Searched reports that the domain's documents were scanned: read normally, or read from a
// reduced source (degraded with something to search).
func (d SearchDomain) Searched() bool {
	return d.State == SearchSearched || (d.State == SearchDegraded && d.Docs > 0)
}

// Reduced reports a domain that was searched but only in part; Reason says how.
func (d SearchDomain) Reduced() bool { return d.Searched() && d.Reason != "" }

// SearchReport is the result of one search: groups with hits, in SearchOrder, and a status per
// domain, so a domain that was not searched is named rather than silently absent.
type SearchReport struct {
	SchemaVersion int            `json:"schema_version"`
	Query         string         `json:"query"`
	Total         int            `json:"total"`
	Groups        []SearchGroup  `json:"groups"`
	Domains       []SearchDomain `json:"domains"`
}

// SearchIndex is the documents of every domain, ready to scan.
type SearchIndex struct{ feeds map[DomainID]SearchFeed }

// NewSearchIndex indexes the feeds (a later feed of the same domain replaces an earlier one).
func NewSearchIndex(feeds ...SearchFeed) *SearchIndex {
	ix := &SearchIndex{feeds: map[DomainID]SearchFeed{}}
	for _, f := range feeds {
		ix.Replace(f)
	}
	return ix
}

// Replace swaps one domain's feed, preparing only that feed: a front-end whose one domain was
// just re-read does not re-prepare the others.
func (ix *SearchIndex) Replace(f SearchFeed) {
	docs := f.Docs
	if len(docs) > MaxSearchDocs {
		docs = docs[:MaxSearchDocs]
		f.Reason = JoinSearchReasons(f.Reason, fmt.Sprintf("only the first %d were searched", MaxSearchDocs))
	}
	f.Docs = make([]SearchDoc, len(docs))
	copy(f.Docs, docs)
	for i := range f.Docs {
		f.Docs[i].prepare()
	}
	ix.feeds[f.Domain] = f
}

// JoinSearchReasons joins two reasons for one domain.
func JoinSearchReasons(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

// SearchStale is the staleness rule: a read older than twice its poll interval. A feed with no
// read time or no interval is a fresh read and is never stale.
func SearchStale(readAt, now time.Time, poll time.Duration) bool {
	return !readAt.IsZero() && poll > 0 && now.Sub(readAt) > 2*poll
}

// SearchAge is a short age: "just now", "3m", "2h", "5d".
func SearchAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
}

// Search runs text against the index. A query that does not parse returns the typed
// *query.Error (with its column). An empty query matches nothing.
func (ix *SearchIndex) Search(text string, o SearchOptions) (SearchReport, error) {
	rep := SearchReport{SchemaVersion: SchemaVersion, Groups: []SearchGroup{}, Domains: []SearchDomain{}}
	q, err := query.Parse(text)
	if err != nil {
		return rep, err
	}
	rep.Query = q.String()
	limit := o.Limit
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	limit = min(limit, MaxSearchLimit)
	wanted := func(d DomainID) bool { return len(o.Domains) == 0 || slices.Contains(o.Domains, d) }
	for _, d := range SearchOrder {
		if !wanted(d) {
			continue
		}
		f, ok := ix.feeds[d]
		st := SearchDomain{Domain: d, Title: d.Title(), State: f.State, Reason: Sanitize(f.Reason), Docs: len(f.Docs)}
		if !ok {
			st.State = SearchLoading
		}
		if st.State == "" {
			st.State = SearchSearched
		}
		if !f.ReadAt.IsZero() {
			st.ReadAt = f.ReadAt.UTC().Format(time.RFC3339)
			st.Age = SearchAge(o.Now.Sub(f.ReadAt))
			st.Stale = SearchStale(f.ReadAt, o.Now, f.PollEvery)
		}
		rep.Domains = append(rep.Domains, st)
		if q.Empty() || len(f.Docs) == 0 {
			continue
		}
		g := scan(q, f, st, o.Now, limit)
		if g.Total > 0 {
			rep.Total += g.Total
			rep.Groups = append(rep.Groups, g)
		}
	}
	return rep, nil
}

type hit struct {
	doc  *SearchDoc
	rank int
}

func scan(q query.Query, f SearchFeed, st SearchDomain, now time.Time, limit int) SearchGroup {
	var hits []hit
	for i := range f.Docs {
		d := &f.Docs[i]
		if !q.Match(d, now) {
			continue
		}
		hits = append(hits, hit{d, rankOf(q, d)})
	}
	slices.SortStableFunc(hits, func(a, b hit) int {
		if c := cmp.Compare(a.rank, b.rank); c != 0 {
			return c
		}
		if c := cmp.Compare(boolInt(a.doc.Updated.IsZero()), boolInt(b.doc.Updated.IsZero())); c != 0 { // undated last
			return c
		}
		if c := b.doc.Updated.Compare(a.doc.Updated); c != 0 { // newest first
			return c
		}
		if c := strings.Compare(a.doc.ID, b.doc.ID); c != 0 {
			return c
		}
		return strings.Compare(string(a.doc.Kind), string(b.doc.Kind))
	})
	g := SearchGroup{Domain: f.Domain, Title: f.Domain.Title(), Total: len(hits), Results: []SearchResult{}}
	for _, h := range hits[:min(len(hits), limit)] {
		g.Results = append(g.Results, resultOf(q, h, st))
	}
	g.Truncated = len(hits) - len(g.Results)
	return g
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Ranking tiers, best first: an exact id, an id prefix, a whole word of the title, a part of the
// title, a label, the body. A document is as good as its weakest word.
const (
	tierID = iota
	tierIDPrefix
	tierTitleWord
	tierTitle
	tierLabel
	tierBody
	tierOther
	tierNone // no free words: ordered by recency alone
)

func wordTier(w query.Word, d *SearchDoc) int {
	id, title := d.lower[0], d.lower[1]
	switch {
	case id == w.Text:
		return tierID
	case strings.HasPrefix(id, w.Text):
		return tierIDPrefix
	case hasWholeWord(title, w.Text):
		return tierTitleWord
	case strings.Contains(title, w.Text):
		return tierTitle
	}
	nl := len(d.lower) - 1
	for _, l := range d.lower[2:nl] {
		if strings.Contains(l, w.Text) {
			return tierLabel
		}
	}
	if strings.Contains(d.lower[nl], w.Text) {
		return tierBody
	}
	return tierOther
}

func hasWholeWord(s, w string) bool {
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if f == w {
			return true
		}
	}
	return false
}

func rankOf(q query.Query, d *SearchDoc) int {
	if len(q.Words) == 0 {
		return tierNone
	}
	worst := 0
	for _, w := range q.Words {
		worst = max(worst, wordTier(w, d))
	}
	return worst
}

func resultOf(q query.Query, h hit, st SearchDomain) SearchResult {
	d := h.doc
	r := SearchResult{Domain: d.Domain, Kind: d.Kind, ID: d.ID, Label: d.Label(), Title: Sanitize(cutRunes(d.Title, MaxSearchTitle)), Meta: Sanitize(cutRunes(d.Meta, MaxSearchTitle)),
		Status: Sanitize(d.Status), Rank: h.rank, Stale: st.Stale, Age: ""}
	if d.Target != "" && d.Target != d.ID {
		r.Target = d.Target
	}
	if st.Stale {
		r.Age = st.Age
	}
	seen := map[string]bool{}
	add := func(f string) {
		if !seen[f] {
			seen[f] = true
			r.Matched = append(r.Matched, f)
		}
	}
	var snippetWord string
	for _, w := range q.Words {
		switch wordTier(w, d) {
		case tierID, tierIDPrefix:
			add("id")
		case tierTitleWord, tierTitle:
			add("title")
		case tierLabel:
			add("label")
		case tierBody:
			add("body")
			if snippetWord == "" {
				snippetWord = w.Text
			}
		default:
			add("title") // a short word that matched as a subsequence of the title or id
		}
	}
	for _, p := range q.Preds {
		add(p.Field)
	}
	if snippetWord != "" && !seen["title"] && !seen["id"] {
		r.Snippet = snippetAround(d.Body, snippetWord)
	}
	return r
}

// Label is the id as a person reads it: a board message or thread has a hash in front.
func (d *SearchDoc) Label() string {
	switch d.Kind {
	case KindThread, KindMessage, KindBoardTask:
		return "#" + Sanitize(d.Ref)
	}
	return Sanitize(d.Ref)
}

// snippetAround is about 48 characters of body around the first occurrence of word, on one line.
// It works on one rune slice and folds case per rune, so a character whose lower-case form has a
// different byte length can never skew a position: no offset of one string indexes another.
func snippetAround(body, word string) string {
	br := []rune(body)
	if len(br) > MaxSearchBody { // what is searched is what is excerpted
		br = br[:MaxSearchBody]
	}
	r := []rune(strings.Join(strings.Fields(Sanitize(string(br))), " "))
	w := foldRunes([]rune(word))
	at := indexRunes(foldRunes(r), w)
	if at < 0 {
		at = 0
	}
	mend := min(len(r), at+len(w))
	start := max(0, at-16)
	end := max(min(len(r), start+48), mend)            // never cut the match itself
	for start > 0 && start < at && r[start-1] != ' ' { // begin and end on word boundaries
		start++
	}
	for end < len(r) && end > mend && r[end] != ' ' {
		end--
	}
	start = min(start, end)
	s := strings.TrimSpace(string(r[start:end]))
	if start > 0 {
		s = "…" + s
	}
	if end < len(r) {
		s += "…"
	}
	return s
}

func foldRunes(r []rune) []rune {
	out := make([]rune, len(r))
	for i, c := range r {
		out[i] = unicode.ToLower(c)
	}
	return out
}

// indexRunes is the index of the first occurrence of sub in s, or -1.
func indexRunes(s, sub []rune) int {
	if len(sub) == 0 {
		return -1
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if slices.Equal(s[i:i+len(sub)], sub) {
			return i
		}
	}
	return -1
}

// firstLineOf is the first non-empty line of s.
func firstLineOf(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(l)
}

func parseWhen(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func joinMeta(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " · ")
}

// BoardSearchDocs are the threads, their latest replies and the open tasks of a board read.
func BoardSearchDocs(r BoardReport) []SearchDoc {
	var out []SearchDoc
	for _, t := range r.Threads {
		upd := parseWhen(t.Root.At)
		reps := ""
		if t.Replies > 0 {
			reps = fmt.Sprintf("%d replies", t.Replies)
		}
		if t.Last != nil {
			if l := parseWhen(t.Last.At); l.After(upd) {
				upd = l
			}
		}
		out = append(out, SearchDoc{Domain: DomainBoard, Kind: KindThread, ID: "t:" + t.Root.ID, Ref: t.Root.ID,
			Title: firstLineOf(t.Root.Text), Body: t.Root.Text, Who: t.Root.Author, Channel: t.Root.Channel, Updated: upd,
			Meta: joinMeta(t.Root.Channel, t.Root.Author, reps)})
		if t.Last != nil {
			out = append(out, SearchDoc{Domain: DomainBoard, Kind: KindMessage, ID: "m:" + t.Last.ID, Target: "t:" + t.Root.ID, Ref: t.Last.ID,
				Title: firstLineOf(t.Last.Text), Body: t.Last.Text, Who: t.Last.Author, Channel: t.Root.Channel, Updated: parseWhen(t.Last.At),
				Meta: joinMeta("reply", t.Last.Author, "#"+t.Root.ID)})
		}
	}
	for _, t := range r.Tasks {
		who := t.Claimant
		out = append(out, SearchDoc{Domain: DomainBoard, Kind: KindBoardTask, ID: "k:" + t.ID, Ref: t.ID, Title: firstLineOf(t.Text), Body: t.Text,
			Status: t.State, Who: who, Channel: t.Channel, Meta: joinMeta("task", t.State, who, t.Channel)})
	}
	return out
}

// LeasesSearchDocs are the leases of a leases read.
func LeasesSearchDocs(r LeasesReport) []SearchDoc {
	out := make([]SearchDoc, 0, len(r.Leases))
	for _, l := range r.Leases {
		meta := string(l.State)
		switch l.State {
		case LeaseHeld:
			meta = joinMeta("held by "+orDashStr(l.Holder), "expires "+l.Expiry())
		case LeaseStale:
			meta = joinMeta("stale", "last holder "+orDashStr(l.Holder))
		}
		out = append(out, SearchDoc{Domain: DomainLeases, Kind: KindLease, ID: l.Resource, Ref: l.Resource, Title: l.Resource,
			Status: string(l.State), Who: l.Holder, Stale: l.State == LeaseStale, Expires: parseWhen(l.ExpiresAt), Meta: meta})
	}
	return out
}

func orDashStr(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// QueuesSearchDocs are the runs, queued items, attention items and history of a queues read.
func QueuesSearchDocs(r QueuesReport) []SearchDoc {
	var out []SearchDoc
	for _, x := range r.Runs {
		title := cmp.Or(x.Workflow, x.Phase, "run")
		out = append(out, SearchDoc{Domain: DomainQueues, Kind: KindRun, ID: x.Issue, Ref: x.Issue, Title: title, Status: string(x.State),
			Who: x.Machine, Phase: x.Phase, Workflow: x.Workflow, Stale: x.Stalled,
			Meta: joinMeta(string(x.State), x.Phase, x.Machine, map[bool]string{true: "stalled"}[x.Stalled])})
	}
	for _, x := range r.Queue {
		st := "ready"
		if x.Blocked {
			st = "blocked"
		}
		out = append(out, SearchDoc{Domain: DomainQueues, Kind: KindQueued, ID: x.ID, Ref: x.ID, Title: x.Title, Status: st, Meta: joinMeta("queued", st)})
	}
	for _, x := range r.Attention {
		out = append(out, SearchDoc{Domain: DomainQueues, Kind: KindAttention, ID: x.Issue, Ref: x.Issue, Title: x.Reason, Body: x.Detail, Meta: "needs attention"})
	}
	for _, x := range r.History {
		out = append(out, SearchDoc{Domain: DomainQueues, Kind: KindHistory, ID: x.Issue, Ref: x.Issue, Title: x.Title, Status: x.Outcome,
			Updated: parseWhen(x.UpdatedAt), Meta: joinMeta("history", x.Outcome)})
	}
	return out
}
