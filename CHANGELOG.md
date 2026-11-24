# Changelog

All notable changes to abacus are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). abacus is pre-1.0: minor versions
may change behavior, and patch versions are fixes and small additions.

## [Unreleased]

### Added

- `abacus sessions [--json]`: the AI coding agents live on this machine (Claude Code, Codex,
  opencode and pi) with their state, tokens, context, child processes, listening ports and account
  quota, joined from the process table and each harness's own store. Read only. A one-shot command
  drains a long transcript that a tab would finish over later ticks.
- Open-source readiness: `SECURITY.md` (private GitHub advisories), `CONTRIBUTING.md`,
  `CODE_OF_CONDUCT.md` (Contributor Covenant 2.1), issue and pull request templates, `CODEOWNERS`,
  Dependabot config, `.editorconfig`, `docs/README.md` (index), `make help` and `make cover`.
  Doc comments on the main exported identifiers in `internal/core`, `internal/lambe`, `internal/board`.

### Changed

- The internal planning notes moved from `docs/plans/` to `docs/design/` (history kept).
- Demo and fixture data no longer carries real-looking host, user and task names (invented ones
  instead); no behavior change.
- The release workflow installs the bun version `bundled-tools.json` pins before packaging (it was
  missing on the runner; the workflow has not run end to end yet).

## [0.4.0] - 2026-11-23

First release since 0.2.0. The 0.3.0 section below was never tagged; its changes ship here.

### Added

- Command prompt and k9s-style layer (U6 of `docs/design/2026-11-23-feat-tasks-workspace-plan.md`):
  `:` opens a prompt in the footer row with aliases (`:tasks :board :leases :queues :doctor`,
  `:list :tree :kanban :graph :insights`, `:search TEXT`, `:filter QUERY`, `:ctx`, `:help`, `:q`),
  tab completion (commands, then query fields and values), history, and a closest-command
  suggestion for typos. It only navigates, filters and describes; it can never write or run a tool.
  `d` describes the selected task and `y` shows it as the typed JSON `tasks show --json` prints
  (read-only panels). From 100 columns the tab bar shows the tracker, task counts and board target
  on its right; the cursor row fill is brighter; `?` lists the prompt commands and scrolls. The
  command table, parse, completion and closest match are pure code in `internal/core/commands.go`.
- Tasks goes live (U10 of `docs/design/2026-11-23-feat-tasks-workspace-plan.md`): the 30-second poll
  is replaced by a cheap check about every 5 seconds (the embedded store's manifest file, which moves
  on every write and never on a read) and the tracker is read in full only when it moved; one check
  or read at a time, none while the tab is hidden, a longer wait after a slow read. A change by
  someone else keeps the cursor on its task, adds a `● N changed just now` marker to the panel title,
  and `F` follows the most recently changed task. `H` shows a task's history (who changed what, newest
  first) in its detail, and `abacus tasks history ID [--limit N] [--json]` prints it. `ctrl+s` and
  `ctrl+o` save and apply named queries (`saved-filters.json` in the abacus config directory), and
  `ctrl+p` and `ctrl+n` recall recent `/` queries. `P` switches tracker among the ones you opened,
  through a confirm screen that saves `tracker_dir`, audits it and re-points every Tasks view; no
  tracker is written. New: `internal/taskview` fingerprint, diff, poll scheduler and history parser,
  `core.SavedFilters`, `core.RecentTrackers`, `core.Switcher`, with fixtures captured from real `bd`
  1.3.4 output.
- Dependency graph view in Tasks (U5 of `docs/design/2026-11-23-feat-tasks-workspace-plan.md`): `g` on
  a task (or `v`) shows what blocks it and what it blocks, as boxes with connectors from 100 columns
  and an indented outline below; `g` again shows the whole graph in topological layers with the
  longest chain of unfinished work, cycles, self-dependencies and missing blockers marked. `[` `]`
  depth, `h` `l` siblings or layers, `enter` refocuses or opens the task in the list. The layout is
  pure and deterministic (`internal/taskview/graphlayout.go`, typed `core.GraphView`); the engine's
  bottlenecks are marked on the nodes. `abacus tasks graph [ID | --whole] [--json]` prints the same
  view. The Tasks view strip is now `List Graph Insights`.

