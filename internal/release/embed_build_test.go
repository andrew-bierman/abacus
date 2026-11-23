package release

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/embedtools/embedtest"
)

// TestTheEmbedtoolsBuildCompilesWithAFixtureSetAndRunsIt builds the real command with -tags
// embedtools against a tiny fixture set (supplied through go build -overlay, so the developer's own
// gitignored blob directory is never touched), then runs the binary in an isolated environment:
// it must extract the fixture tools, print the fixture notices and report the tools as built in.
func TestTheEmbedtoolsBuildCompilesWithAFixtureSetAndRunsIt(t *testing.T) {
	plat := runtime.GOOS + "-" + runtime.GOARCH
	switch plat {
	case "darwin-amd64", "darwin-arm64", "linux-amd64", "linux-arm64":
	default:
		t.Skip("no embedded tool set for " + plat)
	}
	root, err := filepath.Abs("../..")
	must(t, err)
	set := embedtest.Standard()
	repl := map[string]string{}
	tmp := t.TempDir()
	for name, f := range set.FS {
		src := filepath.Join(tmp, "src", name)
		must(t, os.MkdirAll(filepath.Dir(src), 0o755))
		must(t, os.WriteFile(src, f.Data, 0o644))
		repl[filepath.Join(root, "internal", "embedtools", "blobs", plat, name)] = src
	}
	ov, _ := json.Marshal(map[string]any{"Replace": repl})
	ovFile := filepath.Join(tmp, "overlay.json")
	must(t, os.WriteFile(ovFile, ov, 0o644))
	bin := filepath.Join(tmp, "abacus")
	cmd := exec.Command("go", "build", "-overlay", ovFile, "-tags", "embedtools", "-o", bin, "./cmd/abacus")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOMODCACHE="+goEnv(t, "GOMODCACHE"), "GOCACHE="+goEnv(t, "GOCACHE"), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the embedtools build does not compile with a fixture set: %v\n%s", err, out)
	}
	run := func(args ...string) string {
		c := exec.Command(bin, args...)
		home := filepath.Join(tmp, "home")
		c.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "XDG_DATA_HOME=" + filepath.Join(tmp, "d"), "XDG_CONFIG_HOME=" + filepath.Join(tmp, "c"), "XDG_STATE_HOME=" + filepath.Join(tmp, "s")}
		out, _ := c.CombinedOutput()
		return string(out)
	}
	lic := run("licenses")
	if !strings.Contains(lic, "# notices") || !strings.Contains(lic, "BD LICENSE") {
		t.Fatalf("abacus licenses:\n%s", lic)
	}
	ver := run("version")
	// The fixture tools do not speak the tools' protocols, so they read as incompatible; what this
	// checks is that this is an embedded build, that the tools it needs were unpacked into the private
	// cache, and that the Board CLI was not (it is unpacked on first use).
	if !strings.Contains(ver, "Built-in tools: ") {
		t.Fatalf("not an embedded build:\n%s", ver)
	}
	dirs, _ := filepath.Glob(filepath.Join(tmp, "d", "abacus", "tools", "*", "bd"))
	if len(dirs) != 1 {
		t.Fatalf("bd was not unpacked into the private cache:\n%s", ver)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dirs[0]), "board")); err == nil {
		t.Fatal("the Board CLI must not be unpacked by `abacus version`")
	}
	if strings.Contains(ver, "Development build") {
		t.Fatalf("an embedtools build is not a development build:\n%s", ver)
	}
}

func TestAPlainGoBuildStillCompilesAndSaysItIsADevelopmentBuild(t *testing.T) {
	root, err := filepath.Abs("../..")
	must(t, err)
	bin := filepath.Join(t.TempDir(), "abacus")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/abacus")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOMODCACHE="+goEnv(t, "GOMODCACHE"), "GOCACHE="+goEnv(t, "GOCACHE"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	c := exec.Command(bin, "licenses")
	out, _ := c.CombinedOutput()
	if !strings.Contains(string(out), "development build") {
		t.Fatalf("%s", out)
	}
}
