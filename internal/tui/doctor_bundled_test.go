package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
	"github.com/andrew-bierman/abacus/internal/embedtools/embedtest"
)

func bundledEntries(lambe core.Detection) []core.Entry {
	var es []core.Entry
	for _, id := range core.KnownTools {
		det := core.Detection{State: core.Ready, Version: "1.0.0", Source: core.SourceEmbedded}
		if id == "lambe" {
			det = lambe
		}
		es = append(es, core.Entry{Module: &modtest.Fake{IDStr: id}, Detection: det})
	}
	return es
}

func doctorView(t *testing.T, es []core.Entry, w int) string {
	t.Helper()
	ov := core.BuildOverview(context.Background(), es)
	d := newDoctorSection(nil)
	d.SetTheme(asciiTheme())
	var m tea.Model = d
	m, _ = m.Update(doctorMsg{report: core.Report{Overview: &ov}})
	return sized(m, w, 60).View()
}

func TestDoctorLeadsWithWhatFailedAndTheOneFixNotAMissingTool(t *testing.T) {
	t.Cleanup(core.SetEmbeddedForTest(embedtest.Standard().FS))
	v := doctorView(t, bundledEntries(core.Detection{State: core.Absent, Detail: "lambe is not available (bundled copy ignored: could not write into /x: permission denied)"}), 260)
	for _, want := range []string{"Needs attention", "Built-in tools: 3 of 4 ready; 1 unusable (lambe)", "(unavailable)", "The built-in lambe could not be set up: could not write into /x: permission denied", "ABACUS_TOOLS_DIR"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q:\n%s", want, v)
		}
	}
	for _, bad := range []string{"missing", "install", "dev-bundle"} {
		if strings.Contains(v, bad) {
			t.Errorf("a user is never told %q:\n%s", bad, v)
		}
	}
}

func TestDoctorShowsTheBuiltInLineWhenAllAreReady(t *testing.T) {
	t.Cleanup(core.SetEmbeddedForTest(embedtest.Standard().FS))
	v := doctorView(t, bundledEntries(core.Detection{State: core.Ready, Version: "1.0.0", Source: core.SourceEmbedded}), 100)
	if !strings.Contains(v, "Built-in tools: 4 of 4 ready") || strings.Contains(v, "unavailable") || !strings.Contains(v, "built in") {
		t.Errorf("%s", v)
	}
}
