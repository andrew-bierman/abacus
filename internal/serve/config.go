package serve

import (
	"io"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// Config is everything the server needs from its process; every edge is injectable for tests.
type Config struct {
	// Build makes the provider registry. It is called once; detection reruns after EntryTTL.
	Build   func() *core.Registry
	Version string
	// Demo serves invented fixture data: every result says "demo": true. Plans are made against it, flagged
	// and with demo digests (except config set, which has no plan to show); apply is always refused.
	Demo bool
	// Audit records every write; nil means the default file audit log. A write whose attempt cannot be
	// recorded is refused (fail closed), as everywhere.
	Audit  core.Audit
	Config core.ConfigStore
	// Setup builds the first-run setup over the detected modules; nil means the real one.
	Setup func([]core.Entry) *core.Setup
	Now   func() time.Time
	// Log gets lifecycle lines only (never a request, a parameter or a result); nil logs nothing.
	Log io.Writer

	Limits Limits
	// PlanTTL is how long a plan can be applied (default 10 minutes): a plan is only valid against the
	// state it was read from.
	PlanTTL time.Duration
	// EntryTTL is how long a provider detection is reused (default 30 seconds; a write drops it).
	EntryTTL time.Duration
	// PollBase, PollMax and PollSlow are the live-refresh rhythm, the TUI's: check every PollBase, back off
	// to PollMax after a check slower than PollSlow (defaults 5s, 60s, 2s). A tracker with no cheap
	// fingerprint is read in full every SlowBase (default 30s).
	PollBase, PollMax, PollSlow, SlowBase time.Duration
	// WriteStall ends the server when one response cannot be written for this long (default 30s).
	WriteStall time.Duration
	// WriteGrace is how long a write in flight may finish after shutdown or end of input before it is
	// cancelled (and so a result unknown), default 5s.
	WriteGrace time.Duration
}

func (c Config) withDefaults() Config {
	c.Limits = c.Limits.withDefaults()
	for p, v := range map[*time.Duration]time.Duration{&c.PlanTTL: 10 * time.Minute, &c.EntryTTL: 30 * time.Second, &c.PollBase: 5 * time.Second,
		&c.PollMax: 60 * time.Second, &c.PollSlow: 2 * time.Second, &c.SlowBase: 30 * time.Second, &c.WriteStall: 30 * time.Second, &c.WriteGrace: 5 * time.Second} {
		if *p <= 0 {
			*p = v
		}
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}
