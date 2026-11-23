---
title: Graph Core - Assessment of bv and b9s as Go packages
type: assessment
date: 2026-11-22
execution: none
status: shipped, library is the only engine
---

# Graph Core - Assessment of bv and b9s as Go packages

**Status: shipped. The library is the only engine.** The owner cleared the Go directive bump
(`go 1.26.11`), reviewed and accepted the license rider and decided the library engine does not need
to be a flag. `internal/graph` computes the Tasks insights in-process; the bv subprocess provider,
the `ABACUS_GRAPH_ENGINE` switch and the bundled `bv` binary are gone (see "Graph engine" in
`docs/tool-contract.md`). The numbers below were measured on the earlier opt-in prototype, release
flags, `CGO_ENABLED=0`:

| Measure | Probe (this assessment) | Prototype as built |
|---|---|---|
| linux/amd64 binary | 8,388,770 to 9,404,578 (+1.0 MB) | 8,388,770 to 10,186,914 (+1,798,144 bytes, +21 percent) |
| linux/arm64, darwin/arm64, darwin/amd64, windows/amd64 | 8.7 to 9.8 MB | 9,502,882, 9,651,538, 10,392,160, 10,576,896 (all build) |
| `go vet`, `go test ./...` | pass | pass, every TUI golden, theme and color test unchanged |
| 200-issue graph, whole analysis | n/a | about 5 ms |

The growth is larger than the probe because the prototype calls the full insights, plan and triage
paths. It is still well inside the 6 MB budget. The text below is the original assessment and is
left as written.

**Decision: STOP at the assessment. No prototype was built. One budget item tripped: the Go
directive bump.** Everything else measured inside budget. This is a "yes, with one toolchain step"
result, not a "no": clearing the tripped item is a toolchain install, after which the prototype can
be built the way the brief describes.

| Budget item | Limit | Measured | Verdict |
|---|---|---|---|
| Binary size growth | at most 6 MB | +1.0 MB (linux/amd64, only the analysis path linked) | pass |
| CGO required | no | no (`CGO_ENABLED=0` builds on all 5 targets) | pass |
| pkg/analysis needs bv-internal types | no | no, public `pkg/model` types only | pass |
| Go directive bump possible with Go 1.26.x already installed | yes | **no**: needs `go 1.26.11`, installed is 1.26.0 | **TRIPPED** |

What it would take to clear the tripped item: install Go 1.26.11 or newer (for example
`mise use go@1.26.11`), and make CI and the release script use the toolchain named by go.mod. The
Go toolchain auto-download (`GOTOOLCHAIN=auto`, the default) worked here, so a plain `go build` on a
machine with network access fetches it by itself; `GOTOOLCHAIN=local` and any offline or
pinned-toolchain build fails with `requires go >= 1.26.11 (running go 1.26.0; GOTOOLCHAIN=local)`.
That is the owner's call because it changes the project's minimum Go version for every contributor.

## 1. License rider (recorded as a fact, not re-litigated)

The owner has reviewed bv's license and accepts it for this use. The text, verbatim
from the LICENSE file of beads_viewer v0.27.0 (module cache copy), is recorded here so the
conditions travel with the decision. The MIT grant paragraph and the notice and warranty paragraphs
are included in full.

