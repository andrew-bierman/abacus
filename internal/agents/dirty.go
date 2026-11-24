package agents

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// gitDirty counts a repository's uncommitted files by running git once. It is only asked about a
// directory that already has a repository (so git is there to run), and the one fixed argv is
// read-only: no optional lock is taken, nothing is fetched, and a hook-capable setting a hostile
// repository could carry (core.fsmonitor runs a program) is switched off on the command line, which
// wins over the repository's own configuration. The environment is cut to what git needs to find its
// files. A directory that is not absolute, a missing git, a failed or oversized answer is unknown.
func gitDirty(ctx context.Context, dir string) (core.AgentGit, bool) {
	if !filepath.IsAbs(dir) {
		return core.AgentGit{}, false
	}
	bin, err := exec.LookPath("git")
	if err != nil {
		return core.AgentGit{}, false
	}
	r := core.ExecRunner{Timeout: 3 * time.Second, MaxOut: 2 << 20, Scrub: keepEnv}
	out, exit, err := r.Run(ctx, bin, dirtyArgv(dir)...)
	if err != nil || exit != 0 {
		return core.AgentGit{}, false
	}
	return parseDirty(out), true
}

func dirtyArgv(dir string) []string {
	return []string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-C", dir, "status", "--porcelain=v1"}
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
