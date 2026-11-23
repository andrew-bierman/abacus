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
	"github.com/andrew-bierman/abacus/internal/embedtools"
	"github.com/klauspost/compress/zstd"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
	m := map[string]any{"schema": 1, "linked_licenses": realLinkedLicenses(t), "tools": []any{map[string]any{
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
		"HOME="+r.root, "ABACUS_EMBED_BLOBS_DIR="+filepath.Join(r.root, "blobs"), "GOMODCACHE="+goEnv(t, "GOMODCACHE"), "GOCACHE="+goEnv(t, "GOCACHE"),
		"GOFLAGS=-mod=mod", "FAKE_GH_REMOTE="+r.remote, "FAKE_GH_LOG="+filepath.Join(r.root, "gh.log"),
		"ABACUS_BUNDLE_MANIFEST="+r.manifest, "ABACUS_BUNDLE_CACHE="+r.cache, "ABACUS_PACKAGE_PREBUILT_BIN="+prebuilt)
	return r
}

// realLinkedLicenses is the real manifest's linked_licenses (the bv entry): the rig links the real
// module graph, so it needs the real pin to package against.
func realLinkedLicenses(t *testing.T) any {
	t.Helper()
	b, err := os.ReadFile("../../bundled-tools.json")
	must(t, err)
	var m map[string]any
	must(t, json.Unmarshal(b, &m))
	if m["linked_licenses"] == nil {
		t.Fatal("the real manifest has no linked_licenses")
	}
	return m["linked_licenses"]
}

// goEnv reads a Go setting from the process running the tests, so the packaging script (which
// lists linked modules) reuses the real module cache instead of building a read-only one under
// the test's temporary HOME, which TempDir cleanup cannot remove.
func goEnv(t *testing.T, key string) string {
	t.Helper()
	out, err := exec.Command("go", "env", key).Output()
	must(t, err)
	return strings.TrimSpace(string(out))
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

func (r *rig) editTop(f func(m map[string]any)) {
	var m map[string]any
	b, err := os.ReadFile(r.manifest)
	must(r.t, err)
	must(r.t, json.Unmarshal(b, &m))
	f(m)
	r.writeManifest(m)
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
	if files != "EMBEDDED_TOOLS.md SHA256SUMS abacus_0.0.1_linux_amd64.tar.gz" {
		t.Fatalf("out: %s", files)
	}
	if b, _ := os.ReadFile(filepath.Join(r.out, "SHA256SUMS")); strings.Contains(string(b), "stale") || !strings.Contains(string(b), "abacus_0.0.1_linux_amd64.tar.gz") {
		t.Fatalf("SHA256SUMS: %q", b)
	}
	// Unpack and check the layout, the executable and the license.
	dir := t.TempDir()
	must(t, exec.Command("tar", "-xzf", filepath.Join(r.out, "abacus_0.0.1_linux_amd64.tar.gz"), "-C", dir).Run())
	base := filepath.Join(dir, "abacus_0.0.1_linux_amd64")
	if b, _ := os.ReadFile(filepath.Join(base, "third_party", "bd", "LICENSE")); string(b) != r.license {
		t.Fatalf("license not verbatim: %q", b)
	}
	// The archive is one binary plus notices: no sidecar folder, no scripts, no manifest, no README.
	listing, err := exec.Command("tar", "-tzf", filepath.Join(r.out, "abacus_0.0.1_linux_amd64.tar.gz")).Output()
	must(t, err)
	var members []string
	for _, l := range strings.Split(strings.TrimSpace(string(listing)), "\n") {
		if !strings.HasSuffix(l, "/") {
			members = append(members, strings.TrimPrefix(l, "abacus_0.0.1_linux_amd64/"))
		}
	}
	sort.Strings(members)
	if got := strings.Join(members, " "); got != "LICENSE THIRD_PARTY_NOTICES.md abacus third_party/bd/LICENSE third_party/bv/LICENSE" {
		t.Fatalf("archive files: %s", got)
	}
	fi, _ := os.Stat(filepath.Join(base, "abacus"))
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("abacus mode %v", fi.Mode().Perm())
	}
	// The notices in the archive are the very file embedded in the binary's blob set, and the blob
	// round-trips to the verified executable.
	blobs := filepath.Join(r.root, "blobs", "linux-amd64")
	an, _ := os.ReadFile(filepath.Join(base, "THIRD_PARTY_NOTICES.md"))
	bn, _ := os.ReadFile(filepath.Join(blobs, "THIRD_PARTY_NOTICES.md"))
	if len(an) == 0 || !bytes.Equal(an, bn) {
		t.Fatal("the archive notices differ from the embedded ones")
	}
	mb, err := os.ReadFile(filepath.Join(blobs, "manifest.json"))
	must(t, err)
	m, err := embedtools.Parse(mb)
	must(t, err)
	tl, ok := m.Tool("bd")
	if !ok || tl.SHA256 != sum([]byte("#!/bin/sh\necho fake bd\n")) || tl.Version != "1.0.0" || tl.LicenseFile != "licenses/bd/LICENSE" {
		t.Fatalf("manifest: %+v", m)
	}
	f, err := os.Open(filepath.Join(blobs, tl.File))
	must(t, err)
	defer f.Close()
	dec, err := zstd.NewReader(f)
	must(t, err)
	defer dec.Close()
	raw, err := io.ReadAll(dec)
	must(t, err)
	if string(raw) != "#!/bin/sh\necho fake bd\n" {
		t.Fatalf("blob round trip: %q", raw)
	}
	rel, _ := os.ReadFile(filepath.Join(r.out, "EMBEDDED_TOOLS.md"))
	if !strings.Contains(string(rel), "| linux-amd64 | bd | 1.0.0 | `"+tl.SHA256+"` |") {
		t.Fatalf("release notes lack the executable hash:\n%s", rel)
	}
}

// unpack packages linux-amd64 and returns the extracted archive root.
func (r *rig) unpack() string {
	r.t.Helper()
	if out, err := r.pkg("--platforms", "linux-amd64"); err != nil {
		r.t.Fatalf("%v\n%s", err, out)
	}
	dir := r.t.TempDir()
	must(r.t, exec.Command("tar", "-xzf", filepath.Join(r.out, "abacus_0.0.1_linux_amd64.tar.gz"), "-C", dir).Run())
	return filepath.Join(dir, "abacus_0.0.1_linux_amd64")
}

func realLinkedPin(t *testing.T) (module, version, licenseSHA string) {
	t.Helper()
	b, err := os.ReadFile("../../bundled-tools.json")
	must(t, err)
	var m struct {
		Linked []struct {
			Name, Module, Version string
			SHA                   string `json:"license_sha256"`
		} `json:"linked_licenses"`
	}
	must(t, json.Unmarshal(b, &m))
	for _, l := range m.Linked {
		if l.Name == "bv" {
			return l.Module, l.Version, l.SHA
		}
	}
	t.Fatal("no bv entry in linked_licenses")
	return
}

// The notices must say what is true: bd, lambe and beads-lease are separate programs, bv is a
// linked library (no bv program ships), and every linked Go module is listed with its license
// text, ending the file.
func TestPackageNoticesAreTruthfulAboutLinkedGoCode(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not installed")
	}
	base := newRig(t, nil).unpack()
	b, err := os.ReadFile(filepath.Join(base, "THIRD_PARTY_NOTICES.md"))
	must(t, err)
	n := string(b)
	_, version, _ := realLinkedPin(t)
	for _, want := range []string{
		"unmodified", "single file", "subprocess",
		"## bv " + version + " (linked library)",
		"statically links parts of bv's Go packages (bv " + version, "pkg/analysis",
		"MIT with OpenAI/Anthropic rider",
		"third_party/bv/LICENSE",
		"## Go modules linked into abacus",
		"### gonum.org/v1/gonum v", "### github.com/goccy/go-json v", "### golang.org/x/sync v",
		"### github.com/Dicklesworthstone/beads_viewer " + version,
		"Gonum Authors", // a license text, verbatim
	} {
		if !strings.Contains(n, want) {
			t.Errorf("notices lack %q", want)
		}
	}
	// bv is a library, not a shipped program.
	for _, not := range []string{"bundled/bv", "ABACUS_GRAPH_ENGINE", "does not link or import", "optional in-process"} {
		if strings.Contains(n, not) {
			t.Errorf("notices still say %q", not)
		}
	}
	// The Go module list ends the file: bv's own section comes before it, and the last section is
	// the Go toolchain's.
	if strings.Index(n, "## bv ") > strings.Index(n, "## Go modules linked into abacus") {
		t.Error("the bv entry must come before the Go module list")
	}
	if i := strings.LastIndex(n, "### "); !strings.HasPrefix(n[i:], "### Go standard library and runtime") {
		t.Errorf("the notices must end with the Go module list, last section %q", n[i:i+60])
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

// bv is linked, so its license ships verbatim even though no bv sidecar does: the file in the
// archive is the pinned module's LICENSE, and its sha256 is the pin in the manifest.
func TestPackageShipsBvLicenseVerbatimMatchingThePin(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not installed")
	}
	r := newRig(t, nil)
	base := r.unpack()
	got, err := os.ReadFile(filepath.Join(base, "third_party", "bv", "LICENSE"))
	must(t, err)
	module, version, pin := realLinkedPin(t)
	if sum(got) != pin {
		t.Fatalf("third_party/bv/LICENSE sha256 %s, the pin says %s", sum(got), pin)
	}
	out, err := exec.Command("go", "mod", "download", "-json", module+"@"+version).Output()
	must(t, err)
	var dl struct{ Dir string }
	must(t, json.Unmarshal(out, &dl))
	want, err := os.ReadFile(filepath.Join(dl.Dir, "LICENSE"))
	must(t, err)
	if !bytes.Equal(got, want) {
		t.Fatal("third_party/bv/LICENSE is not the module's LICENSE byte for byte")
	}
	if _, err := os.Stat(filepath.Join(base, "bundled")); !os.IsNotExist(err) {
		t.Fatalf("no sidecar folder may ship: %v", err)
	}
}

func TestPackageFailsClosedWhenTheBvLicenseChangedFromItsPin(t *testing.T) {
	r := newRig(t, nil)
	r.editTop(func(m map[string]any) {
		m["linked_licenses"].([]any)[0].(map[string]any)["license_sha256"] = strings.Repeat("0", 64)
	})
	out, err := r.pkg("--platforms", "linux-amd64")
	if err == nil || !strings.Contains(out, "bv: license changed from its pin") {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(r.outFiles()) != 0 {
		t.Fatalf("a failed build must leave nothing in the output: %v", r.outFiles())
	}
}

func TestPackageFailsClosedWhenTheLinkedBvVersionIsNotThePinnedOne(t *testing.T) {
	r := newRig(t, nil)
	r.editTop(func(m map[string]any) { m["linked_licenses"].([]any)[0].(map[string]any)["version"] = "v0.0.1" })
	out, err := r.pkg("--platforms", "linux-amd64")
	if err == nil || !strings.Contains(out, "is linked at") {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(r.outFiles()) != 0 {
		t.Fatalf("%v", r.outFiles())
	}
}

func TestPackageFailsClosedWhenALinkedBvHasNoLicenseEntry(t *testing.T) {
	r := newRig(t, nil)
	r.editTop(func(m map[string]any) { delete(m, "linked_licenses") })
	out, err := r.pkg("--platforms", "linux-amd64")
	if err == nil || !strings.Contains(out, "no linked_licenses entry") {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(r.outFiles()) != 0 {
		t.Fatalf("%v", r.outFiles())
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

func TestUpdateRepinsALinkedLicenseFromTheModuleCache(t *testing.T) {
	r := newRig(t, nil)
	_, _, pin := realLinkedPin(t)
	r.editTop(func(m map[string]any) {
		m["linked_licenses"].([]any)[0].(map[string]any)["license_sha256"] = strings.Repeat("0", 64)
	})
	if out, err := r.run("update-bundled-tools.sh", "--license", "bv"); err != nil || !strings.Contains(out, "pinned license of bv") {
		t.Fatalf("%v\n%s", err, out)
	}
	var m struct {
		Linked []struct {
			SHA string `json:"license_sha256"`
		} `json:"linked_licenses"`
	}
	b, _ := os.ReadFile(r.manifest)
	must(t, json.Unmarshal(b, &m))
	if m.Linked[0].SHA != pin {
		t.Fatalf("re-pinned to %s, want the module's license %s", m.Linked[0].SHA, pin)
	}
}
