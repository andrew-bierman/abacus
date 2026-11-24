package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/andrew-bierman/abacus/internal/core"
)

// runSchema is `abacus schema [NAME] [--out DIR] [--json]`: the JSON Schemas of every --json output,
// derived from the Go types (core.SchemaSpecs). It reads nothing and contacts nothing; --out writes
// one NAME.json per schema into DIR, a folder the caller named, and nowhere else.
func runSchema(rest []string, d Deps) int {
	var pos []string
	for len(rest) > 0 && len(rest[0]) > 0 && rest[0][0] != '-' {
		pos, rest = append(pos, rest[0]), rest[1:]
	}
	fs := flag.NewFlagSet("schema", flag.ContinueOnError)
	fs.SetOutput(d.Err)
	var asJSON bool
	var out string
	jsonVar(d, fs, &asJSON, "print the list as JSON")
	fs.StringVar(&out, "out", "", "write every schema into this folder as NAME.json")
	if fs.Parse(rest) != nil {
		return core.ExitUsage
	}
	pos = append(pos, fs.Args()...)
	if len(pos) > 1 || (len(pos) == 1 && out != "") {
		fmt.Fprintln(d.Err, "usage: schema [NAME] [--out DIR] [--json]")
		return core.ExitUsage
	}
	specs := core.SchemaSpecs()
	if len(pos) == 1 {
		for _, s := range specs {
			if s.Name == pos[0] {
				b, err := core.SchemaJSON(s)
				if err != nil {
					return failf(d, core.CodeFailed, "schema: %v\n", err)
				}
				_, _ = d.Out.Write(b)
				return core.ExitOK
			}
		}
		fmt.Fprintf(d.Err, "schema: unknown schema %q (run `schema` for the list)\n", core.Sanitize(pos[0]))
		return core.ExitUsage
	}
	if out != "" {
		if err := os.MkdirAll(out, 0o755); err != nil {
			return failf(d, core.CodeFailed, "schema: %v\n", err)
		}
		for _, s := range specs {
			b, err := core.SchemaJSON(s)
			if err != nil {
				return failf(d, core.CodeFailed, "schema: %v\n", err)
			}
			if err := os.WriteFile(filepath.Join(out, s.Name+".json"), b, 0o644); err != nil {
				return failf(d, core.CodeFailed, "schema: %v\n", err)
			}
		}
		fmt.Fprintf(d.Out, "wrote %d schemas to %s\n", len(specs), out)
		return core.ExitOK
	}
	idx := core.SchemaIndex{Schemas: []core.SchemaIndexEntry{}}
	for _, s := range specs {
		idx.Schemas = append(idx.Schemas, core.SchemaIndexEntry{Name: s.Name, Commands: s.Commands, File: s.Name + ".json"})
	}
	if asJSON {
		jsonOut(d.Out, idx)
		return core.ExitOK
	}
	for _, e := range idx.Schemas {
		fmt.Fprintf(d.Out, "%-20s %s\n", e.Name, e.Commands)
	}
	return core.ExitOK
}
