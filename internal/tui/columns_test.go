package tui

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

var testCols = []column{
	{min: 6, max: 8, prio: 0},               // id
	{min: 8, max: 12, prio: 1},              // state
	{min: 10, prio: 2},                      // title, takes the slack
	{min: 6, max: 10, prio: 3, right: true}, // expiry
}

func kept(ws []int) (n int) {
	for _, w := range ws {
		if w > 0 {
			n++
		}
	}
	return n
}

func TestFitColumnsNeverExceedsWidth(t *testing.T) {
	for w := 0; w <= 300; w++ {
		ws := (table{testCols, 2}).fit(w)
		used := sum(ws)
		if k := kept(ws); k > 1 {
			used += 2 * (k - 1)
		}
		if used > w {
			t.Fatalf("w=%d: widths %v use %d", w, ws, used)
		}
	}
}

func TestFitColumnsDropsLeastImportantFirst(t *testing.T) {
	// All four fit at 6+8+10+6 + 3 gaps of 2 = 36.
	if ws := (table{testCols, 2}).fit(36); kept(ws) != 4 {
		t.Fatalf("w=36 keeps %v", ws)
	}
	ws := (table{testCols, 2}).fit(35)
	if ws[3] != 0 || kept(ws) != 3 {
		t.Fatalf("w=35 must drop only the lowest-priority column: %v", ws)
	}
	ws = (table{testCols, 2}).fit(17) // id + state need 6+8+2 = 16
	if kept(ws) != 2 || ws[0] == 0 || ws[1] == 0 {
		t.Fatalf("w=17 must keep the two most important: %v", ws)
	}
	ws = (table{testCols, 2}).fit(3)
	if kept(ws) != 1 || ws[0] != 3 {
		t.Fatalf("w=3 keeps the most important column, truncated to fit: %v", ws)
	}
}

func TestFitColumnsSlackGoesToTheUnboundedColumnAfterBoundedOnesFill(t *testing.T) {
	ws := (table{testCols, 2}).fit(100)
	if ws[0] != 8 || ws[1] != 12 || ws[3] != 10 {
		t.Fatalf("bounded columns grow to their max first: %v", ws)
	}
	if ws[2] != 100-2*3-8-12-10 {
		t.Fatalf("the unbounded column takes the rest: %v", ws)
	}
}

func TestFormatRowAlignsCellsAndNeverExceedsWidth(t *testing.T) {
	cells := []string{"ac-zoj", "held", "a very long title that must be cut", "12m"}
	for w := 1; w <= 120; w++ {
		ws := (table{testCols, 2}).fit(w)
		row := (table{testCols, 2}).row(ws, cells)
		if lipgloss.Width(row) > w {
			t.Fatalf("w=%d: %q is %d wide", w, row, lipgloss.Width(row))
		}
	}
	ws := (table{testCols, 2}).fit(60)
	a := (table{testCols, 2}).row(ws, []string{"a", "held", "x", "1m"})
	b := (table{testCols, 2}).row(ws, []string{"bbbbbb", "stale", "y", "12m"})
	if lipgloss.Width(a) != lipgloss.Width(b) {
		t.Fatalf("rows must align:\n%q\n%q", a, b)
	}
	if !strings.HasSuffix(a, " 1m") || !strings.HasSuffix(b, "12m") {
		t.Fatalf("right-aligned column must end flush:\n%q\n%q", a, b)
	}
}

func TestFormatRowMeasuresCellsNotBytes(t *testing.T) {
	cols := []column{{min: 4, max: 4, prio: 0}, {min: 4, prio: 1}}
	for _, hostile := range []string{"日本語日本語", "é́́x", "\U0001F468‍\U0001F469‍\U0001F467ab", "\x1b[31mred\x1b[0mred"} {
		for w := 1; w <= 40; w++ {
			tb := table{cols, 1}
			row := tb.row(tb.fit(w), []string{hostile, hostile})
			if lipgloss.Width(row) > w {
				t.Fatalf("%q at w=%d: %q is %d wide", hostile, w, row, lipgloss.Width(row))
			}
		}
	}
}

func TestFitColumnsRandomSpecsStayInsideTheWidth(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 2000; i++ {
		var cs []column
		for j := 0; j < 1+rng.Intn(6); j++ {
			mn := rng.Intn(20)
			mx := 0
			if rng.Intn(2) == 0 {
				mx = mn + rng.Intn(20)
			}
			cs = append(cs, column{min: mn, max: mx, prio: rng.Intn(5)})
		}
		w, gap := rng.Intn(200), rng.Intn(4)
		ws := (table{cs, gap}).fit(w)
		used := sum(ws)
		if k := kept(ws); k > 1 {
			used += gap * (k - 1)
		}
		if used > w {
			t.Fatalf("cols=%v w=%d gap=%d: %v uses %d", cs, w, gap, ws, used)
		}
	}
}

// Priority, not position, decides what is dropped: here the leftmost column is the least
// important and the middle one the most.
func TestFitColumnsDropsByPriorityNotPosition(t *testing.T) {
	cols := []column{{min: 6, prio: 2}, {min: 6, prio: 0}, {min: 6, prio: 1}}
	ws := (table{cols, 1}).fit(13) // room for two columns and a gap
	if ws[0] != 0 || ws[1] == 0 || ws[2] == 0 {
		t.Fatalf("must keep the two most important and drop the leftmost: %v", ws)
	}
	ws = (table{cols, 1}).fit(6)
	if ws[1] != 6 || ws[0] != 0 || ws[2] != 0 {
		t.Fatalf("one column left must be the most important: %v", ws)
	}
	// Equal priority: the rightmost goes first.
	eq := []column{{min: 6}, {min: 6}, {min: 6}}
	ws = (table{eq, 1}).fit(13)
	if ws[0] == 0 || ws[1] == 0 || ws[2] != 0 {
		t.Fatalf("a tie drops the rightmost: %v", ws)
	}
}

// When the slack cannot fill every bounded column, the most important one is filled first.
func TestFitColumnsFillsBoundedColumnsInPriorityOrder(t *testing.T) {
	cols := []column{{min: 4, max: 10, prio: 1}, {min: 4, max: 10, prio: 0}}
	ws := (table{cols, 1}).fit(4 + 4 + 1 + 3) // three cells of slack, headroom for twelve
	if ws[1] != 7 || ws[0] != 4 {
		t.Fatalf("the more important column takes the slack first: %v", ws)
	}
	ws = (table{cols, 1}).fit(4 + 4 + 1 + 9) // nine: more than the most important column can take
	if ws[1] != 10 || ws[0] != 7 {
		t.Fatalf("the rest spills to the next column: %v", ws)
	}
}