- **abacus is one self-contained binary.** bd, Lambe, beads-lease and the Vercel Board CLI are built
  into it (pinned and sha256-verified at build time, zstd-compressed with `-tags embedtools`) and
  unpacked on first use into a private per-user folder (`$XDG_DATA_HOME/abacus/tools/<content
  hash>/`, mode 0700; `ABACUS_TOOLS_DIR` chooses another). The user never installs, fetches or runs
  a script. One resolver in core: an `ABACUS_*_BIN` override, then the built-in copy, then (development
  builds only) sidecar folders and `PATH`. A binary with its tools built in never runs another
  version from `PATH`. Extraction is atomic and locked, refuses links and files that are not private
  to the user, and fails closed on a hash mismatch. Doctor shows each tool as `built in` with its
  version; a failure says what failed, the path and the one fix (a folder to write to, or download
  abacus again).
- The Board CLI is built from its npm package at a pinned integrity (`scripts/board/package-lock.json`,
  `npm ci --ignore-scripts`, then `bun build --compile` per platform) and shipped inside the binary;
  the sha256 of each compiled output is recorded in `EMBEDDED_TOOLS.md` next to the release archives.
- Review fixes: other versions are pruned only under non-blocking locks, by last use (14 days), keeping
  the newest two; a built-in path is re-checked before every run and unpacked again if it vanished;
  interrupted or killed extractions leave nothing; failures are remembered per run and a full disk is a
  write failure; a tools folder under a directory others can write to is refused; a start waits at most
  30 s for another abacus and prints "Preparing built-in tools (first run only)…"; the Board CLI is
  unpacked on first use; a program the system refuses to start gets a plain message with the folder and
  the fix; the bun runtime inside the Board CLI is the sha256-pinned official release.
- `abacus licenses` prints the third-party notices and every license text carried in the binary.
- `make build` (embedded), `make build-dev`, `make release VERSION=x`, `make check-tools`;
  `scripts/embed-tools.sh` replaces `scripts/dev-bundle.sh`.

- Tasks is a real work browser (U1 of `docs/design/2026-11-23-feat-tasks-workspace-plan.md`): every
  task listed (no 8-row cap) with a full-row cursor, status mark, priority, blocked-by and blocks
  marks, type, age, assignee and (from 140 columns) labels; a detail pane with the description,
  comments, what blocks the task and what it blocks, and children with a progress bar; eight stable
  sorts (`s`, `S`); quick filters (`f`: open, ready, blocked, closed, all); a live `/` query
  (`status:open label:ui !assignee:none priority<=1 updated>7d ...`) that `enter` keeps and `esc`
  clears, with typed errors that name the column; Insights as a second view (`v`). New
  `abacus tasks list` and `abacus tasks show ID` print the same typed reports (`--json`,
  `schema_version` 1). Everything derives from one read of the tracker (`bd --readonly export`, a
  fixed read-only argv), so the list, detail and counts agree on what is blocked and ready. New
  packages `internal/query` (the shared filter grammar) and `internal/taskview` (snapshot parse,
  derive, sort, list, detail), with fixtures captured from real `bd` 1.3.4 output. Read only: no
  write path changed.

