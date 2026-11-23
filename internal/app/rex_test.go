package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/cli"
	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/lambe"
	"github.com/andrew-bierman/abacus/internal/rex"
)

// stubRex writes a script that is not Rex: it answers the two reads from fixed text, so no real
// rex, session or socket is ever involved. mode is "up", "down" (exit 4) or "newer" (an API
// revision this build does not support).
func stubRex(t *testing.T, mode string) string {
	t.Helper()
	body := `#!/bin/sh
case "$*" in
*version*) echo '{"rex_version":"1","platform":"x","dirty":false,"api_revision":` + map[string]string{"up": "13", "down": "13", "newer": "99"}[mode] + `}' ;;
*ls*) [ "` + mode + `" = down ] && exit 4; echo '{"sessions":[{"session_id":"s1","label":"Lambe · T-1"}]}' ;;
esac
`
	p := filepath.Join(t.TempDir(), "stub-not-rex")
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAnAbsentRexIsInvisible(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	e := env(t, nil)
	entries := Build(cli.Options{}, e).Detect(context.Background())
	var rexEntry *core.Entry
	for i := range entries {
		if entries[i].Module.ID() == "rex" {
			rexEntry = &entries[i]
		}
	}
	if rexEntry == nil || rexEntry.Detection.State != core.Absent {
		t.Fatalf("rex must be registered and absent: %+v", rexEntry)
	}
	for _, m := range core.VersionReport("t", entries).Modules {
		if m.ID == "rex" {
			t.Fatal("an absent Rex is not listed by version")
		}
	}
	doc := core.DoctorReport(context.Background(), "t", entries)
	for _, m := range doc.Modules {
		if m.ID == "rex" {
			t.Fatal("an absent Rex is not in Doctor")
		}
	}
	for _, tl := range doc.Overview.Tools {
		if tl.ID == "rex" {
			t.Fatal("an absent Rex is not in the tool list")
		}
	}
}

func TestABACUS_REX_BINOverridesAndRexAppearsOnlyWhenPresent(t *testing.T) {
	for mode, wantCheck := range map[string]core.CheckStatus{"up": core.StatusOK, "down": core.StatusSkip} {
		e := env(t, map[string]string{"ABACUS_REX_BIN": stubRex(t, mode)})
		entries := Build(cli.Options{}, e).Detect(context.Background())
		var got core.Detection
		for _, en := range entries {
			if en.Module.ID() == "rex" {
				got = en.Detection
			}
		}
		if got.State != core.Ready || got.Source != core.SourceOverride || got.Path == "" {
			t.Fatalf("%s: %+v", mode, got)
		}
		doc := core.DoctorReport(context.Background(), "t", entries)
		var found bool
		for _, m := range doc.Modules {
			if m.ID == "rex" {
				found = true
				if m.Checks[0].Status != wantCheck {
					t.Errorf("%s: check %+v, want %s", mode, m.Checks, wantCheck)
				}
			}
		}
		if !found {
			t.Errorf("%s: Rex is present, so Doctor lists it", mode)
		}
		var inTools bool
		for _, tl := range doc.Overview.Tools {
			inTools = inTools || tl.ID == "rex"
		}
		if !inTools {
			t.Errorf("%s: Rex is present, so the tool list shows it", mode)
		}
	}
}

func TestAnUnsupportedRexRevisionIsAWarningAndNeverFailsDoctorOrBlocksReady(t *testing.T) {
	e := env(t, map[string]string{"ABACUS_REX_BIN": stubRex(t, "newer")})
	entries := Build(cli.Options{}, e).Detect(context.Background())
	doc := core.DoctorReport(context.Background(), "t", entries)
	var warned bool
	for _, m := range doc.Modules {
		if m.ID == "rex" {
			c := m.Checks[0]
			warned = c.Status == core.StatusWarn && strings.Contains(c.Detail, "99")
		}
	}
	if !warned {
		t.Fatalf("want a warn naming the revision: %+v", doc.Modules)
	}
	for _, m := range doc.Modules {
		for _, c := range m.Checks {
			if m.ID == "rex" && c.Status == core.StatusFail {
				t.Fatal("Rex must never fail Doctor")
			}
		}
	}
	for _, a := range doc.Overview.Attention {
		if strings.Contains(strings.ToLower(a), "rex") {
			t.Fatalf("Rex must never make Doctor ask for attention: %q", a)
		}
	}
}

func TestStatusGainsAWorkspaceOnlyWhenRexPlacesSomething(t *testing.T) {
	e := env(t, map[string]string{"ABACUS_REX_BIN": stubRex(t, "up")})
	entries := Build(cli.Options{}, e).Detect(context.Background())
	// The stub has a session for T-1; place it directly through the registry's provider.
	rep := core.ReadWorkspace(context.Background(), entries, []core.WorkspaceItem{{Kind: core.WorkspaceRun, ID: "T-1"}, {Kind: core.WorkspaceRun, ID: "T-2"}})
	if len(rep.Placements) != 1 || rep.Placements[0].Session != "s1" {
		t.Fatalf("%+v", rep)
	}
	// Without Rex the section is absent from status.
	t.Setenv("PATH", "/usr/bin:/bin")
	none := Build(cli.Options{}, env(t, nil)).Detect(context.Background())
	if d := core.ReadDomains(context.Background(), none); d.Workspace != nil {
		t.Fatalf("%+v", d.Workspace)
	}
}

// The handoff in the workspace adapter and the one Lambe's Queues tab runs today are the same
// command, so the jump ladder's first rung does not change what `o` does.
func TestTheFirstRungIsTheExistingRexHandoff(t *testing.T) {
	m := lambe.New(lambe.Options{})
	for _, host := range []string{"mbp", "me@host.local"} {
		if a, b := strings.Join(m.RexArgv(host), " "), strings.Join(rex.HandoffArgv(host), " "); a != b {
			t.Errorf("%q != %q", a, b)
		}
	}
}

func TestDemoRegistryProvidesDemoPlacementsWithoutRex(t *testing.T) {
	reg := Build(cli.Options{Demo: true}, Env{Getenv: func(string) string { return "" }})
	entries := reg.Detect(context.Background())
	for _, e := range entries {
		if e.Module.ID() == "rex" {
			t.Fatal("the demo must not register the real Rex adapter")
		}
	}
	p := core.ResolveProviders(entries).Workspace
	if p == nil {
		t.Fatal("the demo provides a workspace")
	}
	items := []core.WorkspaceItem{{Kind: core.WorkspaceRun, ID: "T-1", Host: "operators-mac"}, {Kind: core.WorkspaceLease, ID: "ac-1k2"}, {Kind: core.WorkspaceRun, ID: "T-2"}}
	rep := core.ReadWorkspaceFrom(context.Background(), p, items)
	if len(rep.Placements) != 1 || len(rep.Ambiguous) != 1 || rep.Ambiguous[0].Item.ID != "ac-1k2" {
		t.Fatalf("%+v", rep)
	}
	if plan := p.Open(rep.Placements[0]); len(plan.Rungs) != 0 || plan.Copy != "rex -S operators-mac" {
		t.Fatalf("a demo plan runs nothing: %+v", plan)
	}
}
