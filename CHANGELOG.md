# Changelog

All notable changes to abacus (working name) are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). abacus is pre-1.0: minor versions
may change behavior, and patch versions are fixes and small additions.

## [Unreleased]

### Added

- Release notices: `THIRD_PARTY_NOTICES.md` now says the bundled tools ship unmodified and run as
  subprocesses, and that the `abacus` binary also statically links parts of bv's Go packages (bv
  v0.27.0 `pkg/analysis` and dependencies) under bv's license (`third_party/bv/LICENSE`, verbatim).
  It ends with every Go module linked into the binary and its license text, generated offline by
  `scripts/lib/golicenses.sh` from `go list -deps`.
- The library engine reports a stale export (older than `.beads/*.db` or the dolt directory) as a
  degraded insights header and a Doctor warning, shows the export's age otherwise, never reads an
  export above the tracker directory or through a symlink that leaves it, reports an export over
  64 MiB as `export larger than 64 MiB`, and switches off bv's `BV_DEBUG` stderr logger.
- An opt-in in-process graph engine for the Tasks insights: set `ABACUS_GRAPH_ENGINE=library` and
  abacus runs bv's analysis package inside its own process (new `internal/graph`) over the
  tracker's `.beads/issues.jsonl` export, with no `bv` binary and no subprocess. The default is
  unchanged (the bv subprocess), `--demo` never uses it, and an unknown value keeps the default
  and shows a Doctor warning. Doctor says which engine is active. The report has the same shape,
  so the Tasks tab and JSON output do not change. How the two differ is in
  `docs/tool-contract.md` ("Graph engine"). The assessment is marked "prototype built".
- `scripts/dev-bundle.sh` (`make bundle`, `make dev`): a locally built binary gets abacus's own
  pinned, sha256-verified copies of bd, bv, lambe and beads-lease in `bin/bundled/`, so Doctor shows
  every tool as `bundled` and tabs are no longer "not connected" on a dev machine. It fails closed
  on any hash mismatch, builds in a temp directory and moves it into place, is idempotent and has a
  `--check` mode. `package-release.sh` now also verifies every manifest tool landed in `bundled/`,
  and a test fails when a tool in `bundled-tools.json` lacks an asset for a release platform.
- `abacus doctor` and `abacus version` report "Bundled tools: 4 of 4 verified" (or what is wrong);
  JSON gains `bundled` (version) and `overview.bundled` (doctor) and tool lines gain `bundled_tool`
  and `optional`, all additive (schema_version stays 1). Doctor's header says `Ready` only when bd,
  lambe and beads-lease are usable; bv (graph insights) is optional: it still counts in "Bundled
  tools: N of 4" and is listed as an optional thing to set up, but never blocks `Ready`.
- `scripts/dev-bundle.sh --out DIR` refuses a DIR that is not empty and does not look like its own
  install (it replaces the whole directory), re-checks each kept tool's license and file modes (and
  drops tools the manifest no longer lists), and manifest tool names must match `^[a-z0-9-]+$`.
- `core.HowToConnect` (map) and `core.HowToConnectInsights` (var) are now `core.HowToConnectFor(domain)`
  and `core.HowToConnectInsights()`, computed from the build kind: no shared state, race-free.

### Changed

- A bundled tool abacus cannot use is never "missing" or "install X" to the user. In a release the
  text says abacus's own copy is damaged or was moved, gives the refusal reason when a copy was
  refused, and says to re-extract the archive and keep the folder together; in a dev build
  (`version` is `dev`) it says to run `scripts/dev-bundle.sh`. Optional tools that abacus does not
  ship (Vercel Board CLI, b9s) say they are optional and what they add. Doctor's tool badge reads
  `unavailable` or `optional` instead of `missing`; the raw `where: "missing"` in JSON is unchanged.


- Assessment of bv and b9s as importable Go packages for the Tasks graph insights
  (`docs/plans/2026-11-22-graph-core-assessment.md`): measurements, a decision and the bv license
  rider recorded verbatim. Docs only; insights still come from the bv subprocess. No code or
  dependency change.
- `docs/plans/2026-11-22-board-bundling-assessment.md`: how to ship the Board domain without asking for Node (compiled binary measured, native client judged, trust model, recommendation).
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

- The minimum Go version is now 1.26.11 (it follows `github.com/Dicklesworthstone/beads_viewer`
  v0.27.0, pinned exactly), and lipgloss moves from v1.1.0 to the pseudo-version
  `v1.1.1-0.20250404203927-76690c660834` that bv's module requires, with `x/ansi`,
  `colorprofile`, `displaywidth` and a few smaller modules following. Every TUI golden is
  unchanged. The release binary grows by about 1.8 MB (linux/amd64, 8.39 MB to 10.19 MB).
- A visual pass over the tabs, restrained and meaningful: Tasks opens with a bar of the task states
  in proportion (at most 40 cells; blocked in red, in progress in the accent color, closed quiet),
  status is colored by meaning (blocked red, deferred amber, in progress accent) and only P0 is
  alarmed, Queues states get a mark whose shape differs per state, section headings are accent
  colored, and a person or machine (an agent, a lease holder, a run's machine) has one color on every
  tab. Tints come from a palette that never resembles a state color; a 16-color terminal gets the
  two hues it can tell apart. Each color is also carried by a glyph or word, so nothing depends on color:
  the bar uses a different glyph per state and the dot is a character. Selected rows stay plain on
  the highlight. In `--demo`, Doctor now lists the demo tools as demo instead of missing.
- Internal: Leases, Queues, Board and Tasks share one list-navigation piece (`listNav`: cursor,
  scroll, selected row identity, drill), with direct tests. Selection and key behavior are unchanged
  (Leases and Queues still pin the selection when its row disappears; Board and Tasks follow the
  clamped row). The Tasks section moved to its own file, and the Board builds only the selected
  row's detail text instead of one per row on every render. No output changed.
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