- Global search (U3 of the same plan): `ctrl+k` opens a live search over Tasks, Board (threads,
  replies, tasks), Leases and Queues (runs, queue, attention, history) with the Tasks query grammar,
  results grouped by domain (20 each), `enter` to jump to the owning tab with the row selected,
  stale-read marks, plain-language not-connected and loading lines, and a history of the last 100
  queries in the abacus state directory. `abacus search QUERY [--domain] [--limit] [--json]` prints
  the same hits (`schema_version` 1, exit codes 0/1/2). The core `SearchIndex` is built only from
  the reports the tabs already hold, so search runs no tool of its own. `queues --json` runs gain an
  additive `workflow` field. `expires` durations point ahead (`expires>2h` is more than two hours from now);
  `search_history` is a new config key (`off` keeps no query history).

- Tasks tree (U2 of the same plan): when a tracker has parent-child links the Tasks list opens as a
  foldable tree of epics and children (`T` switches to the flat list; `h` `l` fold and unfold, `+`
  `-` expand and fold all), with branch lines, a progress bar of closed children on each epic row, the
  ancestors of a filter match kept visible and dimmed, and the selection kept on the same task through
  refreshes, filters and resizes. Fold choices are kept per tracker in abacus's state directory, never
  in the tracker. `abacus tasks list --tree` prints the same typed report (`--json`, `schema_version` 1).
  Orphans, cycles of parent links, duplicate ids and deep nesting are handled and tested; fixtures
  come from a real `bd` 1.3.4 tracker with nested epics. Read only.

