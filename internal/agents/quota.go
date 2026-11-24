package agents

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"
)

const (
	// QuotaStaleAfter is how old a quota read may be before its numbers stop being
	// trustworthy. It matches abtop's panel: past this age the tab dims the source and hides
	// the reset countdown rather than presenting a percentage from a rolled-over allowance.
	QuotaStaleAfter = 10 * time.Minute

	// quotaShortWindowMinutes and quotaLongWindowMinutes are Claude's documented window
	// lengths, and Codex uses the short one as the line between its two slots.
	quotaShortWindowMinutes = 5 * 60
	quotaLongWindowMinutes  = 7 * 24 * 60

	// maxClaudeQuotaBytes bounds a status-line file abacus does not own. The real shape is a
	// few hundred bytes; a corrupt or hostile writer must not turn one tick into an unbounded
	// allocation.
	maxClaudeQuotaBytes = 64 << 10
)

// QuotaWindow is one account rate-limit window. UsedPercent is how much of the window is
// spent, not how much is left, because both harnesses report it that way. WindowMinutes is
// the slot's length as the source records it, so a free plan's 30-day window is not drawn as
// a 5-hour gauge; zero means the source did not record a length.
type QuotaWindow struct {
	UsedPercent   float64   `json:"used_percent"`
	ResetsAt      time.Time `json:"resets_at,omitzero"`
	WindowMinutes int64     `json:"window_minutes,omitempty"`
}

// Quota is an account-level rate limit shared by every session on one agent account. It is
// deliberately not a field of Session: ten live sessions do not have ten different quotas. A
// nil window means the source did not report it, never "0% used".
type Quota struct {
	Source Harness `json:"source"`
	// Short is the near window (Claude's 5 hours, Codex's primary). Nil when unreported.
	Short *QuotaWindow `json:"short,omitempty"`
	// Long is the longer window (Claude's 7 days, Codex's secondary or a free plan's 30 days).
	Long *QuotaWindow `json:"long,omitempty"`
	// Updated is when the harness last recorded the numbers. A zero value is not fresh.
	Updated time.Time `json:"updated,omitzero"`
}

// Stale reports whether the read is older than QuotaStaleAfter, or never said when it was
// taken. A front-end dims a stale source and omits its countdown; it does not hide the bar,
// because the percentage is usually still approximately right.
func (q Quota) Stale(now time.Time) bool {
	return q.Updated.IsZero() || now.Sub(q.Updated) > QuotaStaleAfter
}

// ClaudeQuotaPath names the status-line file for one Claude config directory. root is the
// directory itself (normally ~/.claude), not a home. An empty root names nothing.
func ClaudeQuotaPath(root string) string {
	if root == "" {
		return ""
	}
	return filepath.Join(root, "abtop-rate-limits.json")
}

// ClaudeQuota returns the quota a Claude Code status-line hook last wrote under root. Claude
// keeps rate limits out of its transcripts: they reach the hook on stdin, and abtop's setup
// persists them to abtop-rate-limits.json. That is a data file, not a run-time dependency on
// abtop: any hook that writes the documented shape works, and abacus never runs abtop.
//
// The file is account-level, so read it once per tick per config directory rather than once
// per session. A missing, oversized, unreadable or malformed file reports no quota.
func ClaudeQuota(root string) (Quota, bool) {
	path := ClaudeQuotaPath(root)
	if path == "" {
		return Quota{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return Quota{}, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > maxClaudeQuotaBytes {
		return Quota{}, false
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxClaudeQuotaBytes))
	if err != nil {
		return Quota{}, false
	}
	return parseClaudeQuota(raw)
}

// claudeQuotaFile is the shape abtop's StatusLine hook writes. Windows are pointers so a file
// that reports only one of them leaves the other absent instead of showing 0% used.
type claudeQuotaFile struct {
	FiveHour  *claudeQuotaWindow `json:"five_hour"`
	SevenDay  *claudeQuotaWindow `json:"seven_day"`
	UpdatedAt int64              `json:"updated_at"`
}

type claudeQuotaWindow struct {
	UsedPercentage float64 `json:"used_percentage"`
	ResetsAt       int64   `json:"resets_at"`
	WindowMinutes  int64   `json:"window_minutes"`
}

func parseClaudeQuota(raw []byte) (Quota, bool) {
	var f claudeQuotaFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return Quota{}, false
	}
	q := Quota{
		Source:  HarnessClaude,
		Short:   f.FiveHour.quotaWindow(quotaShortWindowMinutes),
		Long:    f.SevenDay.quotaWindow(quotaLongWindowMinutes),
		Updated: unixTime(f.UpdatedAt),
	}
	if q.Short == nil && q.Long == nil {
		return Quota{}, false
	}
	return q, true
}

// quotaWindow converts one hook window, supplying the documented length when the hook omits
// it. A zero or negative length is treated as omitted: the hook has never emitted an
// intentional zero-length window.
func (w *claudeQuotaWindow) quotaWindow(defaultMinutes int64) *QuotaWindow {
	if w == nil {
		return nil
	}
	minutes := w.WindowMinutes
	if minutes <= 0 {
		minutes = defaultMinutes
	}
	return &QuotaWindow{
		UsedPercent:   w.UsedPercentage,
		ResetsAt:      unixTime(w.ResetsAt),
		WindowMinutes: minutes,
	}
}

// codexQuotaLimits is the account rate limit a Codex token_count event may carry. The rollout
// reader folds it into the session it already returns, so quota costs no second pass.
type codexQuotaLimits struct {
	LimitID   string            `json:"limit_id"`
	Primary   *codexQuotaWindow `json:"primary"`
	Secondary *codexQuotaWindow `json:"secondary"`
}

type codexQuotaWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int64   `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"`
}

// quota folds one rate_limits object into the account's two slots. The slot is chosen by
// window length, not by field position: on a free plan the primary slot holds a 30-day window.
// A model-specific allowance (limit_id names a model, not the account) is not the account's
// quota and is ignored. An object with no windows is not a report.
func (l *codexQuotaLimits) quota(timestamp string) (Quota, bool) {
	if l.LimitID != "" && l.LimitID != "codex" {
		return Quota{}, false
	}
	q := Quota{Source: HarnessCodex, Updated: parseQuotaTime(timestamp)}
	for _, w := range [2]*codexQuotaWindow{l.Primary, l.Secondary} {
		if w == nil {
			continue
		}
		window := &QuotaWindow{
			UsedPercent:   w.UsedPercent,
			ResetsAt:      unixTime(w.ResetsAt),
			WindowMinutes: w.WindowMinutes,
		}
		if w.WindowMinutes <= quotaShortWindowMinutes {
			q.Short = window
		} else {
			q.Long = window
		}
	}
	if q.Short == nil && q.Long == nil {
		return Quota{}, false
	}
	return q, true
}

// unixTime converts a source's epoch seconds. Non-positive values mean "not reported" and
// stay zero, so JSON omits them instead of printing 1970. UTC keeps the value independent of
// the machine's zone.
func unixTime(sec int64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}

func parseQuotaTime(ts string) time.Time {
	if ts == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
