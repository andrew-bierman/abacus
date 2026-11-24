// Package agents finds running coding agents: a feed from a live session monitor (when one is
// built in), and a native feed that reads the process list and the session files each harness
// already writes. Everything here is read-only and bounded; every outside source (processes,
// files, terminals) is injected so tests never touch the real machine.
package agents

import (
	"path/filepath"
	"strings"

	"github.com/andrew-bierman/abacus/internal/core"
)

// Proc is one process, as much as the scan needs.
type Proc struct {
	PID, PPID int
	Name      string
	Args      []string // argv, Args[0] first
	Cwd       string
	StartUnix int64
	// UID is the owner when UIDKnown; a process of another user is hidden unless asked for.
	UID      int
	UIDKnown bool
}

// harnessRule recognizes one harness from a process: by executable name, or by a path fragment
// in the first arguments (an npm-installed CLI runs under node).
type harnessRule struct {
	id    string
	names []string
	frags []string
}

var harnessRules = []harnessRule{
	{core.HarnessClaude, []string{"claude"}, []string{"@anthropic-ai/claude-code"}},
	{core.HarnessCodex, []string{"codex"}, []string{"@openai/codex"}},
	{core.HarnessGemini, []string{"gemini"}, []string{"@google/gemini-cli"}},
	{core.HarnessOpenCode, []string{"opencode"}, []string{"opencode-ai"}},
	{core.HarnessAider, []string{"aider"}, []string{"aider/main", "aider-chat"}},
}

// harnessRunner is a runtime a harness may run under.
func harnessRunner(name string) bool {
	switch strings.TrimSuffix(strings.ToLower(filepath.Base(name)), ".exe") {
	case "node", "bun", "python", "python3":
		return true
	}
	return false
}

// versionLike is a process name that is a bare version ("2.1.325"): Claude Code's native build runs
// as versions/<version>, so the process is named by its version and only its arguments say "claude".
func versionLike(name string) bool {
	if name == "" || !strings.Contains(name, ".") {
		return false
	}
	for _, r := range name {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return true
}

// helperArgs are the modes in which a harness binary runs something that is not a session: a
// browser native-messaging host, an MCP or app server.
var helperArgs = map[string]bool{"--chrome-native-host": true, "mcp-server": true, "app-server": true, "mcp": true, "--mcp": true}

// harnessOf names the harness a process is, "" when it is none. Names are matched exactly and in
// lower case: the desktop apps ("Codex", "Claude") are not the command-line agents.
func harnessOf(p Proc) string {
	for _, a := range p.Args[:min(len(p.Args), 4)] {
		if helperArgs[a] {
			return ""
		}
	}
	names := []string{strings.TrimSuffix(filepath.Base(p.Name), ".exe")}
	if len(p.Args) > 0 {
		names = append(names, strings.TrimSuffix(filepath.Base(p.Args[0]), ".exe"))
	}
	for _, r := range harnessRules {
		for _, n := range r.names {
			for _, have := range names {
				if have == n {
					return r.id
				}
			}
		}
	}
	if len(p.Args) > 0 && harnessRunner(p.Name) {
		head := strings.ToLower(strings.Join(p.Args[:min(len(p.Args), 3)], " "))
		for _, r := range harnessRules {
			for _, f := range r.frags {
				if strings.Contains(head, f) {
					return r.id
				}
			}
		}
	}
	return ""
}

// agentProc is a process recognized as a harness.
type agentProc struct {
	Proc
	Harness string
}

// harnessProcs are the processes that are agents. A process whose parent is a process of the same
// harness is a helper of it, not another agent.
func harnessProcs(all []Proc) []agentProc {
	byPID := map[int]string{}
	for _, p := range all {
		if h := harnessOf(p); h != "" {
			byPID[p.PID] = h
		}
	}
	var out []agentProc
	for _, p := range all {
		h := byPID[p.PID]
		if h == "" || byPID[p.PPID] == h || p.PID == p.PPID {
			continue
		}
		out = append(out, agentProc{p, h})
	}
	return out
}
