package cli

import (
	"bytes"
	"encoding/json"
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
	var asJSON, force bool
	var out string
	jsonVar(d, fs, &asJSON, "print the list as JSON")
	fs.BoolVar(&force, "force", false, "with --out: replace an existing file that is not an abacus schema")
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
		return writeSchemas(d, out, specs, force)
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

// writeSchemas writes NAME.json for every spec into dir. Nothing is written until every target has been
// checked: a symlink is never written through or replaced, and an existing regular file is replaced only if
// it is an abacus schema (it carries "x-abacus-schema") or --force was given. Each file is written to a
// temporary name in dir and renamed into place, so a failure never leaves half a schema.
func writeSchemas(d Deps, dir string, specs []core.SchemaSpec, force bool) int {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return failf(d, core.CodeFailed, "schema: %v\n", err)
	}
	docs := make([][]byte, len(specs))
	for i, s := range specs {
		b, err := core.SchemaJSON(s)
		if err != nil {
			return failf(d, core.CodeFailed, "schema: %v\n", err)
		}
		docs[i] = b
		target := filepath.Join(dir, s.Name+".json")
		fi, err := os.Lstat(target)
		switch {
		case err != nil && os.IsNotExist(err):
		case err != nil:
			return failf(d, core.CodeFailed, "schema: %v\n", err)
		case fi.Mode()&os.ModeSymlink != 0:
			return failf(d, core.CodeRefused, "schema: %s is a symbolic link: not written through or replaced; nothing was written\n", target)
		case !fi.Mode().IsRegular():
			return failf(d, core.CodeRefused, "schema: %s is not a regular file; nothing was written\n", target)
		case !force && !isAbacusSchema(target):
			return failf(d, core.CodeRefused, "schema: %s exists and is not an abacus schema: nothing was written (--force replaces it)\n", target)
		}
	}
	for i, s := range specs {
		if err := writeAtomic(filepath.Join(dir, s.Name+".json"), docs[i]); err != nil {
			return failf(d, core.CodeFailed, "schema: %v\n", err)
		}
	}
	fmt.Fprintf(d.Out, "wrote %d schemas to %s\n", len(specs), dir)
	return core.ExitOK
}

func isAbacusSchema(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var m map[string]json.RawMessage
	return json.Unmarshal(bytes.TrimSpace(b), &m) == nil && m["x-abacus-schema"] != nil
}

func writeAtomic(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".schema-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o644)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}
