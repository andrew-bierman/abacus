# Tool contract

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
is shown with an `unavailable` reason and does not change the exit code. Every tool subprocess
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
| Selection | `--lambe-config FILE` or `ABACUS_LAMBE_CONFIG` | `doctor -config`, descriptor `config_path` match |

Decoding is tested against the fixtures vendored in `internal/lambe/testdata/contract/v1/`.

## Known gaps in the reference (filed upstream)

- `lambe status` has no `--json` form: andrew-bierman/lambe#51.
- No command prints the resolved state dir or descriptor location, so clients re-derive the
  default and cannot see an override: andrew-bierman/lambe#52.

## Second reference: beads-lease (read-only, partial)

beads-lease (leases on the Beads hub) meets part of the contract: a `--json list` read with a
`schemaVersion`, and stable exit codes (0, 1, 2 contention, 64 invalid input, 130 interrupted).
abacus uses exactly two commands, `version` and `--json list`, behind an allowlist that rejects
every other argv (no acquire, release, heartbeat, run or sync). Leases show as held, stale or
free.

- Detection parses the plain `version` line, because there is no `version --json`: the
  `api_version` stays unknown (andrew-bierman/beads-lease#6).
- There is no `doctor`, so abacus lists a skipped check (andrew-bierman/beads-lease#7).
- `list` needs the caller's `BEADS_ACTOR` environment variable (the claiming identity); abacus
  never sets it, and shows the tool's message as the unavailable reason when it is missing.
- Override the binary with `ABACUS_BEADSLEASE_BIN`. The tool reads the coordination hub named
  in its own config, so run abacus with the environment you would use for beads-lease itself.

## Not yet composable

- board-beads is an npm library (a Vercel Board backend) with no CLI. Filed
  andrew-bierman/board-beads#15 for a thin contract CLI, or documenting the Vercel Board `vb`
  CLI as the surface to compose; Board compatibility is a priority for a later module.

## Clients

GUI clients, such as a later SwiftUI app, are front-ends over the umbrella's core and use the
same contract over CLI and HTTP (the umbrella's `--json` output is the stable surface for
non-Go clients). No FFI layer is needed. Rust-to-Swift bindings (for example uniffi-rs) were considered and not
adopted: the ecosystem is Go, and the contract already crosses languages.