```
MIT License (with OpenAI/Anthropic Rider)

Copyright (c) 2026 Jeffrey Emanuel

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

ADDITIONAL RIDER / RESTRICTION (OpenAI / Anthropic):

This rider is part of the "conditions" of this License. In the event of any
conflict between this rider and any other portion of this License, this rider
controls.

"Restricted Parties" means OpenAI, L.L.C.; Anthropic, PBC; any of their
respective Affiliates; and any person or entity acting directly or indirectly
on behalf of, for the benefit of, or under the direction of any of the
foregoing (including any officer, director, employee, contractor, agent,
consultant, service provider, or representative).

Notwithstanding any other provision of this License, no rights are granted to
any Restricted Party. Any purported license, sublicense, assignment, transfer,
or other permission to any Restricted Party is null and void absent the
express prior written permission of Jeffrey Emanuel.

You may not provide, disclose, distribute, sublicense, sell, lease, lend,
host, make available, or otherwise permit access to the Software or any
derivative work of the Software (as defined in applicable copyright law)
(collectively, "Derivative Works") to or for any Restricted Party.

For purposes of this rider, "use" includes, without limitation: copying,
modifying, merging, publishing, distributing, sublicensing, selling,
transferring, making available, hosting, deploying, executing, benchmarking,
testing, analyzing, indexing, or incorporating the Software or any Derivative
Works into any dataset, training corpus, evaluation harness, or pipeline for
machine learning or other automated systems.

This rider applies to the Software and all Derivative Works. As a condition of
use, you agree that this rider is a precondition to exercising any rights
under this License, and you agree that any distribution of the Software or any
Derivative Works must include this rider provision unmodified.

Any breach of this rider automatically and immediately terminates the
permissions granted by this License. Upon termination, you must immediately
cease all use and distribution of the Software and any Derivative Works and
destroy all copies under your control.

You agree that a breach of this rider would cause irreparable harm and that
Jeffrey Emanuel may seek injunctive or other equitable relief to enforce this
rider, in addition to any other remedies available at law. To the maximum
extent permitted by applicable law, the prevailing party in any action to
enforce this rider shall be entitled to recover reasonable attorneys' fees and
costs.

For purposes of this rider, "Affiliate" means any entity that directly or
indirectly controls, is controlled by, or is under common control with the
specified party. "Control" means ownership of more than 50% of the voting
securities or other ownership interest, or the power to direct management or
policies by contract or otherwise.

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

One mechanical consequence that does not reopen the question: the rider says any distribution of
the Software or Derivative Works "must include this rider provision unmodified". If bv is ever
linked into released abacus binaries, the release archive must carry bv's LICENSE file as it does
for other bundled tools (see `bundled-tools.json`), and any copied bv source file must keep the
license text. A linked module is a stronger form of redistribution than the bundled `bv` binary
abacus ships today, so the release notice list should be updated in the same change.

## 2. Method

All measurements ran in an isolated worktree on a commit equal to origin/main, with a private
`GOCACHE`, `CGO_ENABLED=0`, and the flags `scripts/package-release.sh` uses
(`-trimpath -ldflags "-s -w"`). The network was used only for `go get` and module and toolchain
download. Nothing was cloned into the repo, and no bd, bv or other Beads tool was run. The probe
imported `pkg/analysis` and `pkg/model` from `github.com/Dicklesworthstone/beads_viewer@v0.27.0`,
ran it on issues built in memory, and was then deleted; go.mod and go.sum are unchanged by this
PR. The b9s probe lived outside the repo in a scratch module.

## 3. bv (beads_viewer v0.27.0, `pkg/analysis`)

### 3.1 Build and platform matrix

`CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" ./cmd/abacus` with the probe linked (cross-compile
only, nothing was run on the non-Linux targets):

| Target | Builds | Binary bytes with bv analysis linked |
|---|---|---|
| linux/amd64 | yes | 9,404,578 |
| linux/arm64 | yes | 8,716,450 |
| darwin/arm64 | yes | 8,926,258 |
| darwin/amd64 | yes | 9,586,496 |
| windows/amd64 | yes | 9,766,912 |

Cgo files exist only in the standard library packages `runtime/cgo`, `net` and `os/user`, all of
which build without cgo. The existing `go test ./...` suite passed unchanged with bv added (the
module bumps in 3.3 did not move any TUI golden).

### 3.2 Binary size

| | linux/amd64 bytes |
|---|---|
| Before (origin/main) | 8,388,770 |
| After (probe calls NewAnalyzer, Analyze, GetExecutionPlan, ComputeTriage) | 9,404,578 |
| Growth | +1,015,808 (about 1.0 MB, 12 percent) |

The probe is gated behind a runtime condition so the linker keeps the code. Calling more of
pkg/analysis (for example the full insights and what-if paths) would grow this somewhat; the budget
of 6 MB leaves wide margin, and `ComputeTriage` already pulls most of the package.

