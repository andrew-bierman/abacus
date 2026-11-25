package agents

import (
	"io"
	"io/fs"
	"path"
	"strings"
)

// gitInfo reads a directory's repository, branch and linked-worktree name from the files git
// keeps (.git, HEAD, commondir), without running git. Paths are relative to fsys (the file system
// rooted at "/"). Anything unreadable gives empty strings.
func gitInfo(fsys fs.FS, cwd string) (repo, branch, worktree string) {
	dir := strings.TrimPrefix(path.Clean(cwd), "/")
	if dir == "" {
		dir = "."
	}
	for i := 0; i < 40; i++ {
		gitPath := path.Join(dir, ".git")
		fi, err := fs.Stat(fsys, gitPath)
		if err == nil {
			gitDir, linked := gitPath, false
			if !fi.IsDir() { // a linked worktree: ".git" is a file "gitdir: <path>"
				b, err := readSmall(fsys, gitPath)
				rest, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
				if err != nil || !ok {
					return "", "", ""
				}
				g := strings.TrimSpace(rest)
				if !path.IsAbs(g) {
					g = path.Join(dir, g)
				}
				gitDir = strings.TrimPrefix(path.Clean(g), "/")
				linked = true
			}
			branch = headBranch(fsys, gitDir)
			repo = path.Base(dir)
			if linked {
				worktree = path.Base(dir)
				if b, err := readSmall(fsys, path.Join(gitDir, "commondir")); err == nil {
					common := path.Clean(path.Join(gitDir, strings.TrimSpace(string(b))))
					if path.Base(common) == ".git" {
						repo = path.Base(path.Dir(common))
					}
				}
			}
			return repo, branch, worktree
		}
		if dir == "." {
			break
		}
		dir = path.Dir(dir) // "." is the root of fsys; a hidden folder name keeps its dot
	}
	return "", "", ""
}

func headBranch(fsys fs.FS, gitDir string) string {
	b, err := readSmall(fsys, path.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(b))
	if ref, ok := strings.CutPrefix(s, "ref: refs/heads/"); ok {
		return ref
	}
	if len(s) >= 7 {
		return s[:7] + " (detached)"
	}
	return ""
}

// readSmall reads at most 4 KiB of a file; the files git keeps for this are a line long.
func readSmall(fsys fs.FS, name string) ([]byte, error) {
	f, _, err := openRegular(fsys, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, 4096)
	n, _ := f.Read(buf)
	return buf[:n], nil
}

// readBounded reads a whole regular file of at most max bytes; a larger one is refused rather than
// cut, because half a JSON document says nothing.
func readBounded(fsys fs.FS, name string, max int64) ([]byte, error) {
	f, fi, err := openRegular(fsys, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi.Size() > max {
		return nil, fs.ErrInvalid
	}
	return io.ReadAll(io.LimitReader(f, max))
}
