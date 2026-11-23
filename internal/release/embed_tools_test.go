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
func newEmbedRig(t *testing.T) (*rig, string) {
	t.Helper()
	r := newRig(t, nil)
	must(t, os.RemoveAll(filepath.Join(r.cache, "bd")))
	return r, filepath.Join(r.root, "dev", "bin", "bundled")
}

func (r *rig) dev(out string, args ...string) (string, error) {
	return r.run("embed-tools.sh", append([]string{"--platform", "linux-amd64", "--out", out}, args...)...)
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

func TestEmbedToolsInstallsVerifiedToolsLicensesAndNotices(t *testing.T) {
	r, out := newEmbedRig(t)
	o, err := r.dev(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, o)
	}
	if !strings.Contains(o, "Tools: 1 of 1 verified") {
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

func TestEmbedToolsFailsClosedOnAShaMismatchAndLeavesNothing(t *testing.T) {
	r, out := newEmbedRig(t)
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

func TestEmbedToolsFailureLeavesAnExistingGoodInstallUntouched(t *testing.T) {
	r, out := newEmbedRig(t)
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

func TestEmbedToolsSecondRunDownloadsNothing(t *testing.T) {
	r, out := newEmbedRig(t)
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

func TestEmbedToolsCheckOnlyVerifies(t *testing.T) {
	r, out := newEmbedRig(t)
	o, err := r.dev(out, "--check")
	if err == nil || !strings.Contains(o, "PROBLEM  bd: not built in yet") {
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
	if err != nil || !strings.Contains(o, "Tools: 1 of 1 verified") {
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

func TestEmbedToolsRefusesAPlatformWithNoPin(t *testing.T) {
	r, out := newEmbedRig(t)
	o, err := r.run("embed-tools.sh", "--platform", "windows-amd64", "--out", out)
	if err == nil || !strings.Contains(o, "has no pin for windows-amd64") {
		t.Fatalf("%v\n%s", err, o)
	}
	o, err = r.run("embed-tools.sh", "--platform", "../etc", "--out", out)
	if err == nil || !strings.Contains(o, "bad platform") {
		t.Fatalf("%v\n%s", err, o)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) || len(leftovers(t, out)) != 0 {
		t.Fatalf("nothing may be created for a refused platform: %v", leftovers(t, out))
	}
}

func TestEmbedToolsOneToolWithoutAPinInstallsNothing(t *testing.T) {
	r, out := newEmbedRig(t)
	var m map[string]any
	b, _ := os.ReadFile(r.manifest)
	must(t, json.Unmarshal(b, &m))
	bd := m["tools"].([]any)[0].(map[string]any)
	second := map[string]any{}
	for k, v := range bd {
		second[k] = v
	}
	second["name"] = "second"
	second["assets"] = map[string]any{"darwin-arm64": map[string]string{"asset": "x.tar.gz", "sha256": strings.Repeat("a", 64), "member": "second"}}
	m["tools"] = []any{bd, second}
	r.writeManifest(m)
	o, err := r.dev(out)
	if err == nil || !strings.Contains(o, "second has no pin for linux-amd64") {
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
			Name      string
			Kind      string
			Integrity string `json:"npm_integrity"`
			Assets    map[string]struct {
				Asset, Sha256, Member string
				BunTarget             string `json:"bun_target"`
			}
			Lic string `json:"license_sha256"`
		}
	}
	must(t, json.Unmarshal(b, &m))
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	if len(m.Tools) == 0 {
		t.Fatal("the manifest lists no tools")
	}
	for _, tool := range m.Tools {
		if tool.Name == "bv" {
			t.Error("bv is linked, not bundled: it belongs in linked_licenses, not tools")
		}
		if !hex64.MatchString(tool.Lic) {
			t.Errorf("%s: license_sha256 is not pinned", tool.Name)
		}
		for _, p := range platforms {
			a, ok := tool.Assets[p]
			if tool.Kind == "npm-bun" { // built from its npm package: pinned by integrity, compiled per target
				if !ok || !strings.HasPrefix(a.BunTarget, "bun-") || !strings.HasPrefix(tool.Integrity, "sha512-") {
					t.Errorf("%s has no complete build pin for %s", tool.Name, p)
				}
				continue
			}
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
	if err == nil || !strings.Contains(out, "packing the tools failed") {
		t.Fatalf("an empty executable must stop the build: %v\n%s", err, out)
	}
	if len(empty.outFiles()) != 0 {
		t.Fatalf("nothing may be written: %v", empty.outFiles())
	}
}

// writeTree writes files (path -> body) under dir.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, body := range files {
		must(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755))
		must(t, os.WriteFile(filepath.Join(dir, p), []byte(body), 0o644))
	}
}

func TestEmbedToolsRefusesAnOutputDirectoryThatHoldsForeignFiles(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"plain files":         {"precious.txt": "keep me", "sub/notes.md": "and me"},
		"notices but no pins": {"THIRD_PARTY_NOTICES.md": "x", "precious.txt": "keep me"},
		"pins but no notices": {".pins/bd": "x", "precious.txt": "keep me"},
	} {
		t.Run(name, func(t *testing.T) {
			r, out := newEmbedRig(t)
			writeTree(t, out, files)
			o, err := r.dev(out)
			if err == nil || !strings.Contains(o, "is not empty and does not look like an abacus bundle") {
				t.Fatalf("a foreign --out must be refused: %v\n%s", err, o)
			}
			for p, body := range files {
				if b, rerr := os.ReadFile(filepath.Join(out, p)); rerr != nil || string(b) != body {
					t.Fatalf("%s must be intact: %v %q", p, rerr, b)
				}
			}
			if r.ghLines() != 0 {
				t.Fatalf("nothing is downloaded for a refused --out (gh calls %d)", r.ghLines())
			}
			if l := leftovers(t, out); len(l) != 0 {
				t.Fatalf("temp files left behind: %v", l)
			}
		})
	}
}

func TestEmbedToolsRefusesAnOutputThatIsAFileOrASymlink(t *testing.T) {
	r, out := newEmbedRig(t)
	must(t, os.MkdirAll(filepath.Dir(out), 0o755))
	must(t, os.WriteFile(out, []byte("a file"), 0o644))
	if o, err := r.dev(out); err == nil || !strings.Contains(o, "not a directory") {
		t.Fatalf("%v\n%s", err, o)
	}
	must(t, os.Remove(out))
	target := filepath.Join(r.root, "elsewhere")
	writeTree(t, target, map[string]string{"x": "y"})
	must(t, os.Symlink(target, out))
	if o, err := r.dev(out); err == nil || !strings.Contains(o, "symlink") {
		t.Fatalf("%v\n%s", err, o)
	}
	if b, _ := os.ReadFile(filepath.Join(target, "x")); string(b) != "y" {
		t.Fatalf("the symlink target must be untouched")
	}
}

func TestEmbedToolsAcceptsAnEmptyOutputDirectoryAndUpgradesItsOwnInstall(t *testing.T) {
	r, out := newEmbedRig(t)
	must(t, os.MkdirAll(out, 0o755))
	if o, err := r.dev(out); err != nil {
		t.Fatalf("an empty --out is fine: %v\n%s", err, o)
	}
	must(t, os.WriteFile(filepath.Join(out, "bd"), []byte("#!/bin/sh\necho evil\n"), 0o755))
	if o, err := r.dev(out); err != nil {
		t.Fatalf("our own install is replaced in place: %v\n%s", err, o)
	}
}

// A mv that fails when it moves the finished stage into place (the second move of the swap).
func (r *rig) failStageMove() {
	bin := filepath.Join(r.root, "bin")
	must(r.t, os.WriteFile(filepath.Join(bin, "mv"), []byte(`#!/bin/sh
case "$1" in *.tools.*.old) ;; *.tools.*) echo "fake mv: refusing to move the stage" >&2; exit 1 ;; esac
exec /bin/mv "$@"
`), 0o755))
}

func TestEmbedToolsRestoresThePreviousInstallWhenTheSwapFails(t *testing.T) {
	if _, err := os.Stat("/bin/mv"); err != nil {
		t.Skip("no /bin/mv")
	}
	r, out := newEmbedRig(t)
	if o, err := r.dev(out); err != nil {
		t.Fatalf("%v\n%s", err, o)
	}
	before, _ := os.ReadFile(filepath.Join(out, "bd"))
	must(t, os.WriteFile(filepath.Join(out, "bd"), []byte("#!/bin/sh\necho evil\n"), 0o755)) // forces a rebuild
	must(t, os.WriteFile(filepath.Join(out, ".pins", "bd"), []byte("stale"), 0o644))
	r.failStageMove()
	o, err := r.dev(out)
	if err == nil || !strings.Contains(o, "refusing to move the stage") {
		t.Fatalf("the failed swap must fail the run: %v\n%s", err, o)
	}
	if b, rerr := os.ReadFile(filepath.Join(out, "bd")); rerr != nil || !strings.Contains(string(b), "evil") {
		t.Fatalf("the previous install must be put back as it was: %v %q (was %q)", rerr, b, before)
	}
	if b, _ := os.ReadFile(filepath.Join(out, ".pins", "bd")); string(b) != "stale" {
		t.Fatalf("the previous install record must be back: %q", b)
	}
	if l := leftovers(t, out); len(l) != 0 {
		t.Fatalf("temp files left behind: %v", l)
	}
}

func TestEmbedToolsVerifiedAlsoChecksTheLicenseModesAndStaleTools(t *testing.T) {
	setup := func(t *testing.T) (*rig, string) {
		r, out := newEmbedRig(t)
		if o, err := r.dev(out); err != nil {
			t.Fatalf("%v\n%s", err, o)
		}
		return r, out
	}
	t.Run("tampered license", func(t *testing.T) {
		r, out := setup(t)
		must(t, os.WriteFile(filepath.Join(out, "third_party", "bd", "LICENSE"), []byte("EVIL LICENSE\n"), 0o644))
		if o, err := r.dev(out, "--check"); err == nil || !strings.Contains(o, "license") {
			t.Fatalf("--check must flag a changed license: %v\n%s", err, o)
		}
		if o, err := r.dev(out); err != nil || strings.Contains(o, "nothing to do") {
			t.Fatalf("a changed license must be repaired, not skipped: %v\n%s", err, o)
		}
		if b, _ := os.ReadFile(filepath.Join(out, "third_party", "bd", "LICENSE")); string(b) != r.license {
			t.Fatalf("license not restored: %q", b)
		}
	})
	t.Run("group-writable tool", func(t *testing.T) {
		r, out := setup(t)
		must(t, os.Chmod(filepath.Join(out, "bd"), 0o775))
		if o, err := r.dev(out, "--check"); err == nil || !strings.Contains(o, "writable") {
			t.Fatalf("--check must flag a group-writable tool: %v\n%s", err, o)
		}
		if o, err := r.dev(out); err != nil || strings.Contains(o, "nothing to do") {
			t.Fatalf("%v\n%s", err, o)
		}
		fi, _ := os.Stat(filepath.Join(out, "bd"))
		if fi.Mode().Perm() != 0o755 {
			t.Fatalf("mode not repaired: %v", fi.Mode().Perm())
		}
	})
	t.Run("world-writable directory", func(t *testing.T) {
		r, out := setup(t)
		must(t, os.Chmod(out, 0o757))
		if o, err := r.dev(out, "--check"); err == nil || !strings.Contains(o, "writable") {
			t.Fatalf("--check must flag a world-writable install directory: %v\n%s", err, o)
		}
	})
	t.Run("tool no longer in the manifest", func(t *testing.T) {
		r, out := setup(t)
		must(t, os.WriteFile(filepath.Join(out, "gone"), []byte("#!/bin/sh\n"), 0o755))
		must(t, os.WriteFile(filepath.Join(out, ".pins", "gone"), []byte("x"), 0o644))
		if o, err := r.dev(out, "--check"); err == nil || !strings.Contains(o, "gone") {
			t.Fatalf("--check must report a tool the manifest dropped: %v\n%s", err, o)
		}
		o, err := r.dev(out)
		if err != nil || strings.Contains(o, "nothing to do") {
			t.Fatalf("a stale tool must not read as nothing to do: %v\n%s", err, o)
		}
		if _, err := os.Stat(filepath.Join(out, "gone")); !os.IsNotExist(err) {
			t.Fatalf("the stale tool must be removed by the rebuild: %v", err)
		}
	})
}

func TestScriptsRefuseUnsafeToolNames(t *testing.T) {
	for _, name := range []string{"../evil", "a/b", "Bd", "bd x", ""} {
		r, out := newEmbedRig(t)
		r.editManifest(func(tool map[string]any) { tool["name"] = name })
		for _, script := range []string{"embed-tools.sh", "package-release.sh"} {
			args := []string{"--platform", "linux-amd64", "--out", out}
			if script == "package-release.sh" {
				args = []string{"0.0.1", "--out", r.out, "--platforms", "linux-amd64"}
			}
			if o, err := r.run(script, args...); err == nil || !strings.Contains(o, "unsafe tool name") {
				t.Errorf("%s with tool name %q must be refused: %v\n%s", script, name, err, o)
			}
		}
		if _, err := os.Stat(out); !os.IsNotExist(err) || len(r.outFiles()) != 0 {
			t.Errorf("nothing may be written for name %q", name)
		}
	}
}

// The linked-license pin names the version go.mod links, and pins its license text: a bump of the
// module in go.mod without re-pinning the license must fail here, before packaging does.
func TestTheRealManifestPinsTheLinkedBvLicenseAtTheGoModVersion(t *testing.T) {
	module, version, pin := realLinkedPin(t)
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(pin) {
		t.Errorf("bv license_sha256 is not pinned: %q", pin)
	}
	gomod, err := os.ReadFile("../../go.mod")
	must(t, err)
	if !strings.Contains(string(gomod), "\t"+module+" "+version+"\n") {
		t.Errorf("go.mod does not require %s %s: re-pin linked_licenses with go.mod", module, version)
	}
}