### 3.3 go directive, toolchain line and modules

| | before | after `go get` + `go mod tidy` |
|---|---|---|
| `go` directive | `go 1.26.0` | `go 1.26.11` |
| `toolchain` line | none | none (Go 1.26 does not add one when the directive is a full patch version) |
| `go list -m all` entries | 36 | 88 |
| Modules linked into the abacus binary (`go list -deps`, non-std) | the existing Charm set | adds gonum, goccy/go-json, x/sync, and the Charm modules below |

Forced upgrades of modules abacus already uses (minimum version selection follows bv's go.mod):

| Module | from | to |
|---|---|---|
| charmbracelet/lipgloss | v1.1.0 | v1.1.1-0.20250404203927-76690c660834 (a pseudo-version) |
| charmbracelet/x/ansi | v0.11.6 | v0.11.10 |
| charmbracelet/colorprofile | v0.4.1 | v0.4.3 |
| clipperhouse/displaywidth, uax29/v2 | v0.9.0, v2.5.0 | v0.11.0, v2.7.0 |
| mattn/go-runewidth, go-isatty | v0.0.19, v0.0.20 | v0.0.31, v0.0.24 |
| lucasb-eyer/go-colorful | v1.3.0 | v1.4.1 |
| golang.org/x/text | v0.3.8 | v0.44.0 |

Two things to weigh: abacus moves from a tagged lipgloss to a pseudo-version, and it takes on every
future bv go.mod bump through MVS. The other roughly 50 new `go list -m all` entries (glamour, huh,
chroma, goldmark, modernc sqlite, plotting and image libraries, cobra, fsnotify, and so on) come
from bv's `pkg/ui`, `pkg/export` and loader packages. They enter go.mod and go.sum as the module
graph but are not compiled into abacus, because abacus imports only `pkg/analysis` and `pkg/model`.
Windows and macOS are not affected by any platform-specific dependency in the linked set.

### 3.4 API surface to call

| Need | Call | Types involved |
|---|---|---|
| Build the graph | `analysis.NewAnalyzer([]model.Issue)` | `model.Issue`, `model.Dependency` (public `pkg/model`) |
| Cycles and critical path | `(*Analyzer).Analyze()` returning `GraphStats`; `Cycles()`, `GetCriticalPathScore(id)`, `GetPageRankScore(id)` | `GraphStats` |
| Actionable and blocked counts, tracks | `(*Analyzer).GetExecutionPlan()` with `TotalActionable`, `TotalBlocked`, tracks | `ExecutionPlan` |
| Direct blockers of one issue | `GetOpenBlockers(id)` | strings |
| Top picks, blockers to clear | `analysis.ComputeTriage([]model.Issue)` with `QuickRef.TopPicks`, `BlockersToClear` | `TriageResult` |

It needs no bv-internal types. `pkg/analysis` imports bv's `internal/env` for its own configuration,
which compiles for outside consumers and is not part of any signature. The input type is
`model.Issue` from the public `pkg/model` package, so abacus must build that struct (ID, Title,
Status, Priority, IssueType, Dependencies with `IssueID`, `DependsOnID` and `Type`), but it needs
none of bv's loaders. `NewAnalyzer(issues)` ran on issues built entirely in memory with no files and
no `.beads` directory. The probe's results on hand-built graphs, with `Dependencies[i].Type =
model.DepBlocks` and `DependsOnID` meaning "is blocked by":

| Graph | actionable | blocked | cycles | critical path scores | top pick |
|---|---|---|---|---|---|
| chain a <- b <- c | 1 | 2 | none | a 3, b 2, c 1 | a |
| diamond a <- b,c <- d | 1 | 3 | none | a 3, b 2, c 2, d 1 | a |
| cycle a <-> b, plus c | 1 | 2 | [a b a] | all 0 | c |
| closed a, open b, in-progress c | 1 | 1 | none | a 3, b 2, c 1 | b |
| empty | 0 | 0 | none | none | none |

Differences from what the bv subprocess adapter already reports, worth designing for (not hiding):

- "Critical path" in bv is a per-issue height score (longest chain below an issue), not an ordered
  path. abacus's `InsightsReport.CriticalPath` is a ranked list, which is the same shape bv's
  `Keystones` produces, so this maps cleanly; a cycle makes every score zero, which matches the
  subprocess behavior of `critical path skipped`.
- A closed issue still contributes to scores of its dependents in bv's raw, unscoped analysis (a
  is closed and still scores 3). The ranked keystone and bottleneck lists exclude closed issues
  (the scoped insights `bv --robot-insights` uses), while the triage pick scores come from bv's
  triage over all issues, closed ones included. A library adapter must decide whether closed
  issues belong in the input; the prototype keeps them, so a closed blocker satisfies its
  dependents.
- "Actionable" counts only issues whose status allows work and whose blockers are all closed; an
  in-progress issue behind an open blocker counts as blocked. v0.21.0 of bv deliberately changed the
  triage counts (`open_count` is now exactly status `open`; the legacy meaning moved to
  `not_closed_count` and `not_actionable_count`). abacus reads `total_actionable` and
  `total_blocked` from `--robot-plan`, not those fields, so it is unaffected today.
- "Next pick" is bv's triage score (PageRank, unblocks, priority and more), not bd's ready ordering.
  The two disagree by design, so the pick should keep coming from `ComputeTriage` with its
  definition documented, rather than being reconstructed.

### 3.5 Stability

| Signal | Observation |
|---|---|
| Versions | tags v0.15.2 to v0.27.0; pre-1.0, no Go API stability promise |
| Release cadence | v0.24.1 on 2026-09-08, v0.25.x in September, v0.26.0 on 2026-11-02, v0.27.0 on 2026-11-21 |
| Churn in `pkg/analysis` | 45 commits in the 30 days before the dossier (research dossier, not re-measured) |
| Breaking entries in CHANGELOG | v0.21.0 "BREAKING (robot JSON semantics)": strict triage count semantics. That is JSON output semantics, not Go signatures, but the Go API behind it moved with it |
| Go version policy | go.mod tracks the newest patch (1.26.11, a security toolchain bump), so abacus's minimum moves whenever bv's does |

Mitigation is "pin and wrap": pin an exact tag, keep every bv import in one adapter file behind
abacus-owned issue and report types, cover the adapter with parity tests, and update on purpose.

## 4. b9s (vanderheijden86/b9s v1.3.6, `pkg/model` and `pkg/loader`)

| | Result |
|---|---|
| License | MIT (Copyright 2026 Andre van der Heijden) |
| `go` directive of the module | 1.25.0, toolchain go1.25.5; compatible with abacus's `go 1.26.0`, no bump needed |
| Probe: `loader.ParseIssues` from a string | built and ran with `CGO_ENABLED=0`; linked modules are only the b9s packages and goccy/go-json |
| Probe binary (trivial main, fmt included) | 3,051,682 bytes |
| `go list -m all` for the probe module | 151 entries (aws, sigstore, grpc, mysql, sqlite and others sit in go.sum only, not linked) |
| Platform | README says Windows is not supported; the two packages compile on any target |
| Analysis | none: no PageRank, critical path or ready scoring; the dossier and a source search agree |
| UI | `pkg/ui` is a 72k-line app in one package (dossier figure); embedding any piece pulls the whole dependency set |

What abacus would take from b9s: possibly `pkg/model` types and the JSONL `ParseIssues`, nothing
else. That is a minor convenience because abacus needs only a handful of fields, and the real
readers (Dolt, SQLite, source discovery) are in `internal/` and cannot be imported. A 100-line
abacus-owned JSONL parser replaces it with no new go.sum weight. The layout the owner likes is in
`pkg/ui`, which is not reusable; copying its layout ideas into abacus's own panels is a design task,
not a dependency.

## 5. Options and decision

| Option | Cost | Value | Verdict |
|---|---|---|---|
| A. Keep the bv subprocess (today) | none | works, but needs bv present and a process per refresh (four robot reads) | stays the default |
| B. Adopt bv `pkg/analysis` behind an adapter | Go directive bump to 1.26.11; lipgloss to a pseudo-version; +1 MB; MVS follows bv; license file ships in releases | no subprocess, one in-memory pass, deterministic tests on hand-built graphs, works when bv is not installed | recommended once the toolchain item is cleared |
| C. Copy a small piece of bv analysis into abacus | license text must travel with copies; ongoing divergence from bv | avoids the module and toolchain bump | not preferred: it takes the code, loses upstream fixes, and is the same license question |
| D. Own implementation (Tarjan SCC, longest path, simple ranking, about 300 lines) | write and maintain | no external dependency, no toolchain bump | the fallback if the toolchain bump is refused; the `Analyzer` interface below makes B and D interchangeable |
| E. Import b9s `pkg/model` and `pkg/loader` | minor | little: parser only, no analysis | not worth a dependency; copy nothing |
| F. Import steveyegge/beads | Go 1.27, CGO for embedded Dolt, schema migration side effects | reads Dolt directly | rejected (dossier; unchanged) |

Decision: adopt B behind an adapter, as an opt-in provider first, after the owner clears the Go
toolchain item. The data path should not pull bv's loaders. abacus-owned issue types are built
from `bd` JSON that abacus already reads, and only the analysis call is bv's. That keeps "writes
only through the gate" intact (bv's loader can write sidecar files and call `bd export`; abacus
never calls it).

## 6. Design for the prototype (not built in this PR)

Package `internal/graph`, sitting beside `core` so `core` stays independent of bv:

- `Analyzer` interface: input is abacus-owned issues (id, title, status, priority, type,
  blocked-by ids), output is `core.InsightsReport` (top picks, blockers, critical path, cycles,
  actionable and blocked counts, graph size).
- `IssueSource` interface; first implementation parses the JSON that bd exports, tested only with
  hand-written fixtures under `internal/graph/testdata` built from `docs/tool-contract.md`.
- The bv adapter is a single file in its own sub-package (for example `internal/graph/bvlib`) so
  removing bv later is one deletion and one import line.
- Selection by `ABACUS_GRAPH_ENGINE=library`; default `subprocess` (unchanged), forced off in
  `--demo`. It satisfies `core.InsightsProvider` so the Tasks tab and JSON output do not change.
- Parity tests against the shape `internal/bv` decodes, on a chain, a diamond, a cycle, an empty
  tracker, closed issues and a 200-issue graph, with the difference list from 3.4 documented.
- Mutation checks at the adapter boundary: status mapping, dependency direction, cycle input,
  empty-tracker short-circuit (no analysis call on zero issues), title sanitization.

## 7. Risks

| Risk | Mitigation |
|---|---|
| API and semantics churn (pre-1.0, weekly releases, breaking entries) | pin an exact tag, one adapter file, parity tests, upgrade deliberately |
| Minimum Go version follows bv's patch releases | state the toolchain in the docs and in CI; `GOTOOLCHAIN=auto` fetches it |
| Lipgloss moves to a pseudo-version through MVS | re-run the TUI goldens (they passed in the probe); revisit when lipgloss tags a release |
| go.sum grows by about 50 unlinked modules | accepted cost; only linked packages affect the binary |
| License file and notice must ship with a linked module | add bv's LICENSE to the release archive notices when the library becomes default |
| Closed-issue and next-pick definitions differ from bd's ready ordering | documented in the parity test file and the Tasks docs; keep bd as the ground truth for "ready" |

## 8. What remains for the owner

1. Say whether abacus may require Go 1.26.11 or newer (installing it with mise, and CI honoring
   go.mod). That clears the one tripped budget item. If no, option D is the path.
2. After that, say yes or no to building the opt-in prototype from section 6, and later to making
   the library the default engine in a follow-up.
3. Confirm that shipping bv's LICENSE file inside the release archive (alongside the bundled tools)
   is the notice you want, since the rider requires it to travel unmodified.
