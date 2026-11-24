# Agents domain: a sessions tab for local AI coding agents

Status: phase 1 landed (`internal/agents`, discovery + processes + ports). Phases 2 to 4 open.

## Goal

A tab in abacus that answers the question abtop answers: which AI coding agents are running on
this machine right now, what each is doing, what it is spending, and what it left behind.

The tab is a front-end over a logic package, following the same rule as every other domain:
`internal/agents` imports no UI package (`internal/core/arch_test.go` enforces it), so the TUI,
`abacus sessions --json`, and any other front-end render one read.

## Why this is not a port

abtop is the reference for the data, not for the code. Three findings decided that:

1. **Two halves, only one of which exists in Go.** abtop is transcript parsers (`claude.rs` 4.2k
   lines, `codex.rs` 3.0k, `opencode.rs` 0.7k) plus a live observer (`ps`, `lsof`, `netstat`,
   `git`, `/proc`, and abtop's own quota hook files). The Go libraries in this space
   (`ferry`, `moirai`, `agent-ouija`) cover the first half only, and `ferry` carries no token
   fields at all. Nothing reads live processes or listening ports.
2. **The libraries are already stale for this machine.** opencode here writes the v2 SQLite
   schema (`session_v2`, `session_message`, ~552 sessions) and has no `storage/session/*.json`
   tree. `ferry` resolves opencode by that tree and would find zero sessions; `moirai` does not
   reference `session_v2`. That reader is ours to write against a schema we can see.
3. **abtop's tests are inline samples, not captured reality.** 225 tests, 26% of the crate, well
   concentrated on the risky parsers, but no fixture corpus. Its format churn (#170, #161, #184)
   is the maintenance we would inherit, not the maintenance we would avoid.

So: port the behaviour, and build the corpus from this machine.

## The seam

`internal/agents` is deliberately split where the data source splits.

| Layer | File | What it needs | Status |
| --- | --- | --- | --- |
| Live process table, ppid tree, CPU ticks, cwd, listening sockets | `proc.go` | procfs (Linux), injectable `Root` | done |
| Discovery, session build, child tree, port attribution, orphan detection | `discover.go` | `Machine` seam, cross-tick state in `Collector` | done |
| Transcript reads: tokens, context, model, title, true state | phases 2 | per-harness readers | open |
| Git branch and dirty counts per session cwd | `git.go` | `Runner` seam | open |
| Quota gauges | `quota.go` | harness state files | open |
| Domain wiring: `DomainSessions`, provider, CLI, TUI tab | `core` + `tui` | the report types above | open |

`Machine.Root` and `Collector.Now` are the only seams needed for phase 1: every test reads a
procfs-shaped fixture in `t.TempDir()` and never this machine.

## Two facts that need more than one read

Both are why `Collector` keeps the previous snapshot, and both match abtop:

- **Executing** is the delta of a session tree's CPU ticks between reads. Accumulated CPU is a
  level, so a finished child would otherwise read as busy forever.
- **Orphan ports** are ports attributed to a session on a previous read whose session process has
  since ended while the socket is still listening. On the first read of a process there are
  none, and `Snapshot.Orphans` says so by being empty.

## Cost model

Chosen to stay cheap on a machine with large histories (this one has 233M of Codex JSONL and a
291M opencode database):

- Enumerate processes and `/proc/net/tcp*` once per tick (a few ms on 3k processes).
- Attribute sockets to pids by walking `/proc/<pid>/fd` rather than shelling out to `lsof`.
- Read transcripts only for live sessions, by byte offset from the last tick (phase 2). Never
  sweep all history: that is what makes a one-shot `abtop --json` take about a second here
  before parsing anything.

## Phases

1. **Landed.** `proc.go` + `discover.go`: sessions with harness, pid, cwd, child tree, ports,
   orphan detection, executing/waiting by CPU delta. Tests read a fake procfs. Verified live on
   this machine: 29 sessions (12 codex, 16 opencode, 1 claude) with correct cwds and children.
2. **Transcripts.** Per-harness readers behind one interface: `claude` and `codex` JSONL by
   offset, `pi` JSONL, `opencode` v2 SQLite (`session_v2.tokens_input`, `cost`, `parent_id`,
   `session_message`). Produce tokens, context used/limit, model, title, and the real state
   (thinking vs waiting) from an open turn. Golden corpus from this machine, redacted; `abtop
   --json` as an oracle while building, never at run time.
3. **Domain wiring.** `DomainSessions` in `core/domains.go`, `SessionsReport`, `SessionProvider`,
   `Providers.Sessions`, `ReadDomains`, `abacus sessions [--json]`. Follows the existing pattern
   exactly, including "not connected" when no procfs is readable.
4. **The tab.** `newSessionsSection` in `internal/tui`: abtop's layout in lipgloss — session
   table with state and context bars, token breakdown, ports/orphans, selected-session detail.
   Golden tests in `internal/tui/testdata/golden/`.

## Non-goals

- Starting, driving or killing an agent. The package is read-only, like every other module.
- Token *cost* in currency. It is derivable once tokens exist; it is not a phase 1 concern.
- Depending on an abtop binary at run time. The sidecar pattern stays a development oracle: it
  would break abacus's one-self-contained-file promise and hand upstream's churn to us.
