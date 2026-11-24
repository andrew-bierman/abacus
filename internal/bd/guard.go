package bd

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/andrew-bierman/abacus/internal/brand"
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
	// readExport writes the issues (dependencies included) to stdout only: the graph engine's
	// source when the tracker keeps no .beads/issues.jsonl. No -o, so it never writes a file.
	readExport = []string{"--readonly", "export"}
)

func readAllowed(a []string, dir string) bool {
	a, ok := stripDir(a, dir)
	if !ok {
		return false
	}
	if len(a) == 4 && a[0] == "--readonly" && a[1] == "show" && a[3] == "--json" && core.TaskIDRE.MatchString(a[2]) {
		return true
	}
	// history of one task: the event log, newest first, bounded (the only flags are fixed).
	if len(a) == 6 && a[0] == "--readonly" && a[1] == "history" && a[3] == "--events" && a[4] == "--json" && core.TaskIDRE.MatchString(a[2]) && historyLimitRE.MatchString(a[5]) {
		return true
	}
	return slices.Equal(a, readVersion) || slices.Equal(a, readStatus) || slices.Equal(a, readList) || slices.Equal(a, readExport)
}

var historyLimitRE = regexp.MustCompile(`^--limit=[1-9][0-9]{0,2}$`)

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

// initFlags are the whole `bd init` argv after the verb: every one exactly once, in any order.
// The prefix is the product name. There is no -C (bd refuses it for a directory with no tracker
// yet; the process runs inside the workspace instead) and no --force, --reinit-local,
// --destroy-token, --discard-remote, --remote or --server: init can only create, never replace.
func initAllowed(rest []string) bool {
	want := map[string]bool{"--non-interactive": true, "--quiet": true, "--skip-agents": true, "--skip-hooks": true, "--prefix=" + brand.Name: true}
	if len(rest) != len(want) {
		return false
	}
	for _, f := range rest {
		if !want[f] {
			return false
		}
		delete(want, f)
	}
	return true
}

func writeAllowed(a []string, dir string) bool {
	if len(a) > 0 && a[0] == "init" {
		return initAllowed(a[1:])
	}
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
			case "--add-label", "--remove-label":
				return core.LabelRE.MatchString(v)
			case "--assignee", "--if-assignee":
				return core.AssigneeOK(v)
			case "--if-status":
				// The guard names the status the task has now; closed is never a source of an update
				// (closed work leaves closed through reopen), so a card cannot be dragged out of it.
				return core.StatusGuardRE.MatchString(v) && v != "closed"
			}
			return false
		})
		if !ok {
			return false
		}
		fields := seen["--title"] || seen["--description"] || seen["--priority"] || seen["--status"] || seen["--if-status"]
		switch {
		case seen["--add-label"] || seen["--remove-label"]:
			// one label, added or removed, and nothing else in the same call
			return seen["--add-label"] != seen["--remove-label"] && !seen["--assignee"] && !seen["--if-assignee"] && !fields
		case seen["--assignee"] || seen["--if-assignee"]:
			// an assignment always carries the guard (who holds it now) and stands alone: never --force or --claim
			return seen["--assignee"] && seen["--if-assignee"] && !fields
		}
		// A status change always carries its guard, and a guard always carries a status change:
		// never --force, --claim or any other flag (the allowlist names every one that is allowed).
		return (seen["--title"] || seen["--description"] || seen["--priority"] || seen["--status"]) && seen["--status"] == seen["--if-status"]
	case "dep":
		// dep add|remove BLOCKED BLOCKER --json: the one blocking edge, both ends named, never the --blocks shorthand,
		// a type or an external reference
		return len(a) == 5 && (a[1] == "add" || a[1] == "remove") && core.TaskIDRE.MatchString(a[2]) && core.TaskIDRE.MatchString(a[3]) && a[2] != a[3] && a[4] == "--json"
	case "comment":
		// the text follows `--`, so it can never be read as a flag; it is one argument
		return len(a) == 5 && core.TaskIDRE.MatchString(a[1]) && a[2] == "--json" && a[3] == "--" && core.CommentTextOK(a[4])
	case "close", "reopen":
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
	if err := m.writeBlocked(); err != nil {
		return nil, err
	}
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
		if ch.IfStatus != "" {
			a = append(a, "--if-status="+ch.IfStatus)
		}
	case core.TaskClose, core.TaskReopen:
		a = []string{string(ch.Op), ch.ID, "--json"}
		if ch.Reason != "" {
			a = append(a, "--reason="+ch.Reason)
		}
	case core.TaskLabelAdd, core.TaskLabelRemove:
		flag := "--add-label="
		if ch.Op == core.TaskLabelRemove {
			flag = "--remove-label="
		}
		a = []string{"update", ch.ID, "--json", flag + ch.Label}
	case core.TaskPriority:
		if ch.Priority == nil {
			return nil, errors.New("a priority is required")
		}
		a = []string{"update", ch.ID, "--json", fmt.Sprintf("--priority=%d", *ch.Priority)}
	case core.TaskAssign:
		if ch.Assignee == nil {
			return nil, errors.New("an assignee is required")
		}
		a = []string{"update", ch.ID, "--json", "--assignee=" + *ch.Assignee, "--if-assignee=" + ch.IfAssignee}
	case core.TaskDepAdd, core.TaskDepRemove:
		verb := "add"
		if ch.Op == core.TaskDepRemove {
			verb = "remove"
		}
		a = []string{"dep", verb, ch.ID, ch.Blocker, "--json"}
	case core.TaskAddComment:
		a = []string{"comment", ch.ID, "--json", "--", ch.Text}
	case core.TaskInit:
		if m.o.InitDir == "" || ch.Dir != m.o.InitDir {
			return nil, errors.New("refused: bd init is only allowed in the managed workspace")
		}
		a = []string{"init", "--non-interactive", "--quiet", "--skip-agents", "--skip-hooks", "--prefix=" + brand.Name}
		if !writeAllowed(a, "") {
			return nil, fmt.Errorf("refused: %v is outside the write allowlist", a)
		}
		return append([]string{"bd"}, a...), nil
	default:
		return nil, fmt.Errorf("operation %q is not allowed", ch.Op)
	}
	if d := m.TrackerDir(); d != "" {
		a = append([]string{"-C", d}, a...)
	}
	if !writeAllowed(a, m.TrackerDir()) {
		return nil, fmt.Errorf("refused: %v is outside the write allowlist", a)
	}
	return append([]string{"bd"}, a...), nil
}
