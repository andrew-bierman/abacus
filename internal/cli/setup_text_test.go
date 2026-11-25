package cli

import (
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

func TestSetupText(t *testing.T) {
	for _, tc := range []struct {
		name   string
		states []core.StepState
		want   []string
		omit   []string
	}{
		{"done", []core.StepState{core.StepDone}, []string{"Nothing left to set up."}, []string{"setup --demo", "setup --yes"}},
		{"unavailable", []core.StepState{core.StepDone, core.StepUnavailable}, []string{"1 step(s) remaining:", "unavailable", "provider-capable build", "configured tools", "setup --demo --dry-run"}, []string{"Nothing left", "setup --yes"}},
		{"blocked", []core.StepState{core.StepBlocked}, []string{"1 step(s) remaining:", "blocked"}, []string{"Nothing left", "provider-capable build", "setup --yes"}},
		{"mixed", []core.StepState{core.StepDone, core.StepNeeded, core.StepBlocked, core.StepUnavailable}, []string{"3 step(s) remaining:", "needed", "blocked", "unavailable", "provider-capable build", "setup --yes"}, []string{"Nothing left"}},
		{"repair", []core.StepState{core.StepRepairing}, []string{"1 step(s) remaining:", "repair"}, []string{"Nothing left", "setup --yes"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var steps []core.StepInfo
			for _, state := range tc.states {
				steps = append(steps, core.StepInfo{State: state, Message: "fixture step"})
			}
			var out strings.Builder
			renderSetup(&out, steps, nil, false, false)
			for _, want := range tc.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("missing %q in %q", want, out.String())
				}
			}
			for _, omit := range tc.omit {
				if strings.Contains(out.String(), omit) {
					t.Errorf("unexpected %q in %q", omit, out.String())
				}
			}
		})
	}
}
