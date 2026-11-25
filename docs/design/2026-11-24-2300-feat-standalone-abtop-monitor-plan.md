# abtop parity: modular harness adapters and a standalone monitor

Status: plan of record. Worked by the abacus-port loop, one item per pass.

## Goal

1. **Coverage** — support far more agents than Claude and Codex, added modularly.
2. **Standalone** — a full-screen Go monitor in `cmd/abtop`, built on `internal/agents`.

abacus keeps its Sessions tab; both front-ends render the same read.

## Modularity: adding an agent must be cheap

Today each harness is a bespoke file. Adding the fifth agent should not mean copying the
fourth. The shape:

```
internal/agents/
  harness.go       # HarnessSpec + registry: the one place an agent is declared
  jsonl_spec.go    # generic JSONL adapter driven by a dotted-path Mapping
  jsonl.go         # the shared incremental reader (already exists)
  store_sqlite.go  # shared read-only SQLite opener (already exists in opencode.go)
  claude.go  codex.go  pi.go  opencode.go   # custom adapters, self-registering
```

A `HarnessSpec` carries:

- `Name` — the harness id shown in the UI
- `Binaries` — executable basenames that mean "this is a session" (`.exe` tolerated)
- `Roots` — where the store lives, as `func(home string) []string`, so XDG, `%APPDATA%` and a
  harness's own env override are all expressible
- `Kind` — `jsonl`, `sqlite`, or `none` (process-only: the agent leaves no readable history)
- `Mapping` — for `jsonl`: dotted paths for timestamp, role, text, model, and the four token
  counters, plus the additive-or-cumulative rule
- `Open` — for `sqlite` and anything exotic: a constructor returning the reader

Two consequences worth stating, because they are the point:

- A JSONL harness is **data, not code**: a new entry in the registry table. Gemini, Qwen,
  Copilot CLI, Trae, Qoder and the rest are all line-oriented JSON.
- A harness that only needs liveness and ports is `Kind: none` — it appears in the monitor
  with a process, children and ports, and no tokens. That is a valid, complete adapter, not a
  stub.

Adapters that do not fit the generic mapping keep their own file and implement the same
reader interface, registering themselves from `init()` or from one `RegisterAll()` call.

## Coverage targets

Ordered by evidence, not enthusiasm. Verified on this machine before claiming support.

| Agent | Store | Evidence here |
| --- | --- | --- |
| Grok CLI | `~/.grok/sessions/<url-encoded-cwd>/` | present, 1.1M |
| Gemini CLI | `~/.gemini/tmp/<hash>/chats/*.jsonl` | config present, no sessions yet |
| GitHub Copilot CLI | `$COPILOT_HOME/session-state/*.jsonl` | documented |
| Qwen Code | `~/.qwen/projects/**/*.jsonl` | documented |
| Hermes Agent | SQLite | directory present, empty |
| Cursor / Amp / Antigravity | own stores | documented, custom readers |

Anything not verifiable locally ships behind a synthetic fixture and is labelled as
documented-only in the plan, never presented as tested.

## Branch

Work continues on `feat/agents-sessions`, landing with pull request #57.

## Progress

The loop works the first unchecked item. Tick one per pass.

- [x] `harness.go`: `HarnessSpec` registry; discovery consults it instead of a hardcoded map
- [x] `jsonl_spec.go`: generic JSONL adapter (dotted-path mapping, additive vs cumulative usage)
- [x] port the three existing JSONL readers onto the generic adapter, deleting the duplication
- [x] `grok` adapter (verified against the store on this machine)
- [x] `gemini` adapter (documented-only until a real session exists)
- [x] `copilot` adapter
- [ ] `qwen` adapter
- [ ] `hermes` adapter (SQLite)
- [ ] process-only adapters (`Kind: none`) for the remaining CLIs, so they appear with ports
- [ ] `cmd/abtop` scaffold: entry point, terminal setup, `--once` and `--json`
- [ ] layout: header, sessions table, detail, ports — abtop's shape in lipgloss
- [ ] host vitals panel (CPU, memory, load) with a history ring
- [ ] token-rate history and sparkline (200 points)
- [ ] per-session memory across the whole process tree
- [ ] MCP server list
- [ ] subagent tree from parent ids
- [ ] themes and a config file
- [ ] kill session and kill orphan ports, both behind a confirm
- [ ] `--setup`: install the rate-limit collection hook, printing the exact command first
- [ ] terminal jump adapters (tmux, then iTerm2, then cmux)
- [ ] golden tests for the standalone layout
- [ ] release wiring for a second binary
- [ ] docs: README section, plus the "add an agent" cookbook proving the modular claim
- [ ] shipping: push and update pull request #57

## Rules

Read-only toward agents; synthetic fixtures with invented names; no run-time dependency on
abtop; logic packages import no UI; never force-push, never merge, never commit to `main`.

A pass that claims support for an agent must say how it was verified: a real store, or a
fixture with the source format documented.

## Pass log

One line per completed item: what it did and how it was verified.

- harness registry — added `internal/agents/harness.go` with `HarnessSpec` (name, binaries, roots,
  kind), the declaration table and a binary index derived from it, and made `HarnessFor` read the
  index instead of `harnessBinaries`; `go build ./...`, `go vet ./...` and `go test ./...` pass,
  explicit contract tests pin a dropped alias, a normalized `.exe` duplicate and a wrong store
  root (each mutation fails as intended); review residual: `Roots`/`Kind` are declared and tested
  but consumed by the adapter items.
