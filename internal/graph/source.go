package graph

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// IssueSource supplies the tracker's issues, dependencies included. Reading is its only job:
// it never writes and never runs a tool.
type IssueSource interface {
	Issues(ctx context.Context) ([]Issue, error)
}

// maxExport bounds how much of an export is read, so a runaway file cannot exhaust memory. The
// whole export is decoded into memory, so the working set is a small multiple of this (see
// docs/tool-contract.md, "Memory ceiling").
const maxExport = 64 << 20

// errTooLarge is what an export over maxExport reports, instead of a parse error on the
// truncated tail.
var errTooLarge = errors.New("export larger than 64 MiB: it is too large to analyze")

// capReader reads at most maxExport bytes and then fails with errTooLarge, so the reader can
// tell a too-large export from a malformed one.
type capReader struct {
	r io.Reader
	n int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.n >= maxExport+1 {
		return 0, errTooLarge
	}
	if int64(len(p)) > maxExport+1-c.n {
		p = p[:maxExport+1-c.n]
	}
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.n > maxExport {
		return n - int(c.n-maxExport), errTooLarge
	}
	return n, err
}

func tooLarge(err error) bool {
	return errors.Is(err, errTooLarge) || errors.Is(err, bufio.ErrTooLong)
}

// rawIssue is the JSON shape the tracker writes, for the fields graph analysis reads: the one
// line of `.beads/issues.jsonl`, or one element of a `--json` array. Anything else in the
// record is ignored. Each dependency is the tracker's `{issue_id, depends_on_id, type}`; the
// `show --json` spellings `id` and `dependency_type` are accepted too.
type rawIssue struct {
	Kind         string          `json:"_type"` // bd export tags each record; only "issue" (or no tag) is an issue
	ID           string          `json:"id"`
	Title        string          `json:"title"`
	Status       string          `json:"status"`
	Priority     int             `json:"priority"`
	IssueType    string          `json:"issue_type"`
	UpdatedAt    string          `json:"updated_at"`
	Dependencies []rawDependency `json:"dependencies"`
	Blocks       []string        `json:"blocks"`
}

type rawDependency struct {
	DependsOnID    string `json:"depends_on_id"`
	ID             string `json:"id"`
	Type           string `json:"type"`
	DependencyType string `json:"dependency_type"`
}

// UnmarshalJSON lets a dependency be a bare issue id (treated as a blocking one) as well as an
// object.
func (d *rawDependency) UnmarshalJSON(b []byte) error {
	var id string
	if json.Unmarshal(b, &id) == nil {
		*d = rawDependency{DependsOnID: id}
		return nil
	}
	type plain rawDependency
	return json.Unmarshal(b, (*plain)(d))
}

func (r rawIssue) issue() Issue {
	i := Issue{ID: r.ID, Title: r.Title, Status: ParseStatus(r.Status), Priority: r.Priority, Type: r.IssueType, Blocks: r.Blocks}
	// A missing or unparseable time is left zero: it only affects a staleness hint.
	if t, err := time.Parse(time.RFC3339, r.UpdatedAt); err == nil {
		i.UpdatedAt = t
	}
	for _, d := range r.Dependencies {
		target := d.DependsOnID
		if target == "" {
			target = d.ID
		}
		if target == "" {
			continue
		}
		t := d.Type
		if t == "" {
			t = d.DependencyType
		}
		// The owner of the record is the dependent, whatever issue_id says: a record that names
		// another issue_id would otherwise invert or lose the edge.
		i.DependsOn = append(i.DependsOn, Dependency{ID: target, Type: ParseDepType(t)})
	}
	return i
}

