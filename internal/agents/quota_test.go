package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Quota fixtures are synthetic: invented directories, ids and timestamps, never a captured
// status-line file or rollout.

func writeClaudeQuota(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	writeCodexFile(t, filepath.Join(dir, "abtop-rate-limits.json"), body)
	return dir
}

func quotaTokenCountLine(t *testing.T, timestamp string, limits map[string]any) string {
	t.Helper()
	return codexLineAt(t, timestamp, "event_msg", map[string]any{
		"type":        "token_count",
		"rate_limits": limits,
	})
}

func quotaLimits(primary, secondary map[string]any) map[string]any {
	return map[string]any{"limit_id": "codex", "primary": primary, "secondary": secondary}
}

func quotaSlot(used float64, minutes int64, resets int64) map[string]any {
	return map[string]any{
		"used_percent":   used,
		"window_minutes": minutes,
		"resets_at":      resets,
	}
}

func quotaWindowsEqual(got, want *QuotaWindow) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return got.UsedPercent == want.UsedPercent &&
		got.WindowMinutes == want.WindowMinutes &&
		got.ResetsAt.Equal(want.ResetsAt)
}

func TestClaudeQuotaReadsBothWindows(t *testing.T) {
	dir := writeClaudeQuota(t, `{
		"source": "claude",
		"five_hour": {"used_percentage": 35, "resets_at": 1774715000},
		"seven_day": {"used_percentage": 12.5, "resets_at": 1775320000},
		"updated_at": 1774714400
	}`)

	q, ok := ClaudeQuota(dir)
	if !ok {
		t.Fatal("ClaudeQuota() reported no quota")
	}
	if q.Source != HarnessClaude {
		t.Errorf("source = %q, want %q", q.Source, HarnessClaude)
	}
	wantShort := &QuotaWindow{
		UsedPercent: 35, ResetsAt: time.Unix(1774715000, 0).UTC(), WindowMinutes: 300,
	}
	if !quotaWindowsEqual(q.Short, wantShort) {
		t.Errorf("short = %+v, want %+v", q.Short, wantShort)
	}
	wantLong := &QuotaWindow{
		UsedPercent: 12.5, ResetsAt: time.Unix(1775320000, 0).UTC(), WindowMinutes: 7 * 24 * 60,
	}
	if !quotaWindowsEqual(q.Long, wantLong) {
		t.Errorf("long = %+v, want %+v", q.Long, wantLong)
	}
	if want := time.Unix(1774714400, 0).UTC(); !q.Updated.Equal(want) {
		t.Errorf("updated = %v, want %v", q.Updated, want)
	}
}

func TestClaudeQuotaOptionalWindowAndLength(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantShort *QuotaWindow
		wantLong  *QuotaWindow
	}{
		{
			name:      "one window only",
			body:      `{"five_hour":{"used_percentage":5}}`,
			wantShort: &QuotaWindow{UsedPercent: 5, WindowMinutes: 300},
		},
		{
			name:     "recorded long length",
			body:     `{"seven_day":{"used_percentage":80,"window_minutes":43200}}`,
			wantLong: &QuotaWindow{UsedPercent: 80, WindowMinutes: 43200},
		},
		{
			name:      "recorded short length",
			body:      `{"five_hour":{"used_percentage":1,"window_minutes":60}}`,
			wantShort: &QuotaWindow{UsedPercent: 1, WindowMinutes: 60},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q, ok := ClaudeQuota(writeClaudeQuota(t, c.body))
			if !ok {
				t.Fatal("ClaudeQuota() reported no quota")
			}
			if !quotaWindowsEqual(q.Short, c.wantShort) {
				t.Errorf("short = %+v, want %+v", q.Short, c.wantShort)
			}
			if !quotaWindowsEqual(q.Long, c.wantLong) {
				t.Errorf("long = %+v, want %+v", q.Long, c.wantLong)
			}
		})
	}
}

