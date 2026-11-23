package release

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/embedtools"
)

// fakeEmbed writes a stand-in for scripts/embed-tools.sh (ABACUS_EMBED_SCRIPT): it makes the files
// package-release.sh reads, except what the case leaves out, and fails for the named platform.
func (r *rig) fakeEmbed(failPlatform string, withBv bool) {
	script := filepath.Join(r.root, "fake-embed.sh")
	bv := ""
	if withBv {
		bv = `mkdir -p "$blobs/licenses/bv"; echo BV >"$blobs/licenses/bv/LICENSE"`
	}
	must(r.t, os.WriteFile(script, []byte(`#!/usr/bin/env bash
set -euo pipefail
platform=""; out=""; blobs=""
while [ $# -gt 0 ]; do case "$1" in --platform) platform=$2; shift 2;; --out) out=$2; shift 2;; --blobs) blobs=$2; shift 2;; *) shift;; esac; done
[ "$platform" != "`+failPlatform+`" ] || { echo "fake embed: failing for $platform" >&2; exit 1; }
mkdir -p "$out/.pins" "$blobs/licenses/bd"
echo "pin $platform exehash" >"$out/.pins/bd"
echo zst >"$blobs/bd.zst"; echo notices >"$blobs/THIRD_PARTY_NOTICES.md"; echo BD >"$blobs/licenses/bd/LICENSE"
`+bv+"\n"), 0o755))
	r.env = append(r.env, "ABACUS_EMBED_SCRIPT="+script)
}

func TestPackageFailsClosedWhenOnePlatformsEmbedStepFails(t *testing.T) {
	r := newRig(t, nil)
	r.fakeEmbed("linux-arm64", true)
	out, err := r.pkg("--platforms", "linux-amd64 linux-arm64")
	if err == nil || !strings.Contains(out, "embed-tools.sh failed for linux-arm64") {
		t.Fatalf("%v\n%s", err, out)
	}
	if f := r.outFiles(); len(f) != 0 {
		t.Fatalf("no archive, SHA256SUMS or release notes may be left when a platform fails: %v", f)
	}
}

func TestPackageFailsClosedWhenTheBvLicenseIsNotInTheEmbeddedSet(t *testing.T) {
	r := newRig(t, nil)
	r.fakeEmbed("none", false)
	out, err := r.pkg("--platforms", "linux-amd64")
	if err == nil || !strings.Contains(out, "no license text for bv") {
		t.Fatalf("%v\n%s", err, out)
	}
	if f := r.outFiles(); len(f) != 0 {
		t.Fatalf("%v", f)
	}
}

func TestPackBlobsLeavesNoWorkDirectoryWhenPackingFails(t *testing.T) {
	r := newRig(t, nil)
	realGo, err := exec.LookPath("go")
	must(t, err)
	must(t, os.WriteFile(filepath.Join(r.root, "bin", "go"), []byte("#!/bin/sh\n[ \"$1\" = run ] && { echo 'fake go: run fails' >&2; exit 1; }\nexec "+realGo+" \"$@\"\n"), 0o755))
	blobs := filepath.Join(r.root, "blobparent", "linux-amd64")
	o, err := r.run("embed-tools.sh", "--platform", "linux-amd64", "--out", filepath.Join(r.root, "tools"), "--blobs", blobs)
	if err == nil || !strings.Contains(o, "packing the tools failed") {
		t.Fatalf("%v\n%s", err, o)
	}
	left, _ := filepath.Glob(filepath.Join(r.root, "blobparent", ".blobs.*"))
	if len(left) != 0 {
		t.Fatalf("a failed pack left its work directory behind: %v", left)
	}
	if _, e := os.Stat(blobs); e == nil {
		t.Fatal("a failed pack must not leave a blob directory")
	}
	if l, _ := filepath.Glob(filepath.Join(r.root, ".tools.*")); len(l) != 0 {
		t.Fatalf("stage left behind: %v", l)
	}
}

// The embedded manifest (generated) and bundled-tools.json (pinned) must name the same tools at the
// same versions. The build asserts it when it packs; this checks the real blobs when they exist.
func TestTheGeneratedManifestMatchesBundledToolsJSON(t *testing.T) {
	files, _ := filepath.Glob("../embedtools/blobs/*/manifest.json")
	if len(files) == 0 {
		t.Skip("no generated blobs here (run make build)")
	}
	b, err := os.ReadFile("../../bundled-tools.json")
	must(t, err)
	var pins struct {
		Tools []struct{ Name, Version string } `json:"tools"`
	}
	must(t, json.Unmarshal(b, &pins))
	for _, f := range files {
		mb, err := os.ReadFile(f)
		must(t, err)
		m, err := embedtools.Parse(mb)
		must(t, err)
		if len(m.Tools) != len(pins.Tools) {
			t.Errorf("%s lists %d tools, bundled-tools.json %d", f, len(m.Tools), len(pins.Tools))
		}
		for _, p := range pins.Tools {
			if tl, ok := m.Tool(p.Name); !ok || tl.Version != p.Version {
				t.Errorf("%s: %s is %+v, bundled-tools.json pins %s", f, p.Name, tl, p.Version)
			}
		}
	}
}
