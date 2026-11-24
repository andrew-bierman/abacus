package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// The flags every command shares: --demo (fixtures, nothing contacted, nothing written) and, for the
// commands that read a tracker, --tracker DIR. Run owns the per-run state (counter on stdout, base
// context); the helpers below read it back from Deps.

// envDemo is the environment switch for demo mode, the same as --demo on every read command.
const envDemo = "ABACUS_DEMO"

func (d Deps) getenv(k string) string {
	if d.Getenv != nil {
		return d.Getenv(k)
	}
	return os.Getenv(k)
}

// bind registers --demo, and --tracker when the command reads a tracker.
func (o *Options) bind(fs *flag.FlagSet, tracker bool) {
	fs.BoolVar(&o.Demo, "demo", false, "use invented fixture data: nothing is contacted and nothing can be changed except as a --dry-run plan (also ABACUS_DEMO=1)")
	if tracker {
		fs.StringVar(&o.Tracker, "tracker", "", "the tracker folder to use (an absolute path to the folder that holds .beads); wins over ABACUS_BD_DIR and the settings")
	}
}

// resolve finishes the options after the flags are parsed: ABACUS_DEMO turns demo on, --tracker is
// validated like every tracker abacus switches to (core.ValidateTrackerDir), and a demo run is
// marked so its JSON says so. It prints and returns an exit code when the options are unusable.
func (o *Options) resolve(d Deps) (code int, ok bool) {
	if d.getenv(envDemo) == "1" {
		o.Demo = true
	}
	if o.Tracker != "" {
		if o.Demo {
			fmt.Fprintln(d.Err, "--tracker cannot be combined with demo mode: demo data has no tracker")
			return core.ExitUsage, false
		}
		dir, err := filepath.Abs(o.Tracker)
		if err == nil {
			dir, err = core.ValidateTrackerDir(dir)
		}
		if err != nil {
			fmt.Fprintf(d.Err, "--tracker: %s\n", core.Sanitize(err.Error()))
			return core.ExitUsage, false
		}
		o.Tracker = dir
	}
	if c, isCounter := d.Out.(*counter); isCounter {
		c.demo = o.Demo
	}
	return 0, true
}

// demoRun reports whether this run is in demo mode (resolve has run).
func demoRun(d Deps) bool {
	c, ok := d.Out.(*counter)
	return ok && c.demo
}

// refuseDemoWrite stops a write in demo mode unless it is a --dry-run plan: demo data has no tracker, so
// nothing can be applied to it, and a pretend "applied" would be a lie.
func refuseDemoWrite(d Deps, label string, dry bool) (int, bool) {
	if !demoRun(d) || dry {
		return 0, false
	}
	return failf(d, core.CodeRefused, "%s: demo data cannot be changed (nothing was written). --dry-run shows the plan\n", label), true
}

// demoAudit is the backstop behind refuseDemoWrite: in demo mode an audit record cannot be written,
// so any apply path that slipped past the guard is stopped by the gate (fail closed) before it runs.
type demoAudit struct{}

func (demoAudit) Record(core.AuditEntry) error {
	return fmt.Errorf("demo mode: nothing can be written")
}

// auditFor is the audit log a write uses.
func auditFor(d Deps) core.Audit {
	if demoRun(d) {
		return demoAudit{}
	}
	if d.Audit != nil {
		return d.Audit
	}
	return core.DefaultAudit()
}

// withTimeout is the context of a command: it ends after t, or when an interrupt arrives while a write
// is being applied (see trapped).
func (d Deps) withTimeout(t time.Duration) (context.Context, context.CancelFunc) {
	base := d.base
	if base == nil {
		base = context.Background()
	}
	return context.WithTimeout(base, t)
}

// trapped runs one apply with SIGINT and SIGTERM caught: the signal cancels the command's context
// instead of killing the process, so the running tool is stopped, the gate writes its outcome line (a
// cancelled write is "unknown", never "not applied") and the command prints and exits non-zero on its
// own. Outside an apply the default behavior stays, so a prompt can still be interrupted.
func trapped[T any](d Deps, apply func() T) T {
	if d.interrupt == nil {
		return apply()
	}
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case <-ch:
			d.interrupt()
		case <-done:
		}
	}()
	defer func() { signal.Stop(ch); close(done) }()
	return apply()
}
