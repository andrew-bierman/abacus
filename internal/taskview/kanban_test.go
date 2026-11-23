package taskview

import (
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/query"
)

func colKeys(r core.KanbanReport) string {
	var ks []string
	for _, c := range r.Columns {
		ks = append(ks, c.Key)
	}
	return strings.Join(ks, " ")
}

func cardIDs(c core.KanbanColumn) string {
	var ids []string
	for _, k := range c.Cards {
		ids = append(ids, k.ID)
	}
	return strings.Join(ids, " ")
}

func col(t *testing.T, r core.KanbanReport, key string) core.KanbanColumn {
	t.Helper()
	for _, c := range r.Columns {
		if c.Key == key {
			return c
		}
	}
	t.Fatalf("no column %q in %s", key, colKeys(r))
	return core.KanbanColumn{}
}

func TestBoardStatusColumnsAreFixedEvenWhenEmpty(t *testing.T) {
	r := view().Board(BoardOptions{})
	if colKeys(r) != "open in_progress blocked deferred closed" || r.Group != "status" || r.Shown != 0 || r.Total != 0 {
		t.Fatalf("%s %+v", colKeys(r), r)
	}
	for _, c := range r.Columns {
		if len(c.Cards) != 0 || c.Count != 0 || c.Cards == nil {
			t.Errorf("%s: an empty column has no cards but a non-nil list (JSON [] not null): %+v", c.Key, c)
		}
	}
}

func TestBoardGroupsByStoredStatusAndClosedIsFolded(t *testing.T) {
	v := view(iss("a", "open", 1), iss("b", "in_progress", 0), iss("c", "closed", 2), iss("d", "closed", 2), iss("e", "blocked", 2), iss("f", "deferred", 3))
	r := v.Board(BoardOptions{})
	if got := cardIDs(col(t, r, "open")); got != "a" {
		t.Errorf("open: %q", got)
	}
	cl := col(t, r, "closed")
	if !cl.Folded || cl.Count != 2 || len(cl.Cards) != 0 || r.ClosedHidden != 2 || r.Shown != 4 || r.Total != 6 {
		t.Errorf("closed is folded to a count: %+v shown=%d hidden=%d total=%d", cl, r.Shown, r.ClosedHidden, r.Total)
	}
	r = v.Board(BoardOptions{ShowClosed: true})
	cl = col(t, r, "closed")
	if cl.Folded || cardIDs(cl) != "c d" || r.ClosedHidden != 0 || r.Shown != 6 {
		t.Errorf("shown: %+v %d %d", cl, r.ClosedHidden, r.Shown)
	}
}

// The derived blocked state is a badge on the card; the column stays the stored status (Q4).
func TestBoardBlockedByDependencyStaysInItsStoredColumnWithABadge(t *testing.T) {
	v := view(iss("blk", "open", 1), iss("held", "open", 1, blk("blk")), iss("stored", "blocked", 2))
	r := v.Board(BoardOptions{})
	open := col(t, r, "open")
	if cardIDs(open) != "blk held" || open.Blocked != 1 {
		t.Fatalf("open: %q blocked=%d", cardIDs(open), open.Blocked)
	}
	if k := open.Cards[1]; !k.Blocked || k.BlockedBy != 1 || k.Status != "open" {
		t.Errorf("held card: %+v", k)
	}
	if open.Cards[0].Blocks != 1 {
		t.Errorf("the blocker holds one: %+v", open.Cards[0])
	}
	if b := col(t, r, "blocked"); cardIDs(b) != "stored" {
		t.Errorf("the Blocked column is the stored status: %q", cardIDs(b))
	}
}

func TestBoardCountsUrgentAndOrdersByPriorityThenID(t *testing.T) {
	v := view(iss("b", "open", 2), iss("a", "open", 2), iss("z", "open", 0), iss("y", "open", 1), iss("x", "open", 4))
	c := col(t, v.Board(BoardOptions{}), "open")
	if cardIDs(c) != "z y a b x" || c.Count != 5 || c.P0P1 != 2 {
		t.Fatalf("%q count=%d p0p1=%d", cardIDs(c), c.Count, c.P0P1)
	}
}

