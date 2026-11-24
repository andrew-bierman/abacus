package core

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// abacus carries its own bd, lambe, beads-lease and Vercel Board CLI inside the binary (see
// embedded.go), so a missing tool is never the user's job to install. This file owns the words for
// the two ways one can still be unusable: a development build that does not include it, and a
// built-in copy that could not be set up. Every surface says the same thing through these.

// DevBuildLine is the one sentence for a development build. It appears only in Doctor's
// tool summary, never in a domain tab.
const DevBuildLine = "Development build: tools are not built in; build with make build"

// bundledDomain is the domain each built-in tool carries; a tool that is unusable already explains
// its domain, so the domain is not listed a second time.
var bundledDomain = map[string]DomainID{"bd": DomainTasks, "beads-lease": DomainLeases, "lambe": DomainQueues, "board": DomainBoard}

// optionalAdds names what a tool that nothing else needs adds, by module ID.
var optionalAdds = map[string]string{
	"abtop": "optional: adds richer live detail to the Agents tab (context use, rate limits, ports, subagents); everything else works without it",
	"board": "optional: adds the Board tab (channels, threads and tasks); everything else works without it",
}

// IsBundledTool reports whether abacus carries tool itself.
func IsBundledTool(tool string) bool {
	for _, t := range KnownTools {
		if t == tool {
			return true
		}
	}
	return false
}

// bundledWhat says what is wrong with a built-in tool. refused is the specific reason a copy was
// turned down, or "" when there is none.
func bundledWhat(tool, refused string) string {
	switch {
	case !EmbeddedEnabled():
		return fmt.Sprintf("%s is not available in this development build.", tool)
	case refused == "":
		return fmt.Sprintf("The built-in %s could not be set up.", tool)
	}
	return fmt.Sprintf("The built-in %s could not be set up: %s.", tool, strings.TrimSuffix(refused, "."))
}

// bundledFix is the one user-level fix: a folder abacus may write to, or a fresh download. It is
// never an install. A development build has none to offer.
func bundledFix(refused string) string {
	switch {
	case !EmbeddedEnabled():
		return ""
	case strings.Contains(refused, damagedMark):
		return "This copy of abacus is damaged; download it again."
	}
	return "Choose a folder you can write to with " + ToolsDirEnv + "=<folder>, then press r."
}

// BuiltInFix is the one user-level fix for an absent built-in tool whose detail is given.
func BuiltInFix(detail string) string { return bundledFix(refusedReason(detail)) }

