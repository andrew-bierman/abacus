package agents

import (
	"slices"
	"strings"
	"testing"
)

// registryContract is written out rather than derived from harnesses: a test that walks the
// registry it validates shrinks when an alias is dropped instead of failing.
type registryContract struct {
	binaries []string
	kind     HarnessKind
	roots    func(home string) []string
}

var registryContracts = map[Harness]registryContract{
	HarnessAmp: {
		binaries: []string{"amp"},
		kind:     KindNone,
	},
	HarnessAntigravity: {
		binaries: []string{"antigravity"},
		kind:     KindNone,
	},
	HarnessClaude: {
		binaries: []string{"claude", "claude-code"},
		kind:     KindJSONL,
		roots:    func(home string) []string { return []string{ClaudeProjectsPath(home)} },
	},
	HarnessCodex: {
		binaries: []string{"codex"},
		kind:     KindJSONL,
		roots:    func(home string) []string { return []string{CodexSessionsPath(home)} },
	},
	HarnessCopilot: {
		binaries: []string{"copilot"},
		kind:     KindJSONL,
		roots:    func(home string) []string { return []string{CopilotSessionsPath(home)} },
	},
	HarnessCrush: {
		binaries: []string{"crush"},
		kind:     KindNone,
	},
	HarnessCursor: {
		binaries: []string{"cursor-agent"},
		kind:     KindNone,
	},
	HarnessGemini: {
		binaries: []string{"gemini"},
		kind:     KindJSONL,
		roots:    func(home string) []string { return []string{GeminiSessionsPath(home)} },
	},
	HarnessGrok: {
		binaries: []string{"grok", "grok-cli"},
		kind:     KindJSONL,
		roots:    func(home string) []string { return []string{GrokSessionsPath(home)} },
	},
	HarnessHermes: {
		binaries: []string{"hermes"},
		kind:     KindSQLite,
		roots:    HermesDBPaths,
	},
	HarnessKiro: {
		binaries: []string{"kiro-cli"},
		kind:     KindNone,
	},
	HarnessOpenCode: {
		binaries: []string{"opencode", "opencode-cli"},
		kind:     KindSQLite,
		roots:    func(home string) []string { return []string{OpenCodeDBPath(home)} },
	},
	HarnessPi: {
		binaries: []string{"pi"},
		kind:     KindJSONL,
		roots:    func(home string) []string { return []string{PiSessionsPath(home)} },
	},
	HarnessQwen: {
		binaries: []string{"qwen"},
		kind:     KindJSONL,
		roots:    func(home string) []string { return []string{QwenSessionsPath(home)} },
	},
}

func TestHarnessRegistryMatchesContract(t *testing.T) {
	specs := make(map[Harness]HarnessSpec, len(harnesses))
	for _, spec := range harnesses {
		if _, dup := specs[spec.Name]; dup {
			t.Errorf("harness %q is declared twice", spec.Name)
		}
		specs[spec.Name] = spec
		if _, known := registryContracts[spec.Name]; !known {
			t.Errorf("harness %q is not in the registry contract", spec.Name)
		}
	}
	if len(specs) != len(registryContracts) {
		t.Errorf("registry has %d harnesses, contract has %d", len(specs), len(registryContracts))
	}

	claimed := make(map[string]Harness)
	for _, spec := range harnesses {
		for _, bin := range spec.Binaries {
			key := binaryKey(bin)
			if prev, ok := claimed[key]; ok {
				t.Errorf("binary %q (normalized %q) is declared by both %q and %q", bin, key, prev, spec.Name)
				continue
			}
			claimed[key] = spec.Name
		}
	}

	for name, want := range registryContracts {
		spec, ok := specs[name]
		if !ok {
			t.Errorf("harness %q has no registry entry", name)
			continue
		}
		if spec.Kind != want.kind {
			t.Errorf("harness %q kind = %q, want %q", name, spec.Kind, want.kind)
		}
		if len(spec.Binaries) != len(want.binaries) {
			t.Errorf("harness %q binaries = %v, want %v", name, spec.Binaries, want.binaries)
		}
		for _, bin := range want.binaries {
			if !slices.Contains(spec.Binaries, bin) {
				t.Errorf("harness %q does not declare binary %q", name, bin)
			}
		}
	}
}

func TestHarnessForResolvesDeclaredBinaries(t *testing.T) {
	for name, want := range registryContracts {
		for _, bin := range want.binaries {
			for _, input := range []string{bin, strings.TrimSuffix(bin, ".exe") + ".exe"} {
				got, ok := HarnessFor(Proc{Name: input})
				if !ok || got != name {
					t.Errorf("HarnessFor(%q) = %q, %v; want %q, true", input, got, ok, name)
				}
			}
		}
	}
}

func TestHarnessRegistryRootsMatchContract(t *testing.T) {
	home := "/home/dev"
	for _, spec := range harnesses {
		want, ok := registryContracts[spec.Name]
		if !ok {
			continue // the registry test reports the unknown declaration
		}
		if spec.Kind == KindNone {
			// A process-only harness has no store to locate, so a declared root would make
			// discovery look for history that no adapter reads.
			if spec.Roots != nil {
				t.Errorf("process-only harness %q declares roots", spec.Name)
			}
			continue
		}
		if spec.Roots == nil {
			t.Errorf("harness %q declares kind %q but no roots", spec.Name, spec.Kind)
			continue
		}
		if roots := spec.Roots(home); !slices.Equal(roots, want.roots(home)) {
			t.Errorf("harness %q roots = %v, want %v", spec.Name, roots, want.roots(home))
		}
		if roots := spec.Roots(""); len(roots) != 0 {
			t.Errorf("harness %q roots for an unknown home = %v, want none", spec.Name, roots)
		}
	}
}
