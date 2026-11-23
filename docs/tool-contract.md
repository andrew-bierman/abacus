# Tool contract

Providers are how abacus fills its fleet domains (Board, Tasks, Leases, Queues); users see domains, not
providers. `bd` (Tasks, with reads and allowlisted writes) and `bv` (graph insights, `--robot-*` JSON
only) are composed the same way as the tools below: through their documented `--json` surfaces, behind
argv allowlists. See `docs/plans/2026-11-21-1700-feat-fleet-domains-plan.md`.

What a Beads-family tool exposes so an umbrella tool can compose it without tool-specific
logic. Lambe is the reference implementation; its owned wire spec is
`docs/api/state-api.md` in the Lambe repo and is cited here, not restated.

A tool is composable when it provides the first five items. The sixth is optional.

1. **`<tool> version --json`** prints one JSON object with at least `version` (string) and
   `api_version` (integer). The umbrella reads only `api_version` values it knows and
   reports any other as incompatible instead of guessing. Bump `api_version` only for
   breaking changes to the machine surfaces below.
2. **`<tool> doctor` and `<tool> doctor -json`** run health checks. The JSON form is an array
   of `{name, status, detail, fix?}` with `status` one of `ok`, `warn`, `fail`, `skip`. The
   process exits 1 only when a check is `fail`, and still prints the full report.
3. **`--json` reads.** Any state a user can see in the tool's CLI is also available as JSON
   with the same exit-code rules. Absent optional fields mean "unknown", and lists accept
   `null` and `[]` alike.
4. **Stable exit codes.** 0 ok, 1 a check or command found a problem, 2 usage error. Codes do
   not change within an `api_version`.
5. **Config and state discovery.** The tool documents where its config and state live and
   lets a caller select one instance (a `-config FILE` flag and an environment override).
   Several live instances without a selector is an explicit ambiguity, never a silent pick.
6. **Optional `serve` local API.** A read-only HTTP API on `127.0.0.1` only, bearer token
   delivered through a `0600` descriptor file, versioned routes, and a single JSON error
   envelope. Client duties (loopback check before sending the token, no redirects, bounded
   timeouts, never print the token) are in Lambe's state-API doc. Mutating routes are a
   separate, explicit owner decision.

## What the umbrella reports

Detection has four states, and one exit-code rule covers all of them:

| State | Meaning | `doctor` shows | Exit |
|---|---|---|---|
| `absent` | the binary is not found | nothing (listed by `version` only) | 0 |
| `broken` | found, but `version` errors, exits non-zero or times out | a failing `installation` check | 1 |
| `incompatible` | runs, but the `api_version` is unsupported or the output is unreadable | a `compatibility` warning | 0 |
| `ready` | usable | the tool's own checks | 1 only if one is `fail` |

A tool whose own `doctor` exits non-zero without reporting a `fail` check also yields a failing
check, so a red tool never reads green. Check statuses are a typed set (`ok`, `warn`, `fail`,
`skip`); anything else a tool emits is shown as `warn`. A ready tool whose state cannot be read
is shown with an `unavailable` reason and does not change the exit code (a labelled degraded
read, below, is not unavailable). Every tool subprocess
has a 10s deadline.

All text that originates outside abacus (tracker titles, daemon messages, tool output) has
control characters removed before any front-end sees it.

The umbrella's own `--json` output carries `schema_version` (currently 1). Native clients should
check it and treat a higher number as a breaking change.

## Reference: how the umbrella uses Lambe

| Surface | Command or route | Used for |
|---|---|---|
| Detection | `lambe version --json` (`api_version` 1) | absent, incompatible or ready |
| Health | `lambe doctor -json [-config FILE]` | `doctor` aggregation, exit code |
| State | `GET /api/v1/state`, `/api/v1/queue` via the descriptor | `status`, TUI section |
| State dir | `lambe state-dir -json` | where descriptors live when `ABACUS_LAMBE_STATE_DIR` is unset; the answer is cached, and the documented default is the fallback if the command is unavailable or fails |
| Degraded state | `lambe status -json [-config FILE]` | only when no live daemon is found: a tracker-only subset, labelled `tracker subset` |
| Selection | `--lambe-config FILE` or `ABACUS_LAMBE_CONFIG` | `doctor -config`, descriptor `config_path` match |

