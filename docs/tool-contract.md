# Tool contract

Providers are how abacus fills its fleet domains (Board, Tasks, Leases, Queues; Agents is native, below); users see domains, not
providers. `bd` (Tasks, with reads and allowlisted writes) is composed the same way as the tools
below: through its documented `--json` surface, behind an argv allowlist. Graph insights are not a
provider tool: they are computed in-process (see "Graph engine"). See `docs/design/2026-11-21-1700-feat-fleet-domains-plan.md`.

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
   timeouts, never print the token) are in Lambe's state-API doc. Mutating routes need an explicit decision.

## What the umbrella reports

Detection has four states, and one exit-code rule covers all of them:

| State | Meaning | `doctor` shows | Exit |
|---|---|---|---|
| `absent` | the binary is not found | nothing (listed by `version` only) | 0 |
| `broken` | found, but `version` errors, exits non-zero or times out | a failing `tool` check | 1 |
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

## Built-in tools: one resolver

bd, lambe, beads-lease and the Board CLI are inside the binary (`internal/embedtools`, linked with
`-tags embedtools`). `core.LocateFound` is the one resolver, first match wins: an explicit
`ABACUS_BD_BIN`, `ABACUS_LAMBE_BIN`, `ABACUS_BEADSLEASE_BIN` or `ABACUS_BOARD_BIN`; the built-in
copy, extracted to `$ABACUS_TOOLS_DIR/<content hash>/<name>` (default
`$XDG_DATA_HOME/abacus/tools`, directory 0700, files 0700, owned by the user) and checked on every
start; then, in a build without embedded tools only, the sidecar folders next to the executable and
the `PATH`. An embedded build never reaches the sidecar or the `PATH` for a built-in tool: a failed
extraction is an absent tool with the reason. Extraction is atomic and serialized by a lock file, and
a hash mismatch against the manifest inside the binary fails closed. Child tools run with a private
directory of links to the verified built-in copies first on `PATH`, so a tool that runs another
finds the built-in one. `abacus licenses` prints the notices and license texts built into the file.

## Vercel Board (`board`, built in)

Verified against `@vercel/board` 0.54.0 (Apache-2.0) with its own `--help`, and its real `--json`
output (`internal/board/testdata`). Provides the Board domain (read) and Board post/reply (write).

- Identity (run with an environment that has no tokens, no team and no board setting): `board --help` must start with `board X.Y.Z: your Vercel team's message board` and
  contain `usage: board <command>` and the version must be 0.54.0 or newer; anything else is
  reported as not Vercel Board and never run again. The check runs on the extracted built-in
  binary with a token-free environment. Lookup: `ABACUS_BOARD_BIN`, else the built-in `board`. The
  unrelated npm package `vb` is never looked for or run.
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

## Rex (`rex`), optional workspace

Provides the workspace capability (`core.WorkspaceProvider`): where a run, lease, task or queue
row lives as a terminal session. It is optional and pre-1.0 (the Rex CLI is closed source), and
nothing else depends on it. The UI says "workspace"; "Rex" appears only in Doctor, how-to-connect
and error text (and in commands you are shown to copy).

**Verified** against the Rex server API reference (`help.json`, API revision 13) as a local file,
never against a running Rex (no `rex` was run to write this):
- `session.list` returns `{"sessions":[{"session_id","label"}]}` (fixture `ls-help-example.json` is
  its response example, verbatim). `server.version` has `api_revision` (the newest revision the
  server supports) and `api_min_revision` (the oldest).
- Rex advertises API revisions 10 to 13 and the API has only added methods so far.

**Unverified assumptions** (second-hand: Lambe's working code and research notes, never re-run
here). Each is a place a real Rex could differ, so check them before relying on the layer:
- `rex --autostart=false ls --json` prints exactly the `session.list` result and exits 0 when a
  server answers and 4 when none is reachable; 3 means more than one server answers; any other
  non-zero exit is an error even if it printed JSON.
- `rex version --json` prints the `server.version` shape (`api_revision`, `api_min_revision`,
  `rex_version`).
- The argv order `rex -S HOST --autostart=false ls --json` (global flags before the subcommand,
  `-S` first) is accepted, and `--autostart=false` is honored together with `-S`.
- `REX_SESSION` and `REX_SERVER` are set in every Rex terminal; Lambe treats either as "inside
  Rex" and otherwise probes with a 500 ms deadline. abacus does the same.