// ParseIssues reads a JSON array of issues or JSONL (one issue per line, blank lines skipped).
// Records without an id are skipped; a repeated id keeps the last record in the first one's
// position, as the tracker's own loader does. Malformed JSON is an error, never a partial graph.
func ParseIssues(r io.Reader) ([]Issue, error) {
	br := bufio.NewReaderSize(&capReader{r: r}, 1<<20)
	var raws []rawIssue
	first, err := peekNonSpace(br)
	switch {
	case err == io.EOF:
		return nil, nil
	case tooLarge(err):
		return nil, errTooLarge
	case err != nil:
		return nil, err
	case first == '[':
		if err := json.NewDecoder(br).Decode(&raws); err != nil {
			if tooLarge(err) {
				return nil, errTooLarge
			}
			return nil, fmt.Errorf("unreadable issue list: %w", err)
		}
	default:
		sc := bufio.NewScanner(br)
		sc.Buffer(make([]byte, 0, 1<<20), maxExport)
		for n := 1; sc.Scan(); n++ {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) == 0 {
				continue
			}
			var ri rawIssue
			if err := json.Unmarshal(line, &ri); err != nil {
				if tooLarge(sc.Err()) { // the line is the truncated tail of an oversized export
					return nil, errTooLarge
				}
				return nil, fmt.Errorf("unreadable issue on line %d", n)
			}
			raws = append(raws, ri)
		}
		if err := sc.Err(); err != nil {
			if tooLarge(err) {
				return nil, errTooLarge
			}
			return nil, fmt.Errorf("unreadable issue export: %w", err)
		}
	}
	idx := map[string]int{}
	var out []Issue
	for _, ri := range raws {
		if ri.ID == "" || (ri.Kind != "" && ri.Kind != "issue") {
			continue
		}
		i := ri.issue()
		if at, dup := idx[i.ID]; dup {
			out[at] = i
			continue
		}
		idx[i.ID] = len(out)
		out = append(out, i)
	}
	return out, nil
}

func peekNonSpace(br *bufio.Reader) (byte, error) {
	for {
		b, err := br.ReadByte()
		if err != nil {
			return 0, err
		}
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		}
		return b, br.UnreadByte()
	}
}

// StaticSource serves a fixed issue list (tests and demos).
type StaticSource []Issue

func (s StaticSource) Issues(context.Context) ([]Issue, error) { return []Issue(s), nil }

// FileSource reads the tracker's JSONL export (.beads/issues.jsonl) from the tracker directory
// shared with the other modules, or from the working directory when none is set. It only reads
// a file: no tool runs.
//
// With a tracker directory set (Ref with a directory) the export must be in that directory:
// nothing above it is read, since an export in a parent belongs to another project. Without one
// (Dir, or a Ref with no directory) the lookup starts at the directory (or the working
// directory) and walks up through each parent, as the tracker itself does. In both cases an
// export (or .beads directory) that is a symlink resolving outside the directory it was found
// in is refused, so a planted link cannot point abacus at another project's file.
//
// The export is written by the tracker, and bd's default setup does not write one. When there is
// no export file, Fallback (when set) reads the tracker itself, read-only, as the same JSONL (the
// app passes bd's `export`); without one the missing export is reported plainly and Tasks shows
// the insights as unavailable. A stale export file is still read and flagged: Freshness reports
// how far behind the tracker's own data it is.
type FileSource struct {
	Ref *core.TrackerRef // when set, the tracker directory; "" means the working directory
	Dir string           // used when Ref is nil
	// Fallback reads the tracker as JSONL when no export file exists. It is never called when a
	// file is found, and never writes.
	Fallback func(context.Context) ([]byte, error)
}

// ExportFile is the tracker export FileSource reads, relative to a directory holding .beads.
var ExportFile = filepath.Join(".beads", "issues.jsonl")

func (s FileSource) dir() string {
	if s.Ref != nil {
		return s.Ref.Dir()
	}
	return s.Dir
}

// bounded reports whether the lookup stays in the tracker directory: a tracker directory is set.
func (s FileSource) bounded() bool { return s.Ref != nil && s.Ref.Dir() != "" }

var errOutside = errors.New("the issue export resolves outside the tracker directory (a symlink), so it was not read")