func TestClaudeQuotaMissingOrUnusable(t *testing.T) {
	cases := map[string]string{
		"empty file":      "",
		"invalid JSON":    `not json`,
		"neither window":  `{"source":"claude","updated_at":1774714400}`,
		"windows null":    `{"five_hour":null,"seven_day":null}`,
		"truncated field": `{"five_hour":{"used_percentage":`,
		"oversized":       `{"five_hour":{"used_percentage":5},"pad":"` + strings.Repeat("x", maxClaudeQuotaBytes) + `"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := ClaudeQuota(writeClaudeQuota(t, body)); ok {
				t.Error("ClaudeQuota() reported a quota from an unusable file")
			}
		})
	}
	if _, ok := ClaudeQuota(t.TempDir()); ok {
		t.Error("ClaudeQuota() reported a quota without a file")
	}
	if _, ok := ClaudeQuota(""); ok {
		t.Error("ClaudeQuota() reported a quota for an empty root")
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "abtop-rate-limits.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, ok := ClaudeQuota(dir); ok {
		t.Error("ClaudeQuota() reported a quota when the file is a directory")
	}
}

func TestClaudeQuotaWithoutTimestampIsStale(t *testing.T) {
	q, ok := ClaudeQuota(writeClaudeQuota(t, `{"five_hour":{"used_percentage":5}}`))
	if !ok {
		t.Fatal("ClaudeQuota() reported no quota")
	}
	if !q.Updated.IsZero() {
		t.Errorf("updated = %v, want zero when the hook records none", q.Updated)
	}
	if !q.Stale(time.Now()) {
		t.Error("a quota with no timestamp is not stale")
	}
}

func TestClaudeQuotaAcceptsTheSizeLimit(t *testing.T) {
	prefix := `{"five_hour":{"used_percentage":5},"pad":"`
	suffix := `"}`
	// writeCodexFile adds the trailing newline, so the file lands exactly on the cap.
	padding := maxClaudeQuotaBytes - 1 - len(prefix) - len(suffix)
	q, ok := ClaudeQuota(writeClaudeQuota(t, prefix+strings.Repeat("x", padding)+suffix))
	if !ok {
		t.Fatal("ClaudeQuota() rejected a file at the size cap")
	}
	if q.Short == nil || q.Short.UsedPercent != 5 {
		t.Errorf("short = %+v, want the window at the size cap", q.Short)
	}
}

func TestClaudeQuotaPath(t *testing.T) {
	if got := ClaudeQuotaPath("/home/dev/.claude"); got != "/home/dev/.claude/abtop-rate-limits.json" {
		t.Errorf("path = %q", got)
	}
	if got := ClaudeQuotaPath(""); got != "" {
		t.Errorf("empty root path = %q, want empty", got)
	}
}

func TestQuotaStale(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	fresh := Quota{Updated: now.Add(-9 * time.Minute)}
	if fresh.Stale(now) {
		t.Error("a nine-minute-old read is stale")
	}
	old := Quota{Updated: now.Add(-11 * time.Minute)}
	if !old.Stale(now) {
		t.Error("an eleven-minute-old read is not stale")
	}
	if (Quota{Updated: now.Add(-10 * time.Minute)}).Stale(now) {
		t.Error("a read exactly at the cutoff is stale")
	}
	if !(Quota{}).Stale(now) {
		t.Error("a read with no timestamp is not stale")
	}
	if (Quota{Updated: now.Add(time.Minute)}).Stale(now) {
		t.Error("a timestamp in the future is stale")
	}
}

func TestQuotaJSONOmitsUnreportedFields(t *testing.T) {
	q := Quota{
		Source: HarnessClaude,
		Short:  &QuotaWindow{UsedPercent: 5, WindowMinutes: 300},
	}
	raw, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"source":"claude","short":{"used_percent":5,"window_minutes":300}}`
	if string(raw) != want {
		t.Errorf("JSON = %s, want %s", raw, want)
	}
}

func TestCodexQuotaReadsAccountWindows(t *testing.T) {
	path := writeCodexRollout(t, t.TempDir(),
		codexMeta(t, "ses_quota", codexTestCWD),
		quotaTokenCountLine(t, "2026-11-24T10:00:00Z",
			quotaLimits(quotaSlot(9, 300, 1774686045), quotaSlot(14, 10080, 1775186466))),
	)

	q := readCodex(t, &CodexStore{}, path).Quota
	if q == nil {
		t.Fatal("rollout reported no quota")
	}
	if q.Source != HarnessCodex {
		t.Errorf("source = %q, want %q", q.Source, HarnessCodex)
	}
	wantShort := &QuotaWindow{
		UsedPercent: 9, ResetsAt: time.Unix(1774686045, 0).UTC(), WindowMinutes: 300,
	}
	if !quotaWindowsEqual(q.Short, wantShort) {
		t.Errorf("short = %+v, want %+v", q.Short, wantShort)
	}
	wantLong := &QuotaWindow{
		UsedPercent: 14, ResetsAt: time.Unix(1775186466, 0).UTC(), WindowMinutes: 10080,
	}
	if !quotaWindowsEqual(q.Long, wantLong) {
		t.Errorf("long = %+v, want %+v", q.Long, wantLong)
	}
	if want := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC); !q.Updated.Equal(want) {
		t.Errorf("updated = %v, want %v", q.Updated, want)
	}
}

func TestCodexQuotaWithoutLimitID(t *testing.T) {
	path := writeCodexRollout(t, t.TempDir(),
		quotaTokenCountLine(t, "2026-11-24T10:00:00Z", map[string]any{
			"primary": quotaSlot(9, 300, 1774686045),
		}),
	)
	q := readCodex(t, &CodexStore{}, path).Quota
	if q == nil || q.Short == nil || q.Short.UsedPercent != 9 {
		t.Fatalf("quota = %+v, want an account quota without limit_id", q)
	}
}

func TestCodexQuotaIgnoresAnEmptyReport(t *testing.T) {
	path := writeCodexRollout(t, t.TempDir(),
		quotaTokenCountLine(t, "2026-11-24T10:00:00Z",
			quotaLimits(quotaSlot(25, 300, 1774686045), quotaSlot(4, 10080, 1775186466))),
		quotaTokenCountLine(t, "2026-11-24T10:01:00Z",
			map[string]any{"limit_id": "codex", "primary": nil, "secondary": nil}),
	)
	q := readCodex(t, &CodexStore{}, path).Quota
	if q == nil || q.Short == nil || q.Short.UsedPercent != 25 {
		t.Fatalf("quota = %+v, want the earlier account quota", q)
	}

	only := writeCodexRollout(t, t.TempDir(),
		quotaTokenCountLine(t, "2026-11-24T10:00:00Z",
			map[string]any{"limit_id": "codex", "primary": nil, "secondary": nil}),
	)
	if q := readCodex(t, &CodexStore{}, only).Quota; q != nil {
		t.Errorf("quota = %+v, want nil for an empty report", q)
	}
}

func TestCodexQuotaWithoutTimestampIsStale(t *testing.T) {
	path := writeCodexRollout(t, t.TempDir(),
		quotaTokenCountLine(t, "not-a-time",
			quotaLimits(quotaSlot(9, 300, 1774686045), nil)),
	)
	q := readCodex(t, &CodexStore{}, path).Quota
	if q == nil {
		t.Fatal("rollout reported no quota")
	}
	if !q.Updated.IsZero() {
		t.Errorf("updated = %v, want zero for an unparsable event time", q.Updated)
	}
	if !q.Stale(time.Now()) {
		t.Error("a quota with no usable timestamp is not stale")
	}
}

func TestCodexQuotaIgnoresModelSpecificLimits(t *testing.T) {
	path := writeCodexRollout(t, t.TempDir(),
		quotaTokenCountLine(t, "2026-11-24T10:00:00Z",
			quotaLimits(quotaSlot(25, 300, 1774686045), quotaSlot(4, 10080, 1775186466))),
		quotaTokenCountLine(t, "2026-11-24T10:01:00Z", map[string]any{
			"limit_id":   "codex_bengalfox",
			"limit_name": "GPT-5.3-Codex-Spark",
			"primary":    quotaSlot(0, 300, 1774686045),
			"secondary":  quotaSlot(0, 10080, 1775186466),
		}),
	)

	q := readCodex(t, &CodexStore{}, path).Quota
	if q == nil {
		t.Fatal("rollout reported no quota")
	}
	if q.Short == nil || q.Short.UsedPercent != 25 {
		t.Errorf("short = %+v, want the account's 25%%", q.Short)
	}
	if q.Long == nil || q.Long.UsedPercent != 4 {
		t.Errorf("long = %+v, want the account's 4%%", q.Long)
	}
	if want := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC); !q.Updated.Equal(want) {
		t.Errorf("updated = %v, want the account event's %v", q.Updated, want)
	}
}

func TestCodexQuotaFreePlanUsesLongSlot(t *testing.T) {
	path := writeCodexRollout(t, t.TempDir(),
		quotaTokenCountLine(t, "2026-11-24T10:00:00Z", map[string]any{
			"limit_id":  "codex",
			"primary":   quotaSlot(48, 43200, 1780000000),
			"secondary": nil,
			"plan_type": "free",
		}),
	)

	q := readCodex(t, &CodexStore{}, path).Quota
	if q == nil {
		t.Fatal("rollout reported no quota")
	}
	if q.Short != nil {
		t.Errorf("short = %+v, want nil", q.Short)
	}
	wantLong := &QuotaWindow{
		UsedPercent: 48, ResetsAt: time.Unix(1780000000, 0).UTC(), WindowMinutes: 43200,
	}
	if !quotaWindowsEqual(q.Long, wantLong) {
		t.Errorf("long = %+v, want %+v", q.Long, wantLong)
	}
}

func TestCodexQuotaLastEventWins(t *testing.T) {
	path := writeCodexRollout(t, t.TempDir(),
		quotaTokenCountLine(t, "2026-11-24T10:00:00Z",
			quotaLimits(quotaSlot(9, 300, 1774686045), quotaSlot(14, 10080, 1775186466))),
		quotaTokenCountLine(t, "2026-11-24T10:05:00Z",
			quotaLimits(quotaSlot(20, 300, 1774686100), quotaSlot(30, 10080, 1775186500))),
	)

	q := readCodex(t, &CodexStore{}, path).Quota
	if q == nil {
		t.Fatal("rollout reported no quota")
	}
	if q.Short == nil || q.Short.UsedPercent != 20 {
		t.Errorf("short = %+v, want the later 20%%", q.Short)
	}
	if q.Long == nil || q.Long.UsedPercent != 30 {
		t.Errorf("long = %+v, want the later 30%%", q.Long)
	}
	if want := time.Date(2026, 11, 24, 10, 5, 0, 0, time.UTC); !q.Updated.Equal(want) {
		t.Errorf("updated = %v, want %v", q.Updated, want)
	}
}

func TestCodexQuotaFollowsTheRolloutCursor(t *testing.T) {
	path := writeCodexRollout(t, t.TempDir(),
		quotaTokenCountLine(t, "2026-11-24T10:00:00Z",
			quotaLimits(quotaSlot(9, 300, 1774686045), nil)),
	)
	store := &CodexStore{}
	if q := readCodex(t, store, path).Quota; q == nil || q.Short == nil || q.Short.UsedPercent != 9 {
		t.Fatalf("first read = %+v, want 9%%", q)
	}

	appendCodexRollout(t, path, quotaTokenCountLine(t, "2026-11-24T10:05:00Z",
		quotaLimits(quotaSlot(20, 300, 1774686100), nil))+"\n")
	if q := readCodex(t, store, path).Quota; q == nil || q.Short == nil || q.Short.UsedPercent != 20 {
		t.Fatalf("after append = %+v, want 20%%", q)
	}

	next := quotaTokenCountLine(t, "2026-11-24T10:10:00Z",
		quotaLimits(quotaSlot(45, 300, 1774686400), nil))
	appendCodexRollout(t, path, next[:len(next)/2])
	if q := readCodex(t, store, path).Quota; q == nil || q.Short == nil || q.Short.UsedPercent != 20 {
		t.Fatalf("with a partial line = %+v, want the previous 20%%", q)
	}
	appendCodexRollout(t, path, next[len(next)/2:]+"\n")
	if q := readCodex(t, store, path).Quota; q == nil || q.Short == nil || q.Short.UsedPercent != 45 {
		t.Fatalf("after completing the line = %+v, want 45%%", q)
	}
}

func TestCodexQuotaAbsentUntilReported(t *testing.T) {
	path := writeCodexRollout(t, t.TempDir(),
		codexMeta(t, "ses_quota", codexTestCWD),
		codexTokenCount(t,
			codexCounts{input: 10, output: 2},
			codexCounts{input: 10, output: 2}, 200_000),
	)
	if q := readCodex(t, &CodexStore{}, path).Quota; q != nil {
		t.Errorf("quota = %+v, want nil when no event reported one", q)
	}
}
