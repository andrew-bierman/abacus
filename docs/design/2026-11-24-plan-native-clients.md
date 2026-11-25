---
title: Native Clients (SwiftUI macOS first) - Plan
type: plan
date: 2026-11-24
execution: none
status: partly built (C1-C4, `tasks reopen` and C5 `serve --stdio` exist; the app and C7-C8 do not)
---

# Native Clients (SwiftUI macOS first) - Plan

**Status:** proposal. Docs only: nothing in this change touches code, and nothing here is built.
It plans a hand-rolled SwiftUI client for macOS that looks fully native (system controls, system
fonts, system colors, no custom chrome, no web view, no cross-platform toolkit) and consumes the
typed `schema_version` 1 JSON that core already prints. It also records what Linux and Windows
clients would reuse.

Evidence labels used throughout:

- **[code]** read in this repository at v0.5.0 (`main`, e02ee21) while writing this plan.
- **[doc]** stated in the repository's docs, not re-checked here.
- **[proposal]** does not exist; it is a suggestion for the owner to accept, change or drop.
- **[unverified]** could not be checked here (nothing was built or run; no tracker, hub or board was touched).

Wording rules that bind the client as they bind the TUI: users never install anything, no tool
names in navigation, headers or labels (the domains are Board, Tasks, Leases, Queues, Agents,
Doctor), and every string from outside is already sanitized by core. The product name is read
from core's brand constant and is not hard-coded in this plan, the client repository or the
client's code paths where it can be avoided.

## 1. Goals and non-goals

Goals:

1. A macOS app that shows every read domain and drives every write the TUI offers, through the
   same plan, confirm, single-use apply and fail-closed audit gate. The app never builds an argv.
2. One contract for every front end: core's typed reports (`internal/core`, `internal/taskview`),
   not a private format [code, `docs/tool-contract.md` "Clients"].
3. One download, no setup: the app carries abacus (and through it the built-in tools) inside the
   `.app`; the user installs nothing else [doc, README "Install"].
4. A contract the client cannot drift from silently: fixtures and tests on both sides.

Non-goals (for this plan): a Windows build (see section 10), any non-loopback serving, a web view
or Electron shell, delete or undo of tasks (core never offers them), FFI between Swift and Go
(considered and not adopted [doc, `docs/tool-contract.md` "Clients"]), and any change to the
write allowlists.

## 2. What the client consumes today (the contract inventory)

