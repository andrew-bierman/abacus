package graph

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// IssueSource supplies the tracker's issues, dependencies included. Reading is its only job:
// it never writes and never runs a tool.
type IssueSource interface {
	Issues(ctx context.Context) ([]Issue, error)
}

// maxExport bounds how much of an export is read, so a runaway file cannot exhaust memory.
const maxExport = 64 << 20

// rawIssue is the JSON shape the tracker writes, for the fields graph analysis reads: the one
// line of `.beads/issues.jsonl`, or one element of a `--json` array. Anything else in the
// record is ignored. Each dependency is the tracker's `{issue_id, depends_on_id, type}`; the
// `show --json` spellings `id` and `dependency_type` are accepted too.
type rawIssue struct {
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
	br := bufio.NewReaderSize(io.LimitReader(r, maxExport+1), 1<<20)
	var raws []rawIssue
	first, err := peekNonSpace(br)
	switch {
	case err == io.EOF:
		return nil, nil
	case err != nil:
		return nil, err
	case first == '[':
		if err := json.NewDecoder(br).Decode(&raws); err != nil {
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
				return nil, fmt.Errorf("unreadable issue on line %d", n)
			}
			raws = append(raws, ri)
		}
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("unreadable issue export: %w", err)
		}
	}
	idx := map[string]int{}
	var out []Issue
	for _, ri := range raws {
		if ri.ID == "" {
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
// shared with the other modules, or from the working directory when none is set, looking in the
// directory and then each parent, as the tracker itself does. It only reads a file: no tool runs.
//
// This is the prototype's reader. The export is written by the tracker; a tracker that does not
// keep one (or keeps it stale) is reported plainly, and Tasks then shows the insights as
// unavailable rather than analysing out-of-date data.
type FileSource struct {
	Ref *core.TrackerRef // when set, the tracker directory; "" means the working directory
	Dir string           // used when Ref is nil
}

// ExportFile is the tracker export FileSource reads, relative to a directory holding .beads.
var ExportFile = filepath.Join(".beads", "issues.jsonl")

func (s FileSource) dir() string {
	if s.Ref != nil {
		return s.Ref.Dir()
	}
	return s.Dir
}

// Path is the export file this source would read, or "" when there is none.
func (s FileSource) Path() string {
	dir := s.dir()
	if dir == "" {
		var err error
		if dir, err = os.Getwd(); err != nil {
			return ""
		}
	}
	dir, _ = filepath.Abs(dir)
	for {
		p := filepath.Join(dir, ExportFile)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

var errNoExport = errors.New("no " + filepath.ToSlash(ExportFile) + " export in this tracker, so the library engine has nothing to read (use the default engine)")

func (s FileSource) Issues(ctx context.Context) ([]Issue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p := s.Path()
	if p == "" {
		return nil, errNoExport
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("could not read the issue export: %w", err)
	}
	defer f.Close()
	return ParseIssues(f)
}
