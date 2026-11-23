# Changelog

All notable changes to abacus (working name) are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). abacus is pre-1.0: minor versions
may change behavior, and patch versions are fixes and small additions.

## [Unreleased]

### Added

- README: supported platforms, verified on Linux amd64 from the release archive (doctor, setup, TUI with only bundled tools). A plan for Windows support is in `docs/plans/`.
- The Board domain: channels, recent threads and messages (author, time, text) and open tasks
  with their state and claimant, read from the Vercel Board CLI (`@vercel/board`, `board` or
  `vb`) as a subprocess behind an argv allowlist and a scrubbed environment. Not found, not set
  up (no board selected, not signed in) and connected are three distinct states, each with the
  exact next step. A `vb` that is not Vercel Board is rejected by its own `--help`.
- `abacus config set board_url URL` and `ABACUS_BOARD_URL` (passed as `--url`), `ABACUS_BOARD_BIN`.
- `abacus board post CHANNEL TEXT` and `abacus board reply ID TEXT` through the plan, confirm,
  single-use and audit gate; the message is re-read by id afterwards.
- Board tab: list with a detail pane, refresh polling only while visible, `o` opens the CLI's
  local web inspector.

### Changed

- Leases, Queues and Doctor are drawn in framed panels with aligned columns. Leases has a cursor
  (up and down), a detail panel beside the list from 100 columns and Enter and Esc to drill in
  below that. Queues keeps its keys, attention block and selection rules, with its run list as a
  table (a stalled column only when a run is stalled; workflow and last event on wide terminals).
  Doctor shows Areas and Tools side by side from 100 columns and one panel per tool, with long
  details wrapped instead of cut. Columns size to their content and drop by priority as the
  terminal narrows; ids and states keep their width (up to 24 and 12 cells for leases), and a short list in a tall terminal is a
  compact box. Output is otherwise the same data; JSON and CLI are unchanged.
- TUI foundations for the redesign: named breakpoints (`bpMin`, `bpCompact`, `bpWide`, `bpUltra`,
  `hMin`, `hRule`, `hSummary`, `hTall`) in one file with unchanged values, new layout helpers
  (`split`, aligned flexing `fitColumns` and `formatRow`, titled rounded `panel`) and panel and focus styles in the
  theme. The help overlay is now a framed panel with aligned keys. Below 40 columns or 12 rows
  the body says "Terminal too small" and what is needed, with the tab bar and footer kept. The
  golden height 10 became 12 (a 10-row terminal now shows the too-small message). While the terminal is too small the hidden tab gets no keys (tab switching, help and quit still work), so a confirm screen the user cannot see can never be answered; growing the window brings it back. Keybindings,
  CLI output and JSON are unchanged.
- Leases tab renders the typed `LeasesReport` (each lease's own state, tone and detail) instead of
  re-shaping it into text lines first. Output and JSON are unchanged.
- Run states are a typed `core.RunState` (pending, running, deferred, settled, failed, canceled; an
  unknown state from a newer supervisor passes through and is shown quietly). The severity
  mapping lives once in core instead of a string switch in the TUI. The JSON `state` stays a plain
  string, so `schema_version` 1 is unchanged. The TUI no longer colors `active`, `error`,
  `blocked`, `waiting`, `retrying` or `paused`, which the supervisor's state API does not report.
- **Breaking, JSON:** the Board report (`abacus board --json` and `status --json` `domains.board`)
  no longer has `columns` or `insights`. `columns` was never populated (no Board provider existed)
  and `insights` was a copy of the Tasks insights; neither ever held Board data, so no consumer
  can have depended on its content, but a consumer that read the keys will not find them. New
  keys `channels`, `threads`, `tasks` and `truncated` are additive, and `schema_version` stays 1.
  The Board domain no longer runs graph insights (bv); they stay on Tasks.
- A board URL (`board_url`, `ABACUS_BOARD_URL`, or the shell's `BOARD_URL`) must be https, or
  http for a local board (localhost, 127.0.0.0/8, ::1), with no query, fragment or credentials.
  `BOARD_URL` is validated and passed as `--url`, never inherited by the CLI.
- The identity probe (`--help`) runs with no tokens; Vercel Board 0.54.0 or newer is required.
- A Board write whose outcome is unclear (timeout, cancel, network error, unreadable answer) is
  reported as "result unknown", and its idempotency key is derived from (board, op, destination,
  text, 10-minute window), so retrying after an unknown outcome posts at most once. The same
  text to the same place within ten minutes is treated as that retry.
- The write confirm shows the board (and where the URL came from) and, for a reply, the message
  it answers; for a post, whether the channel is new. The `[y/N]` prompt goes to stderr.
- `o` on the Board no longer hands off to b9s (b9s stays on Tasks).

## [0.2.0] - 2026-11-22

Works out of the box: a self-contained archive and guided first-run setup.

### Added

- CI on every pull request and push to main (gofmt, vet, shellcheck, tests), and a release
  workflow that builds the four archives from a version tag. The release workflow needs the
  `ABACUS_RELEASE_TOKEN` secret to read the private beads-lease release.
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
