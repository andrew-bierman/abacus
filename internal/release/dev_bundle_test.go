package release

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// devRig is a rig whose cache is empty, so every fetch goes through the fake gh (and shows up in
// its log), plus the output directory the script is pointed at.
func newDevRig(t *testing.T) (*rig, string) {
	t.Helper()
	r := newRig(t, nil)
	must(t, os.RemoveAll(filepath.Join(r.cache, "bd")))
	return r, filepath.Join(r.root, "dev", "bin", "bundled")
}

func (r *rig) dev(out string, args ...string) (string, error) {
	return r.run("dev-bundle.sh", append([]string{"--platform", "linux-amd64", "--out", out}, args...)...)
}

func (r *rig) ghLines() int {
	b, _ := os.ReadFile(filepath.Join(r.root, "gh.log"))
	if len(b) == 0 {
		return 0
	}
	return strings.Count(string(b), "\n")
}

// leftovers lists anything next to out that is not out itself (temp stages must never remain).
func leftovers(t *testing.T, out string) []string {
	t.Helper()
	es, _ := os.ReadDir(filepath.Dir(out))
	var n []string
	for _, e := range es {
		if e.Name() != filepath.Base(out) {
			n = append(n, e.Name())
		}
	}
	return n
}

func TestDevBundleInstallsVerifiedToolsLicensesAndNotices(t *testing.T) {
	r, out := newDevRig(t)
	o, err := r.dev(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, o)
	}
	if !strings.Contains(o, "Bundled tools: 1 of 1 verified") {
		t.Fatalf("summary line missing:\n%s", o)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "bd")); !strings.Contains(string(b), "fake bd") {
		t.Fatalf("bd: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "third_party", "bd", "LICENSE")); string(b) != r.license {
		t.Fatalf("license not verbatim: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "THIRD_PARTY_NOTICES.md")); !strings.Contains(string(b), "## bd 1.0.0") {
		t.Fatalf("notices: %q", b)
	}
	// abacus refuses group/world-writable tools and directories: the result must pass its checks.
	for path, want := range map[string]os.FileMode{out: 0o755, filepath.Join(out, "bd"): 0o755, filepath.Join(out, "third_party"): 0o755} {
		fi, err := os.Stat(path)
		must(t, err)
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode %v, want %v", path, fi.Mode().Perm(), want)
		}
	}
	if r.ghLines() != 1 {
		t.Fatalf("expected exactly one download, got %d", r.ghLines())
	}
	if l := leftovers(t, out); len(l) != 0 {
		t.Fatalf("temp files left behind: %v", l)
	}
}

func TestDevBundleFailsClosedOnAShaMismatchAndLeavesNothing(t *testing.T) {
	r, out := newDevRig(t)
	p := filepath.Join(r.remote, "fake_linux_amd64.tar.gz")
	b, _ := os.ReadFile(p)
	b[len(b)-1] ^= 0xff
	must(t, os.WriteFile(p, b, 0o644))
	o, err := r.dev(out)
	if err == nil || !strings.Contains(o, "does not match the pin") {
		t.Fatalf("a tampered download must fail with the sha message: %v\n%s", err, o)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("a failed run must not create the output: %v", err)
	}
	if l := leftovers(t, out); len(l) != 0 {
		t.Fatalf("temp files left behind: %v", l)
	}
}

func TestDevBundleFailureLeavesAnExistingGoodInstallUntouched(t *testing.T) {
	r, out := newDevRig(t)
	if o, err := r.dev(out); err != nil {
		t.Fatalf("%v\n%s", err, o)
	}
	before, _ := os.ReadFile(filepath.Join(out, "bd"))
	// A new pin the remote cannot satisfy: the old install must survive the failed upgrade.
	r.editManifest(func(tool map[string]any) {
		tool["assets"].(map[string]any)["linux-amd64"].(map[string]any)["sha256"] = strings.Repeat("0", 64)
	})
	o, err := r.dev(out)
	if err == nil || !strings.Contains(o, "does not match the pin") {
		t.Fatalf("%v\n%s", err, o)
	}
	after, rerr := os.ReadFile(filepath.Join(out, "bd"))
	if rerr != nil || string(after) != string(before) {
		t.Fatalf("the previous install must be intact: %v %q", rerr, after)
	}
	if l := leftovers(t, out); len(l) != 0 {
		t.Fatalf("temp files left behind: %v", l)
	}
}

func TestDevBundleSecondRunDownloadsNothing(t *testing.T) {
	r, out := newDevRig(t)
	if o, err := r.dev(out); err != nil {
		t.Fatalf("%v\n%s", err, o)
	}
	first := r.ghLines()
	o, err := r.dev(out)
	if err != nil || !strings.Contains(o, "nothing to do") {
		t.Fatalf("%v\n%s", err, o)
	}
	if r.ghLines() != first {
		t.Fatalf("an idempotent run must not download (gh calls %d -> %d)", first, r.ghLines())
	}
	// A tool changed on disk no longer matches its install record: it is replaced, not trusted.
	must(t, os.RemoveAll(filepath.Join(r.cache, "bd"))) // so the refetch is visible in the gh log
	must(t, os.WriteFile(filepath.Join(out, "bd"), []byte("#!/bin/sh\necho evil\n"), 0o755))
	if o, err := r.dev(out); err != nil {
		t.Fatalf("%v\n%s", err, o)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "bd")); !strings.Contains(string(b), "fake bd") {
		t.Fatalf("a tampered tool must be replaced: %q", b)
	}
	if r.ghLines() != first+1 {
		t.Fatalf("the replaced tool is fetched once: %d -> %d", first, r.ghLines())
	}
}

