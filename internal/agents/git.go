package agents

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"time"
)

// Runner executes one command and returns its stdout and exit code. A non-zero exit is data,
// not an error; err means the command could not run. The shape matches core.Runner, so
// core.ExecRunner satisfies it without agents importing core (which would cycle once the
// Sessions domain imports this package).
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (out []byte, exit int, err error)
}

// GitStatus is the branch and working-tree counts for one session directory. A zero value
// means the directory is not a Git work tree, git could not be run, or the caller has not
// read it yet. Branch is also empty for a detached HEAD, where there is no branch name to show.
type GitStatus struct {
	Branch   string `json:"branch,omitempty"`
	Added    int    `json:"added"`
	Modified int    `json:"modified"`
}

// defaultGitTTL is the cadence abtop polls git at. A tab tick is much faster than that, and
// `git status` is comparatively expensive, so results are reused for this long.
const defaultGitTTL = 10 * time.Second

// defaultGitTimeout bounds one read when the caller's context has no deadline, so a slow work
// tree cannot hold up a snapshot.
const defaultGitTimeout = 2 * time.Second

type gitCacheEntry struct {
	status GitStatus
	at     time.Time
}

// Git reads branch and dirty counts for session directories. It is safe for concurrent use.
//
// A zero Git reads nothing; supply a Runner, and core.ExecRunner satisfies this package's
// Runner unchanged. Nothing here fails a snapshot: a missing git, a non-repository directory or
// a failed command all mean "no git facts to show".
type Git struct {
	Runner Runner
	// TTL is how long a directory's result is reused. Zero means defaultGitTTL.
	TTL time.Duration
	// Now is injectable so a test can pin time; nil means time.Now.
	Now func() time.Time

	mu    sync.Mutex
	cache map[string]gitCacheEntry
}

// Status returns dir's branch and counts, reusing a result younger than TTL. It never returns
// an error; see Git for why. An empty directory is not read.
func (g *Git) Status(ctx context.Context, dir string) GitStatus {
	if dir == "" {
		return GitStatus{}
	}
	now := time.Now
	if g.Now != nil {
		now = g.Now
	}
	ttl := g.TTL
	if ttl <= 0 {
		ttl = defaultGitTTL
	}

	g.mu.Lock()
	if e, ok := g.cache[dir]; ok && now().Sub(e.at) < ttl {
		g.mu.Unlock()
		return e.status
	}
	g.mu.Unlock()

	st := g.read(ctx, dir)

	// Stamp when the read finished, so a slow command does not shorten the cache's life.
	g.mu.Lock()
	if g.cache == nil {
		g.cache = make(map[string]gitCacheEntry)
	}
	g.cache[dir] = gitCacheEntry{status: st, at: now()}
	g.mu.Unlock()
	return st
}

func (g *Git) read(ctx context.Context, dir string) GitStatus {
	if g.Runner == nil {
		return GitStatus{}
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultGitTimeout)
		defer cancel()
	}
	// --no-optional-locks keeps a poll from taking the index lock or rewriting it.
	out, exit, err := g.Runner.Run(ctx, "git", "--no-optional-locks", "-C", dir, "status", "--porcelain", "--branch")
	if err != nil || exit != 0 {
		return GitStatus{}
	}
	return parseGitStatus(out)
}

// parseGitStatus reads `git status --porcelain --branch`, a format we do not control: the
// `##` header carries the branch and the rest carries one path per line. The counts follow
// abtop's projects-panel rule so the tab does not reinvent it.
func parseGitStatus(out []byte) GitStatus {
	var st GitStatus
	for len(out) > 0 {
		line, rest, _ := bytes.Cut(out, []byte("\n"))
		out = rest
		if len(line) == 0 {
			continue
		}
		if header, ok := bytes.CutPrefix(line, []byte("## ")); ok {
			st.Branch = gitBranch(string(header))
			continue
		}
		if len(line) < 2 {
			continue
		}
		code := line[:2]
		switch {
		case bytes.ContainsAny(code, "?A"):
			st.Added++
		case bytes.ContainsRune(code, 'M'):
			st.Modified++
		}
	}
	return st
}

// gitBranch extracts the branch from the `##` header. A repository with no commits says
// "No commits yet on <branch>" instead of naming an upstream, and a detached HEAD says
// "HEAD (no branch)".
func gitBranch(header string) string {
	const noCommits = "No commits yet on "
	if rest, ok := strings.CutPrefix(header, noCommits); ok {
		return strings.TrimSpace(rest)
	}
	// A normal header is "<branch>...<upstream>", optionally followed by "[ahead/behind]".
	if branch, _, ok := strings.Cut(header, "..."); ok {
		return branch
	}
	branch, _, _ := strings.Cut(header, " ")
	if branch == "HEAD" {
		return ""
	}
	return branch
}
