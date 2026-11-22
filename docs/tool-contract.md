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

## Clients

GUI clients, such as a later SwiftUI app, are front-ends over the umbrella's core and use the
same contract over CLI and HTTP (the umbrella's `--json` output is the stable surface for
non-Go clients). No FFI layer is needed. Rust-to-Swift bindings (for example uniffi-rs) were considered and not
adopted: the ecosystem is Go, and the contract already crosses languages.
