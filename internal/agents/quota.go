package agents

import (
	"encoding/json"
	"io/fs"
	"path"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// An account's rate limits, as a harness's own files record them. A window the file does not carry
// stays nil (unknown, never 0% used); core checks the share range and marks old reads stale.

const (
	shortWindowMinutes = 5 * 60
	longWindowMinutes  = 7 * 24 * 60
	// maxQuotaFile bounds a file abacus does not own: the real one is a few hundred bytes.
	maxQuotaFile = 64 << 10
)

type quotaWin struct {
	Pct     float64
	Resets  time.Time
	Minutes int64
}

type quotaRead struct {
	Short, Long *quotaWin
	Updated     time.Time
}

func (w *quotaWin) pct() *float64 {
	if w == nil {
		return nil
	}
	return &w.Pct
}

func (w *quotaWin) resets() string {
	if w == nil {
		return ""
	}
	return iso(w.Resets)
}

func (w *quotaWin) minutes() int64 {
	if w == nil {
		return 0
	}
	return w.Minutes
}

func (q quotaRead) limit(harness string) core.RateLimit {
	return core.RateLimit{
		Harness:     harness,
		FiveHourPct: q.Short.pct(), FiveHourResets: q.Short.resets(), ShortWindowMinutes: q.Short.minutes(),
		SevenDayPct: q.Long.pct(), SevenDayResets: q.Long.resets(), LongWindowMinutes: q.Long.minutes(),
		UpdatedAt: iso(q.Updated),
	}
}

func epochTime(sec int64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}

// ---- Codex: the rate_limits object of a token_count event ----

type codexWindow struct {
	UsedPercent *float64 `json:"used_percent"`
	Minutes     int64    `json:"window_minutes"`
	ResetsAt    int64    `json:"resets_at"`
}

type codexLimits struct {
	LimitID   string       `json:"limit_id"`
	Primary   *codexWindow `json:"primary"`
	Secondary *codexWindow `json:"secondary"`
}

// read folds the two slots by window length, not by position: on a free plan the primary slot holds
// a 30-day window. A model-specific allowance (a limit id other than the account's) is not the
// account's, and an object with no window is not a report.
func (l *codexLimits) read(timestamp string) (quotaRead, bool) {
	if l == nil || (l.LimitID != "" && l.LimitID != "codex") {
		return quotaRead{}, false
	}
	var q quotaRead
	if t, err := time.Parse(time.RFC3339Nano, timestamp); err == nil {
		q.Updated = t.UTC()
	}
	for _, w := range []*codexWindow{l.Primary, l.Secondary} {
		if w == nil || w.UsedPercent == nil {
			continue
		}
		win := &quotaWin{Pct: *w.UsedPercent, Resets: epochTime(w.ResetsAt), Minutes: w.Minutes}
		if w.Minutes > 0 && w.Minutes <= shortWindowMinutes {
			q.Short = win
		} else {
			q.Long = win
		}
	}
	return q, q.Short != nil || q.Long != nil
}

// ---- Claude Code: the file a status-line hook writes ----

// claudeQuotaFile is where a Claude Code status-line hook that persists the rate limits Claude gives
// it writes them (Claude keeps them out of its session files). It exists only when such a hook is set
// up; the live monitor reads the same file. Nothing here creates it.
const claudeQuotaFile = "abtop-rate-limits.json"

type claudeWindow struct {
	Pct      *float64 `json:"used_percentage"`
	ResetsAt int64    `json:"resets_at"`
	Minutes  int64    `json:"window_minutes"`
}

func (w *claudeWindow) read(def int64) *quotaWin {
	if w == nil || w.Pct == nil {
		return nil
	}
	m := w.Minutes
	if m <= 0 {
		m = def
	}
	return &quotaWin{Pct: *w.Pct, Resets: epochTime(w.ResetsAt), Minutes: m}
}

func parseClaudeQuota(raw []byte) (quotaRead, bool) {
	var f struct {
		FiveHour  *claudeWindow `json:"five_hour"`
		SevenDay  *claudeWindow `json:"seven_day"`
		UpdatedAt int64         `json:"updated_at"`
	}
	if json.Unmarshal(raw, &f) != nil {
		return quotaRead{}, false
	}
	q := quotaRead{Short: f.FiveHour.read(shortWindowMinutes), Long: f.SevenDay.read(longWindowMinutes), Updated: epochTime(f.UpdatedAt)}
	return q, q.Short != nil || q.Long != nil
}

func readClaudeQuota(fsys fs.FS, claudeDir string) (quotaRead, bool) {
	raw, err := readBounded(fsys, path.Join(claudeDir, claudeQuotaFile), maxQuotaFile)
	if err != nil {
		return quotaRead{}, false
	}
	return parseClaudeQuota(raw)
}