func TestDevBundleCheckOnlyVerifies(t *testing.T) {
	r, out := newDevRig(t)
	o, err := r.dev(out, "--check")
	if err == nil || !strings.Contains(o, "PROBLEM  bd: not installed") {
		t.Fatalf("--check on an empty dir must fail: %v\n%s", err, o)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) || r.ghLines() != 0 {
		t.Fatalf("--check must not create anything or download (gh calls %d)", r.ghLines())
	}
	if o, err := r.dev(out); err != nil {
		t.Fatalf("%v\n%s", err, o)
	}
	calls := r.ghLines()
	o, err = r.dev(out, "--check")
	if err != nil || !strings.Contains(o, "Bundled tools: 1 of 1 verified") {
		t.Fatalf("%v\n%s", err, o)
	}
	must(t, os.WriteFile(filepath.Join(out, "bd"), []byte("#!/bin/sh\necho evil\n"), 0o755))
	o, err = r.dev(out, "--check")
	if err == nil || !strings.Contains(o, "does not match the pin") {
		t.Fatalf("a changed tool must fail --check: %v\n%s", err, o)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "bd")); !strings.Contains(string(b), "evil") || r.ghLines() != calls {
		t.Fatalf("--check must not repair or download")
	}
}

func TestDevBundleRefusesAPlatformWithNoPin(t *testing.T) {
	r, out := newDevRig(t)
	o, err := r.run("dev-bundle.sh", "--platform", "windows-amd64", "--out", out)
	if err == nil || !strings.Contains(o, "has no pin for windows-amd64") {
		t.Fatalf("%v\n%s", err, o)
	}
	o, err = r.run("dev-bundle.sh", "--platform", "../etc", "--out", out)
	if err == nil || !strings.Contains(o, "bad platform") {
		t.Fatalf("%v\n%s", err, o)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) || len(leftovers(t, out)) != 0 {
		t.Fatalf("nothing may be created for a refused platform: %v", leftovers(t, out))
	}
}

func TestDevBundleOneToolWithoutAPinInstallsNothing(t *testing.T) {
	r, out := newDevRig(t)
	var m map[string]any
	b, _ := os.ReadFile(r.manifest)
	must(t, json.Unmarshal(b, &m))
	bd := m["tools"].([]any)[0].(map[string]any)
	bv := map[string]any{}
	for k, v := range bd {
		bv[k] = v
	}
	bv["name"] = "bv"
	bv["assets"] = map[string]any{"darwin-arm64": map[string]string{"asset": "x.tar.gz", "sha256": strings.Repeat("a", 64), "member": "bv"}}
	m["tools"] = []any{bd, bv}
	r.writeManifest(m)
	o, err := r.dev(out)
	if err == nil || !strings.Contains(o, "bv has no pin for linux-amd64") {
		t.Fatalf("%v\n%s", err, o)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) || len(leftovers(t, out)) != 0 {
		t.Fatalf("bd must not be installed on its own: %v", leftovers(t, out))
	}
}

// TestTheRealManifestPinsEveryToolForEveryPlatform fails when a tool is added to bundled-tools.json
// without an asset (and checksum) for a release platform: a release must never ship a gap.
func TestTheRealManifestPinsEveryToolForEveryPlatform(t *testing.T) {
	b, err := os.ReadFile("../../bundled-tools.json")
	must(t, err)
	var m struct {
		Tools []struct {
			Name   string
			Assets map[string]struct{ Asset, Sha256, Member string }
			Lic    string `json:"license_sha256"`
		}
	}
	must(t, json.Unmarshal(b, &m))
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	if len(m.Tools) == 0 {
		t.Fatal("the manifest lists no tools")
	}
	for _, tool := range m.Tools {
		if !hex64.MatchString(tool.Lic) {
			t.Errorf("%s: license_sha256 is not pinned", tool.Name)
		}
		for _, p := range platforms {
			a, ok := tool.Assets[p]
			if !ok || a.Asset == "" || a.Member == "" || !hex64.MatchString(a.Sha256) {
				t.Errorf("%s has no complete pin for %s (run scripts/update-bundled-tools.sh)", tool.Name, p)
			}
		}
		if len(tool.Assets) != len(platforms) {
			t.Errorf("%s pins %d platforms, release builds %d: keep scripts/package-release.sh and the manifest in step", tool.Name, len(tool.Assets), len(platforms))
		}
	}
}

func TestPackageRefusesAToolThatDidNotLandInBundled(t *testing.T) {
	// An asset whose executable is empty would ship a hole: the build must stop.
	empty := newRig(t, func(string) []member {
		return []member{{name: "./bd", body: ""}, {name: "./LICENSE", body: "FAKE LICENSE TEXT\n"}}
	})
	out, err := empty.pkg("--platforms", "linux-amd64")
	if err == nil || !strings.Contains(out, "missing from bundled/") {
		t.Fatalf("an empty executable must stop the build: %v\n%s", err, out)
	}
	if len(empty.outFiles()) != 0 {
		t.Fatalf("nothing may be written: %v", empty.outFiles())
	}
}
