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

See `docs/tool-contract.md` for what a tool must expose to be composable.
