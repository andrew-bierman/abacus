// Package embedtest builds small fake embedded tool sets for tests: a manifest and zstd blobs in
// an in-memory fs.FS, with tiny shell scripts standing in for the real executables.
package embedtest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"sort"
	"testing/fstest"

	"github.com/andrew-bierman/abacus/internal/embedtools"
	"github.com/klauspost/compress/zstd"
)

// Tool is one fake tool: Body is the file content (a #!/bin/sh script, so it can be run).
type Tool struct{ Name, Version, Body string }

// Script is a tiny executable that prints its own name and arguments.
func Script(name string) string { return "#!/bin/sh\necho \"" + name + " $*\"\n" }

// Set is a fake embedded set that tests can damage before use.
type Set struct {
	FS       fstest.MapFS
	Manifest embedtools.Manifest
}

func compress(b []byte) []byte {
	var buf bytes.Buffer
	w, _ := zstd.NewWriter(&buf)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

// New builds a set with the given tools (sorted by name), notices and license texts.
func New(notices string, licenses map[string]string, tools ...Tool) *Set {
	s := &Set{FS: fstest.MapFS{}}
	s.Manifest.Schema = 1
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	for _, t := range tools {
		sum := sha256.Sum256([]byte(t.Body))
		et := embedtools.Tool{Name: t.Name, Version: t.Version, File: t.Name + ".zst", Size: int64(len(t.Body)), SHA256: hex.EncodeToString(sum[:])}
		s.FS[et.File] = &fstest.MapFile{Data: compress([]byte(t.Body))}
		if l, ok := licenses[t.Name]; ok {
			et.LicenseFile = "licenses/" + t.Name + "/LICENSE"
			s.FS[et.LicenseFile] = &fstest.MapFile{Data: []byte(l)}
		}
		s.Manifest.Tools = append(s.Manifest.Tools, et)
	}
	var extra []string
	for n := range licenses {
		if _, ok := s.Manifest.Tool(n); !ok {
			extra = append(extra, n)
		}
	}
	sort.Strings(extra)
	for _, n := range extra {
		p := "licenses/" + n + "/LICENSE"
		s.FS[p] = &fstest.MapFile{Data: []byte(licenses[n])}
		s.Manifest.Licenses = append(s.Manifest.Licenses, p)
	}
	s.FS[embedtools.NoticesFile] = &fstest.MapFile{Data: []byte(notices)}
	s.Write()
	return s
}

// Write stores the manifest into the FS (call it after editing Manifest).
func (s *Set) Write() {
	b, _ := json.Marshal(s.Manifest)
	s.FS[embedtools.ManifestFile] = &fstest.MapFile{Data: b}
}

// Standard is a set of the four built-in tools, as scripts.
func Standard() *Set {
	var ts []Tool
	for _, n := range []string{"bd", "lambe", "beads-lease", "board"} {
		ts = append(ts, Tool{Name: n, Version: "1.0.0", Body: Script(n)})
	}
	return New("# notices\n", map[string]string{"bd": "BD LICENSE\n", "bv": "BV LICENSE\n"}, ts...)
}

var _ fs.FS = fstest.MapFS{}
