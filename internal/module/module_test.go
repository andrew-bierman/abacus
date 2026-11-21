package module_test

import (
	"context"
	"testing"

	"github.com/andrew-bierman/beaddeck/internal/module"
	"github.com/andrew-bierman/beaddeck/internal/module/modtest"
)

func TestRegistryReportsEachStateInOrderAndDetectsOnce(t *testing.T) {
	ready := &modtest.Fake{IDStr: "a", Det: module.Detection{State: module.Ready}}
	absent := &modtest.Fake{IDStr: "b", Det: module.Detection{State: module.Absent}}
	incompat := &modtest.Fake{IDStr: "c", Det: module.Detection{State: module.Incompatible}}
	entries := module.NewRegistry(ready, absent, incompat).Detect(context.Background())

	want := []module.State{module.Ready, module.Absent, module.Incompatible}
	for i, e := range entries {
		if e.Detection.State != want[i] {
			t.Fatalf("entry %d state = %s, want %s", i, e.Detection.State, want[i])
		}
	}
	for _, f := range []*modtest.Fake{ready, absent, incompat} {
		if f.DetectCalls != 1 {
			t.Fatalf("%s detected %d times, want 1", f.IDStr, f.DetectCalls)
		}
	}
	if got := module.ReadyOnly(entries); len(got) != 1 || got[0].Module.ID() != "a" {
		t.Fatalf("ReadyOnly() = %v, want only module a", got)
	}
}
