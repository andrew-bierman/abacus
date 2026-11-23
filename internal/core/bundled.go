package core

import (
	"fmt"
	"strings"
	"sync/atomic"
)

// abacus brings its own copies of bd, bv, lambe and beads-lease (a release archive carries them in
// bundled/ beside the program), so a missing tool is never the user's job: it is either a damaged
// or moved download (release builds) or an unfinished dev setup (go build). This file owns the
// words for both, so every surface says the same thing.

// DevBundleCommand sets a locally built binary up with its own tools.
const DevBundleCommand = "scripts/dev-bundle.sh"

var devBuild atomic.Bool

// SetBuildVersion tells core which kind of build this is: "dev" (or empty, or a -dev suffix) is a
// locally built binary; anything else is a release. The CLI calls it once at start-up; it is safe
// to call and to read from any goroutine. A zero value reads as a release.
func SetBuildVersion(v string) {
	devBuild.Store(v == "" || v == "dev" || strings.HasSuffix(v, "-dev"))
}

// IsDevBuild reports whether this is a locally built binary (no release archive around it).
func IsDevBuild() bool { return devBuild.Load() }

// bundledDomain is the Tasks, Leases and Queues domain each bundled tool carries; a bundled tool
// that is unusable already explains these domains, so they are not listed a second time.
var bundledDomain = map[string]DomainID{"bd": DomainTasks, "beads-lease": DomainLeases, "lambe": DomainQueues}

// optionalAdds names what an optional, not bundled tool adds, by module ID.
var optionalAdds = map[string]string{
	"board": "optional: adds the Board tab (channels, threads and tasks); everything else works without it",
}

// IsBundledTool reports whether abacus ships tool itself.
func IsBundledTool(tool string) bool {
	for _, t := range KnownTools {
		if t == tool {
			return true
		}
	}
	return false
}

// IsOptionalBundled reports whether a bundled tool is one abacus works without: bv only adds graph
// insights, so its absence never makes the overview "Needs attention".
func IsOptionalBundled(tool string) bool { return tool == "bv" }

// BundledProblem is the one sentence for a bundled tool abacus cannot use. refused is the specific
// reason a copy that exists was turned down ("world-writable", "not executable", ...), or "" when
// the copy is simply not there.
func BundledProblem(tool, refused string) string {
	if IsDevBuild() {
		return fmt.Sprintf("abacus's own %s is not set up for this build: run %s (or make bundle) in the abacus source folder.", tool, DevBundleCommand)
	}
	if refused != "" {
		return fmt.Sprintf("abacus's own copy of %s cannot be used (%s). Re-extract the download and keep the folder together.", tool, refused)
	}
	return fmt.Sprintf("abacus's own copy of %s is damaged or was moved. Re-extract the download and keep the folder together.", tool)
}

// refusedMarker is how LocateFound words a refused bundled copy inside an absent detail.
const refusedMarker = "(bundled copy ignored: "

// refusedReason pulls the refusal out of an absent detail ("" when there was none).
func refusedReason(detail string) string {
	_, rest, ok := strings.Cut(detail, refusedMarker)
	if !ok {
		return ""
	}
	return strings.TrimSuffix(strings.TrimSpace(rest), ")")
}

// AbsentSentence is what to show for a tool that was not located, in place of "not found": the
// bundled-tool problem for one abacus ships, the optional note for one it does not, else detail.
func AbsentSentence(tool, detail string) string {
	if IsBundledTool(tool) {
		return BundledProblem(tool, refusedReason(detail))
	}
	if s, ok := optionalAdds[tool]; ok {
		return s
	}
	return detail
}

// HowToConnectFor is the fix message shown for a domain with no usable provider. It may name the
// provider command because it helps the user fix a provider. The bundled-tool texts are computed
// from the current build kind on every call, so there is no shared state to race on.
func HowToConnectFor(d DomainID) string {
	switch d {
	case DomainBoard:
		return HowToConnectBoard
	case DomainTasks:
		return BundledProblem("bd", "")
	case DomainLeases:
		return BundledProblem("beads-lease", "")
	case DomainQueues:
		return BundledProblem("lambe", "")
	}
	return ""
}

// HowToConnectInsights is shown when no graph-analysis provider is available (a release and a dev
// build word the fix differently).
func HowToConnectInsights() string { return "Graph insights need bv. " + BundledProblem("bv", "") }

// BundledSummary is the self-contained check: how many of the tools abacus ships were found as its
// own verified copies. Additive to the doctor and version reports.
type BundledSummary struct {
	Total     int      `json:"total"`
	Verified  int      `json:"verified"`            // found as abacus's own copy (bundled)
	Elsewhere []string `json:"elsewhere,omitempty"` // usable, but from PATH or an override
	Unusable  []string `json:"unusable,omitempty"`  // absent, broken or incompatible
	Line      string   `json:"line"`
}

// Complete means every bundled tool is usable from some source.
func (b BundledSummary) Complete() bool { return len(b.Unusable) == 0 }

// SummarizeBundled counts the bundled tools among mods. A demo session has nothing to verify.
func SummarizeBundled(mods []ModuleReport) BundledSummary {
	var b BundledSummary
	demo := 0
	for _, m := range mods {
		if !IsBundledTool(m.ID) {
			continue
		}
		b.Total++
		d := m.Detection
		switch {
		case d.State != Ready:
			b.Unusable = append(b.Unusable, m.ID)
		case d.Source == SourceDemo:
			demo++
		case d.Source == SourceBundled:
			b.Verified++
		default:
			b.Elsewhere = append(b.Elsewhere, m.ID)
		}
	}
	switch {
	case demo > 0 && demo == b.Total-len(b.Unusable):
		b.Line = "Bundled tools: demo data, nothing to verify"
	case len(b.Elsewhere) == 0 && len(b.Unusable) == 0:
		b.Line = fmt.Sprintf("Bundled tools: %d of %d verified", b.Verified, b.Total)
	default:
		b.Line = fmt.Sprintf("Bundled tools: %d of %d verified", b.Verified, b.Total)
		if n := len(b.Elsewhere); n > 0 {
			b.Line += fmt.Sprintf("; %d using your own copy (%s)", n, strings.Join(b.Elsewhere, ", "))
		}
		if n := len(b.Unusable); n > 0 {
			b.Line += fmt.Sprintf("; %d unusable (%s)", n, strings.Join(b.Unusable, ", "))
		}
	}
	return b
}

// bundledNote explains where a bundled tool came from when it is not abacus's own verified copy.
func bundledNote(id string, d Detection) string {
	switch d.Source {
	case SourcePATH:
		why := "abacus's own copy was not found next to abacus"
		fix := "keep the download folder together to use it"
		if IsDevBuild() {
			why, fix = "abacus's own copy is not set up for this build", "run "+DevBundleCommand+" to use it"
		}
		if d.Ignored != "" {
			why = "abacus's own copy cannot be used (" + d.Ignored + ")"
			if !IsDevBuild() {
				fix = "re-extract the download to restore it"
			}
		}
		n := fmt.Sprintf("%s; using the %s on your PATH", why, id)
		if d.Version != "" {
			n += " " + d.Version
		}
		return n + " instead (" + fix + ")"
	}
	return ""
}