- Tasks Kanban view (U4 of the same plan; `v` after List): columns by stored status with card,
  urgent and held counts, closed folded to a count (`Z` shows it), regrouping by priority or type
  (`s`), a derived-blocked badge on the card (never a column), keyboard-first (`h` `l` between
  columns, `j` `k` between cards, `H` `L` moves the card), side by side from 100 columns and a tab
  strip below. Moving a card is a gated write: plan, re-read, confirm screen with the exact command,
  single-use apply, audit before and after. New guarded verb `bd update ID --json --status=S
  --if-status=OLD` (the tracker refuses with exit 13 if the status changed; reported as "changed
  since you opened it"), `bd reopen` for the way out of closed, and a re-read after the write. New
  `abacus tasks board [--json]` prints the same typed report. Core owns the board and the pure move
  planner (`internal/taskview`).

### Changed

- A status change now always names the status the task has (`--if-status`): the TUI edit form and
  `abacus tasks update --status` read the task first and send it, and the write allowlist refuses a
  status update without that guard (and `--force`). `abacus tasks update --status` therefore needs a
  readable tracker. The view strip is now List, Kanban, Insights (`V` from List still reaches Insights).
- The release archive is `abacus`, `LICENSE`, `THIRD_PARTY_NOTICES.md` and `third_party/<tool>/LICENSE`;
  the `bundled/` folder and the shipped manifest are gone. The binary is about 87 MB (darwin-arm64) because
  the tools are inside it.
- No message tells the user to install a tool, run a script or put anything on `PATH` (a tripwire test
  scans doctor, status, setup, every domain command and every TUI render). A plain `go build` says
  "Development build: tools are not built in; build with make build" once, in Doctor only. The Board tab
  says how to point it at a board, never how to install anything. The Board CLI is looked up as `board`
  only; the unrelated npm package `vb` is never run.

- abacus no longer says it has no issue browser: the Tasks tab is its own list and detail view. The
  summary counts and the graph insights moved from beside the list to a second view (`v`), the
  `o` handoff to b9s stays as an optional extra, and the demo tracker is now fourteen tasks with an
  epic, a blocking chain and a cycle (the demo summary's ready count is 3).

## [0.3.0] - 2026-11-22

The headline changes, in plain words:

- **Self-sufficient builds:** abacus ships its own pinned, checksum-verified copies of the tools it
  uses, and `make dev` gives a local build the same set. A missing tool is abacus's problem to
  report, never the user's.
- **A framed, responsive TUI** on every tab, form and confirm screen, from 40 to 140+ columns, with
  a restrained visual language that never depends on color alone.
- **A demo** (`abacus tui --demo`) that shows every tab with invented data and never touches a real
  fleet.
- **Optional Rex workspace layer** (core, adapter and TUI): where each item lives, and a gated jump
  that runs only one validated step on an explicit keypress.
- **Optional in-process graph engine** (`ABACUS_GRAPH_ENGINE=library`) using bv's analysis as a Go
  package. The default is unchanged. Building now needs Go 1.26.11 or newer.
- **Safer confirms:** a confirm that does not fit on screen cannot be applied, and the command shown
  is always the command that runs.

### Added

- The TUI shows the optional workspace layer: the Queues, Leases and Tasks detail shows where the
  selected item lives (session and machine, match, ambiguity with candidates, not placed, cached or
  truncated reads), and at 140 columns and wider the lists get an aligned `where` column. The new
  `w` key on Leases and Tasks (and `o` on Queues for a run the workspace knows) runs only the first
  interactive step of core's open plan, on that keypress, when the placement is unambiguous;
  otherwise it shows a framed notice with the candidates or the command to copy. The key is absent
  without workspace data or a placement. A demo session never runs a step (this also stops `o` on
  an unplaced demo run from handing over the terminal). The demo workspace now places task ac-09.
  See `docs/tui.md`. Without workspace data every screen is unchanged.
- Release notices: `THIRD_PARTY_NOTICES.md` now says the bundled tools ship unmodified and run as
  subprocesses, and that the `abacus` binary also statically links parts of bv's Go packages (bv
  v0.27.0 `pkg/analysis` and dependencies) under bv's license (`third_party/bv/LICENSE`, verbatim).
  It ends with every Go module linked into the binary and its license text, generated offline by
  `scripts/lib/golicenses.sh` from `go list -deps`.
- The library engine reports a stale export (older than `.beads/*.db` or the dolt directory) as a
  degraded insights header and a Doctor warning, shows the export's age otherwise, never reads an
  export above the tracker directory or through a symlink that leaves it, reports an export over
  64 MiB as `export larger than 64 MiB`, and switches off bv's `BV_DEBUG` stderr logger.
- An in-process graph engine for the Tasks insights (opt-in when added, now the only engine: see Changed): set `ABACUS_GRAPH_ENGINE=library` and
  abacus runs bv's analysis package inside its own process (new `internal/graph`) over the
  tracker's `.beads/issues.jsonl` export, with no `bv` binary and no subprocess. The default is
  unchanged (the bv subprocess), `--demo` never uses it, and an unknown value keeps the default
  and shows a Doctor warning. Doctor says which engine is active. The report has the same shape,
  so the Tasks tab and JSON output do not change. How the two differ is in
  `docs/tool-contract.md` ("Graph engine"). The assessment is marked "prototype built".
- `scripts/dev-bundle.sh` (`make bundle`, `make dev`): a locally built binary gets abacus's own
  pinned, sha256-verified copies of bd, lambe and beads-lease in `bin/bundled/`, so Doctor shows
  every tool as `bundled` and tabs are no longer "not connected" on a dev machine. It fails closed
  on any hash mismatch, builds in a temp directory and moves it into place, is idempotent and has a
  `--check` mode. `package-release.sh` now also verifies every manifest tool landed in `bundled/`,
  and a test fails when a tool in `bundled-tools.json` lacks an asset for a release platform.
- `abacus doctor` and `abacus version` report "Bundled tools: 3 of 3 verified" (or what is wrong);
  JSON gains `bundled` (version) and `overview.bundled` (doctor) and tool lines gain `bundled_tool`
  and `optional`, all additive (schema_version stays 1). Doctor's header says `Ready` only when bd,
  lambe and beads-lease are usable.
- `scripts/dev-bundle.sh --out DIR` refuses a DIR that is not empty and does not look like its own
  install (it replaces the whole directory), re-checks each kept tool's license and file modes (and
  drops tools the manifest no longer lists), and manifest tool names must match `^[a-z0-9-]+$`.