// locate finds the export. It returns "" and a nil error when there is none, and an error when
// one was found but refused.
func (s FileSource) locate() (string, error) {
	dir := s.dir()
	if dir == "" {
		var err error
		if dir, err = os.Getwd(); err != nil {
			return "", nil
		}
	}
	dir, _ = filepath.Abs(dir)
	for {
		p := filepath.Join(dir, ExportFile)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			if !within(dir, p) {
				return "", errOutside
			}
			return p, nil
		}
		parent := filepath.Dir(dir)
		if s.bounded() || parent == dir {
			return "", nil
		}
		dir = parent
	}
}

// within reports whether file, with every symlink resolved, is inside root, also resolved.
func within(root, file string) bool {
	r, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	f, err := filepath.EvalSymlinks(file)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(r, f)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Path is the export file this source would read, or "" when there is none (or it is refused).
func (s FileSource) Path() string {
	p, err := s.locate()
	if err != nil {
		return ""
	}
	return p
}

var errNoExport = errors.New("no " + filepath.ToSlash(ExportFile) + " export in this tracker, so there is nothing to analyze")

func (s FileSource) Issues(ctx context.Context) ([]Issue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := s.locate()
	if err != nil {
		return nil, err
	}
	if p == "" {
		if s.Fallback == nil {
			return nil, errNoExport
		}
		out, err := s.Fallback(ctx)
		if err != nil {
			return nil, err
		}
		return ParseIssues(bytes.NewReader(out))
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("could not read the issue export: %w", err)
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > maxExport {
		return nil, errTooLarge
	}
	return ParseIssues(f)
}

// staleSlack is how far the tracker data may be ahead of the export before the export counts
// as stale: the tracker writes the export moments after the data changes.
const staleSlack = 5 * time.Second

// Freshness compares the export's modification time with the tracker's own data in the same
// .beads directory.
type Freshness struct {
	Export time.Time
	// Data is the newest modification time among the tracker's database (.beads/*.db and its
	// write-ahead log) and Dolt directory; zero when the tracker keeps neither here.
	Data time.Time
}

// Behind is how far the export trails the data (zero when it does not).
func (f Freshness) Behind() time.Duration {
	if f.Data.IsZero() || !f.Data.After(f.Export) {
		return 0
	}
	return f.Data.Sub(f.Export)
}

// Stale reports an export that is older than the tracker data by more than staleSlack.
func (f Freshness) Stale() bool { return f.Behind() > staleSlack }

// FreshnessSource is an IssueSource that can say how current its export is.
type FreshnessSource interface {
	Freshness() (Freshness, bool)
}

// Freshness stats the export and the tracker data beside it. It reports false when there is no
// export to judge.
func (s FileSource) Freshness() (Freshness, bool) {
	p := s.Path()
	if p == "" {
		return Freshness{}, false
	}
	fi, err := os.Stat(p)
	if err != nil {
		return Freshness{}, false
	}
	return Freshness{Export: fi.ModTime(), Data: newestData(filepath.Dir(p))}, true
}

// maxWalk bounds how many Dolt directory entries are looked at.
const maxWalk = 20000

// newestData is the newest modification time of the database files and Dolt directory in a
// .beads directory. Nothing is followed through a symlink.
func newestData(beads string) time.Time {
	var newest time.Time
	note := func(fi os.FileInfo) {
		if fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
	}
	for _, pat := range []string{"*.db", "*.db-wal"} {
		names, _ := filepath.Glob(filepath.Join(beads, pat))
		for _, n := range names {
			if fi, err := os.Lstat(n); err == nil && fi.Mode().IsRegular() {
				note(fi)
			}
		}
	}
	seen := 0
	_ = filepath.WalkDir(filepath.Join(beads, "dolt"), func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if seen++; seen > maxWalk {
			return filepath.SkipAll
		}
		if d.Type()&fs.ModeSymlink == 0 {
			if fi, err := d.Info(); err == nil {
				note(fi)
			}
		}
		return nil
	})
	return newest
}