- generic JSONL adapter — added `internal/agents/jsonl_spec.go` with a `JSONLMapping` of dotted
  paths for identity, timestamp, role vocabulary, text, model and the four token counters plus
  additive/cumulative usage, optional cache-inclusive input, explicit or derived context fill,
  and a compiled path table; incremental reads use the existing cursor, budget, rotation and drain
  lifecycle. Tests build synthetic nested/event fixtures with invented names and pin incremental
  appends, additive and cumulative levels, resets, zero markers, tool state, titles, escaped dots,
  array indexes, block/nested prompt text, frozen roles, cache-split input, large counters,
  timestamp units and malformed mappings; `go build ./...`, `go vet ./internal/agents` and
  `go test ./...` pass. Review applied the cumulative-fill, block-text and frozen-roles findings;
  residual: no production caller until the next adapter item, and cumulative records that omit a
  counter still replace the whole level.
- three JSONL readers on the shared accumulation — `internal/agents/jsonl_session.go` now owns
  identity, additive and response token folding, bounded history, context fill and the
  `StoredSession` snapshot for the generic adapter, Claude, Codex and pi, so those four no longer
  repeat the same accumulation code; each format keeps only its own record grammar, state machine,
  path resolution and Codex quota. Behaviour is pinned by the existing synthetic store fixtures
  plus new cases for a windowless Codex turn, a Claude model-change window and output-only usage;
  `go build ./...`, `go vet ./...` and `go test ./...` pass. Review applied the test and comment
  findings; residual: pi sets `sawUsage` from auxiliary records (the emitted `Context` is unchanged),
  and the last-wins identity setters and title trim are not separately pinned.
- `grok` adapter — added `internal/agents/grok.go` with `GrokStore` reading `summary.json`,
  `updates.jsonl` and `usage.json`, sidecar caching by size+mtime, cwd resolution with URL-encoded
  groups, slug-plus-`.cwd` long paths and `GROK_HOME`; registered `HarnessGrok` in
  `internal/agents/harness.go` and joined Grok sessions in `internal/core/sessions.go`. Verified
  against the real store under `~/.grok/sessions` (one session, summary-only check) and synthetic
  fixtures with invented names. Review applied fixes for `usage_update`, parallel tool-call tracking,
  mid-session model switches, oldest-mtime sidecar eviction, bounded cwd and group caches, and a
  group-mtime cache so `PathFor` does not walk every historical directory each tick; tests added
  for those cases plus incremental reads and cwd-mismatch filtering. `go build ./...`,
  `go vet ./...` and `go test ./...` pass. Residual: some SessionUpdate variants remain unhandled
  (SessionEnded, SessionSummaryGenerated), field shapes are pinned by binary strings and synthetic
  fixtures rather than a full live transcript, and the group-mtime cache trusts the filesystem to
  update the parent directory mtime when a new session subdirectory is created.
- `gemini` adapter — added `internal/agents/gemini.go` with `GeminiStore` over the generic JSONL
  adapter: the mapping reads the header (`sessionId`), message records (`type` user/gemini,
  `content`, `model`), `tokens.{input,output,cached}` with cached content split out of input, and
  `$set.summary`; a `UsageID` in the mapping makes a rewritten response replace its own
  contribution, and the store resolves the project from `projects.json`, a legacy SHA-256
  directory, or a cached `.project_root` marker, with subagent parent from the nested directory.
  Registered `HarnessGemini` and joined it in `internal/core/sessions.go`. **Verified against
  Gemini CLI's upstream `chatRecordingService.ts`, `chatRecordingTypes.ts`, `storage.ts` and
  `paths.ts` plus synthetic fixtures with invented names; there is no real Gemini session store
  on this machine, so this is documented-only.** Tests pin registry, legacy-hash, marker,
  malformed-registry, cached-hit, cache-eviction, subagent-parent, rewrite-folding, prompt-fill and
  non-conversation records. `go build ./...`, `go vet ./...` and `go test ./...` pass. Review
  applied the marker-index cache (a miss no longer re-walks every project), isolated each
  resolution strategy in tests, and collapsed resolution to one candidate loop; residual: no live
  transcript, older-response rewrites count anew, live subagents resolve the parent session, and
  Gemini tool records do not drive state (CPU deltas still do).
- `copilot` adapter — added `internal/agents/copilot.go` with `CopilotStore` over
  `session-state/<id>/events.jsonl` and the pre-1.0.11 flat `<id>.jsonl`: `session.start`
  identity and cwd, `workspace.yaml` cwd/name fallback with a size+mtime sidecar cache,
  user/assistant/tool state, per-call `assistant.usage` and per-model `session.shutdown` deltas
  (input cache-inclusive, backwards counters reset, repeated rollups zero), message-output and
  `session.usage_info` fallbacks, `session.task_complete` title, and newest-session resolution
  with bounded root-mtime, cwd and sidecar caches. Registered `HarnessCopilot` and joined it in
  `internal/core/sessions.go`. **Verified against Copilot CLI's SDK streaming-events reference
  and token-accounting readers plus synthetic fixtures with invented names; there is no real
  Copilot store on this machine, so this is documented-only.** Tests pin shutdown precedence,
  resumed deltas, repeated rollups, resets, multi-model deltas, inclusive-input subtraction,
  per-call and message fallbacks, usage-info context and limit, tool cleanup for named and
  anonymous calls, workspace rewriting, flat legacy naming, path caching and the core join.
  `go build ./...`, `go vet ./...` and `go test ./...` pass. Review applied sidecar/cwd caches,
  candidate ordering, the dropped duplicate stat, `useContext`, deterministic eviction,
  dangling-tool cleanup, the larger usage source for resumed sessions, and removal of the
  delta-derived context fill; residuals: newer Copilot builds moved history to central SQLite
  stores (`session-store.db`, `data.db`), `session.shutdown.tokenDetails` and OTel exports are
  not read, `readCopilotStartCWD` bypasses the JSONL line cap, and the mod-time/dir helpers
  duplicate Gemini/Grok pending a package-wide refactor.
