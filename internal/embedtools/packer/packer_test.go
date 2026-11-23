package packer_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/andrew-bierman/abacus/internal/embedtools"
	"github.com/andrew-bierman/abacus/internal/embedtools/packer"
	"github.com/klauspost/compress/zstd"
)

func write(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestPackWritesAManifestTheBinaryAcceptsAndBlobsThatRoundTrip(t *testing.T) {
	d := t.TempDir()
	write(t, filepath.Join(d, "src", "bd"), "#!/bin/sh\necho bd\n")
	write(t, filepath.Join(d, "lic", "bd", "LICENSE"), "BD\n")
	write(t, filepath.Join(d, "lic", "bv", "LICENSE"), "BV\n")
	write(t, filepath.Join(d, "lic", "bd", "EXTRA.txt"), "more\n")
	write(t, filepath.Join(d, "notices.md"), "# notices\n")
	out := filepath.Join(d, "out")
	err := packer.Pack(packer.Input{Src: filepath.Join(d, "src"), Licenses: filepath.Join(d, "lic"), Notices: filepath.Join(d, "notices.md"), Out: out, Tools: []packer.Tool{{Name: "bd", Version: "1.3.4"}}})
	if err != nil {
		t.Fatal(err)
	}
	fsys := os.DirFS(out)
	b, _ := fs.ReadFile(fsys, embedtools.ManifestFile)
	m, err := embedtools.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	tl, ok := m.Tool("bd")
	if !ok || tl.Version != "1.3.4" || tl.LicenseFile != "licenses/bd/LICENSE" || len(m.Licenses) != 2 {
		t.Fatalf("%+v %+v", tl, m)
	}
	f, _ := fsys.Open(tl.File)
	dec, _ := zstd.NewReader(f)
	defer dec.Close()
	got := make([]byte, 100)
	n, _ := dec.Read(got)
	if string(got[:n]) != "#!/bin/sh\necho bd\n" || int64(n) != tl.Size {
		t.Fatalf("%q", got[:n])
	}
	if b, _ := fs.ReadFile(fsys, embedtools.NoticesFile); string(b) != "# notices\n" {
		t.Fatal("notices missing")
	}
}

func TestPackFailsClosedAndLeavesNothing(t *testing.T) {
	d := t.TempDir()
	write(t, filepath.Join(d, "notices.md"), "n")
	out := filepath.Join(d, "out")
	err := packer.Pack(packer.Input{Src: filepath.Join(d, "src"), Licenses: filepath.Join(d, "lic"), Notices: filepath.Join(d, "notices.md"), Out: out, Tools: []packer.Tool{{Name: "bd", Version: "1"}}})
	if err == nil {
		t.Fatal("a missing executable must fail")
	}
	if _, e := os.Stat(out); e == nil {
		t.Fatal("a failed pack left output behind")
	}
	if os.MkdirAll(out, 0o755); packer.Pack(packer.Input{Out: out}) == nil {
		os.WriteFile(filepath.Join(out, "x"), nil, 0o644)
	}
	write(t, filepath.Join(out, "x"), "x")
	if packer.Pack(packer.Input{Src: d, Out: out, Notices: filepath.Join(d, "notices.md"), Tools: []packer.Tool{{Name: "bd"}}}) == nil {
		t.Fatal("a non-empty output directory must be refused")
	}
}

func TestParseRefusesUnsafeNamesAndPaths(t *testing.T) {
	for _, bad := range []string{
		`{"schema":1,"tools":[{"name":"../x","file":"a.zst","size":1,"sha256":"` + string(make([]byte, 0)) + `"}]}`,
		`{"schema":1,"tools":[{"name":"x","file":"../a.zst","size":1,"sha256":"0000000000000000000000000000000000000000000000000000000000000000"}]}`,
		`{"schema":1,"tools":[{"name":"x","file":"/a.zst","size":1,"sha256":"0000000000000000000000000000000000000000000000000000000000000000"}]}`,
		`{"schema":1,"tools":[{"name":"x","file":"a.zst","size":0,"sha256":"0000000000000000000000000000000000000000000000000000000000000000"}]}`,
		`{"schema":2,"tools":[]}`,
	} {
		if _, err := embedtools.Parse([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}
