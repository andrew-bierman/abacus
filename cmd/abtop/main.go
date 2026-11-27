// Command abtop is the standalone full-screen monitor for the AI coding agents running on this
// machine. It is a front-end over the sessions read the abacus Sessions tab renders; it starts,
// drives and stops no agent.
package main

import (
	"context"
	"os"
	"sync"

	"github.com/andrew-bierman/abacus/internal/abtop"
	"github.com/andrew-bierman/abacus/internal/agents"
	"github.com/andrew-bierman/abacus/internal/core"
)

// version is overridden at release time with -ldflags.
var version = "dev"

func main() {
	once, tick := sessionReaders(detectModules())
	code := abtop.Run(os.Args[1:], abtop.Deps{
		Out:     os.Stdout,
		Err:     os.Stderr,
		Version: version,
		Read:    once,
		Tick:    tick,
		Host:    agents.NativeHost{}.Vitals,
	})
	core.CleanupSidecarEnv()
	os.Exit(code)
}

// detectModules is the monitor's composition edge: the Sessions domain registered alone, because
// the monitor shows one domain. The registry is built on the first read, so --version and a usage
// error never touch the machine.
func detectModules() func(context.Context) []core.Entry {
	var (
		build   sync.Once
		entries []core.Entry
	)
	return func(ctx context.Context) []core.Entry {
		build.Do(func() {
			entries = core.NewRegistry(core.NewSessions(core.SessionsOptions{})).Detect(ctx)
		})
		return entries
	}
}

// sessionReaders maps one detection to the monitor's two reads. Read() drains a long transcript
// for --once and --json; Tick() is the tab's bounded read, so a frame refresh never waits on a
// backlog.
func sessionReaders(detect func(context.Context) []core.Entry) (once, tick abtop.Reader) {
	once = func(ctx context.Context) (core.SessionsReport, error) {
		return core.ReadDomain(ctx, detect(ctx), core.DomainSessions).Sessions, nil
	}
	tick = func(ctx context.Context) (core.SessionsReport, error) {
		return core.ReadSessions(ctx, detect(ctx)), nil
	}
	return once, tick
}
