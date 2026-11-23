package tui

import (
	"math/rand"
	"testing"
)

func sum(xs []int) (n int) {
	for _, x := range xs {
		n += x
	}
	return n
}

func TestSplitNeverOvershootsOrGoesNegative(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for total := -2; total <= 300; total++ {
		for i := 0; i < 20; i++ {
			var ts []track
			for j := 0; j < 1+rng.Intn(5); j++ {
				switch rng.Intn(3) {
				case 0:
					ts = append(ts, length(rng.Intn(80)))
				case 1:
					ts = append(ts, atLeast(rng.Intn(60)))
				default:
					ts = append(ts, fill(rng.Intn(4)))
				}
			}
			got := split(total, ts...)
			if len(got) != len(ts) {
				t.Fatalf("total=%d: %d sizes for %d tracks", total, len(got), len(ts))
			}
			for _, g := range got {
				if g < 0 {
					t.Fatalf("total=%d tracks=%v: negative size in %v", total, ts, got)
				}
			}
			if sum(got) > max(total, 0) {
				t.Fatalf("total=%d tracks=%v: %v sums to %d", total, ts, got, sum(got))
			}
		}
	}
}

func TestSplitSharesTheRemainderByWeight(t *testing.T) {
	for _, tc := range []struct {
		total int
		ts    []track
		want  []int
	}{
		{100, []track{length(10), fill(1), fill(3)}, []int{10, 23, 67}},
		{100, []track{length(30), atLeast(20), fill(1)}, []int{30, 45, 25}},
		{50, []track{length(30), length(30)}, []int{30, 20}}, // later fixed tracks are clamped, not dropped
		{10, []track{fill(1), fill(1), fill(1)}, []int{4, 3, 3}},
		{7, []track{atLeast(10), fill(1)}, []int{7, 0}}, // a minimum yields before it overshoots
		{20, []track{length(5)}, []int{5}},              // no flexible track: the rest stays unassigned
	} {
		got := split(tc.total, tc.ts...)
		if len(got) != len(tc.want) {
			t.Fatalf("%v: %v", tc, got)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("split(%d, %v) = %v, want %v", tc.total, tc.ts, got, tc.want)
				break
			}
		}
	}
}
