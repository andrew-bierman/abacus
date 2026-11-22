# Changelog

All notable changes to abacus (working name) are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). abacus is pre-1.0: minor versions
may change behavior, and patch versions are fixes and small additions.

## [Unreleased]

### Added

- Works out of the box: the release archive bundles `bd`, `bv`, Lambe and beads-lease as
  sidecar binaries in `bundled/` (pinned and sha256-verified in `bundled-tools.json`, built by
  `scripts/package-release.sh`, refreshed with `scripts/update-bundled-tools.sh`), with their
  licenses in `third_party/` and `THIRD_PARTY_NOTICES.md`. abacus looks for a tool next to its own
  executable, then `../libexec/abacus/`, then `bundled/`, then `PATH`; an `ABACUS_*_BIN` override
  still wins. A bundled tool is run only if it is a regular, executable, non-world-writable file.
- First-run setup, never silent: Tasks offers to create a managed fleet workspace (`bd init`
  through the write gate, with a new narrow `init` verb), Leases offers an actor name, Queues
  offers to write an abacus-owned Lambe config (`lambe init`, no daemon started). Each is a
  confirm screen with the exact command and an audit entry. `abacus setup [--json] [--yes]
  [--dry-run]` does the same from the command line, and `abacus config show|get|set|path` edits
  `$XDG_CONFIG_HOME/abacus/config.json` (`actor`, `tracker_dir`, `lambe_config`).
- Doctor leads with "Ready" or "Needs attention", one line per area, and where each tool was
  found (bundled, PATH, override, missing) with its version; `doctor --json` gains an
  additive `overview`.

### Changed

- An empty tracker reads "no tasks yet" in Insights instead of a bv failure, and bv is not run.
  A bv failure on a tracker with tasks is one plain line; the reason moved to Doctor.
- Empty or unconfigured domains say so in one plain line each; the Board no longer shows
  implementation details (they stay in the README and Doctor).
- Child tools run with the sidecar directories first on `PATH`, so bv finds the bundled bd.

## [0.1.0] - 2026-11-22

First release: one cockpit for Beads-powered agent fleets.

### Added

- Domain-first TUI (`abacus tui`, `--demo` for fixtures): Board, Tasks, Leases, Queues and
  Doctor, with an adaptive light/dark theme, a width-responsive layout (side by side from
  100 columns, stacked below), `/` filter, `?` help and `NO_COLOR` support. Domains with no
  provider show how to connect instead of disappearing.
- Read commands: `abacus tasks|leases|queues|board [--json]`, plus `status`, `doctor` and
  `version` (JSON carries `schema_version` 1; `status --json` adds a `domains` section).
- Tasks writes through `bd` only: `abacus tasks create|update|close` and `n`/`e`/`x` in the
  TUI. Every write is an explicit plan with a confirm step, applied once, with a fail-closed
  audit log, `--dry-run` and `--yes`. A timeout reports "result unknown" rather than "not
  applied".
- Graph insights (ready work, blockers, critical path, cycles) from beads_viewer, run as a
  separate read-only process; absent tools degrade to a "not connected" hint.
- "Open in full view" handoff to b9s on an explicit keypress.
- Providers detected through their own CLIs and APIs: Lambe (queues, runs, attention,
  history), beads-lease (leases, doctor), `bd` (tasks) and `bv` (insights).

### Security

- Read and write allowlists per provider, matched argument by argument; no shell; text from
  tools is sanitized, including bidi and zero-width characters, before display.

### Known limits

- Never run against a real fleet by the maintainers' tests: only fakes, fixtures and
  isolated temporary trackers.
- The TUI has not been reviewed on every terminal; goldens are rendered through a fixed
  color profile.
- Leases, Queues and Board writes are blocked on upstream surfaces (see the README).
