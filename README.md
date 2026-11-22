# abacus (working name)

One terminal tool for the Beads tool family: it detects which tools are installed, runs
`doctor` and `status` across them, and shows one TUI section per detected tool. Tools are
composed only through their `--json` CLIs and versioned local APIs.

> **Name status:** `abacus` is a placeholder. The final name and brand are undecided. The
> repo is private until then. The name lives in `internal/brand`, the module path in
> `go.mod`, and the `cmd/` directory name.

## Architecture

`internal/core` holds all the real logic: the module seam, detection, doctor and status
aggregation, the typed report JSON (`schema_version`), sanitizing and the exit-code rules.
`internal/lambe` (CLI, read-only state API and a labelled tracker-only fallback) and `internal/beadslease` (read-only leases and doctor) are
core adapters. `internal/cli` and `internal/tui` are thin front-ends that only parse
input and render core's typed reports; a Linux or Swift GUI would be another front-end over
the same core or the same `--json` output. A test enforces that core, adapters and the CLI
import no UI library.

## Development rules

- Develop and test against an isolated `HOME`, `XDG_*` dirs and `bd` database. Never touch a
  production Lambe daemon, its state, its binary or real Beads data.
- Nothing here launches an app or takes window focus. Tests use fakes for Lambe, HTTP and Rex.
- Use a private `GOCACHE` for builds.

Note: the two `signalZero` bodies under `internal/lambe` are kept separate on purpose; they port
Lambe's own process-liveness logic per platform, and merging them would drift from it.

License: GPL-3.0-or-later, matching Lambe.

## Usage

```
abacus version [--json]
abacus doctor  [--json] [--lambe-config FILE]
abacus status  [--json] [--lambe-config FILE]
abacus tui     [--demo]
```

Exit codes: 0 healthy, 1 a check failed or a tool is broken, 2 usage error. A tool that is not
installed is reported by `version` and omitted from `doctor` and `status`. Overrides:
`ABACUS_LAMBE_BIN`, `ABACUS_LAMBE_STATE_DIR`, `ABACUS_LAMBE_CONFIG`, `ABACUS_BEADSLEASE_BIN`.
beads-lease reads need your `BEADS_ACTOR` set, as they do for beads-lease itself.
Without a live Lambe daemon, `status` falls back to a read-only tracker subset, labelled
`tracker subset`; without `ABACUS_LAMBE_STATE_DIR`, the state dir comes from `lambe state-dir`.

### TUI

`abacus tui` has one tab per detected tool plus a Doctor tab. Below 100 columns the list fills
the screen and Enter drills into a run; at 100 and wider the run detail sits beside the list.
This is the 80-column render with colors off (`NO_COLOR`), from the golden test: Lambe demo
data plus a fixture beads-lease tab and a fixture broken tool:

```
abacus   [Lambe]  beads-lease  ghost (broken)  Doctor
────────────────────────────────────────────────────────────────────────────────
Runs 3
▸ T-1     running  gating
  T-2     deferred  deferred
  T-3     running  running  (stalled)

Queue 2
  Q-1  ready  ready task
  Q-2  (blocked)  blocked task

Needs attention 1
  T-2  gate_failed  gh:pr rejected: merge conflict
? help · q quit · tab/→ next tab · ↑/↓ move · enter details · / filter …
```

| Key | Action |
| --- | --- |
| `tab` / `shift+tab`, `left` / `right`, `1`-`9` | switch tab (dimmed tabs are skipped) |
| `j` / `k`, `up` / `down` | move |
| `enter` / `esc` | open / close run detail (narrow layout) |
| `/` | filter runs (`enter` keeps, `esc` clears) |
| `o` | open the selected run's machine in Rex |
| `r` | re-run doctor (Doctor tab) |
| `?` | toggle the full key help |
| `q`, `ctrl+c` | quit |

Layout, theming and `NO_COLOR` are described in `docs/tui.md`.

See `docs/tool-contract.md` for what a tool must expose to be composable.