Every command below exists today [code, `internal/cli`] and prints exactly one JSON document on
stdout with `schema_version` 1 when given `--json`; reads exit 0 whatever the connection state
(the outcome is in each report's `state`), a write exits 1 when not applied or result unknown,
and a usage error exits 2. Failures print plain text on stderr and nothing on stdout.
`schema_version` is added by `core.ReportJSON`; the bulk report carries its own copy.

### 2.1 Reads

| Screen | Command | Typed report (Go) | Golden fixture today |
|---|---|---|---|
| Version and tool detection | `abacus version --json` | `core.Report` (`modules`, `bundled`) | none |
| Doctor | `abacus doctor --json [--lambe-config FILE]` | `core.Report` (`overview`, per-module `checks`) | none |
| Status, all domains | `abacus status --json` | `core.Report` + `domains` (`board`, `tasks`, `leases`, `queues`, `agents`, and `workspace` only when there is something to report) | `internal/cli/testdata/status-no-workspace.golden.json` |
| Board | `abacus board --json` | `core.BoardReport` (channels, threads, tasks, `truncated`) | none |
| Tasks summary and insights | `abacus tasks --json` | `core.TasksReport` (`counts`, `recent`, `insights`: picks, blockers, critical path, bottlenecks, influencers, cycles, tracks) | none |
| Task list | `abacus tasks list --json [--query Q] [--sort KEY] [--reverse] [--preset open\|ready\|blocked\|closed\|all]` | `core.TaskListReport` (`rows`, `matched`, `total`, `ready`, `warnings`) | `tasks-list.golden.json` |
| Task tree | `abacus tasks list --tree --json [same flags]` | `core.TaskTreeReport` (`rows` with `depth`, `folded`, `context`; `orphans`, `parent_cycles`) | `tasks-tree.golden.json` |
| One task in full | `abacus tasks show ID --json` | `core.TaskDetailReport` (description, design, notes, acceptance, comments, parent, blocked_by, blocks, children) | `tasks-show.golden.json` |
| Kanban | `abacus tasks board --json [--query Q] [--group status\|priority\|type] [--closed]` | `core.KanbanReport` (`columns`, `cards`, `closed_hidden`) | `tasks-board.golden.json` |
| Dependency graph | `abacus tasks graph [ID] --json [--whole] [--depth 1-6]` | `core.GraphView` (nodes, edges, layers, outline, chain, cycles) | `tasks-graph.golden.json`, `tasks-graph-whole.golden.json` |
| Task history | `abacus tasks history ID --json [--limit N]` | `core.TaskHistoryReport` (`events`, `truncated`) | `internal/taskview/testdata/history-events.json` (reader fixture, not a CLI golden) |
| Leases | `abacus leases --json` | `core.LeasesReport` (held, stale, free) | none |
| Queues | `abacus queues --json [--lambe-config FILE]` | `core.QueuesReport` (runs, queue, attention, history) | none |
| Agents | `abacus agents --json [--harness H] [--state S] [--repo R] [--all]` | `core.AgentsReport` | `internal/agents/testdata` (feed fixtures, not a CLI golden) |
| Global search | `abacus search QUERY --json [--domain D] [--limit N]` | `core.SearchReport` (`groups`, `domains` with per-domain state) | `search*.golden.json` |
| Workspace (optional terminal workspace; "where does this row live") | inside `abacus status --json` as `domains.workspace` | `core.WorkspaceReport` (placements, ambiguities, `open` plan with `rungs` and `copy`) | `internal/rex/testdata` (adapter fixtures) |
| First-run setup | `abacus setup --json [--dry-run]` | `{steps: [core.StepInfo], results}` | none |
| Settings | `abacus config show --json` | `{path, ...core.Config}` | none |

Notes that matter to a decoder [code]:

- Optional fields use `omitempty`: an absent field means "unknown", and list fields accept `null`
  and `[]` alike (`docs/tool-contract.md`). Some lists are never null (`agents`, `rows`, `columns`,
  `open.rungs`).
- Times are mostly plain strings (`updated_at`, `created_at`, `expires_at`) in RFC 3339 form; only the
  history report uses a real `time.Time`. A Swift decoder needs one tolerant date parser (with and
  without fractional seconds) and should keep the raw string for display fidelity.
- Domain reports share `Header` (`domain`, `title`, `state`, `source`, `summary`, `reason`,
  `how_to_connect`, `setup`), flattened into the report. `state` is `connected`, `not-connected`
  or `degraded`, plus a guided "needs setup" state with a `setup` guide. A client must render all
  of them; none is an error.
- Closed sets that may grow: run `state`, workspace `match`, history `kind`, check `status`
  (`ok`, `warn`, `fail`, `skip`, normalized by core). Swift enums need an `unknown(String)` case
  so an additive value never breaks decoding.
- Key naming is mixed by origin (`issue` in Queues rows, `id` in Tasks) and is not renamed within
  `schema_version` 1 [doc, README "Usage"]. The client maps, it does not normalize.
- Provider and tool versions are not part of the contract; only abacus's own `schema_version`
  is. The client reads `schema_version` from every document and treats a higher number as a
  breaking change [doc, `docs/tool-contract.md`].

### 2.2 Writes (every verb the CLI exposes)

All through the one gate (section 3). Each accepts `--yes` and `--dry-run`, and `--json` needs one
of the two (it cannot prompt) [code]:

- Tasks: `create`, `update ID` (title, description, priority, status), `close ID [--reason]`,
  `label add|remove ID LABEL`, `assign ID NAME | --clear`, `priority ID N`,
  `dep add|remove ID --blocked-by|--blocks OTHER`, `comment ID TEXT`.
- Tasks bulk: `bulk --ids a,b,c --op close|status|priority|label-add|label-remove|assign
  [--value V | --clear]` (at most 25; one plan, one confirm).
- Board: `board post CHANNEL TEXT`, `board reply ID TEXT`.
- Setup: `setup [--yes] [--dry-run]` (workspace, actor name, supervisor config), `setup --repair`.
- Config: `config set KEY VALUE` (`actor`, `tracker_dir`, `lambe_config`, `board_url`). This one
  writes a settings file, not the tracker, and is not behind the plan gate [code, `internal/cli/config.go`].
- Terminal workspace layout: `rex layout [--new NAME]` (opens tabs only).

### 2.3 What the TUI can do that the CLI cannot (gaps a client must know about)

Found by comparing `internal/cli` with `internal/tui` and `internal/core` [code]. Each is a
prerequisite or a scope cut for the client:

| Gap | Today | Consequence for a client |
|---|---|---|
| Reopen a closed task | `core.TaskReopen` exists; no CLI verb | no "reopen" and no kanban move out of closed over the CLI |
| Kanban card move | TUI `H` `L` plan a status update with a status guard; the CLI reaches the same argv through `tasks update ID --status S` (the CLI reads the current status as the guard) | works over the CLI for open, in_progress, blocked, deferred; closed needs `close` |
| Workspace actions (focus, run, split, new, window new) | reads are in `status --json`; the writes are TUI-only plus `rex layout` | the client shows placements and the copyable handoff command; it cannot focus or open a session until a CLI or serve verb exists |
| Live change detection | the TUI polls a cheap fingerprint (`core.TaskFingerprinter`, `taskview.Poller`); no CLI surface | the client must poll full reads, or wait for serve mode (section 6) |
| Saved filters, search history, recent trackers, tree fold state | core files in abacus's config and state dirs (`savedfilters.go`, `searchhistory.go`, `recenttrackers.go`, `treestate.go`); no CLI surface | the client keeps its own view state (it must not write core's files itself) |
| Tracker choice per call | `ABACUS_BD_DIR` or the working directory or the saved `tracker_dir`; no `--tracker` flag | the client sets `ABACUS_BD_DIR` per spawned process (no global config write) |
| Stable error codes | exit 0, 1, 2 only; failures are plain stderr text | the client cannot tell "not connected" from "bad input" without matching text (section 9) |
| Environment the GUI does not have | the CLI reads `BEADS_ACTOR`, `BOARD_TOKEN`, `VERCEL_AUTH_TOKEN`, and so on from the shell | a Finder-launched app has none of them; settings (`actor`, `board_url`) cover some, credentials need a decision (section 8) |

## 3. The write-gate contract and how a GUI drives it

### 3.1 What the gate is today [code]

`core.PlanTaskChange` validates a `TaskChange` and returns a `WritePlan` (exact argv, human lines,
an unexported single-use id from an in-process counter). Nothing runs until `plan.Confirm()` and
then `Applier.Apply`: the gate claims the id (`ErrAlreadyApplied` on a second use), records an
`attempt` audit line before anything runs (a failed audit blocks the write), runs the one
allowlisted argv, and records `ok`, `error`, `unknown` or `applied-mismatch`. A timeout or cancel
is "result unknown", never "not applied". Before a write, the CLI re-reads the task and refuses if
its status or assignee changed since the plan (`core.FreshDep`, and bd's `--if-status` and
`--if-assignee` guards exit 13). A bulk plan is single use as a whole and each item is read again
and guarded right before its own write (`core.PlanBulk`, `Applier.ApplyBulk`). Delete, `--force`,
`--set-labels`, `--no-cycle-check`, `batch`, `sql` and `edit` are never reachable.

