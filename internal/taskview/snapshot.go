// Package taskview holds the pure functions of the Tasks workspace: parsing the tracker
// snapshot, deriving blocked and ready, sorting, filtering and building the typed reports. It
// imports core and never the reverse, and it has no UI or process dependency, so the CLI, the
// TUI and any later front-end share one behavior.
package taskview

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// MaxExport bounds how much of an export is read, so a runaway tracker cannot exhaust memory.
// The graph reader applies the same ceiling.
const MaxExport = 64 << 20

// maxLine bounds one record (one issue with its comments).
const maxLine = 16 << 20

// maxWarnings bounds how many consistency warnings a snapshot lists.
const maxWarnings = 6

// ErrTooLarge is what an export over MaxExport reports instead of a parse error on its tail.
var ErrTooLarge = errors.New("the tracker export is larger than 64 MiB: too large to list")

// ErrUnreadable is a record that is not valid JSON. A listing missing records would be read as
// the whole tracker, so one bad line fails the read.
var ErrUnreadable = errors.New("the tracker export has an unreadable record")

// rawIssue is one line of `bd export` (field names verified against bd 1.3.4; see testdata/README.md).
type rawIssue struct {
	Type         string   `json:"_type"`
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	Design       string   `json:"design"`
	Notes        string   `json:"notes"`
	Acceptance   string   `json:"acceptance_criteria"`
	Status       string   `json:"status"`
	Priority     *int     `json:"priority"`
	IssueType    string   `json:"issue_type"`
	Assignee     string   `json:"assignee"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
	ClosedAt     string   `json:"closed_at"`
	CloseReason  string   `json:"close_reason"`
	DeferUntil   string   `json:"defer_until"`
	Labels       []string `json:"labels"`
	Dependencies []struct {
		DependsOn string `json:"depends_on_id"`
		Type      string `json:"type"`
	} `json:"dependencies"`
	Comments []struct {
		Author    string `json:"author"`
		Text      string `json:"text"`
		CreatedAt string `json:"created_at"`
	} `json:"comments"`
	DependencyCount *int `json:"dependency_count"`
	CommentCount    *int `json:"comment_count"`
}

// defaultPriority is bd's default, used for a record that carries none.
const defaultPriority = 2

// Parse reads a `bd export` (one JSON issue per line) into a sanitized snapshot. Records of
// another _type (memories, infrastructure) are skipped. The read fails, rather than listing a
// part of the tracker, on an unreadable record, a record without an id or an export over MaxExport.
func Parse(r io.Reader, readAt time.Time, tracker string) (core.TaskSnapshot, error) {
	snap := core.TaskSnapshot{ReadAt: readAt, Tracker: core.Sanitize(tracker)}
	br := bufio.NewReaderSize(io.LimitReader(r, MaxExport+1), 64<<10)
	var total int64
	seen := map[string]int{}
	var badTimes int
	for n := 1; ; n++ {
		line, err := br.ReadBytes('\n')
		total += int64(len(line))
		if total > MaxExport || len(line) > maxLine {
			return core.TaskSnapshot{}, ErrTooLarge
		}
		if t := bytes.TrimSpace(line); len(t) > 0 {
			var raw rawIssue
			if jerr := json.Unmarshal(t, &raw); jerr != nil {
				return core.TaskSnapshot{}, fmt.Errorf("%w (line %d)", ErrUnreadable, n)
			}
			if raw.Type == "" || raw.Type == "issue" {
				iss, bad, ierr := issueOf(raw)
				if ierr != nil {
					return core.TaskSnapshot{}, fmt.Errorf("%w (line %d: %v)", ErrUnreadable, n, ierr)
				}
				badTimes += bad
				if at, dup := seen[iss.ID]; dup { // the later record is the newer
					snap.Issues[at] = iss
				} else {
					seen[iss.ID] = len(snap.Issues)
					snap.Issues = append(snap.Issues, iss)
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return core.TaskSnapshot{}, err
		}
	}
	if badTimes > 0 {
		snap.Warnings = append(snap.Warnings, fmt.Sprintf("%d time values could not be read", badTimes))
	}
	snap.Warnings = append(snap.Warnings, Consistency(snap)...)
	return snap, nil
}

func issueOf(r rawIssue) (core.TaskIssue, int, error) {
	id := core.Sanitize(strings.TrimSpace(r.ID))
	if id == "" {
		return core.TaskIssue{}, 0, errors.New("a record has no id")
	}
	var bad int
	ts := func(s string) time.Time {
		if s == "" {
			return time.Time{}
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			bad++
		}
		return t
	}
	it := core.TaskIssue{
		ID: id, Title: core.Sanitize(r.Title), Description: core.SanitizeText(r.Description), Design: core.SanitizeText(r.Design),
		Notes: core.SanitizeText(r.Notes), Acceptance: core.SanitizeText(r.Acceptance), Status: core.Sanitize(r.Status),
		Priority: defaultPriority, Type: core.Sanitize(r.IssueType), Assignee: core.Sanitize(r.Assignee),
		CreatedAt: ts(r.CreatedAt), UpdatedAt: ts(r.UpdatedAt), ClosedAt: ts(r.ClosedAt), DeferUntil: ts(r.DeferUntil),
		CloseReason: core.Sanitize(r.CloseReason),
	}
	if r.Priority != nil {
		it.Priority = *r.Priority
	}
	for _, l := range r.Labels {
		if l = core.Sanitize(strings.TrimSpace(l)); l != "" {
			it.Labels = append(it.Labels, l)
		}
	}
	sort.Strings(it.Labels)
	for _, d := range r.Dependencies {
		if to := core.Sanitize(strings.TrimSpace(d.DependsOn)); to != "" {
			it.Deps = append(it.Deps, core.TaskDep{DependsOn: to, Type: core.Sanitize(d.Type)})
		}
	}
	for _, c := range r.Comments {
		it.Comments = append(it.Comments, core.TaskComment{Author: core.Sanitize(c.Author), Text: core.SanitizeText(c.Text), CreatedAt: ts(c.CreatedAt)})
	}
	if r.DependencyCount != nil {
		it.ReportedDeps, it.HasReportedDeps = *r.DependencyCount, true
	}
	if r.CommentCount != nil {
		it.ReportedComments = *r.CommentCount
	}
	return it, bad, nil
}

// Consistency compares what each record says it holds with what was read: an export that lost
// a dependency or a comment (a truncated record) would otherwise show a task as free of blockers
// that is not. dependency_count counts the blocking dependencies only (verified against bd 1.3.4),
// and a count the tracker reports above what was read is the warning; a lower one is not, since a
// count may leave out kinds this reader keeps.
func Consistency(s core.TaskSnapshot) []string {
	var out []string
	more := 0
	warn := func(format string, a ...any) {
		if len(out) < maxWarnings {
			out = append(out, fmt.Sprintf(format, a...))
		} else {
			more++
		}
	}
	for _, it := range s.Issues {
		if it.HasReportedDeps {
			blocks := 0
			for _, d := range it.Deps {
				if d.Type == "blocks" {
					blocks++
				}
			}
			if blocks < it.ReportedDeps {
				warn("%s: %d blocking dependencies reported, %d read", it.ID, it.ReportedDeps, blocks)
			}
		}
		if len(it.Comments) < it.ReportedComments {
			warn("%s: %d comments reported, %d read", it.ID, it.ReportedComments, len(it.Comments))
		}
	}
	if more > 0 {
		out = append(out, fmt.Sprintf("%d more records look incomplete", more))
	}
	return out
}

// Fresh reports whether a snapshot read at readAt is still current for a view that polls every
// poll: older than two polls it is stale. A zero readAt is never fresh.
func Fresh(readAt, now time.Time, poll time.Duration) bool {
	return !readAt.IsZero() && now.Sub(readAt) <= 2*poll
}
