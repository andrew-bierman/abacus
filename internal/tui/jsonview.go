package tui

import (
	"strings"

	"github.com/andrew-bierman/abacus/internal/core"
)

// maxJSONBytes bounds the JSON the panel formats: a huge item is cut there, with a line saying the
// command line prints all of it.
const maxJSONBytes = 256 << 10

// jsonLines is the typed JSON of a report, the text `--json` prints (core.ReportJSON, the same
// function), as lines wrapped to the panel's width. Each line passes the sanitizer again: JSON
// escapes control characters, but invisible format characters are valid inside a JSON string.
func jsonLines(report any) func(w int) []string {
	var b []byte // formatted once: a resize only re-wraps it
	var err error
	cut := false
	done := false
	return func(w int) []string {
		if !done {
			b, err = core.ReportJSON(report)
			if cut = len(b) > maxJSONBytes; cut {
				b = b[:maxJSONBytes]
			}
			done = true
		}
		if err != nil {
			return []string{"could not format this item as JSON"}
		}
		var out []string
		for _, ln := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
			out = append(out, wrap(san(ln), w)...)
		}
		if cut {
			out = append(out, "… cut here: the item is larger than this panel shows (abacus tasks show ID --json prints all of it)")
		}
		return out
	}
}
