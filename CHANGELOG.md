# Changelog

All notable changes to abacus (working name) are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). abacus is pre-1.0: minor versions
may change behavior, and patch versions are fixes and small additions.

## [Unreleased]

### Added

- Assessment of bv and b9s as importable Go packages for the Tasks graph insights
  (`docs/plans/2026-11-22-graph-core-assessment.md`): measurements, a decision and the bv license
  rider recorded verbatim. Docs only; insights still come from the bv subprocess. No code or
  dependency change.
- `docs/plans/2026-11-22-board-bundling-assessment.md`: how to ship the Board domain without asking for Node (compiled binary measured, native client judged, trust model, recommendation).
- An optional workspace layer. `internal/core` gains a `WorkspaceProvider` capability (not a fifth
  domain; navigation stays four tabs plus Doctor): typed placements saying where a run, lease,
  task or queue row lives as a terminal session, and an open plan (an ordered list of argv rungs
  plus a command to copy) that core never runs. Unknown kinds pass through, text from the
  workspace is sanitized and bounded, and no provider is an empty, non-error result. `status
  --json` gains a `workspace` section only when there is something to report (additive;
  `schema_version` stays 1, and the output is byte-identical without a provider).
- `internal/rex`, the first workspace provider, composes only the `rex` CLI: `ls --json` and
  `version --json` (always `--autostart=false`) behind an argv allowlist and a scrubbed
  environment, gated on API revision overlap (compatible iff `api_revision >= 10` and
  `api_min_revision <= 13`). Placement comes from session labels (conventions are data; `bd-1`
  does not match `bd-1.2`), a label two sessions claim is reported as ambiguous and never picked.
  Only this machine and hosts in `ABACUS_REX_HOSTS` are ever contacted (`-S` can open an ssh
  connection; another machine gets a copy-only "host-not-configured" placement), the environment
  passed on is `HOME`, `PATH`, the XDG dirs and `REX_*` (`ABACUS_REX_PASS_ENV` opts in to more),
  and an unrecognized `ls` shape is a degraded read, never an empty one. No server is not an
  error; an unsupported revision or a failed probe is a Doctor warning, never a failure, and an
  absent Rex does not appear in Doctor or the tool list. `ABACUS_REX_BIN` overrides the lookup.
  The open plan (in `status --json` as `open` on every placement, with `rungs` always a list and
  an `interactive` flag) is the existing `rex -S <machine>` handoff, then the command to copy;
  richer jumps are deferred until verified against a real Rex. The workspace read overlaps the
  other reads with a one second budget, and the adapter caches per-host results for 3 s.
- `tui --demo` has fixture placements (including an ambiguous one) from the demo Leases provider.
  The TUI does not show the workspace yet.
- `abacus tui --demo` shows every tab with invented data: Board, Tasks (with graph insights and the
  confirm flow), Leases and Queues, plus Doctor. The fixtures live in `internal/demo` (core types
  only) and are the same ones the TUI goldens use. A demo session reads nothing from your machine,
  a demo "write" is applied to nothing, and nothing is recorded in the real audit log.
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

- `core.Sanitize` now also drops every Unicode format character (word joiner, soft hyphen, tag
  characters and the like) and the blank Hangul fillers, not only bidi controls and zero-width
  characters. `core.ShellJoin` now quotes words containing `=` or `%`.
- A visual pass over the tabs, restrained and meaningful: Tasks opens with a bar of the task states
  in proportion (at most 40 cells; blocked in red, in progress in the accent color, closed quiet),
  status is colored by meaning (blocked red, deferred amber, in progress accent) and only P0 is
  alarmed, Queues states get a mark whose shape differs per state, section headings are accent
  colored, and a person or machine (an agent, a lease holder, a run's machine) has one color on every
  tab. Tints come from a palette that never resembles a state color; a 16-color terminal gets the
  two hues it can tell apart. Each color is also carried by a glyph or word, so nothing depends on color:
  the bar uses a different glyph per state and the dot is a character. Selected rows stay plain on
  the highlight. In `--demo`, Doctor now lists the demo tools as demo instead of missing.
- Tasks and Board are drawn in framed panels with aligned columns. Tasks shows the counts and an
  aligned task table in a list panel and the graph insights in their own panel, beside the list from
  100 columns and under it below that. Board shows the summary, channels and aligned thread and task
  tables in a list panel with the selected message or task in a detail panel beside it from 100
  columns (Enter and Esc drill in below that). Selection still follows the thread or task across
  refreshes. The forms, the confirm screen and every write path are unchanged. The README sample
  is regenerated and now ends at a panel border.
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
