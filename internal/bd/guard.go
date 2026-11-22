package bd

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/andrew-bierman/abacus/internal/core"
)

// Two separate allowlists. A read argv can never be a write and vice versa, and both match
// the whole argv, element by element. Values are always `--flag=value`, so a value can never
// be read as a flag. `-C DIR` is accepted only with the one configured tracker directory.

// stripDir removes an optional leading `-C dir`. With no configured dir, -C is refused.
func stripDir(a []string, dir string) ([]string, bool) {
	if len(a) >= 2 && a[0] == "-C" {
		if dir == "" || a[1] != dir {
			return nil, false
		}
		return a[2:], true
	}
	return a, true
}

var (
	readVersion = []string{"--readonly", "version", "--json"}
	readStatus  = []string{"--readonly", "status", "--json", "--no-activity"}
	readList    = []string{"--readonly", "list", "--json", "--brief", "--all", "--sort", "updated", "--flat", "-n", "8"}
)

func readAllowed(a []string, dir string) bool {
	a, ok := stripDir(a, dir)
	return ok && (slices.Equal(a, readVersion) || slices.Equal(a, readStatus) || slices.Equal(a, readList))
}

var prioRE = regexp.MustCompile(`^--priority=[0-4]$`)

// flagSet checks that every element of rest is --json once, or one of the listed
// --name=value flags at most once, and returns which names were seen.
func flagSet(rest []string, valid func(name, val string) bool) (map[string]bool, bool) {
	seen := map[string]bool{}
	for _, f := range rest {
		name, val, hasVal := strings.Cut(f, "=")
		if f == "--json" {
			name, hasVal = f, false
		} else if !hasVal || !strings.HasPrefix(name, "--") {
			return nil, false
		}
		if seen[name] || (name != "--json" && !valid(name, val)) {
			return nil, false
		}
		seen[name] = true
	}
	return seen, seen["--json"]
}

func writeAllowed(a []string, dir string) bool {
	a, ok := stripDir(a, dir)
	if !ok || len(a) == 0 {
		return false
	}
	switch a[0] {
	case "create":
		seen, ok := flagSet(a[1:], func(n, v string) bool {
			switch n {
			case "--title":
				return v != ""
			case "--description":
				return true
			case "--priority":
				return prioRE.MatchString(n + "=" + v)
			case "--type":
				return slices.Contains(core.TaskTypes, v)
			}
			return false
		})
		return ok && seen["--title"]
	case "update":
		if len(a) < 2 || !core.TaskIDRE.MatchString(a[1]) {
			return false
		}
		seen, ok := flagSet(a[2:], func(n, v string) bool {
			switch n {
			case "--title":
				return v != ""
			case "--description":
				return v != ""
			case "--priority":
				return prioRE.MatchString(n + "=" + v)
			case "--status":
				return slices.Contains(core.TaskStatuses, v)
			}
			return false
		})
		return ok && (seen["--title"] || seen["--description"] || seen["--priority"] || seen["--status"])
	case "close":
		if len(a) < 2 || !core.TaskIDRE.MatchString(a[1]) {
			return false
		}
		_, ok := flagSet(a[2:], func(n, v string) bool { return n == "--reason" })
		return ok
	}
	return false
}

// BuildTaskArgv turns a validated change into the exact argv (argv[0] is the display name
// "bd") and refuses anything the write allowlist does not accept.
func (m *Module) BuildTaskArgv(ch core.TaskChange) ([]string, error) {
	var a []string
	switch ch.Op {
	case core.TaskCreate:
		a = []string{"create", "--json", "--title=" + ch.Title}
		if ch.Description != "" {
			a = append(a, "--description="+ch.Description)
		}
		if ch.Priority != nil {
			a = append(a, fmt.Sprintf("--priority=%d", *ch.Priority))
		}
		if ch.Type != "" {
			a = append(a, "--type="+ch.Type)
		}
	case core.TaskUpdate:
		a = []string{"update", ch.ID, "--json"}
		if ch.Title != "" {
			a = append(a, "--title="+ch.Title)
		}
		if ch.Description != "" {
			a = append(a, "--description="+ch.Description)
		}
		if ch.Priority != nil {
			a = append(a, fmt.Sprintf("--priority=%d", *ch.Priority))
		}
		if ch.Status != "" {
			a = append(a, "--status="+ch.Status)
		}
	case core.TaskClose:
		a = []string{"close", ch.ID, "--json"}
		if ch.Reason != "" {
			a = append(a, "--reason="+ch.Reason)
		}
	default:
		return nil, fmt.Errorf("operation %q is not allowed", ch.Op)
	}
	if m.o.Dir != "" {
		a = append([]string{"-C", m.o.Dir}, a...)
	}
	if !writeAllowed(a, m.o.Dir) {
		return nil, fmt.Errorf("refused: %v is outside the write allowlist", a)
	}
	return append([]string{"bd"}, a...), nil
}
