package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/andrew-bierman/abacus/internal/brand"
)

// Source says where a tool was found. It is shown in Doctor and status so "which bd is this?"
// always has an answer.
type Source string

const (
	SourceBundled  Source = "bundled"  // a sidecar shipped next to abacus
	SourcePATH     Source = "PATH"     // found on the user's PATH
	SourceOverride Source = "override" // an explicit ABACUS_*_BIN
)

// KnownTools are the tools abacus bundles and composes, by the name it looks them up under.
var KnownTools = []string{"bd", "bv", "lambe", "beads-lease"}

// SidecarDirs lists where bundled tools are looked for, in order. It is a variable so tests
// can point it at temp dirs; the default follows the running executable.
var SidecarDirs = func() []string {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	return SidecarDirsFor(exe)
}

// SidecarDirsFor is the sidecar search order for an executable: its own directory, then
// ../libexec/<name>, then bundled/. The executable is resolved through symlinks first, so a
// symlink in ~/bin still finds the install it points into.
func SidecarDirsFor(exe string) []string {
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	dir := filepath.Dir(exe)
	return []string{dir, filepath.Join(dir, "..", "libexec", brand.Name), filepath.Join(dir, "bundled")}
}

// sidecar returns the safe sidecar for tool, or why a candidate was ignored. A sidecar is
// executed only if, after resolving symlinks, it is a regular, executable file that is not
// world-writable and does not sit in a world-writable directory.
func sidecar(tool string) (path, ignored string) {
	for _, dir := range SidecarDirs() {
		cand := filepath.Join(dir, tool)
		real, err := filepath.EvalSymlinks(cand)
		if err != nil {
			continue
		}
		fi, err := os.Stat(real)
		if err != nil {
			continue
		}
		if why := unsafeReason(real, fi); why != "" {
			if ignored == "" {
				ignored = fmt.Sprintf("ignored %s: %s", cand, why)
			}
			continue
		}
		return cand, ""
	}
	return "", ignored
}

func unsafeReason(real string, fi os.FileInfo) string {
	switch {
	case !fi.Mode().IsRegular():
		return "not a regular file"
	case fi.Mode().Perm()&0o111 == 0:
		return "not executable"
	case fi.Mode().Perm()&0o002 != 0:
		return "world-writable"
	}
	if di, err := os.Stat(filepath.Dir(real)); err == nil && di.Mode().Perm()&0o002 != 0 && di.Mode()&os.ModeSticky == 0 {
		return "its directory is world-writable"
	}
	return ""
}

// LocateSource finds a tool's binary. An explicit override wins; a path override is checked
// on disk and a bare name is resolved on PATH, never against the working directory. With no
// override it looks in the sidecar directories first, then on PATH. A non-empty detail means
// the tool is absent and says why (including a sidecar that was ignored as unsafe).
func LocateSource(tool, explicit string, look func(string) (string, error)) (bin string, src Source, detail string) {
	var ignored string
	if explicit == "" {
		if p, ign := sidecar(tool); p != "" {
			return p, SourceBundled, ""
		} else {
			ignored = ign
		}
		explicit = tool
	} else if strings.ContainsRune(explicit, os.PathSeparator) || strings.ContainsRune(explicit, '/') {
		if _, err := os.Stat(explicit); err != nil {
			return "", "", fmt.Sprintf("%s not found at %s", tool, explicit)
		}
		return explicit, SourceOverride, ""
	}
	if look != nil {
		if p, err := look(explicit); err == nil {
			if explicit != tool {
				return p, SourceOverride, ""
			}
			return p, SourcePATH, ""
		}
	}
	detail = fmt.Sprintf("%s not found on PATH", tool)
	if ignored != "" {
		detail += " (" + ignored + ")"
	}
	return "", "", detail
}

// Locate is LocateSource without the source.
func Locate(tool, explicit string, look func(string) (string, error)) (bin, detail string) {
	bin, _, detail = LocateSource(tool, explicit, look)
	return bin, detail
}

// SidecarEnv returns env with the sidecar directories that hold a tool put first on PATH, so
// a tool that shells out to another (bv runs bd) finds the bundled one. env is unchanged when
// no sidecar directory holds a tool.
func SidecarEnv(env []string) []string {
	var dirs []string
	for _, d := range SidecarDirs() {
		for _, t := range KnownTools {
			if p, _ := filepath.EvalSymlinks(filepath.Join(d, t)); p != "" {
				dirs = append(dirs, d)
				break
			}
		}
	}
	if len(dirs) == 0 {
		return env
	}
	out := make([]string, 0, len(env)+1)
	path := ""
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "PATH="); ok {
			path = v
			continue
		}
		out = append(out, e)
	}
	list := strings.Join(dirs, string(os.PathListSeparator))
	if path != "" {
		list += string(os.PathListSeparator) + path
	}
	return append(out, "PATH="+list)
}