- Session labels follow Lambe's conventions (a window is `Lambe · <ISSUE>`, a session is the
  agent's name). The machine a run carries equals the Rex host name (not enforced anywhere).
- A bare `rex` opens the workspace on this machine (used only as a command to copy).

**What abacus runs**, element by element, nothing else (the argv allowlist test lists refused forms):
`rex --autostart=false ls --json`, `rex --autostart=false version --json` and
`rex -S HOST --autostart=false ls --json`. HOST is a name, `user@name` or `name:port`: dot-separated
labels of letters, digits, hyphens and underscores (a label starts and ends with a letter or digit),
an optional user of up to 32 characters, an optional port from 1 to 65535, at most 253 characters in
all, never a leading dash, never an IPv6 literal. `--autostart=false` is always there, so a read can
never start a server. Never `new`, `send`, `capture`, `api call`, `window`, `server`, `config`, `do`
or `hosts`.

**Network egress.** `rex -S HOST` makes Rex talk to HOST, which may mean opening an ssh connection
to it. The machine names come from run data (any program can write them), so abacus contacts only
(a) this machine (compared by short host name, read without `-S`) and (b) hosts you list in
`ABACUS_REX_HOSTS` (comma-separated, validated as above, at most 16; an invalid entry is dropped,
never widened). A run on any other machine gets a placement with match `host-not-configured`, no
session, and a copy-only plan (the handoff command to run yourself). At most 8 hosts are read per
call, in parallel.

**Environment.** Rex runs with `HOME`, `PATH`, `XDG_RUNTIME_DIR`, `XDG_CONFIG_HOME`,
`XDG_STATE_HOME`, `XDG_DATA_HOME`, `XDG_CACHE_HOME` and `REX_*` only. Proxy settings,
`SSH_AUTH_SOCK`, `DISPLAY`, `WAYLAND_DISPLAY` and every token are deliberately not passed, so `-S` to
a host that needs a key from your ssh agent will not authenticate unless you opt in:
`ABACUS_REX_PASS_ENV=SSH_AUTH_SOCK` (comma-separated upper-case names, at most 8; `LD_*`, `DYLD_*`,
`BASH_ENV` and `ENV` are refused). Output is capped at exactly 1 MiB per call.

**Time and ownership.** A workspace read has a one second budget for the whole call, however many
hosts (core owns the budget). core runs it concurrently with the Board and insights reads, so a
slow Rex cannot lengthen them; a read that runs out is a degraded report. Each call also has a
2 s deadline (the probe 500 ms). The adapter owns caching: each host's session list is reused for
3 s within one process (a TUI refresh, or several reads in one command), so a hung Rex costs one
call per 3 s, not one per read; a reused list marks the report `stale`. A new detection clears it.

**Detection and states.** No `rex` binary (`ABACUS_REX_BIN`, bundled, then PATH): absent, and quiet
(not in `version`, Doctor or the tool list). Binary but no server (the probe, or `version`, exits 4):
ready, with a `skip` check ("no Rex server running (optional)"); a read then says "not connected"
with how to start one. Incompatible, which Doctor shows as a `warn` and `version` lists: a probe
that cannot run, times out, exits other than 0 or 4 (including 3, more than one server), or prints
an unrecognized shape; a failing or unreadable `version --json`; or an API revision range that does
not overlap ours. The gate is on interval overlap: compatible iff `api_revision >= 10` and
`api_min_revision` (0 when absent) `<= 13`. `{14, min 10}` is ready; `{14, min 14}` is incompatible.
A revision or minimum that is a string, a fraction, null or negative is reported as "not a whole
number", a missing `api_revision` as missing. Never `fail`, so Rex never changes the exit code or
says "needs attention".

**`ls --json` shape.** `{"sessions": null | [{"session_id": string, "label": string}]}`. Anything
else (a missing or mistyped `sessions`, a session with no id) is "unrecognized rex ls output": the
read is degraded, never a healthy empty list. At most 500 sessions per host are looked at; more
sets `truncated` on the report and `500+ sessions` in status.

**Placement.** Only `ls --json` is read, so only session labels are visible: window (tab) labels
need `session.view` and are deferred, so `tab` is empty. A label matches an item id exactly (one of
the label conventions, by default the id, `Lambe · <id>`, `Lambe: <id>` and `Lambe - <id>`, as
`{id}` templates in `Options.LabelConventions`) or by containing it as a whole word (`LAM-1` does not
match `LAM-12`, and `bd-1` does not match `bd-1.2`: a dot followed by a letter or digit continues an
id). Ids of one character never match. Matching sees the whole sanitized label; only what is shown
is cut to 64 characters. One claimant is a placement; two or more is an ambiguity with every
candidate (up to 8, then `truncated`) and nothing is picked. Labels come from other programs: control,
bidi, zero-width and every other Unicode format character are removed, in the adapter and again in
core.

