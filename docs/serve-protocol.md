# Serve protocol (`abacus serve --stdio`)

A long-lived JSON-RPC 2.0 server for a native client (the planned SwiftUI app, a script, a test
harness). It speaks only on stdin and stdout, one JSON message per line, and listens on no network
port. It returns the same typed reports the commands print with `--json` (it calls the same core
code, not the commands) and it writes only through the same gate as every other front end: plan,
confirm, single-use apply, fail-closed audit, exact argv allowlists, "result unknown" for a timeout or
cancel. This document is the protocol; `docs/tool-contract.md` has the report and error contracts it
reuses, and `docs/schema/v1` the JSON Schemas of the typed results.

Protocol version: **1** (`core.ServeProtocolVersion`). A new method, field or notification is
additive; a breaking change raises the number, and `initialize` is the one place a mismatch is decided.

## Running it

```
abacus serve --stdio [--demo] [--all] [--lambe-config FILE]
```

- `--stdio` is required: it is the only transport. There is no flag for a port or a socket.
- It runs only when stdin is a pipe or a socket (what a parent process gives a child). A terminal, a
  regular file and `/dev/null` are refused with exit 2 and nothing on stdout.
- It drops `DYLD_*`, `LD_*`, `BASH_ENV` and `ENV` from its own environment, and core scrubs each tool's
  environment again when it starts one (as every command does). The client sets the tracker with
  `ABACUS_BD_DIR` in the child's environment, as for any command.
- It ignores SIGPIPE: a client that closes its end of stdout is a write error that ends the server
  (exit 1), never a signal that kills it mid-response. SIGINT and SIGTERM and end of input end it
  cleanly (exit 0), after the shutdown rules below.
- stdout carries only protocol lines. stderr gets the first-run "preparing" notice and, when something
  goes wrong, one plain line (a handler failed, the client stopped reading). It never carries a request, a
  parameter or a result: task text, tokens and argv values are not logged.
- `--demo` serves invented fixture data and writes nothing real; the tests use it.

## Framing and limits

One message per line, UTF-8, compact JSON, `\n` terminated. A client sends requests and notifications;
the server sends responses and notifications. A blank line is ignored; an unterminated last line is not a
request. The server never writes a pretty-printed or multi-line message.

| Limit | Value | Over it |
|---|---|---|
| Request line | 1 MiB | the line is read to its end and thrown away (never buffered past the limit), error -32600 `usage` with id `null`; the server keeps serving |
| Nesting | 32 levels | -32600 `usage` |
| Result | 16 MiB | the request fails with `failed` and says to narrow it (a query, a smaller limit) |
| Requests in flight | 8 | the next one is refused at once with `refused` ("too many requests in flight"), never queued without bound |
| Plans held | 64 | the oldest is pushed out; applying it gives `changed` |
| Plan age | 10 minutes | applying it gives `changed` |
| Deadline | 30 s default, 10 min most | see "Deadlines" |

All of them are announced in `initialize`'s `capabilities.limits`.

A message must be valid UTF-8 JSON. Refused with `id: null` (the id cannot be trusted): invalid JSON
(-32700), a batch (a JSON array; -32600, "one request per line"), a non-object, too deep a nesting, an
unknown top-level member (only `jsonrpc`, `id`, `method`, `params` and `deadline_ms` exist). An `id` is a
string of at most 128 bytes or an integer of at most 2^53; `null`, fractions and objects are refused. `params`,
when present, is an object, and an unknown field in it is an error (`usage`), never ignored. A request
whose id is already in flight is refused (the original keeps running).

## Errors

A failed request is a JSON-RPC error: `error: {code, message, data}`.

- `error.code` is a JSON-RPC number, for generic clients: -32700 parse, -32600 invalid request,
  -32601 unknown method, -32602 invalid params, -32603 internal error, -32002 not initialized, -32800
  request cancelled, -32000 everything else. Branch on `data.code`, not on this.
- `error.message` is plain, sanitized text for a person.
- `error.data` is the error object every failing command prints (`{code, message, retryable?,
  unknown_outcome?}`, the closed set of `docs/tool-contract.md` "Errors: stable codes": `usage`,
  `not_connected`, `needs_setup`, `not_found`, `refused`, `changed`, `already_applied`,
  `unknown_outcome`, `applied_mismatch`, `audit_failed`, `timeout`, `unsupported`, `failed`), plus, for a
  write, `plan_id`, `plan_digest` and `outcome` (below). Schema: `serve-error.json` (`schema_version` is
  not repeated there). A code a client does not know means `failed`.

