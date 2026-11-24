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

- [ ] `harness.go`: `HarnessSpec` registry; discovery consults it instead of a hardcoded map
- [ ] `jsonl_spec.go`: generic JSONL adapter (dotted-path mapping, additive vs cumulative usage)
- [ ] port the three existing JSONL readers onto the generic adapter, deleting the duplication
- [ ] `grok` adapter (verified against the store on this machine)
- [ ] `gemini` adapter (documented-only until a real session exists)
- [ ] `copilot` adapter
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
