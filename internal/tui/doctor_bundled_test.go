package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
)

func bundledEntries(lambe core.Detection) []core.Entry {
	var es []core.Entry
	for _, id := range core.KnownTools {
		det := core.Detection{State: core.Ready, Version: "1.0.0", Source: core.SourceBundled}
		if id == "lambe" {
			det = lambe
		}
		es = append(es, core.Entry{Module: &modtest.Fake{IDStr: id}, Detection: det})
	}
	return append(es, core.Entry{Module: &modtest.Fake{IDStr: "board"}, Detection: core.Detection{State: core.Absent, Detail: "no board"}})
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

func TestDoctorLeadsWithAbacusOwnCopyNotAMissingToolInARelease(t *testing.T) {
	core.SetBuildVersion("1.2.3")
	t.Cleanup(func() { core.SetBuildVersion("t") })
	v := doctorView(t, bundledEntries(core.Detection{State: core.Absent, Detail: "lambe not found on PATH"}), 200)
	for _, want := range []string{"Needs attention", "Bundled tools: 2 of 3 verified; 1 unusable (lambe)", "(unavailable)", "own copy of lambe is damaged or was moved", "(optional)", "adds the Board tab"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "missing") {
		t.Errorf("a release user is never told a tool is missing:\n%s", v)
	}
}

func TestDoctorShowsTheBundledLineWhenAllThreeAreVerified(t *testing.T) {
	core.SetBuildVersion("1.2.3")
	t.Cleanup(func() { core.SetBuildVersion("t") })
	v := doctorView(t, bundledEntries(core.Detection{State: core.Ready, Version: "1.0.0", Source: core.SourceBundled}), 100)
	if !strings.Contains(v, "Bundled tools: 3 of 3 verified") || strings.Contains(v, "unavailable") {
		t.Errorf("%s", v)
	}
}
