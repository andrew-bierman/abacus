# abacus (working name)

The fleet cockpit for Beads-powered agent fleets. See the board, the tasks, the leases and the
Symphony queues, and change the tasks that are safe to change, from one terminal tool. abacus
does not browse issues itself and does not wrap tools: it shows what your fleet is doing, and
the tools that supply the data are providers behind each view (see Providers).

> **Name status:** `abacus` is a placeholder. The final name and brand are undecided. The
> repo is private until then. The name lives in `internal/brand`, the module path in
> `go.mod`, and the `cmd/` directory name.

## Domains

| Domain | What you see | What you can change today |
| --- | --- | --- |
| Board | the board and epic lanes | nothing yet: not connected until a Board contract exists |
| Tasks | counts by state, ready work, recent changes, graph insights (next pick, blockers, critical path, cycles) | create, update, close (with a confirm step) |
| Leases | held, stale and free leases | read only |
| Queues | runs, the queue, what needs attention, history | read only |

A domain with no provider shows "not connected" and how to connect; it never disappears.
`abacus tasks|leases|queues|board [--json]` print the same typed reports the TUI renders.

## Architecture

`internal/core` holds all the real logic: the domain reports (Board, Tasks, Leases, Queues),
provider interfaces and the registry that maps detected providers to domains, the write gate,
detection, doctor and status aggregation, the typed report JSON (`schema_version`), sanitizing and the exit-code rules.
Adapters (`internal/lambe`, `internal/beadslease`, `internal/bd`, `internal/bv`) compose the
tools only through their `--json` CLIs and versioned local APIs. `internal/cli` and `internal/tui` are thin front-ends that only parse
input and render core's typed reports; a Linux or Swift GUI would be another front-end over
the same core or the same `--json` output. A test enforces that core, adapters and the CLI
import no UI library.

## Providers

Providers are invisible in navigation and appear only here, in Doctor and in "how to connect"
or error messages that help you fix one.

- **Beads (`bd`)**: Tasks reads and writes. Reads always run `--readonly`; writes go through a
  separate allowlist and the confirm step.
- **beads-lease**: Leases (read only).
- **Lambe**: Queues (read only; demo and a tracker-only fallback exist).
- **bv**: graph insights for Tasks and Board, run as a subprocess in `--robot-*` JSON mode only.
  Not installed? Tasks says so and how to install it.
- **b9s**: works with b9s for the full board, tree and graph view: press `o` on Tasks or Board to
  open it in the tracker directory (probes for it first; shows an install hint if it is missing).
- **Board**: no provider yet (the Board contract is an open decision upstream).

## Credits & ecosystem

- [Beads](https://github.com/gastownhall/beads) (`bd`) is the tracker everything sits on.
- [beads_viewer](https://github.com/Dicklesworthstone/beads_viewer) (`bv`) by Jeffrey Emanuel
  supplies the graph analysis; abacus only runs the `bv` binary and imports none of its code. See
  its [license](https://github.com/Dicklesworthstone/beads_viewer/blob/main/LICENSE).
- [b9s](https://github.com/vanderheijden86/b9s) (MIT, by another author) is the full interactive
  board and tree view abacus hands off to.
- [k9s](https://k9scli.io) inspired the keyboard-first, terminal-native feel.

## Development rules

- Develop and test against an isolated `HOME`, `XDG_*` dirs and `bd` database. Never touch a
  production Lambe daemon, its state, its binary or real Beads data.
- Nothing here launches an app or takes window focus. Tests use fakes for Lambe, HTTP and Rex; the only real
  tools tests run are `bd` and `bv`, against temp projects with an isolated `HOME`, and they skip when absent.
- Use a private `GOCACHE` for builds.

Note: the two `signalZero` bodies under `internal/lambe` are kept separate on purpose; they port
Lambe's own process-liveness logic per platform, and merging them would drift from it.

License: GPL-3.0-or-later, matching Lambe.

## Usage

```
abacus board|tasks|leases|queues [--json]
abacus tasks create [--title T] [--description D] [--priority N] [--type X] [--yes] [--dry-run]
abacus tasks update ID [--title T] [--description D] [--priority N] [--status S] [--yes] [--dry-run]
abacus tasks close  ID [--reason R] [--yes] [--dry-run]
abacus tui     [--demo]
abacus status  [--json] [--lambe-config FILE]     domains plus provider state
abacus doctor  [--json] [--lambe-config FILE]
abacus version [--json]
```

Exit codes: 0 healthy, 1 a check failed, a provider is broken or a write was not applied, 2 usage
error. `status --json` and `doctor --json` keep `schema_version` 1; `status --json` gains an
additive `domains` section. Every read command's JSON carries `schema_version`.

Overrides: `ABACUS_BD_DIR` (the tracker directory, passed as `bd -C`), `ABACUS_BD_BIN`,
`ABACUS_BV_BIN`, `ABACUS_LAMBE_BIN`, `ABACUS_LAMBE_STATE_DIR`, `ABACUS_LAMBE_CONFIG`,
`ABACUS_BEADSLEASE_BIN`. Leases need your `BEADS_ACTOR` set.

### Writes

Only Tasks can be changed. Every change is planned first (validated, with the exact command), then
needs an explicit yes: `y` on the TUI confirm screen, or the `[y/N]` prompt (`--yes` skips it,
`--dry-run` prints the change and the command and runs nothing). A change can be applied once, every
attempt is appended to an audit log (`$XDG_STATE_HOME/abacus/audit.jsonl`), and if the log cannot be
written nothing runs. Writes are limited by an allowlist (create, update, close; no delete).
Leases, Queues and Board writes are blocked upstream: see `docs/plans/2026-11-21-1700-feat-fleet-domains-plan.md`.

### TUI

`abacus tui` has one tab per domain (Board, Tasks, Leases, Queues) plus Doctor. In Queues, below 100
columns the list fills the screen and Enter drills into a run; at 100 and wider the run detail sits
beside the list. This is the 80-column Tasks render with colors off (`NO_COLOR`), from the golden test,
using data from fixture providers:

```
abacus   Board  [Tasks]  Leases  Queues  Doctor
────────────────────────────────────────────────────────────────────────────────
Tasks
14 tasks: 6 open · 3 in progress · 2 blocked · 1 deferred · 2 closed
ready 4

Recent changes
▸ ac-12         in_progress  P1  wire the lease heartbeat
  ac-09         open  P2  queue attention digest
  ac-03         closed  P3  retire the old poller

Insights (graph analysis)
  4 actionable · 2 blocked · 1 cycles
  next     ac-12  wire the lease heartbeat  (unblocks 2)
  blocker  ac-12  wire the lease heartbeat  (unblocks 2)
  critical path  ac-12 > ac-09
  cycle  ac-20 -> ac-21 -> ac-20
? help · q quit · tab/→ next tab · ↑/↓ move · n new · e edit · x close task …
```