package release

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// boardRig adds the Board CLI to a rig as an npm-bun tool, with a fake npm and a fake bun that log
// what they were asked to do (the real ones must never run here), and a fake gh api answer for the
// bun runtime license.
type boardRig struct {
	*rig
	out      string
	pristine map[string]any
}

const fakeBunLicense = "FAKE BUN LICENSE\n"

func newBoardRig(t *testing.T) *boardRig {
	t.Helper()
	r := newRig(t, nil)
	real := realBoardEntry(t)
	bin := filepath.Join(r.root, "bin")
	must(t, os.WriteFile(filepath.Join(bin, "npm"), []byte(`#!/bin/sh
echo "npm $*" >>"$FAKE_TOOLS_LOG"
[ "$1" = ci ] || { echo "fake npm: only ci is expected" >&2; exit 1; }
mkdir -p node_modules/@vercel/board/dist node_modules/jose
echo 'console.log("board")' >node_modules/@vercel/board/dist/cli.mjs
printf 'FAKE BOARD LICENSE\n' >node_modules/@vercel/board/LICENSE
echo '{"name":"@vercel/board","version":"0.54.0","license":"Apache-2.0"}' >node_modules/@vercel/board/package.json
echo '{"name":"jose","version":"6.0.0","license":"MIT"}' >node_modules/jose/package.json
printf 'JOSE LICENSE\n' >node_modules/jose/LICENSE
`), 0o755))
	must(t, os.WriteFile(filepath.Join(bin, "bun"), []byte(`#!/bin/sh
echo "bun $*" >>"$FAKE_TOOLS_LOG"
[ "$1" = --version ] && { echo "${FAKE_BUN_VERSION:-1.4.4}"; exit 0; }
out=""; rt=""; for a in "$@"; do case "$a" in --compile-executable-path=*) rt=${a#*=} ;; esac; done
while [ $# -gt 0 ]; do [ "$1" = --outfile ] && out=$2; shift; done
[ -n "$rt" ] || { echo "fake bun: --compile-executable-path is required" >&2; exit 1; }
echo "runtime $(cat "$rt")" >>"$FAKE_TOOLS_LOG"
printf '#!/bin/sh\necho board 0.54.0 fake compiled\n' >"$out"
`), 0o755))
	// gh api answers the bun runtime license; gh release download keeps working as before.
	gh, _ := os.ReadFile(filepath.Join(bin, "gh"))
	must(t, os.WriteFile(filepath.Join(bin, "gh"), []byte(strings.Replace(string(gh), "name=\"\"; outfile", "[ \"$1\" = api ] && { printf 'FAKE BUN LICENSE\\n'; exit 0; }\nname=\"\"; outfile", 1)), 0o755))
	// The bun runtime archives: tiny zips served by the fake gh, pinned in the manifest like the real ones.
	rt := map[string]any{}
	for p, v := range real["bun_runtime"].(map[string]any) {
		e := v.(map[string]any)
		z := zipOf(t, e["member"].(string), "FAKE BUN RUNTIME "+p+"\n")
		must(t, os.WriteFile(filepath.Join(r.remote, e["asset"].(string)), z, 0o644))
		rt[p] = map[string]any{"asset": e["asset"], "sha256": sum(z), "member": e["member"]}
	}
	real["bun_runtime"] = rt
	real["license_sha256"] = sum([]byte("FAKE BOARD LICENSE\n"))
	real["runtime_license"].(map[string]any)["sha256"] = sum([]byte(fakeBunLicense))
	r.editTop(func(m map[string]any) { m["tools"] = append(m["tools"].([]any), real) })
	r.env = append(r.env, "FAKE_TOOLS_LOG="+filepath.Join(r.root, "tools.log"))
	pristine := map[string]any{}
	pb, _ := json.Marshal(real)
	must(t, json.Unmarshal(pb, &pristine))
	return &boardRig{rig: r, out: filepath.Join(r.root, "tools"), pristine: pristine}
}