### Workspace JSON

`status --json` gains a top-level `domains.workspace` only when there is something to report
(additive; `schema_version` stays 1; byte-identical without a provider):

```
"workspace": {
  "state": "connected" | "not-connected" | "degraded",
  "reason": "...", "how_to_connect": "...",          // clipped to 300 and 200 characters
  "placements": [ { "id": "pl-<12 hex>", "item": {"kind","id","host"}, "host", "session", "tab",
                    "label", "match": "exact" | "label" | "host-not-configured" | <future>,
                    "open": { "rungs": [ {"name": "handoff", "argv": [...], "interactive": true} ],
                              "copy": "...", "note": "..." } } ],
  "ambiguous": [ { "item": {...}, "candidates": [ <placement> ], "truncated": true } ],
  "stale": true,      // some of this is a short-lived cached read
  "truncated": true   // something was cut: sessions, placements, or one with an unsafe host name
}
```

`open.rungs` is always a list (never null), possibly empty. A rung is tried in order, only on an
explicit keypress and never by core; `interactive` says it takes over the terminal. A rung or `copy`
that sanitizing would change is dropped, never altered. Hosts are never clipped: one that is too
long or would change is dropped with its placement and `truncated` is set. Unknown `kind` and `match`
values pass through.

**Open plan.** Rung `handoff`: `rex -S <machine>` (the located binary's path when found), the same
argv Queues' `o` runs today, then the same command to copy. A machine that was not configured for
reads: no rung, the command to copy, and a note. A session on this machine (no host recorded): no
rung (focusing or selecting a session is unverified), the command to open the workspace as `copy`
and a note naming the session to pick. Core and the adapter only build the plan; a front-end runs it
on an explicit keypress.

**Verified against a real Rex** (0.1.0-preview.19, API revision 14, run as an isolated server on a
temporary socket; fixtures are in `internal/rex/testdata`): `ls`, `version`, `block ls -s`, `window ls
-s`, `capture -s -b --trim`, `focus`, `run`, `split`, `new`, `window new` (each with `--json` where
it exists) and `events --json`. The supported revision window is 10 to 14.

**Pane placements.** A pane whose own label names an item (a pane opened by "work on this" carries the
item id as its label) is the item's placement, with the session that holds it; `Placement.block` is
additive. `block ls -s` is read per session (at most 24 sessions, 4 at a time, 600 ms, best effort).
Two panes naming one item are an ambiguity, never a pick.

**Writes** go through the shared gate (plan, confirm, single use, audit before and after; a timeout,
signal or unexpected exit is "result unknown"). Exact argv shapes, always `--autostart=false`, nothing
that sends input, closes, moves or renames, `api call` unreachable:
`[-S HOST] focus -s SESSION BLOCK`; `run -s SESSION --label ID [-c DIR] --keep-open --json -- CMD...`;
`split -s SESSION [-b BLOCK] --split=right --label ID [-c DIR] --keep-open --json -- CMD...`;
`new NAME --json --window TAB [-c DIR] --keep-open -- CMD...`;
`window new -s SESSION TAB [-c DIR] --keep-open [--focus=false] --json -- CMD...`. Only focus takes `-S`,
and only for a host in `ABACUS_REX_HOSTS`; work and the layout use the current session
(`REX_SESSION`) or a session they create. The agent command (`config set agent_command
'["prog","Work on {task_id}: {title}"]'`) keeps every word one argument; a title that would start a word
with `-` is refused.

**Terminal reads** (`capture`) keep the end of the output (128 KiB, 200 lines of 240 characters),
sanitized, within a 2 s deadline, and only for a visible detail pane.

**Change feed.** `events --json` (one process, own process group, killed on exit) invalidates the
session and pane caches on session, pane, tab and rename events only (client connections, which every
rex command makes, are ignored so reads cannot cause reads). While it is connected a read is reused up to
60 s; otherwise 3 s polling as before. It reconnects with back-off (1 s to 1 min, 5 min if it never
connected) and never starts a server.

**Not done**: `session.select` (needs a client), window labels as placements, `rex hosts`.

**Deferred** until someone runs them against a real Rex and records argv and JSON: `rex window
focus`, `rex window new`, `session.select` (also needs a client with Remote control on; `rex-tui`
support is unknown), window labels via `session.view`, `rex hosts`, program status, spawning. A
rung is data (a name and an argv), so each is a new value, not a new type.
## Agents (native feeds, plus an optional built-in monitor)

The Agents domain has no required tool. Any module that implements `core.AgentFeed`
(`Agents(ctx) ([]Agent, AgentsExtra, error)`) is a feed; `core.ReadAgents` runs every ready feed side by
side (each under a 10 s deadline), merges the answers by harness and pid or session id, and turns a
failed feed into a note instead of failing the domain. The report is `AgentsReport` (`schema_version`
1; additive only; `agents` is `[]`, never null). Pointer fields are nil when unknown. `state` is a
closed set and `source` says how it was learned.

- Native feed (`internal/agents`, module `agents`): process list, session-file tails and tmux panes,
  every source injected, nothing written. See `docs/agents.md` for limits and state inference.
- Live monitor (module `abtop`, built in, optional): argv allowlist is exactly `--json`; deadline 15 s;
  output capped at 8 MiB; environment reduced to HOME, PATH, USER/LOGNAME, TMUX*, XDG_*, TMPDIR, LANG,
  CLAUDE_CONFIG_DIR and CODEX_HOME. Output is read tolerantly (unknown fields ignored, missing ones
  unknown). It has no schema version, so the shape of the pinned release is the contract, and anything
  without a `sessions` list degrades to a note. No write path exists.

## Bulk task changes

One verb on up to 25 tasks, through the same gate as a single write: `core.PlanBulk` plans one
single-task change per task (so each has its own exact argv from the same allowlist; a task the change
no longer fits is left out and named in `skipped`), the front end shows the whole plan and asks once,
and `Applier.ApplyBulk` runs it. Nothing is atomic. Before each task is written it is read again and
checked (the status it was planned against, then the edit's own check); a task that changed or cannot be
read is skipped, never written. A failure does not stop the rest; a timeout or cancel makes that task
and every task after it `unknown`; an audit log that cannot be written stops the batch (`not_run`), so
nothing is sent unrecorded. The batch plan and each of its tasks are single use. Every audit line
carries the same `batch` id (additive field of the audit entry). `abacus tasks bulk --json` prints
`{schema_version: 1, batch_id, op, applied, skipped, failed, unknown, not_run, items: [{id, outcome,
detail, argv}]}`; the exit code is 0 only when every task was applied: tasks left out at plan time (already closed, already that priority, ...) are in `items` as `skipped`, so a batch with any of them exits 1. `--ids` must be whole task ids: one the tracker resolves to another id (a prefix) is refused. The verbs are close, status,
priority, label add or remove and assign; never delete, comment or dependency edits.

## Graph engine (in-process)

The Tasks insights (top picks, blockers, critical path, bottlenecks, influence, cycles, actionable and blocked counts) are
computed inside abacus by bv's `pkg/analysis`, in one in-memory pass, behind
`core.InsightsProvider` (`internal/graph`; the only bv import is `bvanalyzer.go`). It is the only
engine: there is no switch, no `ABACUS_GRAPH_ENGINE`, no bv subprocess and no bv program in the
archive. It needs nothing installed beyond `bd` (already required for Tasks), and it never
applies to `--demo`. `abacus doctor` shows `insights` and `export` lines for it.

**What the engine reads.** Issues come from `.beads/issues.jsonl` in the tracker directory the
Tasks tab uses (`ABACUS_BD_DIR`, a tracker at or above the working directory, the saved
`tracker_dir`, or the managed workspace). When that directory is known the export must be in it:
nothing above it is read, because an export in a parent belongs to another project. With no
directory set the search starts at the working directory and walks up through its parents. An
export or `.beads` directory that is a symlink resolving outside the directory it was found in is
refused with a plain reason, never read. The reader takes
JSONL or a JSON array with `id`, `title`, `status`, `priority`, `issue_type`, `updated_at`, `labels` and
`dependencies` (`{issue_id, depends_on_id, type}`; a bare id counts as a blocking dependency; an
empty type is `blocks`), and an optional `blocks` list on the blocker. It never writes.

**A tracker without an export file.** bd's default setup keeps no `.beads/issues.jsonl` (its
auto-export is off). When no export file is found, the engine reads the tracker itself through
the bd adapter's read allowlist: `bd [-C DIR] --readonly export`, one fixed argv that prints the
issues as JSONL on stdout and writes nothing (records that are not issues are skipped). A file,
when there is one, wins. If that read fails the insights show as unavailable and Doctor names the
reason. An empty tracker is "no tasks yet" and bv's code is not called.

**Stale exports.** An export file is a copy the tracker writes after changes, so it can trail the
tracker's own data (a live `bd export` read has no such lag and no age line). abacus compares the export's modification time with the newest of
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
file size in memory while an insights read runs. A tracker near the cap needs a bigger limit in a later
release; today it shows the insights as unavailable.

**Debug logging.** bv's `pkg/debug` turns on a stderr logger in an `init()` when `BV_DEBUG` is
set. That runs before abacus's `main`, so clearing the variable is too late; abacus instead
switches the logger off when the analyzer is built and again before each analysis, so it cannot
write over the TUI. (The analysis path of v0.27.0 does not log; this is for a later bv bump.)
bv's metrics (`BV_METRICS`) only collect in memory and print nothing.

**Mapping.** "a blocks b" means b depends on a; both spellings are the same edge. Statuses
`open`, `in_progress`, `blocked`, `deferred` and `closed` map to bv's; `tombstone` reads as closed;
any other status is kept and treated as parked (neither closed nor actionable). Only a `blocks`
dependency holds an issue back; `related`, `discovered-from` and any type abacus does not know
never block. Every id, title and reason in the report is sanitized.

**Where bv's definitions differ from what a reader may expect** (each is a test in
`internal/graph/parity_test.go`, which also compares the engine with static bv output):

