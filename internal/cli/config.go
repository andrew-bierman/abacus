package cli

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

func configStore(d Deps) (core.ConfigStore, error) {
	if d.Config.Path != "" {
		return d.Config, nil
	}
	p, err := core.DefaultConfigPath()
	return core.ConfigStore{Path: p}, err
}

// runConfig is `abacus config show|get KEY|set KEY VALUE|path`: the user's own settings file,
// edited by an explicit command.
func runConfig(rest []string, d Deps) int {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	fs.SetOutput(d.Err)
	asJSON := jsonBool(d, fs, "print JSON (show)")
	if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
		fmt.Fprintln(d.Err, "usage: config show [--json] | get KEY | set KEY VALUE | path\nkeys: "+strings.Join(core.ConfigKeys, ", "))
		return core.ExitUsage
	}
	verb, args := rest[0], rest[1:]
	if fs.Parse(args) != nil {
		return core.ExitUsage
	}
	args = fs.Args()
	st, err := configStore(d)
	if err != nil {
		fmt.Fprintf(d.Err, "config: %v\n", err)
		return core.ExitFail
	}
	switch {
	case verb == "path" && len(args) == 0:
		fmt.Fprintln(d.Out, st.Path)
	case verb == "show" && len(args) == 0:
		c, err := st.Load()
		if err != nil {
			fmt.Fprintf(d.Err, "config: %v\n", err)
			return core.ExitFail
		}
		if *asJSON {
			jsonOut(d.Out, core.ConfigShow{Path: st.Path, Config: c})
			return core.ExitOK
		}
		fmt.Fprintf(d.Out, "%s\n", st.Path)
		for _, k := range core.ConfigKeys {
			v, _ := c.Get(k)
			if v == "" {
				v = "(not set)"
			}
			fmt.Fprintf(d.Out, "  %-13s %s\n", k, core.Sanitize(v))
		}
	case verb == "get" && len(args) == 1:
		c, err := st.Load()
		if err != nil {
			fmt.Fprintf(d.Err, "config: %v\n", err)
			return core.ExitFail
		}
		v, err := c.Get(args[0])
		if err != nil {
			fmt.Fprintf(d.Err, "config: %v\n", err)
			return core.ExitUsage
		}
		fmt.Fprintln(d.Out, core.Sanitize(v))
	case verb == "set" && len(args) == 2:
		// A bad key or value is a usage error; checked on a scratch copy so it never depends on the file.
		var scratch core.Config
		if err := scratch.Set(args[0], args[1]); err != nil {
			fmt.Fprintf(d.Err, "config: %v\n", err)
			return core.ExitUsage
		}
		// The agent command is what "work on this" later runs, so changing it is recorded like any
		// other write, before it happens (a failed record stops it), and shown back as the argv.
		var au core.Audit
		if args[0] == "agent_command" {
			if au = d.Audit; au == nil {
				au = core.DefaultAudit()
			}
			if err := au.Record(core.AuditEntry{Time: time.Now().UTC(), Domain: "config", Verb: "set agent_command", Phase: "attempt", Argv: scratch.AgentCommand}); err != nil {
				return failf(d, core.CodeAuditFailed, "config: not set: %v\n", err)
			}
		}
		note, err := st.UpdateNote(func(c *core.Config) error { return c.Set(args[0], args[1]) })
		if au != nil {
			e := core.AuditEntry{Time: time.Now().UTC(), Domain: "config", Verb: "set agent_command", Phase: "ok", Argv: scratch.AgentCommand}
			if err != nil {
				e.Phase, e.Error = "error", core.Sanitize(err.Error())
			}
			_ = au.Record(e)
		}
		if err != nil { // the file, the lock or the disk: not the user's typing
			fmt.Fprintf(d.Err, "config: %v\n", err)
			return core.ExitFail
		}
		fmt.Fprintf(d.Out, "%s set\n", args[0])
		if au != nil {
			fmt.Fprintf(d.Out, "work on this will run: %s\n", core.DisplayArgv(scratch.AgentCommand))
		}
		if note != "" {
			fmt.Fprintln(d.Err, "config: "+note)
		}
	default:
		fmt.Fprintln(d.Err, "usage: config show [--json] | get KEY | set KEY VALUE | path")
		return core.ExitUsage
	}
	return core.ExitOK
}
