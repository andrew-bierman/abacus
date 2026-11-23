// Package embedtools is the set of tools built into the abacus binary: the executables for this
// build's platform (compressed), their license texts and the third-party notices. The bytes are
// generated at build time by scripts/embed-tools.sh into blobs/<os>-<arch>/ (gitignored, never
// committed) and linked in only with -tags embedtools. A plain `go build` has no embedded tools:
// FS is nil. This package has no dependencies, so core (the resolver) and the packer both use it.
package embedtools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
)

// FS is the generated blob directory (manifest.json, one .zst per tool, notices, licenses), or nil
// in a build without the embedtools tag.
var FS fs.FS

// Names inside FS.
const (
	ManifestFile = "manifest.json"
	NoticesFile  = "THIRD_PARTY_NOTICES.md"
)

// Tool is one embedded executable: File is its zstd-compressed bytes in FS, Size and SHA256 are of
// the decompressed executable. Name becomes a file name in the extraction directory, so it is
// checked against a closed pattern when the manifest is read.
type Tool struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	File    string `json:"file"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	// License is the text shown by `abacus licenses`, as a path inside FS.
	LicenseFile string `json:"license_file,omitempty"`
}

// Manifest is manifest.json.
type Manifest struct {
	Schema int    `json:"schema"`
	Tools  []Tool `json:"tools"`
	// Licenses are further license texts (linked libraries) as paths inside FS, for `abacus licenses`.
	Licenses []string `json:"licenses,omitempty"`
}

var (
	nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	fileRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(/[A-Za-z0-9][A-Za-z0-9._-]*)*$`)
	shaRE  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Parse reads and validates a manifest. Nothing in it can name a path outside the extraction
// directory or FS: tool names and file paths are matched against closed patterns.
func Parse(b []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("manifest is not readable: %w", err)
	}
	if m.Schema != 1 || len(m.Tools) == 0 {
		return nil, errors.New("manifest has an unknown format or no tools")
	}
	seen := map[string]bool{}
	for _, t := range m.Tools {
		switch {
		case !nameRE.MatchString(t.Name) || seen[t.Name]:
			return nil, fmt.Errorf("manifest has a bad or repeated tool name %q", t.Name)
		case !fileRE.MatchString(t.File) || (t.LicenseFile != "" && !fileRE.MatchString(t.LicenseFile)):
			return nil, fmt.Errorf("manifest has an unsafe file name for %s", t.Name)
		case !shaRE.MatchString(t.SHA256) || t.Size <= 0:
			return nil, fmt.Errorf("manifest has no usable size or hash for %s", t.Name)
		}
		seen[t.Name] = true
	}
	for _, l := range m.Licenses {
		if !fileRE.MatchString(l) {
			return nil, errors.New("manifest has an unsafe license file name")
		}
	}
	return &m, nil
}

// Tool returns the entry for name.
func (m *Manifest) Tool(name string) (Tool, bool) {
	for _, t := range m.Tools {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// Hash identifies the content: the same tool set (names and executable hashes) always gives the
// same hash, a changed tool gives a new one. It is computed from the entries, never read from a
// field, and names the extraction directory.
func (m *Manifest) Hash() string {
	ts := append([]Tool(nil), m.Tools...)
	sort.Slice(ts, func(i, j int) bool { return ts[i].Name < ts[j].Name })
	h := sha256.New()
	for _, t := range ts {
		fmt.Fprintf(h, "%s\x00%s\x00%d\x00", t.Name, t.SHA256, t.Size)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