1. The critical path is bv's per-issue height score (how long a chain an issue starts), not an
   ordered path; a diamond lists both middle issues with the same score. abacus reports that ranked
   list and does not derive an ordered path.
2. A dependency cycle makes bv skip the critical path for the whole graph. The engine leaves it
   empty and sets `critical_path_note`.
3. A closed issue is a satisfied blocker (its dependents become actionable). The ranked
   keystone and bottleneck lists exclude closed issues, as `bv --robot-insights` does (bv's raw,
   unscoped scores do count them; the engine uses the scoped insights bv's own CLI uses). The triage
   pick scores come from bv's triage over all issues, closed ones included, so a pick's score is
   not the keystone list's.
4. The next pick is bv's triage score (PageRank, unblocks, priority, staleness), not `bd ready`'s
   ordering. Scores can differ slightly from a `bv` run on the same tracker because the engine
   reads only the fields above plus labels (no estimates or created_at); the picked issue, the
   counts, the cycles and the critical-path scores agree on the parity fixtures.
5. "Blocked" is an unfinished blocker or a blocked status; deferred and custom-status issues are
   in neither count.

**Cost and licensing.** The module is pinned to the exact tag v0.27.0, so the Go directive is
`go 1.26.11` (CI and the release workflow read the toolchain from `go.mod`) and lipgloss is a
pseudo-version (see the assessment). bv is linked, not bundled: the `abacus` binary statically
links parts of bv's Go packages (bv v0.27.0 `pkg/analysis` and the packages it depends on). That
code is under bv's license (MIT with an additional AI-provider clauseit), which ships verbatim in the release archive as `third_party/bv/LICENSE`, copied from the
pinned Go module and checked against the sha256 in the `linked_licenses` entry of
`bundled-tools.json` (the packaging fails on any mismatch, or if the linked version is not the
pinned one). The archive's `THIRD_PARTY_NOTICES.md` lists bv as a linked library and ends with
every Go module linked into the binary and its license text (generated by
`scripts/lib/golicenses.sh` from `go list -deps` and the module cache, offline; it fails the
packaging if a linked module has no license file).

Fixtures: `internal/graph/testdata` holds hand-written trackers (`*.jsonl`, including the seven
invented issues behind the recorded bv fixtures) and hand-written `bv --robot-*` documents in
`testdata/robot` (worked out from the graph shapes, not captured from a run). The only recorded bv
output used is `testdata/bv`, static JSON whose provenance is in its README; no bv program is run
anywhere in the repository or its tests.

## Clients

GUI clients, such as a later SwiftUI app, are front-ends over the umbrella's core and use the
same contract over CLI and HTTP (the umbrella's `--json` output is the stable surface for
non-Go clients). No FFI layer is needed. Rust-to-Swift bindings (for example uniffi-rs) were considered and not
adopted: the ecosystem is Go, and the contract already crosses languages.
