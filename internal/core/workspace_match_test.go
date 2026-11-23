package core_test

import (
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

func TestMatchSessionLabel(t *testing.T) {
	for _, tc := range []struct {
		label, id string
		want      core.PlacementMatch
		ok        bool
	}{
		{"LAM-1", "lam-1", core.MatchExact, true},
		{"Lambe · LAM-1", "LAM-1", core.MatchExact, true},
		{"lambe: LAM-1", "LAM-1", core.MatchExact, true},
		{"Lambe - LAM-1", "LAM-1", core.MatchExact, true},
		{" LAM-1 ", "LAM-1", core.MatchExact, true},  // the label is trimmed
		{"LAM-1", "  LAM-1 ", core.MatchExact, true}, // so is the id
		{"\tLambe · LAM-1\n", "LAM-1", core.MatchExact, true},
		{"fix LAM-1 now", "LAM-1", core.MatchLabel, true},
		{"(LAM-1)", "LAM-1", core.MatchLabel, true},
		{"LAM-1.", "LAM-1", core.MatchLabel, true},
		{"LAM-1. Next", "LAM-1", core.MatchLabel, true},
		{"LAM-12", "LAM-1", "", false},
		{"XLAM-1", "LAM-1", "", false},
		{"LAM-1_x", "LAM-1", "", false},
		{"pre-LAM-1", "LAM-1", "", false},
		{"LAM-12 LAM-1", "LAM-1", core.MatchLabel, true},
		{"anything", "", "", false},
		{"a b c", "a", "", false}, // one-character ids never match
		{"ab", "ab", core.MatchExact, true},
		{"日本 LAM-1 語", "LAM-1", core.MatchLabel, true},
		{"日本LAM-1", "LAM-1", "", false}, // a letter before the id continues it
		// dotted child ids: bd-1 is not bd-1.2
		{"Lambe · bd-1.2", "bd-1", "", false},
		{"bd-1.2", "bd-1", "", false},
		{"fix bd-1.2 now", "bd-1", "", false},
		{"bd-1.a", "bd-1", "", false},
		{"bd-1.", "bd-1", core.MatchLabel, true},
		{"bd-1. 2", "bd-1", core.MatchLabel, true},
		{"bd-1 .2", "bd-1", core.MatchLabel, true},
		{"bd-1.2 bd-1", "bd-1", core.MatchLabel, true},
		{"Lambe · bd-1.2", "bd-1.2", core.MatchExact, true},
	} {
		got, ok := core.MatchSessionLabel(tc.label, tc.id, nil)
		if got != tc.want || ok != tc.ok {
			t.Errorf("MatchSessionLabel(%q, %q) = %q, %v; want %q, %v", tc.label, tc.id, got, ok, tc.want, tc.ok)
		}
	}
}

func TestLabelConventionsAreData(t *testing.T) {
	conv := []string{"agent/{id}", "[{id}]", "no id here", ""}
	if got, ok := core.MatchSessionLabel("Agent/LAM-1", "LAM-1", conv); !ok || got != core.MatchExact {
		t.Errorf("custom convention: %q %v", got, ok)
	}
	if got, ok := core.MatchSessionLabel("[lam-1]", "LAM-1", conv); !ok || got != core.MatchExact {
		t.Errorf("custom convention: %q %v", got, ok)
	}
	// a custom list replaces the defaults, and a template without {id} is dropped
	if got, _ := core.MatchSessionLabel("Lambe · LAM-1", "LAM-1", conv); got != core.MatchLabel {
		t.Errorf("the default Lambe form must not apply to a custom list: %q", got)
	}
	if got, ok := core.MatchSessionLabel("no id here", "LAM-1", conv); ok {
		t.Errorf("a template without {id} is not a convention: %q", got)
	}
	// nothing valid means the defaults
	if got, _ := core.MatchSessionLabel("Lambe · LAM-1", "LAM-1", []string{"nope"}); got != core.MatchExact {
		t.Errorf("an invalid list falls back to the defaults: %q", got)
	}
	if got := core.ValidConventions(nil); len(got) != 4 || got[1] != "Lambe · {id}" {
		t.Errorf("%v", got)
	}
}

func TestHandoffArgvIsTheOneSource(t *testing.T) {
	if got := strings.Join(core.HandoffArgv("", "mbp"), " "); got != "rex -S mbp" {
		t.Fatal(got)
	}
	if got := strings.Join(core.HandoffArgv("/opt/x/rex", "mbp"), " "); got != "/opt/x/rex -S mbp" {
		t.Fatal(got)
	}
}