func TestBoardCustomStatusGetsItsOwnColumnBeforeClosed(t *testing.T) {
	v := view(iss("a", "open", 1), iss("p", "pinned", 1), iss("h", "hooked", 2), iss("c", "closed", 1))
	r := v.Board(BoardOptions{})
	if colKeys(r) != "open in_progress blocked deferred hooked pinned closed" {
		t.Fatalf("%s", colKeys(r))
	}
	if c := col(t, r, "pinned"); c.Title != "pinned" || cardIDs(c) != "p" {
		t.Errorf("%+v", c)
	}
}

func TestBoardGroupByPriorityAndType(t *testing.T) {
	a, b, c := iss("a", "open", 0), iss("b", "in_progress", 2), iss("c", "closed", 2)
	a.Type, b.Type, c.Type = "bug", "task", "bug"
	v := view(a, b, c, iss("d", "open", 2))
	r := v.Board(BoardOptions{Group: GroupPriority})
	if r.Group != "priority" || colKeys(r) != "p0 p2" {
		t.Fatalf("only the priorities that hold cards: %s", colKeys(r))
	}
	if p2 := col(t, r, "p2"); cardIDs(p2) != "b d" || p2.Title != "P2" || r.ClosedHidden != 1 {
		t.Errorf("%+v hidden=%d", p2, r.ClosedHidden)
	}
	r = v.Board(BoardOptions{Group: GroupType, ShowClosed: true})
	if colKeys(r) != "bug task" && colKeys(r) != "bug task none" {
		t.Fatalf("%s", colKeys(r))
	}
	if bug := col(t, r, "bug"); cardIDs(bug) != "a c" {
		t.Errorf("closed shown in its type: %q", cardIDs(bug))
	}
	if r = v.Board(BoardOptions{Group: "bogus"}); r.Group != "status" {
		t.Errorf("an unknown grouping is status: %q", r.Group)
	}
}

func TestBoardQueryNarrowsCardsAndCountsMatchesOnly(t *testing.T) {
	a, b := iss("a", "open", 1), iss("b", "open", 1)
	a.Labels = []string{"ui"}
	q, err := query.Parse("label:ui")
	if err != nil {
		t.Fatal(err)
	}
	r := view(a, b).Board(BoardOptions{Query: q})
	if open := col(t, r, "open"); cardIDs(open) != "a" || open.Count != 1 || r.Shown != 1 || r.Total != 2 || r.Query != "label:ui" {
		t.Errorf("%+v %+v", open, r)
	}
}

func TestBoardWithoutDependencyDataClaimsNothingBlockedOrReady(t *testing.T) {
	v := Build(core.TaskSnapshot{Issues: []core.TaskIssue{iss("a", "open", 1)}, ReadAt: fixedNow, DepsUnknown: true}, fixedNow)
	r := v.Board(BoardOptions{})
	if !r.DepsUnknown || col(t, r, "open").Cards[0].Ready || col(t, r, "open").Blocked != 0 {
		t.Errorf("%+v", r)
	}
}

func TestBoardIsStableUnderInputOrder(t *testing.T) {
	issues := []core.TaskIssue{iss("a", "open", 1), iss("b", "open", 1), iss("c", "blocked", 1, blk("a")), iss("d", "closed", 3)}
	want := view(issues...).Board(BoardOptions{ShowClosed: true})
	rev := []core.TaskIssue{issues[3], issues[2], issues[1], issues[0]}
	got := view(rev...).Board(BoardOptions{ShowClosed: true})
	if colKeys(got) != colKeys(want) {
		t.Fatal("columns")
	}
	for i := range want.Columns {
		if cardIDs(want.Columns[i]) != cardIDs(got.Columns[i]) {
			t.Errorf("%s: %q vs %q", want.Columns[i].Key, cardIDs(want.Columns[i]), cardIDs(got.Columns[i]))
		}
	}
}

func TestBoardAgeUsesTheInjectedClock(t *testing.T) {
	a := iss("a", "open", 1)
	a.CreatedAt = fixedNow.Add(-48 * time.Hour)
	if got := col(t, view(a).Board(BoardOptions{}), "open").Cards[0].Age; got != "2d" {
		t.Errorf("age %q", got)
	}
}
