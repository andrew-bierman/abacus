// Package graph is the in-process graph-insights engine, the only one abacus has. It owns the
// issue model abacus feeds to an analyzer, reads issues from the tracker's JSON export, and
// returns the core.InsightsReport behind core.InsightsProvider. Core imports nothing from here.
//
// The only file that imports bv's analysis package is bvanalyzer.go.
package graph

import (
	"strings"
	"time"
)

// Status is an issue status as the tracker reports it, lowercased. The five statuses abacus
// knows by name have constants; any other value (a custom workflow state) is kept as is and the
// analyzer treats it as parked: neither closed nor actionable.
type Status string

const (
	StatusOpen       Status = "open"
	StatusInProgress Status = "in_progress"
	StatusBlocked    Status = "blocked"
	StatusDeferred   Status = "deferred"
	StatusClosed     Status = "closed"
)

// ParseStatus normalizes a tracker status. An empty status reads as open (the tracker's default
// for a new issue); "tombstone" (a soft-deleted issue) reads as closed.
func ParseStatus(raw string) Status {
	s := Status(strings.ToLower(strings.TrimSpace(raw)))
	switch s {
	case "":
		return StatusOpen
	case "tombstone":
		return StatusClosed
	}
	return s
}

// DepType is how one issue depends on another.
type DepType string

const (
	// DepBlocks is the one hard dependency: the depended-on issue must close first.
	DepBlocks DepType = "blocks"
	// The rest are soft or structural links the tracker records; the analyzer decides how each
	// counts (see bvanalyzer.go), and an unknown type never blocks.
	DepConditionalBlocks DepType = "conditional-blocks"
	DepWaitsFor          DepType = "waits-for"
	DepParentChild       DepType = "parent-child"
	DepRelated           DepType = "related"
	DepDiscoveredFrom    DepType = "discovered-from"
)

// ParseDepType normalizes a tracker dependency type. An empty type reads as DepBlocks, which is
// the tracker's default for `bd dep add`.
func ParseDepType(raw string) DepType {
	t := DepType(strings.ToLower(strings.TrimSpace(raw)))
	if t == "" {
		return DepBlocks
	}
	return t
}

// Dependency says the owning issue depends on issue ID: when Type is DepBlocks, ID must be
// closed before the owner is actionable.
type Dependency struct {
	ID   string
	Type DepType
}

// Issue is the part of a tracker issue the graph analysis needs. Text fields come from outside
// abacus and are sanitized where they reach a report, not here.
type Issue struct {
	ID       string
	Title    string
	Status   Status
	Priority int
	Type     string
	// UpdatedAt is when the issue last changed (zero when the source has none); the analysis
	// uses it to flag stale work.
	UpdatedAt time.Time
	// DependsOn are the issues this one depends on ("this is blocked by").
	DependsOn []Dependency
	// Blocks are the issues that depend on this one ("this blocks"). It is the inverse of
	// DependsOn, for sources that record the link on the blocker: "a blocks b" means b depends
	// on a, and the analyzer sees one and the same edge either way.
	Blocks []string
	// Labels are the issue's labels; bv's triage reads them.
	Labels []string
}

// Edges returns every dependency edge in issues as (dependent, dependsOn, type), merging the
// DependsOn and Blocks spellings, dropping exact duplicates and edges that name an issue that
// is not in the set (the tracker may hold links to deleted or out-of-scope issues).
func Edges(issues []Issue) []Edge {
	known := make(map[string]bool, len(issues))
	for _, i := range issues {
		known[i.ID] = true
	}
	seen := map[Edge]bool{}
	var out []Edge
	add := func(e Edge) {
		if !known[e.Dependent] || !known[e.DependsOn] || seen[e] {
			return
		}
		seen[e] = true
		out = append(out, e)
	}
	for _, i := range issues {
		for _, d := range i.DependsOn {
			add(Edge{Dependent: i.ID, DependsOn: d.ID, Type: d.Type})
		}
		for _, b := range i.Blocks {
			add(Edge{Dependent: b, DependsOn: i.ID, Type: DepBlocks})
		}
	}
	return out
}

// Edge is one dependency: Dependent cannot proceed until DependsOn is done (for DepBlocks).
type Edge struct {
	Dependent string
	DependsOn string
	Type      DepType
}
