package cli

import (
	"fmt"
	"io/fs"
	"strings"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/embedtools"
)

// runLicenses prints the third-party notices and every license text built into this binary, so the
// single file carries what its tools' licenses require. A development build carries none.
func runLicenses(args []string, d Deps) int {
	if len(args) > 0 {
		fmt.Fprintf(d.Err, "licenses: unexpected argument %q\n", args[0])
		return core.ExitUsage
	}
	fsys, m := core.EmbeddedFS()
	if fsys == nil {
		fmt.Fprintln(d.Out, "This development build has no tools built in, so it carries no tool licenses or notices. A release build (make build) prints them here.")
		return core.ExitOK
	}
	read := func(name string) (string, bool) {
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			fmt.Fprintf(d.Err, "licenses: this copy of abacus is damaged (%s is unreadable); download it again\n", name)
			return "", false
		}
		return strings.TrimRight(string(b), "\n"), true
	}
	n, ok := read(embedtools.NoticesFile)
	if !ok {
		return core.ExitFail
	}
	fmt.Fprintln(d.Out, n)
	var files []string
	for _, t := range m.Tools {
		if t.LicenseFile != "" {
			files = append(files, t.LicenseFile)
		}
	}
	files = append(files, m.Licenses...)
	for _, f := range files {
		text, ok := read(f)
		if !ok {
			return core.ExitFail
		}
		fmt.Fprintf(d.Out, "\n\n================================================================\n%s\n================================================================\n\n%s\n", strings.TrimPrefix(f, "licenses/"), text)
	}
	return core.ExitOK
}
