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

// harnessOf names the harness a process is, "" when it is none.
func harnessOf(p Proc) string {
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(p.Name)), ".exe")
	if len(p.Args) > 0 && (base == "node" || base == "bun" || base == "python" || base == "python3") {
		head := strings.ToLower(strings.Join(p.Args[:min(len(p.Args), 3)], " "))
		for _, r := range harnessRules {
			for _, f := range r.frags {
				if strings.Contains(head, f) {
					return r.id
				}
			}
		}
	}
	for _, r := range harnessRules {
		for _, n := range r.names {
			if base == n {
				return r.id
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