The module can run only these argvs (`version --json`, `state-dir -json`, `doctor -json` and
`status -json`, the last two with an optional `-config FILE`); anything else is refused before
it reaches a process.

### Tracker subset (degraded read)

With the state API off or no daemon running (`ErrNoDaemon`), `status` runs `lambe status -json`
and shows what the tracker alone knows, labelled `tracker subset` (a `source` field in
`status --json`, a `source:` line in text, and a labelled Runs header in the TUI). It is never
presented as a live daemon: runs are the tracker's in-progress claims (no phase, no event
times), there is no queue, no gate rejections and no daemon identity, and the `daemon` block
(pid 0) is never used for process checks. Several live daemons (`ErrAmbiguous`) are never
degraded, and a failed or unreadable tracker read leaves the plain `state unavailable` reason.
The process-identity and loopback guards for the live API are unchanged.

Decoding is tested against the fixtures vendored in `internal/lambe/testdata/contract/v1/`.

## Known gaps in the reference

None open. `lambe status -json` (andrew-bierman/lambe#51) and `lambe state-dir`
(andrew-bierman/lambe#52) shipped in Lambe PR #57 and are used as described above.

## Second reference: beads-lease (read-only, partial)

beads-lease (leases on the Beads hub) meets most of the contract: `version --json` with an
`api_version`, `doctor [--json]`, a `--json list` read with a `schemaVersion`, and stable exit
codes (0, 1, 2 contention, 64 invalid input, 130 interrupted). abacus runs exactly four argvs
behind an allowlist that rejects every other one (no acquire, release, heartbeat, run, sync or
plain `doctor`): `version`, `version --json`, `doctor --json` and `--json list`. Leases show as
held, stale or free.

- Detection runs `version --json` and reads `api_version` (1 is supported; any other value is
  `incompatible`). Missing or unparsable JSON means an older binary: abacus falls back to the
  plain `version` line, leaves `api_version` unknown and still treats the tool as `ready`.
- Health is the tool's own `doctor --json` (checks `actor`, `config`, `coord`, `hub`, where
  `hub` is a read-only `coord list --json`). Unknown statuses show as `warn`, an empty report
  is a `warn`, and a non-zero exit with no failing check adds a failing check. Older binaries
  keep a skipped `doctor` check asking for an upgrade.
- `list` needs the caller's `BEADS_ACTOR` environment variable (the claiming identity); abacus
  never sets it, and shows the tool's message as the unavailable reason when it is missing.
- Override the binary with `ABACUS_BEADSLEASE_BIN`. The tool reads the coordination hub named
  in its own config, so run abacus with the environment you would use for beads-lease itself.

## Vercel Board (`board` / `vb`)

Verified against `@vercel/board` 0.54.0 (Apache-2.0) with its own `--help`, and its real `--json`
output (`internal/board/testdata`). Provides the Board domain (read) and Board post/reply (write).

- Identity (run with an environment that has no tokens, no team and no board setting): `board --help` must start with `board X.Y.Z: your Vercel team's message board` and
  contain `usage: board <command>` and the version must be 0.54.0 or newer; anything else (the unrelated npm package `vb` included) is
  reported as not Vercel Board and never run again. Lookup: `ABACUS_BOARD_BIN`, else `board`, else `vb`.
- Read argvs, element by element, optionally followed by `--url URL`: `channels list --json
  --limit N`, `read CHANNEL --json --limit N`, `read ID --json`, `tasks --json --limit N`
  (N 1-100). One Board read is at most 1 + 3 + 1 calls. Never `serve`, `tail`, `wait`, `inspect`
  (except the explicit-keypress handoff), `whoami` or any other write verb.
- Write argvs: `post CHANNEL --json [--url URL] --idempotency-key abacus-HEX16 -- TEXT` and
  `reply ID --json [...] -- TEXT`; the text follows `--` so it can never be an option.
- Failures are one JSON document on stderr: `{status, reason, message, next:[{command,argv,when}]}`.
  `reason` `missing_scope` is "no board selected" and `login_required` is "not signed in" (both
  state not-connected with the next step); `network_error`/`timeout` is a failed read (degraded).
- Truncation: more than 50 channels, threads shown for only the 3 most recently active channels,
  and a full page of 20 tasks are named in `truncated`; this does not make the domain degraded.
  A stdout answer over 4 MiB is cut off and the part is reported in `reason`.
- Unknown outcome (writes): a timeout, cancel, runner error, `network_error`/`timeout` reason,
  unreadable stdout or an exit with no failure document is "result unknown"; permission_denied,
  login_required, missing_scope, channel_not_found and validation errors are "not applied".
  Only `missing_scope` and `network_error` were captured from the real 0.54.0 CLI.
- Environment passed on (BOARD_URL is never inherited; credentials only reach an https or
  loopback target): `HOME`, `PATH`, `TMPDIR`, `USER`, `LANG`, `LC_*`, `TERM`, `NO_COLOR`,
  the XDG dirs, `BOARD_URL`, `BOARD_NAME`, `BOARD_TOKEN`, `VERCEL_AUTH_TOKEN`, `VERCEL_ORG_ID`,
  `VERCEL_AUTOMATION_BYPASS_SECRET`, proxy variables, `NODE_EXTRA_CA_CERTS`, `SSL_CERT_FILE`.
  Token values are redacted from every printed text.

board-beads is only the beads-backed backend a board can use; it stays a library (board-beads#15).

## Graph engine (bv subprocess or in-process library)

The Tasks insights (top picks, blockers, critical path, cycles, actionable and blocked counts) have
two engines behind one `core.InsightsProvider`. The report is the same `InsightsReport`, so the
Tasks tab and the JSON output do not change.

| | subprocess (default) | library (opt-in) |
|---|---|---|
| Runs | the `bv` binary: four `--robot-*` reads | bv's `pkg/analysis` inside abacus, one in-memory pass |
| Needs | `bv` (bundled with the download, or on PATH) | nothing installed; reads the tracker's `.beads/issues.jsonl` |
| Code | `internal/bv` | `internal/graph` (the only bv import is `bvanalyzer.go`) |

**Opt in.** Set `ABACUS_GRAPH_ENGINE=library` in the environment abacus starts in. It applies to
every command and tab that reads insights, and never to `--demo`.
`abacus doctor` shows a `graph engine` line saying which engine is active.

**Go back.** Unset the variable, or set it to `subprocess`. Any other value (including a typo)
keeps the default and makes Doctor warn `ABACUS_GRAPH_ENGINE: unknown value, using the default`;
it never fails a command.

**What the library reads.** Issues come from `.beads/issues.jsonl` in the tracker directory the
Tasks tab uses (`ABACUS_BD_DIR`, a tracker at or above the working directory, the saved
`tracker_dir`, or the managed workspace). When that directory is known the export must be in it:
nothing above it is read, because an export in a parent belongs to another project. With no
directory set the search starts at the working directory and walks up through its parents. An
export or `.beads` directory that is a symlink resolving outside the directory it was found in is
refused with a plain reason, never read. The reader takes
JSONL or a JSON array with `id`, `title`, `status`, `priority`, `issue_type`, `updated_at` and
`dependencies` (`{issue_id, depends_on_id, type}`; a bare id counts as a blocking dependency; an
empty type is `blocks`), and an optional `blocks` list on the blocker. It never runs `bd` and
never writes. A tracker with no export file shows the insights as unavailable and Doctor names the
reason: use the default engine for that tracker. An empty export is "no tasks yet" and bv's code
is not called.

**Stale exports.** The export is a copy the tracker writes after changes, so it can trail the
tracker's own data. abacus compares the export's modification time with the newest of
`.beads/*.db` (and its `-wal`) and the `.beads/dolt` directory. If the data is newer by more than
5 seconds, the insights header is degraded with "the issue export is 6m older than the tracker
data, so these numbers may be out of date" (the graph is still shown) and Doctor warns on an
`export` line. With no database to compare against, or a current export, the header carries the
export's age (`export 10m old`) after the counts, and Doctor shows the age. Modification times are
the only signal: a tracker that rewrites the export without changing its data looks current, and
a clock change can mislead it.

**Memory ceiling.** The whole export is read into memory and decoded before analysis, so the
reader refuses anything over 64 MiB with `export larger than 64 MiB` (Doctor shows the same
reason) instead of an unreadable-issue error. The cap bounds the file, not the working set: the
decoded issues, the graph and bv's analysis each hold their own copy, so expect several times the
file size in memory while an insights read runs. A tracker near the cap should use the default
engine.

**Debug logging.** bv's `pkg/debug` turns on a stderr logger in an `init()` when `BV_DEBUG` is
set. That runs before abacus's `main`, so clearing the variable is too late; abacus instead
switches the logger off when the engine is built and again before each analysis, so it cannot
write over the TUI. (The analysis path of v0.27.0 does not log; this is for a later bv bump.)
bv's metrics (`BV_METRICS`) only collect in memory and print nothing.

**Mapping.** "a blocks b" means b depends on a; both spellings are the same edge. Statuses
`open`, `in_progress`, `blocked`, `deferred` and `closed` map to bv's; `tombstone` reads as closed;
any other status is kept and treated as parked (neither closed nor actionable). Only a `blocks`
dependency holds an issue back; `related`, `discovered-from` and any type abacus does not know
never block. Every id, title and reason in the report is sanitized.

**What differs from the subprocess** (each is a test in `internal/graph/parity_test.go`):

1. The critical path is bv's per-issue height score (how long a chain an issue starts), not an
   ordered path; a diamond lists both middle issues with the same score. Both engines report the
   same ranked list, and abacus does not derive an ordered path.
2. A dependency cycle makes bv skip the critical path for the whole graph. Both engines leave it
   empty and set `critical_path_note`.
3. A closed issue is a satisfied blocker (its dependents become actionable). The ranked
   keystone and bottleneck lists exclude closed issues, as `bv --robot-insights` does (bv's raw,
   unscoped scores do count them; the library uses the scoped insights the CLI uses). The triage
   pick scores come from bv's triage over all issues, closed ones included, so a pick's score is
   not the keystone list's.
4. The next pick is bv's triage score (PageRank, unblocks, priority, staleness), not `bd ready`'s
   ordering. Scores can differ slightly from a `bv` run on the same tracker because the library
   reads only the fields above (no labels, estimates or created_at); the picked issue, the
   counts, the cycles and the critical-path scores agree on the parity fixtures.
5. "Blocked" is an unfinished blocker or a blocked status; deferred and custom-status issues are
   in neither count.

**Cost and licensing.** The module is pinned to the exact tag v0.27.0, so the Go directive is
`go 1.26.11` and lipgloss is a pseudo-version (see the assessment). The release binary is about
1.8 MB larger (linux/amd64, 8.39 MB to 10.19 MB). The bundled tools still ship unmodified and run
as subprocesses; separately, the `abacus` binary statically links parts of bv's Go packages (bv
v0.27.0 `pkg/analysis` and the packages it depends on). That code is under bv's license, which
ships verbatim in the release archive as `third_party/bv/LICENSE`. The archive's
`THIRD_PARTY_NOTICES.md` says so and ends with every Go module linked into the binary and its
license text (generated by `scripts/lib/golicenses.sh` from `go list -deps` and the module cache,
offline; it fails the packaging if a linked module has no license file).

Fixtures: `internal/graph/testdata` holds hand-written trackers (`*.jsonl`, including the seven
invented issues behind the recorded bv fixtures) and hand-written `bv --robot-*` documents in
`testdata/robot` (worked out from the graph shapes, not captured from a run). The only recorded bv
output used is `internal/bv/testdata`.

## Clients

GUI clients, such as a later SwiftUI app, are front-ends over the umbrella's core and use the
same contract over CLI and HTTP (the umbrella's `--json` output is the stable surface for
non-Go clients). No FFI layer is needed. Rust-to-Swift bindings (for example uniffi-rs) were considered and not
adopted: the ecosystem is Go, and the contract already crosses languages.