| Situation | `data.code` |
|---|---|
| bad params, unknown method, bad query, malformed or oversized input | `usage` |
| a read cancelled by the client, over its deadline, or cut by shutdown | `timeout` (retryable) |
| a domain the call needs is not connected | `not_connected` (the message says how to connect) |
| the task, message or plan id does not exist | `not_found` |
| the plan no longer fits (task moved, plan expired, digest differs) | `changed` (retryable: plan again) |
| the plan was already used | `already_applied` |
| a write may or may not have happened | `unknown_outcome` (`unknown_outcome: true`, never retryable) |
| the write landed but is not what was asked | `applied_mismatch` |
| the audit log could not be written, so the write was blocked | `audit_failed` |
| too many requests in flight, a repeat board post, a request before `initialize` | `refused` |

A domain that is not connected is not an error for the read that reports it (`board`, `leases`,
`queues`, `tasks.summary`, `agents`, `status`): the report says `state: "not-connected"` with
`reason` and `how_to_connect`, as the command does. It is an error (`not_connected`) for a method that
needs it to answer (`tasks.list`, `tasks.show`, a plan).

## Handshake and lifecycle

`initialize` must come first; every other request before it fails with -32002 `refused`, and
`shutdown` is always answered. It is handled in order with the lines around it.

```
-> {"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocol_version":1,"client":{"name":"MyApp","version":"1.0"}}}
<- {"jsonrpc":"2.0","id":1,"result":{"abacus_version":"0.5.0","capabilities":{"limits":{...},"methods":[...],"notifications":["tasks.changed","workspace.changed","engine.degraded"],"topics":["tasks","workspace"]},"protocol_version":1,"schema_version":1}}
```

`protocol_version` is required and must be 1 (else `unsupported`, and the gate stays shut). The result
(`serve-initialize.json`) carries `schema_version` (the report contract this server prints),
`abacus_version`, and `capabilities`: every method name, the notifications, the subscribable topics and
the limits. A client treats a method missing from `methods` as unavailable.

`shutdown` (no params): the server stops accepting requests (new ones get `refused`), cancels reads in
flight, lets a write in flight finish for up to 5 seconds (past that it is cancelled, and a cancelled
write is `unknown_outcome`), answers every request in flight, then answers `shutdown` with `{}` plus
`schema_version`, stops its watchers and exits 0. End of input does the same without the answer.

## Request ids, cancellation, deadlines, concurrency

- Responses carry the request's id and may arrive in any order. At most one request per id is in
  flight.
- **Cancel** with a notification (no id, no answer): `{"jsonrpc":"2.0","method":"$/cancelRequest","params":{"id":7}}`.
  The cancelled request is answered with error -32800 and `data.code` `timeout` if it was a read or had
  not been claimed yet. A cancel that arrives after an `apply` has started the write cannot un-write it:
  the answer is `unknown_outcome`. An id nobody is serving is ignored.
- **Deadlines**: a request may carry `"deadline_ms": N` (1 to 600000, a top-level member). Without it
  a read or a plan has 30 seconds and an `apply` has the batch time (5 minutes) plus 30 seconds. A read
  that runs out of time is `timeout`; a write that does is `unknown_outcome`.
- **Concurrency**: requests run concurrently up to the in-flight limit, with two rules the TUI also
  follows: one read per domain at a time (a second `tasks.*` read waits for the first, under its own
  deadline), and one write at a time (applies are serialized; a second waits, and a cancel while it
  waits leaves its plan untouched).
- A panic in a handler is answered with -32603 `failed` ("internal error"), logged without its value
  (it could hold task text), and the server keeps running.

## Methods: reads

Parameters are the command's flags as named fields (snake_case, all optional unless noted). Every
result is the typed report the command prints with `--json`, on one line, with `schema_version`; the
tests compare the bytes with the command's output. The schema is the same file the command's `--json`
is described by.

