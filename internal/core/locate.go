package core

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

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
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	dir := filepath.Dir(exe)
	return []string{dir, filepath.Join(dir, "..", "libexec", brand.Name), filepath.Join(dir, "bundled")}
}

// Found is where a tool was found. Ignored says why a bundled copy that exists was refused
// (so a fallback to PATH is never silent).
type Found struct {
	Bin     string
	Src     Source
	Ignored string
}

// currentUID is a seam for tests.
var currentUID = os.Getuid

// SetCurrentUIDForTest swaps the uid the owner checks compare against and returns a restore
// func. Only tests call it.
func SetCurrentUIDForTest(f func() int) (restore func()) {
	old := currentUID
	currentUID = f
	return func() { currentUID = old }
}

func ownerOK(fi os.FileInfo) (string, bool) {
	uid, ok := ownerUID(fi)
	if !ok || uid == 0 || uid == currentUID() {
		return "", true
	}
	return fmt.Sprintf("owned by uid %d", uid), false
}

// safeDir reports why dir may not hold a sidecar ("" when it may): it must be owned by the
// current user or root and writable by nobody else. A sticky world-writable directory is
// accepted only when root owns it.
func safeDir(dir string) string {
	di, err := os.Stat(dir)
	if err != nil {
		return "its directory cannot be read"
	}
	if why, ok := ownerOK(di); !ok {
		return "its directory is " + why
	}
	if di.Mode().Perm()&0o022 != 0 {
		if uid, _ := ownerUID(di); di.Mode()&os.ModeSticky != 0 && uid == 0 {
			return ""
		}
		return "its directory is writable by others"
	}
	return ""
}

// safeTool is the one check every sidecar passes before it is executed or exposed on a child's
// PATH. It returns the symlink-resolved path (what must be executed, so nothing can change
// between the check and the exec through a link) or, when the file exists but is unsafe, why.
// A tool that is simply not there returns ("", "").
func safeTool(dir, tool string) (resolved, why string) {
	cand := filepath.Join(dir, tool)
	real, err := filepath.EvalSymlinks(cand)
	if err != nil {
		return "", ""
	}
	fi, err := os.Stat(real)
	if err != nil {
		return "", ""
	}
	switch {
	case !fi.Mode().IsRegular():
		return "", "not a regular file"
	case fi.Mode().Perm()&0o111 == 0:
		return "", "not executable"
	case fi.Mode().Perm()&0o002 != 0:
		return "", "world-writable"
	case fi.Mode().Perm()&0o020 != 0:
		return "", "group-writable"
	}
	if w, ok := ownerOK(fi); !ok {
		return "", w
	}
	for _, d := range []string{filepath.Dir(cand), filepath.Dir(real)} { // the link's and the target's
		if w := safeDir(d); w != "" {
			return "", w
		}
	}
	return real, ""
}

// sidecar returns the verified, resolved sidecar for tool, or why a candidate was ignored.
func sidecar(tool string) (path, ignored string) {
	for _, dir := range SidecarDirs() {
		p, why := safeTool(dir, tool)
		if p != "" {
			return p, ""
		}
		if why != "" && ignored == "" {
			ignored = fmt.Sprintf("%s (%s)", why, filepath.Join(dir, tool))
		}
	}
	return "", ignored
}

// LocateFound finds a tool's binary. An explicit override wins; a path override is checked
// on disk and a bare name is resolved on PATH, never against the working directory. With no
// override it looks in the sidecar directories first, then on PATH; a bundled copy that exists
// but fails the safety checks is recorded in Found.Ignored (or the detail, if PATH has none
// either). A non-empty detail means the tool is absent and says why.
func LocateFound(tool, explicit string, look func(string) (string, error)) (Found, string) {
	var ignored string
	if explicit == "" {
		var p string
		if p, ignored = sidecar(tool); p != "" {
			return Found{Bin: p, Src: SourceBundled}, ""
		}
		explicit = tool
	} else if strings.ContainsRune(explicit, os.PathSeparator) || strings.ContainsRune(explicit, '/') {
		if _, err := os.Stat(explicit); err != nil {
			return Found{}, fmt.Sprintf("%s not found at %s", tool, explicit)
		}
		return Found{Bin: explicit, Src: SourceOverride}, ""
	}
	if look != nil {
		if p, err := look(explicit); err == nil {
			if explicit != tool {
				return Found{Bin: p, Src: SourceOverride}, ""
			}
			return Found{Bin: p, Src: SourcePATH, Ignored: ignored}, ""
		}
	}
	detail := fmt.Sprintf("%s not found on PATH", tool)
	if ignored != "" {
		detail += " (bundled copy ignored: " + ignored + ")"
	}
	return Found{}, detail
}