- `core.HowToConnect` (map) and `core.HowToConnectInsights` (var) are now `core.HowToConnectFor(domain)`
  and `core.HowToConnectInsights()`, computed from the build kind: no shared state, race-free.

### Changed

- Graph insights are now computed in-process, always. The bv binary is no longer bundled or
  required: the `bv` subprocess provider (`internal/bv`), `ABACUS_BV_BIN`, `bv` in
  `bundled-tools.json`, in the sidecar lookup, in Doctor's tool list and in "Bundled tools" (now
  `N of 3`) are gone, and so is `ABACUS_GRAPH_ENGINE` with its Doctor line (the in-process engine
  from "Added" below is no longer opt-in). bv's analysis package is linked, so the archive's
  `THIRD_PARTY_NOTICES.md` lists bv v0.27.0 as a linked library and `third_party/bv/LICENSE`
  still ships verbatim, now copied from the pinned Go module and checked against a sha256 pin in
  `bundled-tools.json` (`linked_licenses`; the build fails on a mismatch or if the linked version
  is not the pinned one; `update-bundled-tools.sh --license bv` re-pins). The license is shipped verbatim.
- The archive is smaller because it no longer carries the bv binary (bytes, 0.0.0 build before to
  after): darwin-amd64 82,277,755 to 66,749,912 (-15.5 MB), darwin-arm64 75,243,652 to 60,629,053
  (-14.6 MB), linux-amd64 80,695,147 to 65,322,147 (-15.4 MB), linux-arm64 74,049,082 to
  59,936,655 (-14.1 MB). The abacus binary itself already linked bv's analysis and is 30 to 65 KB
  smaller.
- A tracker without `.beads/issues.jsonl` (bd's default) is read for insights through
  `bd --readonly export`, one more fixed read-only argv on the bd adapter's allowlist; an export
  file still wins when there is one.
- A bundled tool abacus cannot use is never "missing" or "install X" to the user. In a release the
  text says abacus's own copy is damaged or was moved, gives the refusal reason when a copy was
  refused, and says to re-extract the archive and keep the folder together; in a dev build
  (`version` is `dev`) it says to run `scripts/dev-bundle.sh`. Optional tools that abacus does not
  ship (Vercel Board CLI, b9s) say they are optional and what they add. Doctor's tool badge reads
  `unavailable` or `optional` instead of `missing`; the raw `where: "missing"` in JSON is unchanged.


- `docs/design/2026-11-22-board-bundling-assessment.md`: how to ship the Board domain without asking for Node (compiled binary measured, native client judged, trust model, recommendation).
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
- README: supported platforms, verified on Linux amd64 from the release archive (doctor, setup, TUI with only bundled tools). A plan for Windows support is in `docs/design/`.
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
- The Tasks forms and the screens around the write gate are framed panels like the other tabs: the
  new and edit forms (fields aligned in a table with a marker and caret on the focused one, the
  error under them), "Checking the current state…", the confirm screen (the "changed since you
  opened it" note, the plan, and the exact command wrapped in full, never cut) and its "applying…"
  state. The first-run setup prompt and its confirm screen, and the not-connected and unavailable
  states of Tasks, Board, Leases and Queues, are framed too. Wording, keys and the write gate are
  unchanged: nothing is written before `y` on the confirm screen. Long titles and commands wrap
  instead of being cut, and a body that does not fit a short terminal ends in a "+N more" row.
  A confirm screen that does not fit cannot be applied: its more-row says to enlarge the terminal
  to review, `y apply` leaves the footer and `y` does nothing until the whole plan and command are
  visible (the same for the first-run setup confirm). The command shown is the command run: an
  argument with an invisible, bidi or control character is shown quoted. While a form or confirm is
  open its keys lead the footer. The not-connected panels of Board, Leases and Queues put how to
  connect first when the height cannot hold everything, and a one-row panel still marks hidden rows.
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