| Method | Params | Result (schema) | Command |
|---|---|---|---|
| `version` | none | `report` | `version --json` |
| `doctor` | none | `report` | `doctor --json` |
| `status` | none | `report` | `status --json` |
| `board` | none | `board` | `board --json` |
| `leases` | none | `leases` | `leases --json` |
| `queues` | none | `queues` | `queues --json` |
| `agents` | `harness`, `state`, `repo`, `query` | `agents` | `agents --json` |
| `tasks.summary` | none | `tasks` | `tasks --json` |
| `tasks.list` | `query`, `sort`, `reverse`, `preset` | `tasks-list` | `tasks list --json` |
| `tasks.tree` | the same | `tasks-tree` | `tasks list --tree --json` |
| `tasks.board` | `query`, `group`, `closed` | `tasks-board` | `tasks board --json` |
| `tasks.show` | `id` (required) | `tasks-show` | `tasks show ID --json` |
| `tasks.graph` | `id` or `whole`, `depth` (1 to 6, default 2) | `tasks-graph` | `tasks graph --json` |
| `tasks.history` | `id` (required), `limit` | `tasks-history` | `tasks history ID --json` |
| `search` | `query` (required), `domains`, `limit` | `search` | `search QUERY --json` |
| `workspace` | none | `workspace` | `status --json`, `domains.workspace` (an empty report when there is none) |
| `setup.state` | none | `setup` (steps and `plan_digest`; nothing is applied) | `setup --json` |
| `config.show` | none | `config-show` | `config show --json` |

The provider detection behind the reads is reused for 30 seconds and dropped after a write. There is no
snapshot id: each request is one read of its domain, as a command is, so two requests can show two
instants (a client that needs one instant uses one request: `tasks.list`, `tasks.board` and
`tasks.graph` each read the whole tracker once).

## Methods: writes

A write is two requests. `plan.*` validates the change, reads what the plan is guarded by, builds the
exact command core would run and **holds** it under a server-made single-use plan id (`plan_` and 32
random hex digits). `apply` claims the id once. The client never sends an argv, a path or a plan; a field
that is not one of the verb's own is refused.

### Plans

| Method | Params | Plans |
|---|---|---|
| `plan.task.create` | `title` (required), `description`, `priority`, `type` | `tasks create` |
| `plan.task.update` | `id`, `title`, `description`, `priority`, `status` | `tasks update` |
| `plan.task.close` | `id`, `reason` | `tasks close` |
| `plan.task.reopen` | `id`, `reason` | `tasks reopen` (refused unless the task is closed) |
| `plan.task.label-add`, `plan.task.label-remove` | `id`, `label` | `tasks label add\|remove` |
| `plan.task.assign` | `id` and exactly one of `assignee`, `clear: true` | `tasks assign` |
| `plan.task.priority` | `id`, `priority` | `tasks priority` |
| `plan.task.dep-add`, `plan.task.dep-remove` | `id` and exactly one of `blocked_by`, `blocks` | `tasks dep add\|remove` |
| `plan.task.comment` | `id`, `text` | `tasks comment` |
| `plan.tasks.bulk` | `ids` (at most 25, whole ids), `op` (`close`, `status`, `priority`, `label-add`, `label-remove`, `assign`), `value`, `clear` | `tasks bulk` |
| `plan.board.post` | `channel`, `text`, `again` | `board post` |
| `plan.board.reply` | `id`, `text`, `again` | `board reply` |
| `plan.cancel` | `plan_id` | discards a held plan: `{plan_id, cancelled: true}` |

Planning runs nothing and writes nothing (it reads the task or tracker it needs, as the commands do). A
change core refuses (a bad id, a status out of the set, a control character in a comment, a no-op) is a
`usage` error. A board post made again within ten minutes of the same content (the audit log says so) is
`refused` unless `again: true`, as `board post --again`.

The result is `serve-plan.json`:

```
{"plan_id":"plan_...", "plan_digest":"<64 hex>", "kind":"task|bulk|board", "op":"priority",
 "lines":["set the priority of task ac-12", ...], "context":[...], "argv":["bd","priority","ac-12","--priority=3"],
 "items":[...], "skipped":[...], "expires_at":"2026-11-24T09:10:00Z", "schema_version":1}
```

`lines` and `context` are what a confirm screen shows; `argv` (a batch has it per item) is **display only**
(the same command `DisplayArgv` shows), never executed by a client and never accepted back. `plan_digest` is
the digest `--dry-run --json` prints for the same change and `--expect-digest` takes: a test checks they
match for every verb.

### Apply

`apply {plan_id, expected_digest?}`:

1. `plan_id` must be a plan id; `expected_digest`, when given, must be 64 hex digits (an empty or
   malformed one is `usage`, never "no check").
2. One write at a time: the call waits its turn (cancelling while it waits changes nothing).
3. The plan is **claimed**: removed from the held set in one atomic step, so a second `apply` of the
   same id (concurrent or later) gets `already_applied` and nothing runs. A digest that is not the
   plan's is `changed` (nothing written, nothing audited, the plan stays held: the client had a different
   one). An expired or pushed-out plan is `changed`; an id never issued, or cancelled, is `not_found`.