### 3.2 The honest gap over the CLI

The plan id is a process-local counter and the plan is not serializable. So over the CLI the
single-use guarantee exists only inside one invocation:

- `abacus tasks <verb> --dry-run --json` prints `{dry_run, change: [lines], context, argv, steps}`
  and runs nothing. It carries no plan id.
- `abacus tasks <verb> --yes --json` plans again, re-reads the task, applies, and prints
  `{applied, result}`. It is a different plan from the one the user saw.

A GUI built on spawn-per-call therefore has a time-of-check to time-of-use window between the
preview and the apply: the user confirmed one argv, and a second process builds the argv it
applies. The freshness re-read and bd's guards make a stale status refuse, but the client cannot
prove that the argv applied equals the argv shown, and a second click is stopped only by the GUI's
own state. This is acceptable for a first read-mostly release and must be closed before the client
offers writes broadly.

### 3.3 Proposed contract (proposals, in order of preference)

**P1. Plan tokens over the CLI.** `--dry-run --json` additionally prints `plan_id` and
`plan_digest` (a hash of the normalized argv, and of the bulk items), and a new `--apply-plan
DIGEST` form applies only if re-planning yields the same digest, else exits with a typed "plan
changed" error and writes nothing. This gives "what you confirmed is what ran" without a
long-lived process. It does not give cross-process single use (two processes can both apply the
same digest); the audit line plus bd's own guards are the backstop, and the GUI serializes its own
applies. Additive to JSON; new flag.

**P2. Single-use ids in serve mode (section 6).** In `abacus serve --stdio` one process holds the
`Applier` for its whole life, so the existing in-memory single-use ids are real: `plan.create`
returns `{plan_id, lines, argv, context, warnings}`, the GUI shows exactly these lines and the
command, the user confirms, `plan.apply {plan_id}` claims the id once. A second apply returns the
typed "already applied" error. Plans expire when the serve process ends and after a bounded age
(proposal: 10 minutes), because a plan is only valid against the state it was read from.
The client can never send argv; it sends a typed change (`TaskChange`) and gets lines back.

**P3. Result-unknown stays a first-class state.** The apply result carries an outcome enum
(`applied`, `not_applied`, `unknown`, `applied_mismatch`, `already_applied`, `changed`), never a
bare error string. The GUI shows "result unknown, refresh before retrying" and offers only a
refresh, never an automatic retry, exactly as the TUI does [doc, README "Writes"].

Audit stays core's: the GUI never writes `audit.jsonl`. A proposed additive audit field `via`
(for example `cli`, `tui`, `app`) records which front end made the write (the web plan already
proposes `via: web`).

### 3.4 The confirm screen

Native, but the content is core's: the plan lines, the exact command (`core.DisplayArgv`), the
read-only context, any warning, and the revert hint where core supplies one. Rules carried over
from the TUI [doc]: the confirm button is disabled until the whole plan has been shown (a long
comment or batch scrolls), a bulk plan lists every task, the board confirm shows the board URL and
whether the post creates a channel, and a "result unknown" screen has no retry button.

## 4. Where the app lives

A separate repository under `andrew-bierman`, created by the owner after the name decision, not
inside this one. Reasons: Xcode project and Swift Package Manager have their own toolchain, CI
(macOS runners), signing identity and release cadence; this repository's CI is Go and runs on a different budget and cadence; and nothing in `internal/`
may import a UI library [code, `internal/core/arch_test.go`], which a monorepo Swift tree would not
break but would complicate. The app repository pins a specific abacus release (version and sha256)
in one file, the way `bundled-tools.json` pins tools here.

Proposed layout of that repository [proposal]:

- `Contract/` a Swift package with the Codable types, the decoder, the fixture tests, and nothing
  that imports SwiftUI (so a Linux Swift client could reuse it).
- `Engine/` a package that locates and runs the bundled abacus (spawn-per-call first, serve
  later), owns the environment scrub, and exposes async functions per report and per plan.
- `App/` the SwiftUI app: one `NavigationSplitView` with the domains in the sidebar, system
  `Table` for lists, `Inspector` for detail, native sheets for confirm, `Settings` scene for
  preferences. No custom controls where a system one exists.

The Swift code stays free of domain logic: sorting, filtering, query parsing, blocked and ready
derivation, kanban grouping and graph layout are core's, returned in the report. The client sends
`--query`, `--sort`, `--group` and renders rows in the order given. A client-side reimplementation
of the query grammar is a drift risk and is not planned.

## 5. Finding and bundling the abacus binary

Requirement: ships inside the `.app`, the user installs nothing [doc, README].

**Recommended: embed the release binary unmodified.** Place the self-contained release
executable (the `-tags embedtools` build, about 80 to 90 MB on disk [doc, README]) at
`Contents/Helpers/abacus` (a helper, not `Contents/MacOS`, so the app's main executable is the
SwiftUI app). The app resolves it with `Bundle.main` and never searches `PATH`; nothing on the
user's machine can substitute it. Core's resolver then does the rest: the built-in tools are
extracted once, sha256-checked against the manifest inside the binary, into a private per-user
folder (`$ABACUS_TOOLS_DIR`, default `$XDG_DATA_HOME/abacus/tools`), taking about 3 seconds on
first run and about 0.2 seconds on later starts [doc, `docs/built-in-tools.md`]. The app should
show a one-line "preparing" state during that first run (core already prints it on stderr) and run
a first `version --json` as a warm-up so the first screen does not pay for it.

