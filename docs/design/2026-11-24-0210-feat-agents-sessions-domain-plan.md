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

## Libraries

The default is to use one, and to write only what no library does. This was checked against
the source, not the READMEs.

| Need | Library | Why it wins |
| --- | --- | --- |
| Process table, ppid tree, cmdline, cwd, CPU, RSS | `shirou/gopsutil/v4` | Per-platform detail already written: procfs, libproc, toolhelp. `net.Connections` returns listening sockets with the owning pid, replacing an `/proc/<pid>/fd` walk. |
| opencode v2 session store | `modernc.org/sqlite` | Pure Go, no CGO, so the one-binary promise holds. Reads 4 recent sessions in 1ms. |
| Session list, port table | `charmbracelet/bubbles/table` | Already a dependency. |
| Context and quota bars | `bubbles/progress` | Already a dependency. |
| Token-rate and per-turn sparklines | `bubbles/sparkline` | Already a dependency. |
| Selected-session detail scrolling | `bubbles/viewport` | Already a dependency. |
| Panels, colour, layout | `lipgloss` (in the tree) | Same. |

Written here, because nothing covers it:

- **opencode v2 reads.** `ferry` requires `…/storage/session/<project>/`, which this install
  does not have, and `moirai` queries the v1 tables (`session`, `message`, `part`). Neither
  knows `session_v2` / `session_message` / `session_part`. About 150 lines against a schema we
  can read.
- **Live process state.** No Go library in this space reads which sessions are running, what
  they spawned, or which ports they hold. This is the half of abtop that is not a parser.
- **Orphan detection.** Needs cross-tick history; it is a rule, not a read.

## The seam

`internal/agents` is deliberately split where the data source splits.

| Layer | File | What it needs | Status |
| --- | --- | --- | --- |
| Live state behind a seam: Procs, CWD, Listeners | `proc.go` | `Machine` interface | done |
| gopsutil implementation of that seam | `gopsutil.go` | `gopsutil/v4` | done |
| Discovery, session build, child tree, port attribution, orphan detection | `discover.go` | `Machine` seam, cross-tick state in `Collector` | done |
| Transcript reads: tokens, context, model, title, true state | phases 2 | per-harness readers | open |
| Git branch and dirty counts per session cwd | `git.go` | `Runner` seam | open |
| Quota gauges | `quota.go` | harness state files | open |
| Domain wiring: `DomainSessions`, provider, CLI, TUI tab | `core` + `tui` | the report types above | open |

`Machine` is the only seam the Collector needs for live state: tests supply a fixed process
table and never read this machine.

Measured: one snapshot of the 29 sessions live on this machine takes 150ms.


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

## Progress

The loop works the first unchecked item. Tick one per pass.

- [x] `proc.go` + `gopsutil.go`: live state behind a `Machine` seam
- [x] `discover.go`: sessions, child trees, ports, orphans, executing/waiting
- [x] `opencode.go`: opencode v2 SQLite reader (tokens, model, title, parent, state)
- [ ] `claude.go`: Claude Code JSONL reader, incremental by offset
- [ ] `codex.go`: Codex JSONL rollout reader, incremental by offset
- [ ] `pi.go`: Pi JSONL reader, incremental by offset
- [ ] `git.go`: branch and dirty counts per session cwd
- [ ] `quota.go`: Claude and Codex quota gauges
- [ ] `core`: `DomainSessions`, `SessionsReport`, `SessionProvider`, `Providers.Sessions`
- [ ] `cli`: `abacus sessions [--json]`
- [ ] `tui`: sessions tab (table, context bars, token sparklines, ports, detail viewport)
- [ ] `tui`: golden tests for the sessions tab
- [ ] shipping: push the branch and open the pull request

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
4. **The tab.** `newSessionsSection` in `internal/tui`, abtop's layout in abacus's existing
   widget set. No new UI dependency: bubbles already ships the four things abtop hand-rolled.

   | abtop panel | abacus tab |
   | --- | --- |
   | Header: host CPU/mem/load | `lipgloss` row over `gopsutil` host info |
   | Context: 200-point token-rate sparkline + per-session % bars | `bubbles/sparkline` + `bubbles/progress` |
   | Quota: 5h/7d gauges with reset countdown | `bubbles/progress` + a lipgloss countdown |
   | Tokens: totals + per-turn sparkline | `bubbles/sparkline` |
   | Projects: branch + dirty counts | phase 2 `git.go` |
   | Ports: table incl. orphans | `bubbles/table` |
   | Sessions: list + selected detail | `bubbles/table` or the existing tasks-list pattern, detail in `bubbles/viewport` |

   Golden tests in `internal/tui/testdata/golden/`, matching the other domains, with invented
   names per `956fbb4`.

## Fixtures

Synthetic, never captured. `956fbb4` already established that fixtures use invented names, so
a redacted real corpus still does not belong in the repo. Each harness reader is tested against
a store built in the test — for opencode, a SQLite database created with the real `session_v2`
schema and invented rows. A live compare against `abtop --json` is a local, uncommitted oracle
while building, never a run-time dependency.


## Non-goals

- Starting, driving or killing an agent. The package is read-only, like every other module.
- Token *cost* in currency. It is derivable once tokens exist; it is not a phase 1 concern.
- Depending on an abtop binary at run time. The sidecar pattern stays a development oracle: it
  would break abacus's one-self-contained-file promise and hand upstream's churn to us.
