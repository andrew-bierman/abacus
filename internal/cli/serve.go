package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/serve"
)

const serveUsage = "usage: abacus serve --stdio [--demo] [--tracker DIR] [--all] [--lambe-config FILE]\n\nA long-lived JSON-RPC 2.0 server for a native client: one request per line on stdin, one response per line on stdout. It listens on no network port, runs only when stdin is a pipe or socket, and writes only through the same plan, confirm, single-use apply and audit gate as the commands. See docs/serve-protocol.md."

// runServe is `abacus serve --stdio`. The engine is the serve package's; this is only the process edge:
// refuse a terminal or a file on stdin, scrub the environment, ignore SIGPIPE, end on a signal.
func runServe(rest []string, d Deps) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(d.Err)
	var o Options
	stdio := fs.Bool("stdio", false, "serve one client over stdin and stdout (the only transport)")
	o.bind(fs, true)
	fs.BoolVar(&o.AllUsers, "all", false, "include agents run by other users (default: only yours)")
	fs.StringVar(&o.LambeConfig, "lambe-config", "", "config file selecting the queues daemon")
	if fs.Parse(rest) != nil {
		return core.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(d.Err, "serve: unexpected argument %q\n", core.Sanitize(fs.Arg(0)))
		return core.ExitUsage
	}
	if code, ok := o.resolve(d); !ok {
		return code
	}
	if !*stdio {
		fmt.Fprintln(d.Err, "serve: needs --stdio (the only transport: there is no network port)")
		return core.ExitUsage
	}
	if err := checkServeStdin(d.In); err != nil {
		fmt.Fprintf(d.Err, "serve: %v\n", err)
		return core.ExitUsage
	}
	scrubServeEnv()
	ignoreSIGPIPE()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	s := serve.New(serve.Config{Build: func() *core.Registry { return d.Build(o) }, Version: d.Version, Demo: o.Demo, Audit: d.Audit, Config: d.Config,
		Setup: func(en []core.Entry) *core.Setup { return setupFor(d, en) }, Now: d.Now, Log: d.Err})
	if err := s.Serve(ctx, d.In, rawOut(d.Out)); err != nil {
		fmt.Fprintf(d.Err, "serve: %v\n", err)
		return core.ExitFail
	}
	return core.ExitOK
}

// rawOut is stdout without the command wrapper's counter in the way of nothing: the protocol writes
// whole lines, and nothing else may ever write to this stream while serving.
func rawOut(w io.Writer) io.Writer {
	if c, ok := w.(*counter); ok {
		return c.w
	}
	return w
}

// checkServeStdin accepts a pipe or a socket (what a parent process gives a child) and refuses a
// terminal, a regular file and /dev/null: a person typing at the server, or a file replayed into it,
// is not a client. A plain io.Reader (as tests pass) is taken at its word.
func checkServeStdin(in io.Reader) error {
	if in == nil {
		return fmt.Errorf("stdin is not connected: run it from the client, which gives it a pipe")
	}
	f, ok := in.(*os.File)
	if !ok {
		return nil
	}
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("cannot examine stdin: %v", err)
	}
	if fi.Mode()&(os.ModeNamedPipe|os.ModeSocket) == 0 {
		return fmt.Errorf("stdin must be a pipe or a socket from the client, not a terminal or a file")
	}
	return nil
}

// scrubServeEnv drops what could make a child tool load code the user did not choose: the dynamic
// loader's variables and the shell startup files. Core scrubs each tool's environment again when it
// starts one.
func scrubServeEnv() {
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "DYLD_") || strings.HasPrefix(name, "LD_") || name == "BASH_ENV" || name == "ENV" {
			_ = os.Unsetenv(name)
		}
	}
}