Points to decide or verify:

- Shared cache. The default tools folder is the same one a command-line user of the same account
  would use, so the GUI and a terminal install share one extraction. Setting `ABACUS_TOOLS_DIR` to
  an app-specific folder isolates them at the cost of a second 250 MB extraction [doc, "roughly
  250 MB of tools"]. Recommendation: share the default.
- `ABACUS_*_BIN` overrides must not be forwarded from the app's environment; an override runs a file
  with none of the integrity checks [doc, `docs/built-in-tools.md`]. The app never sets them.
- Universal build. Release archives are per architecture [doc]. Either ship two app builds, or lipo
  the Go binary into a universal helper; the embedded tools are per-architecture blobs, so a
  universal helper is bigger. [unverified: whether the embedtools build can be combined; start with
  per-architecture apps.]
- Alternative considered: a non-embedded build with signed helper binaries under
  `Contents/Helpers` and `libexec`. Core's sidecar lookup already searches `../libexec/<name>` next
  to the executable, but an embedded build never reaches the sidecar, and the sidecar path is
  documented as a development override. This would let each tool carry the owner's signature, at
  the cost of a second, differently verified install mode. Not recommended for the first release;
  revisit only if section 11's check of extracted binaries fails.
- Updates. The app carries an abacus version; the app updater updates both together. There is no
  separate "update the CLI" step for the user. A mismatched contract (`schema_version` higher than
  the app knows) shows a plain "update the app" state, never partial data.

## 6. Spawn-per-call versus a long-lived serve mode

### 6.1 Options

| Option | How | For | Against |
|---|---|---|---|
| A. Spawn per call | `Process` runs `abacus <cmd> --json`, reads stdout, exits | exists today; no new core code; crash-isolated; each call independent | start cost per call; every view re-reads the tracker, so list, kanban, graph and insights can show different instants; no push; plan ids cannot span calls (3.2) |
| B. `abacus serve --stdio` | one child process per app window or per tracker; JSON-RPC 2.0 messages, newline-delimited, on its stdin and stdout; lifecycle bound to the parent | one snapshot feeds every view (core's KTD1 "one snapshot, many views" holds across calls); real single-use plan ids; change notifications; no port, no token, no network surface | new core surface; the app must supervise and restart it; stdout must carry only protocol (logs to stderr) |
| C. Unix socket | `serve --socket PATH` in a 0700 directory, peer-credential check, same JSON-RPC | lets several clients (TUI, app, scripts) attach to one engine | another lifecycle (who starts and stops it, stale sockets, ambiguity with several live instances, which the contract treats as an explicit error); not needed for a single GUI |
| D. Loopback HTTP | the web portal's W1 (`abacus web`): bearer token, Host and Origin checks, SSE | shared with a future browser client [doc, tasks workspace plan "Web portal"] | highest security surface (DNS rebinding, token handling); a native app gains nothing over B |

### 6.2 Latency and refresh, with what is and is not known

Known [doc]: abacus starts in about 0.2 seconds once the tools are extracted, and the TUI's live
refresh avoids full reads by comparing a small manifest fingerprint every 5 seconds, reading the
whole tracker only when it moved. Each CLI report call runs `bd --readonly export` and, for
insights, the in-process graph analysis over that export.

Not known [unverified]: the wall-clock of one spawn-per-call round trip on a realistic tracker.
Phase 0 of the roadmap measures it before any serve work is committed: list, show, board, graph
and insights on the demo fixtures and on a synthetic 5,000-task tracker, in an isolated HOME,
reporting the 50th and 95th percentile. The decision rule is [proposal]: if p95 for the common
screens (list, show, board) is under 300 ms on the synthetic tracker and the screens can tolerate
reading at different instants, ship phase 1 on spawn-per-call and defer serve; otherwise serve
moves up.

Why serve is still the destination regardless of the numbers:

1. Consistency. Blocked and ready must agree across list, kanban, graph and insights; today that
   holds because one process reads one snapshot. Across spawned calls it holds only per call.
2. The gate. P2 in 3.3 is only real in a long-lived process.
3. Liveness. A fingerprint poll in the server can push a `tasks.changed` notification (the cheap
   check exists in core and has no CLI surface). Over spawn-per-call the app would poll full reads.
   Rex's change feed (`events --json`, already a long-lived child inside core) is the model for
   a notification stream that never starts a server of its own.

### 6.3 Proposed serve protocol (all [proposal])

- Command: `abacus serve --stdio [--tracker DIR]`. No listening socket, no network. It speaks only
  on stdin/stdout, reads one request per line, writes one response per line, and exits when stdin
  closes. A handshake `server.hello` returns `{schema_version, app_version, capabilities}` and is
  the one place a version mismatch is decided.
- Methods mirror section 2 one to one and return the same typed report as the CLI's `--json`,
  wrapped in the JSON-RPC result: `report.status`, `report.doctor`, `report.board`,
  `tasks.summary`, `tasks.list`, `tasks.tree`, `tasks.show`, `tasks.kanban`, `tasks.graph`,
  `tasks.history`, `report.leases`, `report.queues`, `report.agents`, `search.query`,
  `setup.report`, `config.show`. Parameters are the CLI's flags as named fields. A method that
  needs a snapshot takes an optional `snapshot_id` so a client can ask several views of one read;
  the response echoes the `snapshot_id` and `read_at`.
- Writes: `plan.create {change}` (typed `TaskChange`, `BulkSpec`, board post or reply, setup step),
  returns `{plan_id, lines, context, argv_display, warnings, expires_at}`; `plan.apply {plan_id}`;
  `plan.cancel {plan_id}`. Nothing else writes. The argv is display-only in the response.
- Notifications (server to client): `tasks.changed {snapshot_hint}` from the fingerprint poller
  (same pacing and back-off rules as `taskview.Poller`), `domain.changed {domain}` for others, and
  `engine.degraded {reason}`. A missed notification costs one refetch, never correctness.
- Errors: a JSON-RPC error with a stable string `code` from a closed set (section 9) plus the
  sanitized message.
- Limits: one in-flight read per domain (the TUI rule), request and response size caps, a
  per-request deadline with the existing cancel semantics (cancel of a write is result unknown),
  and no method that accepts a path or an argv.

Framing is deliberately not LSP-style `Content-Length` headers: a newline-delimited frame is
trivial to implement in Swift (`FileHandle` async bytes) and in a shell for debugging, and core's
`ReportJSON` already emits indented JSON, so serve must emit compact single-line JSON.

## 7. Contract fixtures and drift tests

Three ways to get Swift types that match Go. They are not exclusive; the order is the proposed
phasing.

### 7.1 Hand-written Codable with golden fixtures (phase 1)

Swift `Codable` structs written by hand, decoded from the Go-produced goldens already in
`internal/cli/testdata` and `internal/taskview/testdata` (section 2.1 lists what exists). It
needs no core change and is enough to start. Its weakness is drift: a new additive field is
silently ignored by `Decodable`, and a renamed or retyped field fails only if a fixture exercises
it.

Drift tests that close most of the gap without core work [proposal]:

- **Vendor the fixtures by script**, not by hand: a script in the app repository copies the
  goldens from a pinned abacus tag (the one in the pin file) into `Contract/Fixtures/` and
  records the tag and sha256. A CI job on the app repository fails if the pin changed and the
  fixtures did not.
- **Strict decode test**: decode each fixture, re-encode with a key-sorted encoder, and compare
  the set of key paths with the fixture's. A key path present in the fixture and absent from the
  Swift type fails the test, so an additive Go field forces a deliberate decision (adopt it or
  list it as ignored). Unknown enum values are tested separately with a hand-edited fixture.
- **Mutation fixtures**: for each report, one fixture with every optional field absent, one with
  every list `null`, one with hostile strings (the control characters core already strips are not
  present, but long text, emoji and bidi-free RTL are).
- **Missing reports**: core has no CLI goldens for `version`, `doctor`, `board`, `leases`,
  `queues`, `agents`, `setup`, `config show` and the bulk and dry-run envelopes. Adding them to
  this repository (generated by the existing `-update` golden harness from fixture providers) is
  a small core change and the first prerequisite (section 12, C1).

### 7.2 A schema export from core: `abacus schema` (phase 2, proposal)

Add `abacus schema [--json] [REPORT]` that prints a JSON Schema (draft 2020-12) for each report
and write envelope, generated from the Go types, plus a `contract_version` that equals
`schema_version` and a list of the reports. Generation options:

- **A maintained reflection library**, for example `invopop/jsonschema` (draft 2020-12 from Go
  types by reflection, honoring `omitempty` as optional, with comment extraction) [documentation
  checked with the Context7 index while writing this plan; library not tried here]. This follows
  the owner's preference for libraries over hand-rolled parsers [owner preference]; it adds one
  dependency and needs a size and license check, which this plan did not do.
- Points a generated schema must get right, each a test [proposal]: the embedded `Header` is
  flattened by `encoding/json`, so the generator must flatten it too; `ReportJSON` injects
  `schema_version` after marshaling, so the schema needs it added by the same function; the three
  custom marshalers (`OpenPlan`, `OpenRung`) force lists to be non-null; `time.Time` is a
  `date-time` while most times are plain strings; `any` result fields in the write envelopes need
  a `oneOf` per verb; `json:"-"` fields (`Guide`) must not appear.
- A test in this repository regenerates the schema and compares it with a committed
  `docs/schema/v1/*.json` (the same pattern as the goldens). Because the schema is committed and
  versioned with the code, the app repository vendors it by script exactly like the fixtures, and
  a schema change that is not additive fails the Go build before it reaches Swift.

Swift side: generate Codable types from those schemas with **quicktype**
(`quicktype -s schema ... -o Models.swift`; it supports JSON Schema as a source language and
Swift as a target [documentation checked with the Context7 index; not run here]). Generated types
are committed to the app repository with the schema sha256 in a header comment; a CI job
regenerates and fails on a diff. Known limits to evaluate [unverified]: how quicktype renders
`oneOf` and open enums (it tends to produce closed enums, which would break on a new value, so
status-like fields should stay `String` in the schema with documented values rather than JSON
Schema enums), and whether its output is idiomatic enough for the hand-written `Contract/`
extensions (computed properties, sorting helpers) layered on top.

### 7.3 Alternatives considered

- **Hand-write everything forever.** Cheapest now, drift-prone with this many report types and
  growing additive fields; acceptable only for phase 1.
- **OpenAPI plus Apple's swift-openapi-generator.** Right if the transport were HTTP (option D);
  for stdio JSON-RPC the schema-first route above is simpler. Revisit if the web portal ships and
  both clients want one description.
- **Protobuf or any non-JSON contract.** Rejected: core's contract is JSON and the CLI is the
  stable surface for non-Go clients [doc].
- **Swift types generated by a Go program that prints Swift.** Rejected: a bespoke generator is the
  hand-rolling the owner prefers to avoid when a maintained tool exists.

Recommendation: phase 1 hand-written plus vendored goldens plus the strict key-path test; phase 2
add `abacus schema` and switch the Swift types to generated, keeping hand-written extensions in a
separate file. Decide at the end of phase 1 with real drift evidence.

## 8. Security

The app is a front end; the boundaries are core's, and the client must not weaken them.

1. **Scrubbed environment.** The app builds the child environment from an allowlist and never
   forwards its own: `HOME`, `PATH` (a fixed system path; embedded tools do not use it), `TMPDIR`,
   `USER`, `LANG`, `LC_*`, the `XDG_*` directories, and `ABACUS_BD_DIR` for the chosen tracker. It
   never forwards `ABACUS_*_BIN`, `DYLD_*`, `LD_*`, `BASH_ENV` or `ENV`, matching core's own
   refusals [doc, Rex section]. Core then scrubs again per tool (for example Rex gets only `HOME`,
   `PATH`, the XDG dirs and `REX_*`; the Board CLI gets a short documented list) [doc].
2. **No network from the app.** The app opens no sockets of its own and has no network
   entitlement beyond what notarization and updates need (see section 11 for the update channel
   decision). All network access remains inside the tools core already launches under their
   allowlists (Board over https or loopback; Rex only for this machine and `ABACUS_REX_HOSTS`).
   Serve mode (6.3) listens on nothing.
3. **No argv from the GUI.** The client sends typed changes and renders core's plan lines; it never
   assembles or edits an argv, and it never offers a free-form "run this command". Display-only
   argv in a response is rendered as text and copyable, never executed by the app (the workspace
   `open` rungs are executed only by core on an explicit keypress equivalent: a button the user
   presses, and only once serve or a CLI verb exposes it).
4. **Text is already sanitized by core** [doc]; SwiftUI `Text` is used with plain strings only. No
   `AttributedString` from markdown of tracker text and no web view, so tracker content cannot
   inject links or markup. Descriptions and comments are displayed as selectable plain text.
5. **Credentials.** A Finder-launched app has no shell environment, so `BEADS_ACTOR`, `BOARD_TOKEN`
   and `VERCEL_AUTH_TOKEN` are absent. `actor` and `board_url` are config keys, so the app can set
   them through `config set`. Tokens are an owner decision [proposal options]: (a) read-only
   (the Board shows "not signed in" and how to sign in with the terminal tool the user may already
   have), (b) the app stores a token in the Keychain and passes it only to the board child through
   one named variable, never logged. Recommendation: (a) for the first release.
6. **Untrusted data paths.** Tracker directory names, task text and file paths from reports are
   data. The app opens a path only when the user picks it in a system panel; it never opens a URL
   from tracker text. The Board inspector handoff (`o` in the TUI, which opens a browser) has no
   counterpart in the first release.
7. **App Sandbox.** The sandbox forbids running an extracted executable and arbitrary file access
   to trackers, so the app is a notarized Developer ID app without the App Sandbox (hardened
   runtime on). That makes it a non-App-Store app; say so in distribution notes [proposal, to
   confirm with the owner].
8. **Audit and logs.** Writes are audited by core (`audit.jsonl`), fail closed. The app's own logs
   (unified logging) record request names and durations, never task text, tokens or argv values.
9. **Plan integrity.** See 3.2 and 3.3: until P1 or P2 lands, the app offers writes behind a
   "preview, then apply" flow and states plainly that the applied change is re-planned.

## 9. Core prerequisites, stated precisely

What must change in this repository before the client can rely on the contract. All are additive
to JSON and none changes an existing key or the write allowlists. Numbers are the proposed order,
referenced by the roadmap.

| # | Prerequisite | Why | Size |
|---|---|---|---|
| C1 | **Contract goldens for every report and write envelope** (`version`, `doctor`, `board`, `tasks` summary, `leases`, `queues`, `agents`, `setup`, `config show`, dry-run, apply result, bulk result), generated through the existing `-update` harness from fixture providers, and a `docs/` index of them | the app's fixtures need a source of truth; only tasks views and search have CLI goldens today | small |
| C2 | **Stable error codes.** A closed set of string codes (for example `not_connected`, `needs_setup`, `invalid_input`, `plan_changed`, `already_applied`, `result_unknown`, `applied_mismatch`, `audit_unavailable`, `tool_incompatible`, `timeout`) on stderr as one JSON document when `--json` is given (stdout stays empty, as today), and as `error.code` in serve. Exit codes stay 0, 1, 2 | the client must branch on a code, not on prose; core already has typed errors (`ErrAlreadyApplied`, `ErrChanged`, `ErrOutcomeUnknown`, `ErrAppliedMismatch`, `NeedsSetupError`, `NotConnectedError`) to map from | small |
| C3 | **`abacus schema`** (section 7.2) and a committed, tested schema directory | typed drift protection both ways | medium, plus a dependency decision |
| C4 | **Plan tokens** (P1: `plan_digest` in dry-run, `--apply-plan`), at least for tasks, bulk and board writes | closes the preview-versus-apply gap over the CLI | small to medium |
| C5 | **`abacus serve --stdio`** (section 6.3) with `plan.create`, `plan.apply`, snapshot ids and change notifications | consistency, real single-use ids, liveness | medium to large; its own plan and review |
| C6 | **Missing verbs the TUI has**: `tasks reopen ID`, and a typed `workspace` action surface (focus, open) | parity | small each |
| C7 | **`--tracker DIR`** on every command (or `ABACUS_BD_DIR` documented as the supported selector for clients), and `via` in the audit entry | per-call tracker choice without writing the shared config; attribute app writes | small |
| C8 | **A `contract` section in `abacus version --json`** listing the supported `schema_version` and (once C5 exists) the serve protocol version | one place to negotiate | small |

**Implemented since this plan was written** (C1, C2, C3, C4, the `tasks reopen` half of C6, and `--tracker` and
the `contract` section of `version --json` (C7, C8; see "Demo mode, `--tracker`, interrupts" in the contract); `docs/tool-contract.md`
has the contract, `CHANGELOG.md` the list). Where it differs from the proposal above: the error document
is printed on **stdout** under `--json` (with stderr text and exit codes unchanged), not on stderr; the
codes are `usage`, `not_connected`, `needs_setup`, `not_found`, `refused`, `changed`, `already_applied`,
`unknown_outcome`, `applied_mismatch`, `audit_failed`, `timeout`, `unsupported` and `failed`; the plan
token is `--expect-digest DIGEST` on every write (there is no `plan_id` and no `--apply-plan`); and
`abacus schema` derives the JSON Schemas by reflection in core with no new dependency (about 250 lines over a
closed set of types), checked in at `docs/schema/v1`. Not built: `serve` (C5, so `contract.serve` is false), the
typed workspace actions, and `via` in the audit entry (the C7 half). The native client's gaps added `--demo` on
every read command, a `tasks history` golden and a trapped SIGINT/SIGTERM during an apply.

**C5 is built too** (`abacus serve --stdio`; the protocol is `docs/serve-protocol.md`). Where it differs from
section 6.3: the handshake is `initialize` (not `server.hello`) and carries the protocol version, `schema_version`,
the abacus version and every method and limit; writes are `plan.task.<verb>`, `plan.tasks.bulk` and
`plan.board.post|reply`, each returning a server-held single-use `plan_id` with the same `plan_digest` the command
line prints, and one `apply {plan_id, expected_digest?}` (there is no `plan.create {change}` or `plan.apply`);
errors are JSON-RPC errors whose `data` is the closed-code error object (plus `plan_id`, `plan_digest` and the P3
`outcome`), with a JSON-RPC number beside it; cancellation is the `$/cancelRequest` notification and a deadline is a
top-level `deadline_ms`; the notifications are `tasks.changed`, `workspace.changed` (from the workspace provider's own
event feed) and `engine.degraded`, behind `subscribe` and `unsubscribe`. Not built: `snapshot_id` and `read_at` (each
request is one read, as a command is), `--tracker DIR` on the command (the client sets `ABACUS_BD_DIR`), the
`domain.changed` notification, and any setup, config or workspace-action write (setup state is readable;
applying it stays a command).

C2 and C1 are prerequisites for phase 1; C3 and C4 for phase 2; C5 for phase 3. Nothing in C1 to C8
needs a new Go dependency except C3's reflection library.

## 10. Linux and Windows clients

The contract is language-neutral JSON, so the client story is the same shape per platform: a thin
native front end over the same bundled abacus, no FFI.

- **Linux.** Release archives exist for amd64 (run end to end) and arm64 (cross-compiles, not run
  on hardware) [doc, README]. A native client would use GTK 4 or Qt over the same engine
  process and the same schema. Candidates, evaluated only when there is demand: a Swift client
  reusing `Contract/` (the contract package must not import SwiftUI, section 4) with a GTK binding
  (maturity unverified), or a client in a language with first-class GTK or Qt support generating
  types from `abacus schema` with that language's quicktype target. The TUI already covers
  terminal-first Linux users. Packaging would be a tarball or AppImage that includes the abacus
  binary; there is no equivalent of notarization to plan.
- **Windows.** Out of scope until core supports it: Windows compiles but three safety checks are
  absent or fail open (sidecar owner check, config write lock, process groups) and the built-in tools
  are not pinned for Windows [doc, `docs/design/2026-11-22-1900-plan-windows-support.md`]. A Windows
  client is blocked on that plan, not on the client. When it unblocks, the same stdio serve
  protocol works on Windows (pipes, no socket), which is a reason to prefer stdio over a Unix
  socket in the design.
- **One schema, many clients** is the point of C3: a generated type target per language replaces
  per-client hand-writing, and the web portal (planned, not built [doc]) would use the same schema.

## 11. Signing and notarization (owner decision)

This repository ships unsigned archives; macOS users clear quarantine with `xattr` [doc, README].
A downloadable `.app` needs signing and notarization, which depends on the owner's Apple developer
account and is not something this plan can decide or perform. The owner decides: whether to enrol
(or use an existing Apple Developer Program membership), the identity name, who holds the
certificates, and the distribution channel (direct download of a notarized `.dmg` is assumed; Mac
App Store is excluded by section 8 point 7).

Steps once decided (all [proposal], none run here):

1. Enrol in the Apple Developer Program under the account that will own the app; create a
   Developer ID Application certificate; keep the private key off CI runners where possible.
2. Create an app identifier and decide bundle id and minimum macOS version (SwiftUI features used
   set the floor).
3. Set up notarization credentials: an App Store Connect API key (preferred over an app-specific
   password), stored as CI secrets in the app repository only. No secret in this repository.
4. Sign inside-out: the embedded abacus helper first (hardened runtime, a timestamp, an entitlements
   file; no App Sandbox), then the app bundle. `codesign --deep` is discouraged; sign each
   Mach-O explicitly.
5. Hardened runtime entitlements: start with none. Add only what a measured failure requires.
6. Zip or build a `.dmg`, submit with `xcrun notarytool submit --wait`, then `xcrun stapler staple`
   and `spctl --assess` to verify. Keep the log for each submission.
7. Test on a clean machine or account with the quarantine flag set (a downloaded archive), because
   a developer machine bypasses Gatekeeper in ways a user's does not.
8. Updates: decide the channel (Sparkle with an EdDSA key, or a manual download). Sparkle is a
   maintained library and fits the owner's preference; it introduces an app network call, which
   section 8 point 2 must then allow explicitly and only to the release host.
9. Release process: tag the app repository, build the pinned abacus release in, sign, notarize,
   staple, publish a draft release for the owner to publish (publishing stays an owner action
   [doc, tasks workspace plan "Finishes and ships"]).

The check that decides the helper design [unverified, first thing to test on a signed build]: the
embedded abacus extracts the built-in tools at run time into a user folder. Binaries written by the
running app carry no quarantine attribute, so Gatekeeper is not expected to block them, but each
extracted Mach-O must still be runnable on Apple silicon (they need at least an ad-hoc signature,
and the Go ones are linker-signed; whether the compiled Board CLI is acceptable under the app's
hardened runtime and what it needs, for example JIT entitlements for its runtime, has not been
tested). If any extracted tool is refused, the fallback is the signed-helpers layout in section 5's
alternative.

## 12. Phased roadmap

Each phase ends in something runnable and is its own set of small reviewed changes. Phases 0 and 1
need only core prerequisites C1 and C2.

**Phase 0: measure and prepare (this repository, small).**
Core work: C1 (contract goldens for every report and write envelope) and C2 (stable error codes).
Measure spawn-per-call latency on the fixture trackers (section 6.2) and record the result in a
design note. Decide the open questions below. Nothing in the app repository yet.

**Phase 1: read-only app on spawn-per-call.** First three build steps, in order:

1. **Contract package and engine.** Create the app repository, the pin file and the fixture
   vendoring script. Build `Contract/` (hand-written Codable for status, tasks list, show, board
   and search first) with the strict key-path drift test, and `Engine/` with the environment
   scrub, binary resolution inside the bundle, a per-call deadline and cancellation, and the
   `schema_version` check. Exit criterion: a command-line test target decodes every vendored
   fixture and the engine round-trips `version --json` against the real built-in abacus in an
   isolated HOME.
2. **Shell and the first domains.** A `NavigationSplitView` with the five domains plus Doctor in the
   sidebar, a native `Table` for the task list with the query field (`--query` passed through),
   sort and preset controls, an inspector for task detail (description, comments, blockers,
   children), and every domain's `not-connected`, `needs setup` and `degraded` states rendered from
   `reason` and `how_to_connect`. A first-run "preparing" state. Exit criterion: the app shows
   the demo-equivalent data from fixture trackers in an isolated HOME and never shows a raw error.
3. **Views and search.** Kanban, tree, graph (outline first; a drawn graph only if it stays legible
   from the report's own layers), insights, history, global search, Leases, Queues, Agents and the
   workspace placements (read only, with the copyable handoff command). Refresh is a user action
   plus a slow timer, with one read in flight per domain.

**Phase 2: gated writes and the schema.** C3 (schema export), C4 (plan tokens), C6 (reopen) first;
then quick create, edit, close and reopen, labels, assignee, priority, dependencies and comments,
kanban drag as a status update, bulk actions, and board post and reply, each behind a native confirm
sheet built from the plan lines (3.4). Switch the Swift types to generated ones. Add the signing and
notarization pipeline (section 11) here, because a distributable build is the exit criterion.

**Phase 3: serve mode and liveness.** C5 and C7, C8: `serve --stdio` with snapshot ids, real
single-use plans and change notifications; the app moves from spawn-per-call to one supervised
engine with restart and a "reconnecting" state; live refresh replaces the timer. Exit criterion:
two views of one `snapshot_id` agree on blocked and ready, and a repeated `plan.apply` is refused
by id.

**Phase 4 (later, separately decided).** Workspace actions (focus, open) through typed verbs, a
Linux client from the same schema, and the web portal if the owner wants it.

## 13. Risks

- **Drift between contract and client.** Mitigated by C1 now, the strict key-path test, and the
  schema export later; the cost of a missed additive field is a missing feature, not a crash
  (unknown keys are ignored), but a retyped field is a decode failure on the affected screen, so
  decoding is per screen and a failure shows that screen's error state, not an app failure.
- **Window between preview and apply** until C4 or C5 (3.2). Mitigated by serializing applies in
  the app, the freshness re-read and bd's guards, and by gating writes to phase 2.
- **Size.** The embedded helper is 80 to 90 MB and extracts about 250 MB on first run
  [doc]. A download of that size is the price of "installs nothing else"; a smaller app is only
  possible with the signed-helpers alternative or a lighter tool set, both out of scope.
- **Extracted-tool signing on macOS** (section 11): untested, and the largest unknown in the
  distribution story.
- **Unmeasured latency** (6.2): the decision between phases is data-dependent by design.
- **Upstream tools change** (Beads, the Board CLI, Rex): contained by core's adapters and
  allowlists; the client sees only core's JSON, which is the reason it never talks to a tool
  directly.
- **Scope creep into logic.** A Swift reimplementation of query, sort or graph logic would
  duplicate core. The rule in section 4 is the guard: render what the report says.

## 14. Questions for the owner

1. App name and repository name (read from the brand constant where the app needs it), and
   whether the repository is public or private.
2. Signing identity and notarization account (section 11), and direct download versus any store.
3. Accept the phasing: spawn-per-call first, `serve --stdio` as the destination, loopback HTTP
   only with the web portal.
4. Credentials for the Board in a GUI (section 8, point 5): read-only first, or Keychain.
5. Schema generation: accept a reflection library dependency for `abacus schema` (C3)?
6. Update channel: Sparkle or manual download.
7. Whether the first public release is read-only (end of phase 1) or waits for gated writes.

## Verification note

This is a documentation change. It was written from the repository's code and docs at v0.5.0; nothing
was built or run, and no tracker, hub, board or coordination data was read. The two library facts
in section 7 (quicktype's JSON Schema to Swift path and the reflection library's handling of
optional fields) come from the Context7 documentation index and were not tried. Every `[unverified]`
item above is a thing to check before relying on it.
