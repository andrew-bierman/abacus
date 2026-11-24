# Agents domain: a sessions tab for local AI coding agents

Status: phase 2 complete (`internal/agents` has the opencode, claude, codex, pi, git and quota
readers); the core wiring and the tab are open.

## Goal

A tab in abacus that answers the question abtop answers: which AI coding agents are running on
this machine right now, what each is doing, what it is spending, and what it left behind.

The tab is a front-end over a logic package, following the same rule as every other domain:
`internal/agents` imports no UI package (`internal/core/arch_test.go` enforces it), so the TUI,
`abacus sessions --json`, and any other front-end render one read.

## Why this is not a port

abtop is the reference for the data, not for the code. Three findings decided that:

1. **Two halves, only one of which exists in Go.** abtop is transcript parsers (`claude.rs` 4.2k
   lines, `codex.rs` 3.0k, `opencode.rs` 0.7k) plus a live observer (`ps`, `lsof`, `netstat`,
   `git`, `/proc`, and abtop's own quota hook files). The Go libraries in this space
   (`ferry`, `moirai`, `agent-ouija`) cover the first half only, and `ferry` carries no token
   fields at all. Nothing reads live processes or listening ports.
2. **The libraries are already stale for this machine.** opencode here writes the v2 SQLite
   schema (`session_v2`, `session_message`, ~552 sessions) and has no `storage/session/*.json`
   tree. `ferry` resolves opencode by that tree and would find zero sessions; `moirai` does not
   reference `session_v2`. That reader is ours to write against a schema we can see.
3. **abtop's tests are inline samples, not captured reality.** 225 tests, 26% of the crate, well
   concentrated on the risky parsers, but no fixture corpus. Its format churn (#170, #161, #184)
   is the maintenance we would inherit, not the maintenance we would avoid.

So: port the behaviour, and build the corpus from this machine.

## Libraries

The default is to use one, and to write only what no library does. This was checked against
the source, not the READMEs.

| Need | Library | Why it wins |
| --- | --- | --- |
| Process table, ppid tree, cmdline, cwd, CPU, RSS | `shirou/gopsutil/v4` | Per-platform detail already written: procfs, libproc, toolhelp. `net.Connections` returns listening sockets with the owning pid, replacing an `/proc/<pid>/fd` walk. |
| opencode v2 session store | `modernc.org/sqlite` | Pure Go, no CGO, so the one-binary promise holds. Reads 4 recent sessions in 1ms. |
| Session list, port table | `charmbracelet/bubbles/table` | Already a dependency. |
| Context and quota bars | `bubbles/progress` | Already a dependency. |
| Token-rate and per-turn sparklines | none | `bubbles v1.0.0` ships no sparkline package (checked with `go list`). The series is `StoredSession.TokenHistory`; draw it as a row of `lipgloss` blocks, or bump the dependency only if that buys more than one panel. |
| Selected-session detail scrolling | `bubbles/viewport` | Already a dependency. |
| Panels, colour, layout | `lipgloss` (in the tree) | Same. |

Written here, because nothing covers it:

- **opencode v2 reads.** `ferry` requires `…/storage/session/<project>/`, which this install
  does not have, and `moirai` queries the v1 tables (`session`, `message`, `part`). Neither
  knows `session_v2` / `session_message` / `session_part`. About 150 lines against a schema we
  can read.
- **Live process state.** No Go library in this space reads which sessions are running, what
  they spawned, or which ports they hold. This is the half of abtop that is not a parser.
- **Orphan detection.** Needs cross-tick history; it is a rule, not a read.

## The seam

`internal/agents` is deliberately split where the data source splits.

| Layer | File | What it needs | Status |
| --- | --- | --- | --- |
| Live state behind a seam: Procs, CWD, Listeners | `proc.go` | `Machine` interface | done |
| gopsutil implementation of that seam | `gopsutil.go` | `gopsutil/v4` | done |
| Discovery, session build, child tree, port attribution, orphan detection | `discover.go` | `Machine` seam, cross-tick state in `Collector` | done |
| Transcript reads: tokens, context, model, title, true state | phases 2 | per-harness readers | done |
| Git branch and dirty counts per session cwd | `git.go` | `Runner` seam | done |
| Quota gauges | `quota.go` | harness state files | done |
| Domain wiring: `DomainSessions`, provider, CLI, TUI tab | `core` + `tui` | the report types above | open |

`Machine` is the only seam the Collector needs for live state: tests supply a fixed process
table and never read this machine.

Measured: one snapshot of the 29 sessions live on this machine takes 150ms.


## Two facts that need more than one read

Both are why `Collector` keeps the previous snapshot, and both match abtop:

- **Executing** is the delta of a session tree's CPU ticks between reads. Accumulated CPU is a
  level, so a finished child would otherwise read as busy forever.
- **Orphan ports** are ports attributed to a session on a previous read whose session process has
  since ended while the socket is still listening. On the first read of a process there are
  none, and `Snapshot.Orphans` says so by being empty.

## Per-harness readers

One shape for every harness, so the join in `core` stays one loop:

`StoredSession` gains `Context Context` and `TokenHistory []int64`. A zero `Context`, or an empty
history, means "this harness does not record it", never "zero tokens". Tokens stay cumulative for
the session, `Context.Used` is the last turn's window fill, and `TokenHistory` is one entry per
assistant response, oldest first, bounded to the 200 points the sparkline draws.

Readers differ in how they find a store, not in what they return:

| Harness | Entry point | Store |
| --- | --- | --- |
| opencode | `OpenCodeStore.Sessions(ctx, since, limit)` | one SQLite row per session |
| claude | `ClaudeStore.Transcript(path)`, `ClaudeStore.PathFor(cwd, since)` | one JSONL per session |
| codex | `CodexStore.Rollout(path)` | one JSONL rollout per session |
| pi | `PiStore.Transcript(path)`, `PiStore.PathFor(cwd, since)` | one JSONL per session |

opencode lists sessions because its database is the list. Claude and pi have no list: a session
exists because a transcript exists for a live process's cwd, so the caller passes the path it
already resolved. Codex has no list either; its rollout is found from the live process's open
files, which is the core join's job. Sweeping every project directory each tick is what the cost
model forbids.

### `claude.go`

`ClaudeStore` reads `~/.claude/projects/<encoded-cwd>/<sessionId>.jsonl`, encoding the cwd the way
Claude Code does (`/ \ : _ .` become `-`). `PathFor` returns the newest transcript in the encoded
directory, and falls back to the newest transcript under any project directory whose mtime is at or
after `since`, because worktree sessions live under a branch-keyed directory. A zero `since` accepts
any.

Per line, by `type`:

| Line | Fact |
| --- | --- |
| `assistant` | `message.model`; `message.usage` tokens; `message.id` for dedupe; `tool_use` blocks; `timestamp` |
| `user` (real prompt) | opens a turn; the first one is the title fallback; `cwd`, `sessionId`, `timestamp` |
| `user` (synthetic) | ignored for title: `isMeta`, all-`tool_result`, or text opening with `<command-name>`, `<local-command-*>` or `<bash-*>`; a `tool_result` reopens the turn, the rest leave it alone, and an interrupt marker closes it |
| `ai-title`, or legacy `summary` | title, preferred over the prompt fallback |
| `cost-state` | ignored: cost in currency is a non-goal |

Facts are derived the way abtop derives them, with three corrections the data on this machine forced:

1. **Dedupe by `message.id`.** Claude Code writes one assistant line per content block, and every
   line of a response carries the same `usage`. Summing by line doubles the real totals (measured
   2.0x on two real transcripts here). A repeated id replaces the previous contribution instead of
   adding to it, which also handles a usage snapshot that grows mid-stream.
2. **Context is the last response's fill**: `input + cache_read + cache_creation`. `input_tokens`
   excludes cached content, so the prompt the model saw is all three counts. abtop's
   `input + cache_read` rule (with cache creation only on the first turn) undercounts the current
   format, which mixes both cache fields in one response: 31.2k read as 11.9k on a real entry here.
   The window limit is not recorded in the transcript, so it is inferred: 1M when the model name
   carries `[1m]` or the observed high-water mark passed 200k, else 200k. `Context.Limit` is
   therefore an inference and says so in its comment.
3. **State follows the open turn**, judging a response's block lines together: a real user prompt
   opens a turn (`thinking`); a response containing a `tool_use` that no later line closes is
   `executing`; a `tool_result` closes the tool and reopens the turn, because the model is being
   asked to answer it; anything else is `waiting`. abtop reports `executing` through that post-tool
   generation because it never clears the pending tool; `thinking` is the truthful read and lasts
   only until the next response lands. `discover.go` still owns `executing` by CPU delta and the
   join prefers it, because an I/O-bound tool leaves the transcript ahead of the CPU.

Incremental by offset, with `ClaudeStore` state keyed by path:

- Stat and compare against the stored `os.FileInfo` with `os.SameFile`. A different file, or one
  shorter than the stored offset, resets the accumulation and reparses from byte 0.
- Seek to the stored offset and read whole lines. A trailing line without `\n` is not consumed, so a
  half-written record is reread and never counted twice. Complete lines that are not JSON are
  skipped and consumed.
- Lines are capped at 10 MB; past the cap the rest of that line is discarded to its newline, so one
  hostile record cannot make the tab allocate without bound.
- `Updated` is the file's mtime. `Tokens`, `Context`, `TokenHistory`, `Model`, `Title`, `State`, `Dir`
  and `ID` accumulate across reads.

Deliberately not here: mapping a PID to `~/.claude/sessions/<pid>.json`, and subagent transcripts
under `<sessionId>/subagents/`. Both need the live-process join and belong to the `core` wiring item;
this reader takes a path and does not guess which pid owns it.

Tests in `claude_test.go`, synthetic transcripts only, built line by line with invented prompts and
ids:

- one response split across block lines counts once, for tokens, context and history length;
- a real prompt followed by a `tool_use` response reads `thinking` then `executing`, and a later
  `tool_result` plus text response reads `waiting`;
- synthetic user lines do not open a turn;
- `ai-title` overrides the first-prompt fallback, and a transcript with neither leaves the title empty;
- a line appended between reads adds only that line; a partial trailing line is not counted and is
  counted exactly once when completed;
- a truncated or replaced file resets totals instead of carrying them forward;
- an over-cap line is skipped without stalling the next read;
- `PathFor` picks the newest transcript in the encoded directory and falls back by mtime;
- the context limit crosses to 1M only after the observed high-water mark passes 200k.

### `codex.go`

`CodexStore` reads Codex's rollouts under
`~/.codex/sessions/<YYYY>/<MM>/<DD>/rollout-*.jsonl`, incremental by offset with the same
cursor, shared read budget, 10 MB line cap and reset rules as `ClaudeStore`. `Rollout(path)`
takes the path it is given; tying a live process to its rollout needs the process's open files,
which is the core wiring's join, exactly like the Claude PID note above.

Per line, by `type`:

| Line | Fact |
| --- | --- |
| `session_meta` | `payload.id`, `payload.cwd` |
| `turn_context` | `payload.model`, and the window when it records one |
| `event_msg` `task_started`, `user_message`; `item_completed` `UserMessage` | opens a turn; the first prompt is the title fallback |
| `event_msg` `token_count` | cumulative totals, last-turn fill, window limit, one history point |
| `event_msg` `task_complete`, `turn_aborted`; `item_completed` `AgentMessage` with `phase: final_answer` | closes the turn |
| `response_item` `function_call`, `custom_tool_call` | a tool is running; its `*_output` closes it |

Facts are derived the way abtop derives them, with the corrections this machine's rollouts force:

1. **Totals are levels, not sums.** `total_token_usage` already covers the whole session, so each
   `token_count` replaces the previous totals instead of adding to them. `input_tokens` *includes*
   `cached_input_tokens` (measured: 22,488 total with 12,416 cached), so
   `Input = input_tokens - cached_input_tokens` and the cache read is counted apart, which keeps
   `Tokens.Total()` from double-counting it. `cache_write_input_tokens` is zero in every rollout
   here and is ignored rather than guessed into a field that might double-count it.
2. **Context is the last turn's prompt**: `last_token_usage.input_tokens`, which includes the
   cached part. The window is recorded (`model_context_window`: 475k and 258k on this machine), so
   no inference is needed and `Context.Limit` is zero only when the rollout omits it.
3. **State follows the open turn.** A user prompt opens it (`thinking`); a tool call no output has
   closed makes it `executing`; a completion event closes it (`waiting`). `item_completed` items
   are already finished when they land, so they only keep the turn open. `discover.go`'s CPU delta
   still wins in the join.

Tests in `codex_test.go`, synthetic rollouts only, built line by line with invented ids:

- one session's meta, model and recorded context window survive a read;
- repeated `token_count` events grow history but replace totals, and `Input` excludes the cached part;
- a prompt then a tool call reads `thinking` then `executing`, and the output reads `thinking`;
  `task_complete` and `final_answer` read `waiting`;
- the first prompt becomes the title and a later prompt does not replace it;
- a line appended between reads adds only that line; a partial trailing line is counted exactly once;
- a truncated or replaced file resets totals instead of carrying them forward;
- an over-cap line is skipped without stalling the next read.

### `pi.go`

`PiStore` reads pi's sessions under
`~/.pi/agent/sessions/--<encoded-cwd>--/<timestamp>_<sessionId>.jsonl`, where pi drops the leading
path separator from `<encoded-cwd>` and replaces `/`, `\` and `:` with `-`. `Transcript(path)` takes
the path the caller resolved; `PathFor(cwd, since)` returns the newest transcript in the encoded
directory at or after `since` and falls back to a flat configured `sessionDir`, matching each
file's header cwd. `PiSessionsPath(home)` names the default root so an override is a `Root` field
rather than a guess. Pi injects `PI_SESSION_FILE` into the shell commands
it spawns, not into its own process, which carries only the `AI_AGENT` and `PI_CODING_AGENT`
markers, so the core wiring resolves the transcript by cwd with `PathFor`; this reader takes the
path and does not guess which pid owns it.

Per line, by `type`:

| Line | Fact |
| --- | --- |
| `session` | `id`, `cwd`; `parentSession` names the parent by file path, not by id |
| `message` role `user` | opens a turn; the first real prompt is the title fallback |
| `message` role `assistant` | `provider`/`model`; `usage`; `stopReason`; `toolCall` blocks |
| `message` role `toolResult` | closes the `toolCallId`; an optional `usage` is nested model work |
| `usage` | tokens from cache warming and similar work; totals only, never context |
| `compaction`, `branch_summary` | an optional `usage` adds to totals; the summary is not a response |
| `session_info` | `name`, preferred over the prompt fallback |
| `model_change` | `provider`/`modelId`, a fallback until a response names its model |

Facts are derived the way the documented format and this machine's sessions define them:

1. **Usage is per response, so it adds.** Pi writes one assistant record per response, content
   blocks included, so there is no block duplication to dedupe. `input` excludes cached content
   (measured: 162 input + 14,464 cache read + 32 output = 14,658 totalTokens), so the prompt the
   model saw is `input + cacheRead + cacheWrite`. `reasoning` is already inside `output` and is not
   added again. `cost` is ignored, per the currency non-goal.
2. **Context is the last response's fill**: the same three prompt counters. The session file does
   not record the window, so `Context.Limit` stays zero and the tab draws no bar instead of a
   made-up percentage. Pi's model catalog is a separate store; joining it belongs to the tab, not
   this reader.
3. **State follows the open turn.** A user message opens it (`thinking`). An assistant response
   with `stopReason: toolUse` leaves its `toolCall` ids pending (`executing`); `deferred` keeps the
   turn open while the provider result is retrieved. Each `toolResult` clears its id and reopens
   the turn, because the model is being asked to answer it (`thinking`; still `executing` while
   another parallel call is pending). Any other terminal stop reason (`stop`, `length`, `error`,
   `aborted`) closes it (`waiting`). Streaming `pending` responses are not persisted.
   `discover.go`'s CPU delta still wins in the join.
4. **Titles**: `session_info.name` wins; otherwise the first real user prompt. User content is a
   string or text blocks; non-user roles and extension messages are never prompts. A forked
   session that copies history is read linearly: totals are what the file records.

Incremental by offset with the same `jsonlCursor`/`jsonlFiles` lifecycle, shared read budget, 10 MB
line cap and reset rules as `ClaudeStore`, with state keyed by path. `Updated` is the file's mtime.

Tests in `pi_test.go`, synthetic sessions only, built line by line with invented names and ids:

- a header and one response give the id, cwd, model, tokens, context and one history point;
- repeated responses add totals, and `reasoning` is not added twice;
- a prompt then a tool-call response reads `thinking` then `executing`, a `toolResult` reads
  `thinking`, and a final response reads `waiting`;
- a steering prompt while a call is pending stays `executing`, and a completed response settles
  the old call;
- two parallel tool calls stay `executing` until both results land;
- a `deferred` response keeps the turn open, and an `aborted` or `error` response closes it;
- `session_info` overrides the first-prompt fallback, and a session with neither leaves the title
  empty;
- `usage` and tool-result usage reach the totals but not the context or history;
- a line appended between reads adds only that line; a partial trailing line is counted exactly
  once;
- a truncated or replaced file resets totals instead of carrying them forward;
- an over-cap line is skipped without stalling the next read;
- `PathFor` picks the newest transcript in the encoded directory, finds a flat `sessionDir` by
  header cwd, and honors `since` in both.

### `git.go`

`Git` reads one session directory's branch and working-tree counts. It shells out through a
`Runner` seam, so tests never read this machine. `Runner` is declared here instead of imported
from `core`: the Sessions domain will import `agents`, and importing back would cycle. Its shape
matches `core.Runner`, so `core.ExecRunner` drops in unchanged.

- **One command per directory**: `git -C <cwd> status --porcelain --branch`. The `##` header is
  the branch (`## main...origin/main`, `## No commits yet on main`; a detached HEAD has no
  branch) and the following lines are the counts. A separate `branch` command would double the
  cost of one atomic fact.
- **Counts match abtop's projects panel**: a porcelain code containing `?` or `A` counts as
  `Added`; one containing `M` counts as `Modified`. Deletes, renames and conflicts are not
  distorted into either number.
- **Cache**: results are kept per directory for 10s (`TTL`, `Now` injectable), the cadence abtop
  polls git. Several sessions share a cwd, so one tick still runs one command per project. A
  directory that is not a work tree, a missing `git`, or a failed command yields the zero
  status, never an error; the UI shows no branch instead of flickering. A nil `Runner` returns
  the zero status rather than panicking.
- `Session` gains `Git GitStatus`, so the join stores the read on the session it describes.
  `Collector` does not shell out; the core wiring fills it, as it fills the transcript fields.

Tests in `git_test.go` with a fake `Runner`:

- a branch and mixed `??`/`A`/`M` lines produce the expected counts;
- the header is parsed for a normal branch, a repository with no commits, and a detached HEAD;
- `R`, `D` and conflict lines add to neither count;
- a non-repository or failed command yields the zero status and caches that failure;
- a second read within `TTL` reuses the cached result, and one after it re-runs;
- two sessions in one directory share one command.

### `quota.go`

Quota is account-level, not per session: abtop's panel answers "how much of the plan's allowance
is left", and both harnesses report the same numbers for every session on the account. `Quota`
therefore sits beside `Session`, not inside it, and a report carries one entry per source.

The Codex reader records the quota on the `StoredSession` it already returns; a small reader
handles Claude's separate status-line file.

| Source | File | Shape |
| --- | --- | --- |
| Claude | `<claude config dir>/abtop-rate-limits.json` | status-line hook JSON: `five_hour` and `seven_day` windows with `used_percentage` and `resets_at` |
| Codex | the live session's `rollout-*.jsonl` | `event_msg` → `token_count` → `payload.rate_limits` |

- **Claude has no quota in its transcripts.** The account windows reach Claude Code through the
  StatusLine hook's stdin, and abtop's `--setup` persists them to `abtop-rate-limits.json`. That
  file is a data file, not a runtime dependency on abtop: any hook that writes the documented
  shape works, a missing file means "no gauge", and abacus never runs abtop. The core wiring
  resolves each config directory (`~/.claude`, `CLAUDE_CONFIG_DIR`, and any discovered from live
  processes) and passes it to `ClaudeQuota`; the reader itself reads no environment.
- **Codex reports quota inside the rollout.** Each `token_count` event may carry
  `payload.rate_limits`; only the account-level entry counts (`limit_id` is `codex` or absent).
  Slots are assigned by window length, not position: `window_minutes <= 300` is the short window
  and anything longer is the long one, which keeps a free plan's 30-day primary from being drawn
  as 5 hours. The last event wins, and a reported value keeps serving until the next event
  arrives, matching abtop's cache.
- **Windows are optional.** A nil window means the source did not report it, never 0% used.
  Claude omits the window length, so the defaults are its 5 hours and 7 days. Codex records it;
  a zero length stays zero so the tab falls back to the slot's label instead of inventing one.
- **Codex rides the rollout's existing offset read.** The quota arrives on the same
  `token_count` records as the token levels, so the reader folds it into the `StoredSession` it
  already returns: one parse, one 8 MB budget, no second pass over the history. The cursor rules
  are the JSONL readers' own (append-only, reset on inode change or a shorter file, a partial
  trailing line waits for its newline).
- **`Updated` says how fresh the numbers are.** Claude takes the hook's `updated_at`, Codex the
  event timestamp. The tab dims a source older than 10 minutes and hides its reset countdown,
  the rule abtop uses; the reader reports the timestamp rather than deciding the styling.

`Quota` carries `Source`, `Short`, `Long` and `Updated`; `QuotaWindow` carries `UsedPercent`,
`ResetsAt` and `WindowMinutes`. `StoredSession` gains `Quota *Quota`, so the Codex rollout reader
and the core join cannot disagree about where the account value lives. `ClaudeQuota(root)` reads
one config directory, and `ClaudeQuotaPath(root)` names its file, so the reader and its test
agree on the filename without repeating it. The Claude file is capped at 64 KiB: abacus does not
own it, so a corrupt or hostile writer must not turn one tick into an unbounded allocation.

Tests in `quota_test.go`, synthetic state files only, with invented names:

- a Claude file with both windows yields the source, both percentages, both reset times and the
  default 5-hour and 7-day lengths;
- a Claude file with only one window leaves the other nil;
- a missing file, invalid JSON, an oversized file, or a file with neither window reports no
  quota;
- explicit `window_minutes` values survive the read, including a 30-day long window;
- a Codex rollout's account-level `token_count` rate limits become its short and long windows,
  with both reset times and `Updated` from the event timestamp;
- an account report without `limit_id` counts, and a model-specific `limit_id` is ignored;
- an empty report neither creates a quota nor replaces an earlier one;
- a free-plan rollout with only a 30-day primary puts it in the long slot and leaves the short
  one nil;
- repeated `token_count` events replace the previous quota instead of accumulating;
- a rollout with no rate limit reports no quota until one arrives, and a source with no usable
  timestamp is stale;
- the quota follows the rollout cursor: an appended event updates it, a partial trailing line
  waits for its newline, a record after an over-budget line still lands, and a replaced or
  truncated rollout resets it;
- the JSON shape omits unreported windows and timestamps, and an unreadable Claude path or an
  empty root reports no quota.

## Cost model

Chosen to stay cheap on a machine with large histories (this one has 233M of Codex JSONL and a
291M opencode database):

- Enumerate processes and `/proc/net/tcp*` once per tick (a few ms on 3k processes).
- Attribute sockets to pids by walking `/proc/<pid>/fd` rather than shelling out to `lsof`.
- Read transcripts only for live sessions, by byte offset from the last tick (phase 2). Never
  sweep all history: that is what makes a one-shot `abtop --json` take about a second here
  before parsing anything.

## Progress

The loop works the first unchecked item. Tick one per pass.

- [x] `proc.go` + `gopsutil.go`: live state behind a `Machine` seam
- [x] `discover.go`: sessions, child trees, ports, orphans, executing/waiting
- [x] `opencode.go`: opencode v2 SQLite reader (tokens, model, title, parent, state)
- [x] `claude.go`: Claude Code JSONL reader, incremental by offset
- [x] `codex.go`: Codex JSONL rollout reader, incremental by offset
- [x] `pi.go`: Pi JSONL reader, incremental by offset
- [x] `git.go`: branch and dirty counts per session cwd
- [x] `quota.go`: Claude and Codex quota gauges
- [ ] `core`: `DomainSessions`, `SessionsReport`, `SessionProvider`, `Providers.Sessions`
- [ ] `cli`: `abacus sessions [--json]`
- [ ] `tui`: sessions tab (table, context bars, token sparklines, ports, detail viewport)
- [ ] `tui`: golden tests for the sessions tab
- [ ] shipping: push the branch and open the pull request

## Phases

1. **Landed.** `proc.go` + `discover.go`: sessions with harness, pid, cwd, child tree, ports,
   orphan detection, executing/waiting by CPU delta. Tests read a fake procfs. Verified live on
   this machine: 29 sessions (12 codex, 16 opencode, 1 claude) with correct cwds and children.
2. **Transcripts.** Per-harness readers behind one interface: `claude` and `codex` JSONL by
   offset, `pi` JSONL, `opencode` v2 SQLite (`session_v2.tokens_input`, `cost`, `parent_id`,
   `session_message`). Produce tokens, context used/limit, model, title, and the real state
   (thinking vs waiting) from an open turn. Golden corpus from this machine, redacted; `abtop
   --json` as an oracle while building, never at run time.
3. **Domain wiring.** `DomainSessions` in `core/domains.go`, `SessionsReport`, `SessionProvider`,
   `Providers.Sessions`, `ReadDomains`, `abacus sessions [--json]`. Follows the existing pattern
   exactly, including "not connected" when no procfs is readable.
4. **The tab.** `newSessionsSection` in `internal/tui`, abtop's layout in abacus's existing
   widget set. No new UI dependency: bubbles already ships the four things abtop hand-rolled.

   | abtop panel | abacus tab |
   | --- | --- |
   | Header: host CPU/mem/load | `lipgloss` row over `gopsutil` host info |
   | Context: 200-point token-rate sparkline + per-session % bars | a lipgloss block row over `TokenHistory` + `bubbles/progress` |
   | Quota: 5h/7d gauges with reset countdown | `bubbles/progress` + a lipgloss countdown |
   | Tokens: totals + per-turn sparkline | the same lipgloss block row |
   | Projects: branch + dirty counts | phase 2 `git.go` |
   | Ports: table incl. orphans | `bubbles/table` |
   | Sessions: list + selected detail | `bubbles/table` or the existing tasks-list pattern, detail in `bubbles/viewport` |

   Golden tests in `internal/tui/testdata/golden/`, matching the other domains, with invented
   names per `956fbb4`. The sparkline is drawn rather than borrowed: `bubbles v1.0.0` has no
   sparkline package, and none of the other four panels needs one.

## Fixtures

Synthetic, never captured. `956fbb4` already established that fixtures use invented names, so
a redacted real corpus still does not belong in the repo. Each harness reader is tested against
a store built in the test — for opencode, a SQLite database created with the real `session_v2`
schema and invented rows. A live compare against `abtop --json` is a local, uncommitted oracle
while building, never a run-time dependency.


## Non-goals

- Starting, driving or killing an agent. The package is read-only, like every other module.
- Token *cost* in currency. It is derivable once tokens exist; it is not a phase 1 concern.
- Depending on an abtop binary at run time. The sidecar pattern stays a development oracle: it
  would break abacus's one-self-contained-file promise and hand upstream's churn to us.

## Open questions

Found while building the readers; none blocks the tab, and each names the item that owns it.

- **Shared read budget is first-come.** Within one store, the first file in the caller's order
  can take the whole 8 MB of a tick while every later file advances one line, so a large backlog
  on one session makes the others stale. Core wiring should rotate the session order or split
  the budget per file; the reader cannot see the caller's ordering.
- **Unterminated trailing record is reread.** Allocation is capped at 10 MB, but a malformed
  newline-free file is rescanned from the previous offset on every tick. A bounded tail buffer
  in `jsonlCursor` would fix it; the current rule is the one the Claude reader shipped with.
- **Subagent rollouts have no title.** Those threads record their prompt only as a
  `response_item` message with role `user`, which this reader ignores (abtop does too). Whether
  the tab shows those threads, and how it filters their injected `<environment_context>` and
  `AGENTS.md` records, belongs to the core wiring's process-to-rollout join.
- **pi attribution is per-directory.** Pi has no session list, so `PathFor` returns the newest
  transcript for the cwd: two live pi sessions in one directory read the same transcript. A
  `sessionDir` override must be passed as `Root` (that layout is flat and `PathFor` matches each
  file's header cwd), because `PI_SESSION_FILE` only reaches pi's shell-tool children. The core
  wiring owns both limits.
- **pi's PathFor re-lists one directory per tick.** The default root needs one `ReadDir` of the
  cwd's own directory; a custom flat `sessionDir` reads header cwds newest-first, as pi itself
  does. Neither walks the sessions root, but a cwd with thousands of transcripts would be worth
  a cache; measure before the tab polls every tick. The core wiring owns the call frequency.
- **pi's own context number is branch-aware.** Pi's footer ignores aborted and error responses,
  scopes usage to the active branch, and reports nothing after a compaction until a fresh
  response. This reader reports the last assistant record in file order, which is the plan's one
  context rule for every harness. Aligning would need branch-aware state; the tab can compare
  once a window limit is joined.
- **In-place growth is not detected.** A transcript rewritten to the same path with a larger size
  keeps its accumulator and seeks past the new prefix; only an inode change or a shorter file
  resets. All three JSONL readers (claude, codex and pi) share this pre-existing rule.
- **pi's context window lives outside the transcript.** Pi records the fill per response but the
  window only in `~/.pi/agent/models-store.json`: each provider entry holds a `models` list whose
  entries carry `id` and `contextWindow`. The tab item can match the assistant message's provider
  and model against that list when it wants a pi percentage; until then the reader reports the
  fill with a zero limit, which draws nothing rather than a guess.
- **The git cache neither collapses concurrent first reads nor expires entries.** Two callers
  asking for the same directory at once can both run `git status`, and a directory seen once stays
  in the map for the process lifetime. The core wiring owns the call pattern: keep one `Git` on
  one goroutine, or add single-flight and per-entry expiry if it parallelizes or runs for days.
- **Quota is unjoined until the core wiring.** `ClaudeQuota` and `StoredSession.Quota` have no
  non-test caller yet. The join must dedupe by `Source`, or ten live sessions on one account draw
  ten identical gauges, and it must resolve each Claude config directory itself, because the
  reader reads no environment.
- **Codex here reports one long window.** All 7,516 sampled `rate_limits` events carry only
  `primary` at 10,080 minutes and a null secondary, so the long gauge fills and the short one
  stays empty; a plan that reports a 5-hour window is untested against this machine.
- **A zero Codex window length is filed as short.** A missing or zero `window_minutes` goes into
  the short slot, matching abtop's `mins <= 300` rule, so an omitted length would draw a 5-hour
  label. No sampled rollout omits it; the slot rule would need a richer signal to tell the two
  apart.

## Pass log

One line per completed item: what it did and how it was verified.

- opencode v2 reader — `go build ./...`, `go test ./internal/agents`; verified against the
  real store (4 sessions, 1ms, busy session marked thinking).
- claude JSONL reader — `go build ./...`, `go test ./...`; totals match a deduped real transcript
  (24/25,800/982,726/100,576 tokens, context 111,283), and synthetic tests pin offset resume,
  partial lines, in-place and inode replacement, symlinks, the three-counter context sum and trim.
- codex rollout reader — `go build ./...`, `go test ./...`; token math matches an independent
  read of a real rollout (537,649 input / 18,028 output / 14,713,344 cached, context 245,001,
  104 history points), and mutation checks pin offset resume, fork identity, in-place truncation,
  the empty-call and nil-payload guards, and both tool-call vocabularies.
- pi JSONL reader — `go build ./...`, `go test ./...`; matches an independent read of a real
  21-response session (37,214 input / 780 output / 312,448 cached, context 17,638, 21 history
  points), and synthetic tests pin offset resume, inode replacement, flat `sessionDir` lookup,
  the turn states (including parallel calls and `deferred`), name clearing, and the split between
  response and auxiliary usage.
- git reader — `go build ./...`, `go test ./...`; one cached read-only `git status --porcelain
  --branch` per directory matches abtop's added/modified rule, with synthetic tests pinning
  branch headers (including unborn and detached), per-directory TTL reuse, failed-read caching,
  and the read deadline.
- quota reader — `go build ./...`, `go test ./...`; Codex quota is folded into the rollout read
  it already pays for (no second pass), and a real rollout read independently gives 74% of a
  10,080-minute window resetting 2026-11-25 08:43:52 UTC. Synthetic tests pin both Claude
  windows and defaults, the account-level filter and slot-by-length rule, empty reports,
  timestamp fallback, the quota following the rollout cursor through append, partial line,
  replacement and truncation, the 64 KiB boundary, and the report JSON's omitted fields.