4. The task is read again and checked against what the plan was made from (the status or assignee
   guard, a dependency cycle, a reopen of a task that is no longer closed). A task that moved is `changed`,
   before anything is audited or run, and the claimed plan is spent (`changed` again if reused).
5. Core's gate runs it: the attempt is recorded in the audit log first (a log that cannot be written is
   `audit_failed`, nothing ran, the plan is spent as in step 4), then the one allowlisted argv, then
   the outcome (`ok`, `error`, `unknown`, `applied-mismatch`).

Success is `serve-applied.json`: `{plan_id, plan_digest, kind, outcome: "applied", task | board | bulk}`.
Every failure after the claim carries the plan and what became of it in `data`:

| `data.outcome` | `data.code` | Meaning | The client |
|---|---|---|---|
| `applied` | (a result) | it ran and is what was asked | refresh |
| `not_applied` | `failed`, `audit_failed`, `refused`, ... | it did not run | plan again if it still makes sense |
| `unknown` | `unknown_outcome` | it may or may not have happened (timeout, cancel, killed, an answer that could not be matched) | show "result unknown", offer only a refresh, never retry |
| `applied_mismatch` | `applied_mismatch` | it landed, but not as asked | refresh |
| `already_applied` | `already_applied` | the plan was used | nothing |
| `changed` | `changed` | the plan no longer fits; nothing was written | plan again |

A batch is a result, not an error, once it ran: `outcome` is `applied` (every task written), `partial`
(some were skipped or failed: read `bulk.items`) or `unknown` (a task's result is unknown, and the tasks
after it were not started: read `bulk.items`), and `bulk` is the same report `tasks bulk --json` prints,
with the `batch_id` every audit line of the batch carries. A batch that could not start at all is an error.

What the protocol does not give: it does not make two *different* processes unable to apply the same
change. The id's single use holds inside one server; across processes the digest, the audit log and the
tracker's own guards remain the backstops, as for the command line.

Not exposed: `setup` and `setup --repair` (the setup state is readable; applying it stays a command),
`config set`, `rex layout` and the workspace actions (focus, run, split), `tasks delete` and everything
else the gate never offered.

## Notifications

A client subscribes to topics; the server sends a notification when something changed. A notification says
that, never what: refetch. Losing one costs one refetch, and the server never queues them without
bound.

`subscribe {topics: [...]}` and `unsubscribe {topics: [...]}` (topics: `tasks`, `workspace`) answer
`{active: [...], unavailable: {topic: reason}}`. Subscribing twice is harmless (one watcher per topic).

| Notification | Params | When |
|---|---|---|
| `tasks.changed` | `{topic, seq, reason}` | the tracker's change fingerprint moved (`reason: "fingerprint"`), or, for a tracker with no cheap fingerprint, a full read differs from the last (`"snapshot"`) |
| `workspace.changed` | `{topic, seq, reason}` | the terminal workspace's own event feed reported a relevant change |
| `engine.degraded` | `{topic: "engine", seq, reason}` | the tracker could not be checked three times in a row (once per episode) |

The tasks watcher is the TUI's live refresh without the screen (`taskview.Poller`): a check every 5
seconds, one at a time, a longer wait (up to 60 seconds) after a slow or failed one; the first look is a
baseline, not a change. `seq` counts per topic. At most one notification per topic is queued: while it
waits to be written, further changes are covered by it. If the output queue is full the watcher keeps its
old baseline and tries again at the next look, so a change is never lost, only late. Watchers end when the
server does.

## Backpressure

Output goes through one bounded queue (256 messages) and one writer. A response is never dropped: the
request that made it waits for room, and its read or write has already finished. A notification never waits.
If a single write to stdout stays blocked for 30 seconds (a client that does not read) the server stops
with an error (exit 1) instead of holding every request, and a write error ends it too. Input is read
line by line within the size limit, so a slow or hostile writer cannot make the server buffer more than one
request.

## Compatibility

- A client ignores fields it does not know (additive changes) and branches on `data.code`, treating an
  unknown code as `failed`; it treats a method missing from `capabilities.methods` as unavailable.
- Reports follow `schema_version` 1 of `docs/tool-contract.md`, so a client written against the command's
  `--json` output decodes the same documents here. Key naming is mixed by origin and not renamed.
- `docs/schema/v1` has `serve-initialize`, `serve-plan`, `serve-applied`, `serve-error` and `workspace`
  beside the report schemas; `abacus schema` prints them.
- The golden transcripts in `internal/serve/testdata` record whole conversations (handshake and
  malformed input, plan and apply and single use and digest mismatch, an unknown outcome) and are
  validated against those schemas by a test.