// BundledProblem is the sentence for a built-in tool abacus cannot use.
func BundledProblem(tool, refused string) string {
	return strings.TrimSpace(bundledWhat(tool, refused) + " " + bundledFix(refused))
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
// built-in problem for one abacus carries, the optional note for one it does not, else detail.
func AbsentSentence(tool, detail string) string {
	if IsBundledTool(tool) {
		return BundledProblem(tool, refusedReason(detail))
	}
	if s, ok := optionalAdds[tool]; ok {
		return s
	}
	return detail
}

// toolOfDomain is the built-in tool behind a domain.
func toolOfDomain(d DomainID) (string, bool) {
	for tool, dom := range bundledDomain {
		if dom == d {
			return tool, true
		}
	}
	return "", false
}

// howToConnectFor is the text shown for a domain with no usable provider. reason is what pick
// found: when it already says what is wrong, only the fix is added; with no reason it says both.
// Nothing here says to install anything.
func howToConnectFor(d DomainID, reason string) string {
	tool, ok := toolOfDomain(d)
	switch {
	case !ok:
		return ""
	case reason == "":
		return BundledProblem(tool, "")
	}
	return bundledFix(reason)
}

// HowToConnectFor is the text for a domain with no usable provider and no more specific reason.
func HowToConnectFor(d DomainID) string { return howToConnectFor(d, "") }

// HowToConnectInsights is shown when no graph-analysis provider is available. The analysis runs
// inside abacus on the tracker bd keeps, so the fix is about bd.
func HowToConnectInsights() string {
	return "Graph insights are computed inside abacus from the tracker. " + BundledProblem("bd", "")
}

// BundledSummary is the self-contained check: how many of abacus's built-in tools are ready.
// Additive to the doctor and version reports.
type BundledSummary struct {
	Total     int      `json:"total"`
	Verified  int      `json:"verified"`            // ready as abacus's own built-in copy
	Elsewhere []string `json:"elsewhere,omitempty"` // usable, but from an override or a development build's PATH
	Unusable  []string `json:"unusable,omitempty"`  // absent, broken or incompatible
	Embedded  bool     `json:"embedded"`            // this binary carries its tools
	Line      string   `json:"line"`
}

// Complete means every built-in tool is usable from some source.
func (b BundledSummary) Complete() bool { return len(b.Unusable) == 0 }

// SummarizeBundled counts the built-in tools among mods. A demo session has nothing to check.
func SummarizeBundled(mods []ModuleReport) BundledSummary {
	b := BundledSummary{Embedded: EmbeddedEnabled()}
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
		case d.Source == SourceEmbedded:
			b.Verified++
		default:
			b.Elsewhere = append(b.Elsewhere, m.ID)
		}
	}
	switch {
	case demo > 0 && demo == b.Total-len(b.Unusable):
		b.Line = "Built-in tools: demo data, nothing to check"
	case !b.Embedded:
		b.Line = DevBuildLine
	default:
		b.Line = fmt.Sprintf("Built-in tools: %d of %d ready", b.Verified, b.Total)
		if n := len(b.Elsewhere); n > 0 {
			b.Line += fmt.Sprintf("; %d using your own copy (%s)", n, strings.Join(b.Elsewhere, ", "))
		}
		if n := len(b.Unusable); n > 0 {
			b.Line += fmt.Sprintf("; %d unusable (%s)", n, strings.Join(b.Unusable, ", "))
		}
	}
	return b
}

// bundledNote explains where a built-in tool came from when it is not abacus's own copy.
func bundledNote(id string, d Detection) string {
	switch {
	case d.Source == SourceOverride:
		return "using the copy you chose with the ABACUS_*_BIN setting instead of the built-in one"
	case d.Ignored != "":
		return "the copy next to abacus was not used (" + d.Ignored + ")"
	}
	return ""
}

// BuiltInExecProblem says, in plain words, why a built-in tool could not even be started: the
// folder it was unpacked into does not allow running programs (a noexec mount), or the system
// blocked it (macOS quarantine or Gatekeeper, a security tool), or the system killed it with a
// signal right away. "" when path is not a built-in copy or the failure is something else (a timeout,
// a normal exit code). The one shared wording; every module's Detect uses it.
func BuiltInExecProblem(path string, exit int, err error) string {
	if !IsBuiltInPath(path) || errors.Is(err, ErrTimedOut) || errors.Is(err, ErrCanceled) || errors.Is(err, ErrOutputTooLarge) {
		return ""
	}
	var why string
	switch {
	case err != nil:
		low := strings.ToLower(err.Error())
		if !(errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.ENOEXEC) ||
			strings.Contains(low, "permission denied") || strings.Contains(low, "not permitted") || strings.Contains(low, "exec format")) {
			return ""
		}
		why = plainWhy(err)
	case exit < 0:
		why = "it was stopped by a signal"
	default:
		return ""
	}
	dir := filepath.Dir(path)
	msg := fmt.Sprintf("The built-in %s could not be started from %s (%s). This folder may not allow running programs, or the system blocked it: choose another folder with %s=<folder>, then press r.", filepath.Base(path), dir, why, ToolsDirEnv)
	if runtime.GOOS == "darwin" {
		msg += " If macOS blocked it, you can also run: xattr -dr com.apple.quarantine " + dir
	}
	return msg
}
