package agents

import "strings"

// HarnessKind is the shape of a harness's store. It is data, not a layout switch: a capability a
// harness lacks is simply an empty field.
type HarnessKind string

const (
	// KindJSONL is an append-only line-oriented transcript read incrementally by offset.
	KindJSONL HarnessKind = "jsonl"
	// KindSQLite is a database a harness writes its sessions into.
	KindSQLite HarnessKind = "sqlite"
	// KindNone is a harness with no readable history: the session still appears with its
	// process, children and ports, and no tokens.
	KindNone HarnessKind = "none"
)

// HarnessSpec is the one declaration of an agent: the id every front-end shows, the executables
// that mean a process is a session, where its store lives, and what shape that store has.
//
// Discovery reads this registry, so an agent's identity and executables are declared in one
// place. Roots and Kind describe the store behind those processes; a harness that leaves no
// readable history declares KindNone and is still a session with its process, children and
// ports.
type HarnessSpec struct {
	Name Harness
	// Binaries are executable basenames that mean "this process is a session". A trailing
	// ".exe" is tolerated because the same agent ships that name on several platforms.
	Binaries []string
	// Roots returns where a harness's store lives for a home. A harness with an XDG, %APPDATA%
	// or its own environment override supplies its own function; an unknown home returns
	// nothing.
	Roots func(home string) []string
	// Kind is the shape of the store at Roots.
	Kind HarnessKind
}

// harnesses is the one table an agent is added to.
var harnesses = []HarnessSpec{
	{
		Name:     HarnessClaude,
		Binaries: []string{"claude", "claude-code"},
		Roots:    rootsOf(ClaudeProjectsPath),
		Kind:     KindJSONL,
	},
	{
		Name:     HarnessCodex,
		Binaries: []string{"codex"},
		Roots:    rootsOf(CodexSessionsPath),
		Kind:     KindJSONL,
	},
	{
		Name:     HarnessGrok,
		Binaries: []string{"grok", "grok-cli"},
		Roots:    rootsOf(GrokSessionsPath),
		Kind:     KindJSONL,
	},
	{
		Name:     HarnessOpenCode,
		Binaries: []string{"opencode", "opencode-cli"},
		Roots:    rootsOf(OpenCodeDBPath),
		Kind:     KindSQLite,
	},
	{
		Name:     HarnessPi,
		Binaries: []string{"pi"},
		Roots:    rootsOf(PiSessionsPath),
		Kind:     KindJSONL,
	},
}

// harnessByBinary is the executable lookup discovery uses. Deriving it from the registry means a
// harness is reachable exactly when it is declared there.
var harnessByBinary = indexHarnesses(harnesses)

func indexHarnesses(specs []HarnessSpec) map[string]Harness {
	index := make(map[string]Harness)
	for _, spec := range specs {
		for _, bin := range spec.Binaries {
			index[binaryKey(bin)] = spec.Name
		}
	}
	return index
}

func binaryKey(name string) string { return strings.TrimSuffix(name, ".exe") }

func rootsOf(path func(home string) string) func(home string) []string {
	return func(home string) []string {
		if p := path(home); p != "" {
			return []string{p}
		}
		return nil
	}
}