// Locate is LocateFound without the provenance.
func Locate(tool, explicit string, look func(string) (string, error)) (bin, detail string) {
	f, detail := LocateFound(tool, explicit, look)
	return f.Bin, detail
}

var (
	envMu   sync.Mutex
	envDirs = map[string]string{} // tool set -> private dir
)

// privateToolDir returns a private (0700) directory holding symlinks to the verified binaries,
// created once per distinct tool set and removed by CleanupSidecarEnv.
func privateToolDir(tools map[string]string) (string, error) {
	names := make([]string, 0, len(tools))
	for n := range tools {
		names = append(names, n)
	}
	sort.Strings(names)
	var key strings.Builder
	for _, n := range names {
		key.WriteString(n + "=" + tools[n] + ";")
	}
	envMu.Lock()
	defer envMu.Unlock()
	if d, ok := envDirs[key.String()]; ok {
		return d, nil
	}
	d, err := os.MkdirTemp("", brand.Name+"-tools-")
	if err != nil {
		return "", err
	}
	for _, n := range names {
		if err := os.Symlink(tools[n], filepath.Join(d, n)); err != nil {
			os.RemoveAll(d)
			return "", err
		}
	}
	envDirs[key.String()] = d
	return d, nil
}

// CleanupSidecarEnv removes the private tool directories SidecarEnv created.
func CleanupSidecarEnv() {
	envMu.Lock()
	defer envMu.Unlock()
	for k, d := range envDirs {
		os.RemoveAll(d)
		delete(envDirs, k)
	}
}

// SidecarEnv returns env with a private directory of links to the verified bundled tools first
// on PATH, so a tool that shells out to another (bv runs bd) finds the bundled one. Only known
// tools that pass the same checks as Locate are exposed, never the sidecar directory itself. env
// is unchanged when no tool qualifies.
func SidecarEnv(env []string) []string {
	tools := map[string]string{}
	for _, t := range KnownTools {
		if p, _ := sidecar(t); p != "" {
			tools[t] = p
		}
	}
	if len(tools) == 0 {
		return env
	}
	dir, err := privateToolDir(tools)
	if err != nil {
		return env // never fall back to exposing the real directory
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
	list := dir
	if path != "" {
		list += string(os.PathListSeparator) + path
	}
	return append(out, "PATH="+list)
}

// ScrubBeadsEnv drops every BEADS_* and BD_* variable except BEADS_ACTOR, so an inherited
// BEADS_DIR, BEADS_DB or a server setting cannot steer bd (or a tool that runs it) at a
// tracker other than the one abacus chose. `bd init --help` documents BD_NON_INTERACTIVE,
// BEADS_ACTOR and the BEADS_DOLT_* server settings; only BEADS_ACTOR is wanted (embedded mode
// needs none of the others, and abacus passes --non-interactive itself).
func ScrubBeadsEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		name, _, _ := strings.Cut(e, "=")
		if (strings.HasPrefix(name, "BEADS_") || strings.HasPrefix(name, "BD_")) && name != "BEADS_ACTOR" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Locator is embedded in a module to remember where its tool was found.
type Locator struct {
	lmu   sync.Mutex
	found Found
}

// Find locates the tool and, when it is there, remembers it. The detail is non-empty when absent.
func (l *Locator) Find(tool, explicit string, look func(string) (string, error)) (Found, string) {
	f, detail := LocateFound(tool, explicit, look)
	if detail == "" {
		l.SetFound(f)
	}
	return f, detail
}

// SetFound records a location (tests use it to skip detection).
func (l *Locator) SetFound(f Found) {
	l.lmu.Lock()
	l.found = f
	l.lmu.Unlock()
}

// Located is the Located interface: which binary, where from, and what was ignored.
func (l *Locator) Located() Found {
	l.lmu.Lock()
	defer l.lmu.Unlock()
	return l.found
}

// Bin is the path to run ("" before detection).
func (l *Locator) Bin() string { return l.Located().Bin }
