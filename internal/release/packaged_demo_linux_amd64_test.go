package release

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestPackagedDemo(t *testing.T) {
	archive := os.Getenv("ABACUS_RELEASE_ARCHIVE")
	sums := os.Getenv("ABACUS_RELEASE_SHA256SUMS")
	if archive == "" && sums == "" {
		t.Skip("set ABACUS_RELEASE_ARCHIVE and ABACUS_RELEASE_SHA256SUMS to test a local package")
	}
	if archive == "" || sums == "" {
		t.Fatal("set both ABACUS_RELEASE_ARCHIVE and ABACUS_RELEASE_SHA256SUMS")
	}
	archive, err := filepath.Abs(archive)
	must(t, err)
	name := filepath.Base(archive)
	if !strings.HasPrefix(name, "abacus_") || !strings.HasSuffix(name, "_linux_amd64.tar.gz") {
		t.Fatalf("expected a Linux amd64 release archive, got %q", name)
	}

	manifest, err := os.ReadFile(sums)
	must(t, err)
	// Match the exact filename in the two-space format emitted by package-release.sh.
	var entry string
	for _, line := range strings.Split(string(manifest), "\n") {
		_, file, ok := strings.Cut(line, "  ")
		if ok && file == name {
			if entry != "" {
				t.Fatalf("duplicate checksum entry for %q", name)
			}
			entry = line
		}
	}
	if entry == "" {
		t.Fatalf("no checksum entry for %q", name)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	check := exec.CommandContext(ctx, "sha256sum", "--check", "--strict", "-")
	check.Dir = filepath.Dir(archive)
	check.Stdin = strings.NewReader(entry + "\n")
	if out, err := check.CombinedOutput(); err != nil {
		t.Fatalf("release checksum verification: %v\n%s", err, out)
	}

	root := t.TempDir()
	bundle := strings.TrimSuffix(name, ".tar.gz")
	extract := exec.CommandContext(ctx, "tar", "-xzf", archive, "--", bundle+"/abacus")
	extract.Dir = root
	extract.Env = []string{"PATH=/usr/bin:/bin"}
	if out, err := extract.CombinedOutput(); err != nil {
		t.Fatalf("extract packaged abacus: %v\n%s", err, out)
	}

	env := []string{"PATH=/usr/bin:/bin", "TERM=xterm-256color", "LANG=C.UTF-8", "TMPDIR=" + root}
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"} {
		dir := filepath.Join(root, key)
		must(t, os.Mkdir(dir, 0o700))
		env = append(env, key+"="+dir)
	}

	status := exec.CommandContext(ctx, filepath.Join(root, bundle, "abacus"), "status", "--demo", "--json")
	status.Dir, status.Env = root, env
	out, err := status.Output()
	if err != nil {
		t.Fatalf("packaged demo status: %v", err)
	}
	var report struct {
		Demo bool `json:"demo"`
	}
	must(t, json.Unmarshal(out, &report))
	if !report.Demo {
		t.Fatalf("packaged status missing demo=true: %s", out)
	}

	// script owns a private PTY. The inner timeout also bounds the lifetime of the
	// PTY child if script or Go stops forwarding input.
	tui := exec.CommandContext(ctx, "script", "--quiet", "--return", "--command",
		"stty rows 30 cols 100 && exec timeout --kill-after=1s 10s ./abacus tui --demo", "/dev/null")
	tui.Dir, tui.Env = filepath.Join(root, bundle), env
	tui.WaitDelay = 2 * time.Second
	input, err := tui.StdinPipe()
	must(t, err)
	frame := &demoFrame{input: input}
	tui.Stdout, tui.Stderr = frame, frame
	if err := tui.Run(); err != nil {
		t.Fatalf("packaged demo TUI: %v\n%s", err, frame.output.Bytes())
	}
	if !frame.quitSent {
		t.Fatalf("packaged demo TUI exited before rendering the Board fixture:\n%s", frame.output.Bytes())
	}
}

type demoFrame struct {
	input    io.Writer
	output   bytes.Buffer
	quitSent bool
}

func (f *demoFrame) Write(p []byte) (int, error) {
	n, err := f.output.Write(p)
	// Search accumulated output so a frame split across reads still matches.
	if !f.quitSent && strings.Contains(ansi.Strip(f.output.String()), "dev/web (3)") {
		_, err = io.WriteString(f.input, "q")
		f.quitSent = err == nil
	}
	return n, err
}
