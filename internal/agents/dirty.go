package agents

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// gitDirty counts a repository's uncommitted files by running git, and it is the one place abacus
// runs a program in a directory an agent (or anything else) controls, so what it does and does not
// switch off is spelled out:
//
// Disabled on the command line (which beats the repository's own configuration):
//   - core.fsmonitor (a program git would run to ask what changed) and core.untrackedCache;
//   - submodules are not inspected (--ignore-submodules=all). That alone is not what stops a submodule's
//     own fsmonitor: the -c override above travels to the status git starts inside a submodule, and a
//     test with a real submodule fails if it is removed. The flag keeps git out of other repositories;
//   - every content filter the configuration defines (filter.NAME.clean, .smudge and .process are set to empty,
//     which git reads as "no filter"). A filter is a program a .gitattributes line can make git run to
//     compare a file with the index; the names are found first with a read-only `git config` and a
//     name that is not plain text makes the whole count unknown rather than being guessed at.
//
// Also set: the system attributes file is ignored (GIT_ATTR_NOSYSTEM) and so is core.attributesFile,
// no optional lock is taken, and the environment is cut to what git needs to find its files.
//
// NOT disabled: git itself, its global configuration, and the repository's own .gitattributes; a
// program git ships that the repository cannot name is not a concern here. status does not run hooks,
// textconv or diff.external. A directory that is not absolute, a missing git, a failed or oversized
// answer is unknown (no count), never zero.
func gitDirty(ctx context.Context, dir string) (core.AgentGit, bool) {
	if !filepath.IsAbs(dir) {
		return core.AgentGit{}, false
	}
	bin, err := exec.LookPath("git")
	if err != nil {
		return core.AgentGit{}, false
	}
	r := core.ExecRunner{Timeout: 3 * time.Second, MaxOut: 2 << 20, Scrub: keepEnv, Env: func() []string {
		return []string{"GIT_ATTR_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0"}
	}}
	names, exit, err := r.Run(ctx, bin, filterNamesArgv(dir)...)
	if err != nil || exit > 1 { // 1 is "no such key": no filters
		return core.AgentGit{}, false
	}
	argv, ok := dirtyArgv(dir, names)
	if !ok {
		return core.AgentGit{}, false
	}
	out, exit, err := r.Run(ctx, bin, argv...)
	if err != nil || exit != 0 {
		return core.AgentGit{}, false
	}
	return parseDirty(out), true
}

// filterNamesArgv lists the configuration keys that name a filter's program (any scope).
func filterNamesArgv(dir string) []string {
	return []string{"--no-optional-locks", "-C", dir, "config", "--name-only", "--get-regexp", `^filter\..*\.(clean|smudge|process)$`}
}

var filterKeyRE = regexp.MustCompile(`^filter\.([A-Za-z0-9_./+-]+)\.(clean|smudge|process)$`)

// dirtyArgv is the one status command, with every named filter emptied. ok is false when a filter's
// name is not plain text (it could not be neutralized safely).
func dirtyArgv(dir string, filterKeys []byte) ([]string, bool) {
	argv := []string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "-c", "core.attributesFile=/dev/null"}
	seen := map[string]bool{}
	for _, ln := range strings.Split(string(bytes.TrimSpace(filterKeys)), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		m := filterKeyRE.FindStringSubmatch(ln)
		if m == nil {
			return nil, false
		}
		for _, kind := range []string{"clean", "smudge", "process"} {
			if k := "filter." + m[1] + "." + kind; !seen[k] {
				seen[k] = true
				argv = append(argv, "-c", k+"=")
			}
		}
	}
	return append(argv, "-C", dir, "status", "--porcelain=v1", "--ignore-submodules=all"), true
}

// parseDirty reads `git status --porcelain=v1`: two status letters, a space, a path. New files (an
// untracked one, or one staged as added) count as added; any other change, a deletion or rename
// included, as modified. Ignored files are not listed by that command.
func parseDirty(out []byte) core.AgentGit {
	var g core.AgentGit
	for _, ln := range bytes.Split(out, []byte("\n")) {
		if len(ln) < 3 {
			continue
		}
		switch xy := ln[:2]; {
		case bytes.Equal(xy, []byte("!!")):
		case bytes.Equal(xy, []byte("??")) || bytes.ContainsRune(xy, 'A'):
			g.Added++
		default:
			g.Modified++
		}
	}
	return g
}