func zipOf(t *testing.T, member, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(member)
	must(t, err)
	_, err = w.Write([]byte(body))
	must(t, err)
	must(t, zw.Close())
	return buf.Bytes()
}

func realBoardEntry(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile("../../bundled-tools.json")
	must(t, err)
	var m struct {
		Tools []map[string]any `json:"tools"`
	}
	must(t, json.Unmarshal(b, &m))
	for _, tl := range m.Tools {
		if tl["name"] == "board" {
			return tl
		}
	}
	t.Fatal("the real manifest has no board entry")
	return nil
}

func (b *boardRig) embed(args ...string) (string, error) {
	return b.run("embed-tools.sh", append([]string{"--platform", "linux-amd64", "--out", b.out}, args...)...)
}

func (b *boardRig) log() string {
	l, _ := os.ReadFile(filepath.Join(b.root, "tools.log"))
	return string(l)
}

func TestTheBoardCliIsBuiltFromItsPinnedNpmPackageNotDownloaded(t *testing.T) {
	b := newBoardRig(t)
	o, err := b.embed()
	if err != nil {
		t.Fatalf("%v\n%s", err, o)
	}
	if !strings.Contains(o, "Tools: 2 of 2 verified") {
		t.Fatalf("%s", o)
	}
	exe, _ := os.ReadFile(filepath.Join(b.out, "board"))
	if !strings.Contains(string(exe), "fake compiled") {
		t.Fatalf("board: %q", exe)
	}
	log := b.log()
	for _, want := range []string{"npm ci --ignore-scripts", "bun --version", "--compile", "--no-compile-autoload-dotenv", "--no-compile-autoload-bunfig", "--target=bun-linux-x64"} {
		if !strings.Contains(log, want) {
			t.Errorf("the recipe must run %q:\n%s", want, log)
		}
	}
	if !strings.Contains(log, "--compile-executable-path=") || !strings.Contains(log, "runtime FAKE BUN RUNTIME linux-amd64") {
		t.Errorf("the executable must be compiled from the verified bun runtime for the target, the host included:\n%s", log)
	}
	for _, never := range []string{"npx", "npm install", "npm i "} {
		if strings.Contains(log, never) {
			t.Errorf("the recipe must never run %q:\n%s", never, log)
		}
	}
	pin, _ := os.ReadFile(filepath.Join(b.out, ".pins", "board"))
	ent := b.pristine
	rtsha := ent["bun_runtime"].(map[string]any)["linux-amd64"].(map[string]any)["sha256"].(string)
	inputs := ent["npm_integrity"].(string) + "|" + ent["lockfile_sha256"].(string) + "|" + ent["bun_version"].(string) + "|bun-linux-x64|" + rtsha
	want := ent["npm_integrity"].(string)
	if !strings.HasPrefix(string(pin), sum([]byte(inputs))+" linux-amd64 ") {
		t.Fatalf("the install record is the digest of every build input: %q", pin)
	}
	for f, body := range map[string]string{"LICENSE": "FAKE BOARD LICENSE\n", "BUN_LICENSE.md": fakeBunLicense} {
		if got, _ := os.ReadFile(filepath.Join(b.out, "third_party", "board", f)); string(got) != body {
			t.Errorf("%s: %q", f, got)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(b.out, "third_party", "board", "DEPENDENCY_LICENSES.txt")); !strings.Contains(string(got), "jose 6.0.0 (MIT)") || !strings.Contains(string(got), "JOSE LICENSE") {
		t.Fatalf("dependency licenses: %q", got)
	}
	if n, _ := os.ReadFile(filepath.Join(b.out, "THIRD_PARTY_NOTICES.md")); !strings.Contains(string(n), "bun 1.4.4") || !strings.Contains(string(n), want) {
		t.Fatalf("notices must name how the Board CLI was built:\n%s", n)
	}
}

func TestACachedBoardBuildIsReusedOnlyForTheSameInputs(t *testing.T) {
	b := newBoardRig(t)
	if o, err := b.embed(); err != nil {
		t.Fatalf("%v\n%s", err, o)
	}
	first := strings.Count(b.log(), "npm ci")
	must(t, os.RemoveAll(b.out))
	if o, err := b.embed(); err != nil || strings.Count(b.log(), "npm ci") != first {
		t.Fatalf("a cached build must not be rebuilt (%d -> %d): %v\n%s", first, strings.Count(b.log(), "npm ci"), err, o)
	}
	// A cached executable that no longer matches its record is rebuilt, never trusted.
	must(t, os.RemoveAll(b.out))
	cached := filepath.Join(b.cache, "board", "0.54.0-linux-amd64", "board")
	must(t, os.WriteFile(cached, []byte("tampered"), 0o755))
	if o, err := b.embed(); err != nil || strings.Count(b.log(), "npm ci") != first+1 {
		t.Fatalf("a tampered cache entry must be rebuilt: %v\n%s", err, o)
	}
}

func TestTheBoardBuildFailsClosed(t *testing.T) {
	cases := map[string]struct {
		edit func(b *boardRig)
		want string
	}{
		"lockfile changed from its pin": {func(b *boardRig) {
			b.editTop(func(m map[string]any) {
				for _, tl := range m["tools"].([]any) {
					if tl.(map[string]any)["name"] == "board" {
						tl.(map[string]any)["lockfile_sha256"] = strings.Repeat("0", 64)
					}
				}
			})
		}, "does not match its pin lockfile_sha256"},
		"pinned integrity is not the lockfile's": {func(b *boardRig) {
			b.editTop(func(m map[string]any) {
				for _, tl := range m["tools"].([]any) {
					if tl.(map[string]any)["name"] == "board" {
						tl.(map[string]any)["npm_integrity"] = "sha512-AAAA"
					}
				}
			})
		}, "does not record the pinned npm integrity"},
		"bun is another version": {func(b *boardRig) { b.env = append(b.env, "FAKE_BUN_VERSION=9.9.9") }, "pins bun 1.4.4"},
		"the runtime license changed": {func(b *boardRig) {
			b.editTop(func(m map[string]any) {
				for _, tl := range m["tools"].([]any) {
					if tl.(map[string]any)["name"] == "board" {
						tl.(map[string]any)["runtime_license"].(map[string]any)["sha256"] = strings.Repeat("0", 64)
					}
				}
			})
		}, "runtime license changed from its pin"},
		"the package license changed": {func(b *boardRig) {
			b.editTop(func(m map[string]any) {
				for _, tl := range m["tools"].([]any) {
					if tl.(map[string]any)["name"] == "board" {
						tl.(map[string]any)["license_sha256"] = strings.Repeat("0", 64)
					}
				}
			})
		}, "license changed from its pin"},
		"the bun runtime archive was tampered with": {func(b *boardRig) {
			must(b.t, os.WriteFile(filepath.Join(b.remote, "bun-linux-x64.zip"), zipOf(b.t, "bun-linux-x64/bun", "EVIL\n"), 0o644))
		}, "does not match the pin"},
		"no bun runtime pin for the platform": {func(b *boardRig) {
			b.editTop(func(m map[string]any) {
				for _, tl := range m["tools"].([]any) {
					if tl.(map[string]any)["name"] == "board" {
						delete(tl.(map[string]any)["bun_runtime"].(map[string]any), "linux-amd64")
					}
				}
			})
		}, "no bun runtime pin"},
		"an unsafe bun target": {func(b *boardRig) {
			b.editTop(func(m map[string]any) {
				for _, tl := range m["tools"].([]any) {
					if tl.(map[string]any)["name"] == "board" {
						tl.(map[string]any)["assets"].(map[string]any)["linux-amd64"].(map[string]any)["bun_target"] = "x; rm -rf /"
					}
				}
			})
		}, "unsafe bun target"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			b := newBoardRig(t)
			c.edit(b)
			o, err := b.embed()
			if err == nil || !strings.Contains(o, c.want) {
				t.Fatalf("%v\n%s", err, o)
			}
			if _, e := os.Stat(b.out); e == nil {
				t.Fatal("a failed build must leave no output")
			}
		})
	}
}

