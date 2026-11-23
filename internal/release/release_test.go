// Package release tests the packaging scripts offline: tiny fake archives, a fake gh, and a
// manifest and cache the scripts are pointed at through their environment. Nothing here touches
// the network, a real tool or the real manifest.
package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var platforms = []string{"darwin-amd64", "darwin-arm64", "linux-amd64", "linux-arm64"}

type member struct {
	name, body, link string
	typ              byte
}

func tarGz(t *testing.T, members ...member) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, m := range members {
		typ := m.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		h := &tar.Header{Name: m.name, Typeflag: typ, Mode: 0o755, Size: int64(len(m.body)), Linkname: m.link}
		if typ != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(m.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

type rig struct {
	t                        *testing.T
	root, cache, remote, out string
	manifest                 string
	env                      []string
	license                  string
}

func scriptsDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.Abs("../../scripts")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// newRig writes, for every platform, a fake bd archive into the cache (and the same into a fake
// "remote" for gh), and a manifest pinning them.
func newRig(t *testing.T, members func(platform string) []member) *rig {
	t.Helper()
	for _, tool := range []string{"jq", "bash", "tar", "paste", "awk"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	r := &rig{t: t, root: t.TempDir()}
	r.cache, r.remote, r.out = filepath.Join(r.root, "cache"), filepath.Join(r.root, "remote"), filepath.Join(r.root, "out")
	for _, d := range []string{filepath.Join(r.cache, "bd"), r.remote, r.out} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r.license = "FAKE LICENSE TEXT\n"
	if members == nil {
		members = func(string) []member {
			return []member{{name: "./bd", body: "#!/bin/sh\necho fake bd\n"}, {name: "./LICENSE", body: r.license}}
		}
	}
	assets := map[string]any{}
	var sums strings.Builder
	for _, p := range platforms {
		b := tarGz(t, members(p)...)
		name := "fake_" + strings.ReplaceAll(p, "-", "_") + ".tar.gz"
		must(t, os.WriteFile(filepath.Join(r.cache, "bd", name), b, 0o644))
		must(t, os.WriteFile(filepath.Join(r.remote, name), b, 0o644))
		assets[p] = map[string]string{"asset": name, "sha256": sum(b), "member": "bd"}
		sums.WriteString(sum(b) + "  " + name + "\n")
	}
	must(t, os.WriteFile(filepath.Join(r.remote, "checksums.txt"), []byte(sums.String()), 0o644))
	m := map[string]any{"schema": 1, "tools": []any{map[string]any{
		"name": "bd", "repo": "o/fake", "license": "MIT", "version": "1.0.0", "tag_template": "v{version}",
		"asset_template": "fake_{os}_{arch}.tar.gz", "checksums_asset": "checksums.txt", "member": "bd",
		"license_source": map[string]string{"from": "archive", "member": "LICENSE"},
		"assets":         assets, "license_sha256": sum([]byte(r.license)),
	}}}
	r.manifest = filepath.Join(r.root, "bundled-tools.json")
	r.writeManifest(m)
	prebuilt := filepath.Join(r.root, "abacus-prebuilt")
	must(t, os.WriteFile(prebuilt, []byte("fake abacus binary"), 0o755))
	// A fake gh that serves files from the remote dir; the real one must never run here.
	bin := filepath.Join(r.root, "bin")
	must(t, os.MkdirAll(bin, 0o755))
	must(t, os.WriteFile(filepath.Join(bin, "gh"), []byte(`#!/bin/sh
name=""; outfile=""; outdir="."
[ -z "$FAKE_GH_LOG" ] || echo "$*" >>"$FAKE_GH_LOG"
while [ $# -gt 0 ]; do case "$1" in -p) name=$2; shift 2;; -O) outfile=$2; shift 2;; -D) outdir=$2; shift 2;; *) shift;; esac; done
src="$FAKE_GH_REMOTE/$name"; [ -f "$src" ] || { echo "fake gh: no $name" >&2; exit 1; }
if [ -n "$outfile" ]; then cp "$src" "$outfile"; else cp "$src" "$outdir/$name"; fi
`), 0o755))
	r.env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HOME="+r.root, "FAKE_GH_REMOTE="+r.remote, "FAKE_GH_LOG="+filepath.Join(r.root, "gh.log"),
		"ABACUS_BUNDLE_MANIFEST="+r.manifest, "ABACUS_BUNDLE_CACHE="+r.cache, "ABACUS_PACKAGE_PREBUILT_BIN="+prebuilt)
	return r
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (r *rig) writeManifest(m any) {
	b, _ := json.MarshalIndent(m, "", "  ")
	must(r.t, os.WriteFile(r.manifest, b, 0o644))
}

func (r *rig) editManifest(f func(tool map[string]any)) {
	var m map[string]any
	b, err := os.ReadFile(r.manifest)
	must(r.t, err)
	must(r.t, json.Unmarshal(b, &m))
	f(m["tools"].([]any)[0].(map[string]any))
	r.writeManifest(m)
}

func (r *rig) run(script string, args ...string) (string, error) {
	cmd := exec.Command("bash", append([]string{filepath.Join(scriptsDir(r.t), script)}, args...)...)
	cmd.Env = r.env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

func (r *rig) pkg(extra ...string) (string, error) {
	return r.run("package-release.sh", append([]string{"0.0.1", "--out", r.out}, extra...)...)
}

func (r *rig) outFiles() []string {
	es, _ := os.ReadDir(r.out)
	var n []string
	for _, e := range es {
		n = append(n, e.Name())
	}
	return n
}

func TestPackageUntouchedArchivePasses(t *testing.T) {
	r := newRig(t, nil)
	must(t, os.WriteFile(filepath.Join(r.out, "SHA256SUMS"), []byte("stale\n"), 0o644))
	if out, err := r.pkg("--platforms", "linux-amd64"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	files := strings.Join(r.outFiles(), " ")
	if files != "SHA256SUMS abacus_0.0.1_linux_amd64.tar.gz" {
		t.Fatalf("out: %s", files)
	}
	if b, _ := os.ReadFile(filepath.Join(r.out, "SHA256SUMS")); strings.Contains(string(b), "stale") || !strings.Contains(string(b), "abacus_0.0.1_linux_amd64.tar.gz") {
		t.Fatalf("SHA256SUMS: %q", b)
	}
	// Unpack and check the layout, the executable and the license.
	dir := t.TempDir()
	must(t, exec.Command("tar", "-xzf", filepath.Join(r.out, "abacus_0.0.1_linux_amd64.tar.gz"), "-C", dir).Run())
	base := filepath.Join(dir, "abacus_0.0.1_linux_amd64")
	if b, _ := os.ReadFile(filepath.Join(base, "bundled", "bd")); !strings.Contains(string(b), "fake bd") {
		t.Fatalf("bundled/bd: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(base, "third_party", "bd", "LICENSE")); string(b) != r.license {
		t.Fatalf("license not verbatim: %q", b)
	}
	fi, _ := os.Stat(filepath.Join(base, "bundled", "bd"))
	if fi.Mode().Perm()&0o022 != 0 || fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("bundled mode %v", fi.Mode().Perm())
	}
	for _, f := range []string{"THIRD_PARTY_NOTICES.md", "LICENSE", "README.md", "abacus", "third_party/bundled-tools.json"} {
		if _, err := os.Stat(filepath.Join(base, f)); err != nil {
			t.Errorf("missing %s", f)
		}
	}
}

// The notices must say what is true: the tools are separate programs, and the binary also
// links parts of bv's Go packages and other modules, each with its license text.
func TestPackageNoticesAreTruthfulAboutLinkedGoCode(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not installed")
	}
	r := newRig(t, nil)
	if out, err := r.pkg("--platforms", "linux-amd64"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	dir := t.TempDir()
	must(t, exec.Command("tar", "-xzf", filepath.Join(r.out, "abacus_0.0.1_linux_amd64.tar.gz"), "-C", dir).Run())
	b, err := os.ReadFile(filepath.Join(dir, "abacus_0.0.1_linux_amd64", "THIRD_PARTY_NOTICES.md"))
	must(t, err)
	n := string(b)
	if strings.Contains(n, "does not link or import") {
		t.Errorf("the notices still claim abacus links no tool code:\n%s", n)
	}
	for _, want := range []string{
		"unmodified", "subprocesses",
		"statically links parts of bv's Go packages (bv v", "pkg/analysis",
		"third_party/bv/LICENSE",
		"## Go modules linked into abacus",
		"### gonum.org/v1/gonum v", "### github.com/goccy/go-json v", "### golang.org/x/sync v",
		"### github.com/Dicklesworthstone/beads_viewer v",
		"Gonum Authors", // a license text, verbatim
	} {
		if !strings.Contains(n, want) {
			t.Errorf("notices lack %q", want)
		}
	}
	// One section per module, however many of its packages are linked.
	if c := strings.Count(n, "### gonum.org/v1/gonum v"); c != 1 {
		t.Errorf("gonum has %d sections, want 1", c)
	}
	// Listed in go.mod but not linked on linux (x/text), and the main module, are not listed.
	for _, not := range []string{"### golang.org/x/text v", "### github.com/andrew-bierman/abacus"} {
		if strings.Contains(n, not) {
			t.Errorf("lists %q, which is not a linked dependency", not)
		}
	}
}

func TestPackageFailsClosedOnAFlippedByte(t *testing.T) {
	r := newRig(t, nil)
	must(t, os.WriteFile(filepath.Join(r.out, "SHA256SUMS"), []byte("stale\n"), 0o644))
	p := filepath.Join(r.cache, "bd", "fake_linux_amd64.tar.gz")
	b, _ := os.ReadFile(p)
	b[len(b)-1] ^= 0xff
	must(t, os.WriteFile(p, b, 0o644))
	out, err := r.pkg("--platforms", "linux-amd64")
	if err == nil || !strings.Contains(out, "does not match the pin") {
		t.Fatalf("a tampered asset must fail with the sha message: %v\n%s", err, out)
	}
	if len(r.outFiles()) != 0 {
		t.Fatalf("a failed build must leave nothing in the output: %v", r.outFiles())
	}
}

func TestPackageFailsWhenTheLicenseChangedFromItsPin(t *testing.T) {
	r := newRig(t, nil)
	r.editManifest(func(tool map[string]any) { tool["license_sha256"] = strings.Repeat("0", 64) })
	out, err := r.pkg("--platforms", "linux-amd64")
	if err == nil || !strings.Contains(out, "license changed from its pin") {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestPackageRefusesLinkAndPathEscapingMembers(t *testing.T) {
	cases := map[string][]member{
		"symlink":  {{name: "./bd", body: "x"}, {name: "./LICENSE", body: "FAKE LICENSE TEXT\n"}, {name: "./evil", typ: tar.TypeSymlink, link: "/etc/passwd"}},
		"hardlink": {{name: "./bd", body: "x"}, {name: "./LICENSE", body: "FAKE LICENSE TEXT\n"}, {name: "./hl", typ: tar.TypeLink, link: "bd"}},
		"dotdot":   {{name: "./bd", body: "x"}, {name: "./LICENSE", body: "FAKE LICENSE TEXT\n"}, {name: "../escape", body: "x"}},
		"absolute": {{name: "./bd", body: "x"}, {name: "./LICENSE", body: "FAKE LICENSE TEXT\n"}, {name: "/abs/path", body: "x"}},
	}
	for name, ms := range cases {
		r := newRig(t, func(string) []member { return ms })
		out, err := r.pkg("--platforms", "linux-amd64")
		if err == nil || !(strings.Contains(out, "link member") || strings.Contains(out, "unsafe member")) {
			t.Errorf("%s member must be refused: %v\n%s", name, err, out)
		}
	}
	// The executable itself being a symlink is refused too.
	r := newRig(t, func(string) []member {
		return []member{{name: "./bd", typ: tar.TypeSymlink, link: "/bin/sh"}, {name: "./LICENSE", body: "FAKE LICENSE TEXT\n"}}
	})
	if out, err := r.pkg("--platforms", "linux-amd64"); err == nil {
		t.Errorf("a symlink executable must be refused:\n%s", out)
	}
}

func TestPackageIsAllOrNothingAcrossPlatforms(t *testing.T) {
	r := newRig(t, nil)
	r.editManifest(func(tool map[string]any) { delete(tool["assets"].(map[string]any), "linux-arm64") })
	out, err := r.pkg("--platforms", "linux-amd64 linux-arm64")
	if err == nil || !strings.Contains(out, "has no pin for linux-arm64") {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(r.outFiles()) != 0 {
		t.Fatalf("the first platform's archive must not be left behind: %v", r.outFiles())
	}
}

func (r *rig) removeChecksumFor(platform string) {
	name := "fake_" + strings.ReplaceAll(platform, "-", "_") + ".tar.gz"
	p := filepath.Join(r.remote, "checksums.txt")
	b, _ := os.ReadFile(p)
	var keep []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if !strings.HasSuffix(l, name) {
			keep = append(keep, l)
		}
	}
	must(r.t, os.WriteFile(p, []byte(strings.Join(keep, "\n")+"\n"), 0o644))
}

func TestUpdateRefusesWhenUpstreamPublishesNoChecksumUnlessTrusted(t *testing.T) {
	r := newRig(t, nil)
	r.removeChecksumFor("linux-arm64")
	// Start from an unpinned manifest so the script has real work to do.
	r.editManifest(func(tool map[string]any) { delete(tool, "assets"); delete(tool, "license_sha256") })
	before, _ := os.ReadFile(r.manifest)
	out, err := r.run("update-bundled-tools.sh", "bd", "1.0.0")
	if err == nil || !strings.Contains(out, "publishes no checksum") || !strings.Contains(out, "--trust-first-download") {
		t.Fatalf("%v\n%s", err, out)
	}
	if after, _ := os.ReadFile(r.manifest); !bytes.Equal(before, after) {
		t.Fatalf("a refused update must leave the manifest untouched:\n%s", after)
	}
	if left, _ := filepath.Glob(r.manifest + ".*"); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
	out, err = r.run("update-bundled-tools.sh", "bd", "1.0.0", "--trust-first-download")
	if err != nil || !strings.Contains(out, "WARNING") {
		t.Fatalf("%v\n%s", err, out)
	}
	var m struct {
		Tools []struct {
			Version string
			Assets  map[string]struct{ Sha256 string }
			License string `json:"license_sha256"`
		}
	}
	b, _ := os.ReadFile(r.manifest)
	must(t, json.Unmarshal(b, &m))
	arm, _ := os.ReadFile(filepath.Join(r.remote, "fake_linux_arm64.tar.gz"))
	if m.Tools[0].Assets["linux-arm64"].Sha256 != sum(arm) || m.Tools[0].License != sum([]byte(r.license)) || m.Tools[0].Version != "1.0.0" {
		t.Fatalf("%+v", m.Tools[0])
	}
}

func TestUpdateRejectsADownloadThatDoesNotMatchUpstream(t *testing.T) {
	r := newRig(t, nil)
	// The remote checksum file says one thing, the asset served is another.
	p := filepath.Join(r.remote, "fake_linux_amd64.tar.gz")
	b, _ := os.ReadFile(p)
	b[len(b)-1] ^= 0xff
	must(t, os.WriteFile(p, b, 0o644))
	must(t, os.Remove(filepath.Join(r.cache, "bd", "fake_linux_amd64.tar.gz")))
	out, err := r.run("update-bundled-tools.sh", "bd", "1.0.0")
	if err == nil || !strings.Contains(out, "does not match upstream") {
		t.Fatalf("%v\n%s", err, out)
	}
}
