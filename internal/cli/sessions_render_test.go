package cli_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/agents"
	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
)

// sessionsMod serves the tab's bounded read and the one-shot complete read from different
// reports.
type sessionsMod struct {
	*modtest.Fake
	one  core.SessionsReport
	full core.SessionsReport
}

func (m sessionsMod) Sessions(context.Context) (core.SessionsReport, error) { return m.one, nil }

func (m sessionsMod) SessionsComplete(context.Context) (core.SessionsReport, error) {
	return m.full, nil
}

func sessionsFixture() sessionsMod {
	hdr := core.Header{Domain: core.DomainSessions, Title: "Sessions", State: core.Connected, Source: core.SourceLive}
	taken := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	partial := agents.Session{
		Harness: agents.HarnessClaude, PID: 1000, CWD: "/home/dev/shop", Title: "Invoice list",
		Model: "claude-sonnet-5", State: agents.StateThinking,
		Tokens: agents.Tokens{Input: 80}, Context: agents.Context{Used: 5000, Limit: 100000},
		Git: agents.GitStatus{Branch: "main"}, Ports: []agents.Port{{Port: 3000, PID: 1000}},
	}
	whole := partial
	whole.Tokens = agents.Tokens{Input: 100, Output: 20, CacheRead: 300, CacheCreate: 10}
	quotas := []agents.Quota{{Source: agents.HarnessClaude, Updated: time.Now(),
		Short: &agents.QuotaWindow{UsedPercent: 42},
		Long:  &agents.QuotaWindow{UsedPercent: 7, WindowMinutes: 30 * 24 * 60}}}
	return sessionsMod{
		Fake: &modtest.Fake{IDStr: "sessions", Det: core.Detection{State: core.Ready}},
		one:  core.SessionsReport{Header: hdr, Snapshot: agents.Snapshot{Taken: taken, Sessions: []agents.Session{partial}}},
		full: core.SessionsReport{Header: hdr, Snapshot: agents.Snapshot{
			Taken: taken, Sessions: []agents.Session{whole},
			Orphans:    []agents.Port{{Port: 3100, Command: "node", Project: "shop", Orphan: true}},
			Unreadable: []string{"claude: store unreadable"},
		}, Quotas: quotas},
	}
}

// tabOnlySessions is a provider without the drain read.
type tabOnlySessions struct {
	*modtest.Fake
	rep core.SessionsReport
}

func (m tabOnlySessions) Sessions(context.Context) (core.SessionsReport, error) { return m.rep, nil }

func TestSessionsAcceptsAProviderWithOnlyTheTabRead(t *testing.T) {
	f := sessionsFixture()
	m := tabOnlySessions{Fake: f.Fake, rep: f.one}
	code, out, _, _ := runIn(t, "", []core.Module{m}, "sessions", "--json")
	if code != 0 || !strings.Contains(out, `"domain": "sessions"`) || !strings.Contains(out, `"harness": "claude"`) {
		t.Fatalf("code=%d\n%s", code, out)
	}
}

func TestSessionsPrintsSessionsQuotaOrphansAndUnreadable(t *testing.T) {
	_, out, _, _ := runIn(t, "", []core.Module{sessionsFixture()}, "sessions")
	for _, want := range []string{
		"Sessions  connected  (live)",
		"    claude   1000    thinking  Invoice list  claude-sonnet-5  130 tok  5% ctx  main  ports 3000\n",
		"    quota claude  5h 42%  30d 7%\n",
		"    orphan port 3100  node  (shop)\n",
		"    unreadable claude: store unreadable\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "80 tok") {
		t.Errorf("printed the bounded read's partial tokens:\n%s", out)
	}
}

func TestSessionsMarksAStaleQuota(t *testing.T) {
	f := sessionsFixture()
	f.full.Quotas[0].Updated = time.Time{}
	_, out, _, _ := runIn(t, "", []core.Module{f}, "sessions")
	if !strings.Contains(out, "    quota claude  5h 42%  30d 7%  (stale)\n") {
		t.Fatalf("a gauge with no reported time must read stale:\n%s", out)
	}
}

func TestSessionsSanitizesTranscriptText(t *testing.T) {
	f := sessionsFixture()
	f.full.Sessions[0].Title = "line one\r\nline two\x1b[31m"
	f.full.Sessions[0].Model = "model\x1b]0;evil\x07"
	f.full.Orphans[0].Command = "node\x1b[2J"
	_, out, _, _ := runIn(t, "", []core.Module{f}, "sessions")
	if strings.ContainsAny(out, "\x1b\r") {
		t.Fatalf("control runes reached the terminal:\n%q", out)
	}
	if !strings.Contains(out, "line one  line two[31m") {
		t.Fatalf("the title was not flattened and kept printable:\n%s", out)
	}
}

func TestSessionsJSONIsTheTypedReport(t *testing.T) {
	_, out, _, _ := runIn(t, "", []core.Module{sessionsFixture()}, "sessions", "--json")
	var got struct {
		SchemaVersion int    `json:"schema_version"`
		Domain        string `json:"domain"`
		State         string `json:"state"`
		Summary       string `json:"summary"`
		Sessions      []struct {
			Harness string           `json:"harness"`
			PID     int              `json:"pid"`
			State   string           `json:"state"`
			Title   string           `json:"title"`
			Tokens  agents.Tokens    `json:"tokens"`
			Context agents.Context   `json:"context"`
			Git     agents.GitStatus `json:"git"`
		} `json:"sessions"`
		Quotas []agents.Quota `json:"quotas"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if got.SchemaVersion != 1 || got.Domain != "sessions" || got.State != "connected" {
		t.Fatalf("header = %+v", got)
	}
	if len(got.Sessions) != 1 {
		t.Fatalf("sessions = %+v", got.Sessions)
	}
	s := got.Sessions[0]
	if s.Harness != "claude" || s.PID != 1000 || s.State != "thinking" || s.Title != "Invoice list" {
		t.Errorf("session = %+v", s)
	}
	if s.Tokens.Total() != 130 || s.Context != (agents.Context{Used: 5000, Limit: 100000}) || s.Git.Branch != "main" {
		t.Errorf("session facts = %+v", s)
	}
	if len(got.Quotas) != 1 || got.Quotas[0].Source != "claude" || got.Quotas[0].Long == nil || got.Quotas[0].Long.UsedPercent != 7 {
		t.Errorf("quotas = %+v", got.Quotas)
	}
}