func TestEmbedToolsPacksCompressedBlobsWithTheCompleteNotices(t *testing.T) {
	b := newBoardRig(t)
	blobs := filepath.Join(b.root, "packed")
	o, err := b.embed("--blobs", blobs)
	if err != nil {
		t.Fatalf("%v\n%s", err, o)
	}
	for _, f := range []string{"manifest.json", "bd.zst", "board.zst", "THIRD_PARTY_NOTICES.md", "licenses/bd/LICENSE", "licenses/bv/LICENSE", "licenses/board/BUN_LICENSE.md"} {
		if _, err := os.Stat(filepath.Join(blobs, f)); err != nil {
			t.Errorf("missing %s", f)
		}
	}
	n, _ := os.ReadFile(filepath.Join(blobs, "THIRD_PARTY_NOTICES.md"))
	if !strings.Contains(string(n), "## Go modules linked into abacus") || !strings.Contains(string(n), "## bv ") {
		t.Fatal("the embedded notices must be the complete ones")
	}
	// Packing again over an existing blob dir replaces it whole.
	if o, err := b.embed("--blobs", blobs); err != nil {
		t.Fatalf("%v\n%s", err, o)
	}
}

func TestEmbedToolsRefusesToPackWhenAMustShipLicenseHasNoEntry(t *testing.T) {
	b := newBoardRig(t)
	b.editTop(func(m map[string]any) { delete(m, "linked_licenses") })
	blobs := filepath.Join(b.root, "packed")
	o, err := b.embed("--blobs", blobs)
	if err == nil || !strings.Contains(o, "no linked_licenses entry") {
		t.Fatalf("%v\n%s", err, o)
	}
	if _, e := os.Stat(blobs); e == nil {
		t.Fatal("nothing may be packed")
	}
}

func TestChangingAnyBoardBuildInputInvalidatesTheInstallRecord(t *testing.T) {
	b := newBoardRig(t)
	if o, err := b.embed(); err != nil {
		t.Fatalf("%v\n%s", err, o)
	}
	if o, err := b.embed("--check"); err != nil {
		t.Fatalf("a fresh build verifies: %v\n%s", err, o)
	}
	for name, edit := range map[string]func(tl map[string]any){
		"bun version": func(tl map[string]any) { tl["bun_version"] = "9.9.9" },
		"runtime hash": func(tl map[string]any) {
			tl["bun_runtime"].(map[string]any)["linux-amd64"].(map[string]any)["sha256"] = strings.Repeat("a", 64)
		},
		"bun target": func(tl map[string]any) {
			tl["assets"].(map[string]any)["linux-amd64"].(map[string]any)["bun_target"] = "bun-linux-x64-baseline"
		},
		"lockfile hash": func(tl map[string]any) { tl["lockfile_sha256"] = strings.Repeat("b", 64) },
		"npm integrity": func(tl map[string]any) { tl["npm_integrity"] = "sha512-CHANGED" },
	} {
		b.editTop(func(m map[string]any) {
			for _, tl := range m["tools"].([]any) {
				if tl.(map[string]any)["name"] == "board" {
					edit(tl.(map[string]any))
				}
			}
		})
		o, err := b.embed("--check")
		if err == nil || !strings.Contains(o, "PROBLEM  board: does not match the pin") {
			t.Errorf("%s: a changed input must make the installed Board CLI stale: %v\n%s", name, err, o)
		}
		b.editTop(func(m map[string]any) { // restore for the next case
			for i, tl := range m["tools"].([]any) {
				if tl.(map[string]any)["name"] == "board" {
					m["tools"].([]any)[i] = b.pristine
				}
			}
		})
	}
}
