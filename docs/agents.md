# Agents

One dashboard of every coding agent running on this machine, across harnesses: Claude Code and
Codex, OpenCode and pi read from the files and database they write, with Gemini CLI and Aider seen as
processes. It is read only. abacus never
starts, stops, signals or types into an agent.

`abacus agents [--json] [--harness H] [--state S] [--repo R] [WORDS...]` prints the report, the
Agents tab shows it live, `abacus search` and `ctrl+k` cover it, and `:agents` jumps to it.

## What an agent is

The typed report (`AgentsReport`, `schema_version` 1, additive changes only) holds `agents[]` and,
`rate_limits[]` (from the session files, the status-line file and the live monitor), and, when the live
monitor supplied them, `orphan_ports[]` and `token_rate`.
`notes[]` says which source could not be read; the other sources still count.

| Field | Meaning |
| --- | --- |
| `id`, `harness`, `pid`, `session_id` | `claude-code`, `codex`, `opencode`, `gemini-cli`, `aider`, `pi`; the id is harness plus pid, else harness plus session |
| `repo`, `branch`, `worktree`, `cwd` | from the directory's own git files (`.git`, `HEAD`, `commondir`); `worktree` is set only for a linked worktree |
| `state` | `running`, `idle`, `waiting` (wants a person), `error`, `stopped`, `unknown`: a closed set, anything else becomes `unknown` |
| `source`, `sources` | how `state` was learned and every source that saw the agent (below) |
| `task` | the last prompt a person typed (or the monitor's title), one line, at most 160 characters, obvious credentials masked |
| `last_activity` | when its session file last changed (RFC 3339, UTC) |
| `tokens`, `cost_usd`, `context_pct` | pointers: absent means unknown, never zero |
| `context_tokens`, `context_window` | what the last model call read in, and the model's limit when the harness records one (Codex does; Claude Code, pi and OpenCode do not). `context_pct` is derived from the two when no source gave a share; a call larger than its window is unknown, not 100%+ |
| `turn_tokens` | one point per model response, oldest first, at most 120, for a sparkline. Cache reads are left out (input, output and cache writes are counted). It covers the part of the session file a bounded read saw (the last 512 KiB), not necessarily the whole session; OpenCode records none |
| `git` | `added` (new or untracked files) and `modified` (every other change, deletions and renames included) uncommitted in the agent's repository; absent when git could not be read, `{0,0}` when the tree is clean |
| `term` | where its terminal is: `kind`, `ref` and a `command` a person can copy; abacus never runs it |
| `ports`, `children`, `subagents` | live detail from the monitor |

### Evidence: a guess is never shown as certain

Every state carries where it came from. In the TUI a state that is an inference from files or a bare
process is drawn with `~` (`idle~`); one the monitor reported is not.

| `source` | Meaning | Strength |
| --- | --- | --- |
| `hook` | the harness announced it itself (reserved, see Later) | strongest |
| `monitor` | the live monitor, which watches processes and sessions together | strong |
| `transcript` | inferred from the last records of the session file and its age | a guess |
| `pane` | inferred from a terminal pane | a guess |
| `process` | a matching process exists, nothing is known about what it does | `unknown` |

When two sources see the same agent (same harness and pid, or same session id) they are merged: each
field comes from the strongest source that has it, and `state` and `source` from the strongest source
that knew a state.

### How a state is inferred from a session file

Only the tail of a file is read. The last meaningful record and the file's age decide:

| Last record | Age | State |
| --- | --- | --- |
| a person's prompt, or a tool's result | up to 3 minutes | `running` |
| the same | longer | `unknown` (a long tool run looks like this) |
| the model asked for a tool | up to 5 seconds | `running` |
| the same | up to 30 minutes | `waiting` (a permission prompt, or a long tool: a guess) |
| the model finished its turn | any | `idle` |
| an API error | under 30 minutes | `error` |

pi's last records map the same way (a user message, a tool result, a response that stopped for a tool, a
response that ended, an error); OpenCode's own busy and idle stamps stand in for the last record.

A session with no process is `stopped` (shown for 15 minutes after its last change); when the
process list itself could not be read it is not called stopped.

## Sources

A process named only `pi` or `aider` (names common enough to be something else) is listed only when
something corroborates it: pi's session folder for its directory exists, or aider's history files are in
its directory. A package path in its arguments (`@mariozechner/pi-coding-agent`, `aider-chat`) is enough
by itself.

**Native** (always there, in this process, no helper and no daemon):

- *Processes*: the process list through `gopsutil` (BSD-3-Clause, maintained), keeping processes whose
  name or first arguments match a known harness. A helper whose parent is the same harness is not a
  second agent. The directory of each match is read for git facts.
- *Session files*: `~/.claude/projects/*/*.jsonl`, `~/.codex/sessions/YYYY/MM/DD/*.jsonl` and
  `~/.pi/agent/sessions/--<directory>--/*.jsonl` (a session folder pi's own settings move elsewhere is
  not looked at). Reads are bounded: the last 512 KiB of a file (plus the first line of a Codex file, where its identity is), at
  most 64 files and 24 MiB per read, under a deadline. Text is sanitized (control characters removed,
  one line, credentials masked) before anything shows it. On Windows only the process list is read.
- *OpenCode's session database*: `$XDG_DATA_HOME/opencode/opencode.db` (else `~/.local/share/opencode/`),
  read through a pure-Go SQLite driver linked into abacus (`modernc.org/sqlite`, BSD-3-Clause, no cgo, no
  `sqlite3` program: users install nothing; the driver adds about 6 MB to the binary, and its license texts and those of the modules it needs ship in the notices). It is opened `mode=ro&immutable=1` with `query_only`, only
  after the path is checked to be a regular file: no lock is taken and no `-shm` or `-wal` file is
  created, so nothing is ever written next to OpenCode's data. The price is that rows still in OpenCode's
  write-ahead log are invisible until it checkpoints, so a live session can lag; its state is therefore
  read with the same age rules as a transcript (a `busy` stamp older than three minutes is `unknown`, never
  `running`). A database over 2 GiB is not opened (a note says so); text columns are cut inside SQLite (title and model 400, directory 1024 characters). The newest 50 sessions of the last 7 days are read; a session with a parent is a subagent and
  not an agent of its own. The table must have `id`, `directory` and `time_updated`; every other column is
  optional, so a layout change costs a field and a different file reads as "nothing here". Any other
  failure (a corrupt file, a torn read) is a note. The column names come from OpenCode's own schema as
  described by the parallel work, not from a live OpenCode checked here: the invented test databases pin
  what is read, and a real one should be looked at before relying on `state`.
- *Rate limits*: Codex writes the account's limits into its session files (a `token_count` event); the
  newest read wins and the window length decides which slot it fills, so a free plan's 30-day window is
  never drawn as 5 hours. A read dated in the future (past two minutes of clock skew) is as untrustworthy as one with no time and is stale too. A limit named for one model is not the account's and is ignored. Claude Code
  keeps its limits out of its files; they are read from `~/.claude/abtop-rate-limits.json` only when a
  status-line hook has written one (the live monitor reads the same file, and abacus creates nothing).
  Every entry older than ten minutes, or with no time, is `stale`: the tab draws it dim with no reset
  countdown, since the window may have rolled over. One entry per harness is kept, the most recently
  updated winning when two sources saw the same account.
- *Uncommitted files*: for an agent that runs inside a repository, `git status --porcelain=v1` after a
  read-only `git config --name-only --get-regexp` that finds the filter names in effect. This is the one
  place abacus runs a program in a directory a repository controls, so exactly what is switched off, on
  the command line (which beats the repository's own configuration): `core.fsmonitor` and
  `core.untrackedCache`; submodules are not inspected (`--ignore-submodules=all`; the fsmonitor override
  is also what stops a submodule's own fsmonitor, as it travels to the status git starts there); and every content filter the configuration defines
  (`filter.NAME.clean`, `.smudge` and `.process` set empty), since a `.gitattributes` line can make git run
  a filter to compare a file with the index. A filter name that is not plain text makes the count
  unknown. Also: `GIT_ATTR_NOSYSTEM`, `core.attributesFile=/dev/null`, no optional lock, an environment cut
  to what git needs, a 3 second deadline and a 2 MiB output cap. Not switched off: git itself, the user's
  global configuration, and the repository's own `.gitattributes` (its filters are emptied, not its
  attributes). `status` runs no hooks, textconv or `diff.external`. Only an absolute directory that
  already has a repository is asked, at most 24 directories per read, four at a time, each answer reused
  for 15 seconds. No git, a failure or a timeout is `git` absent. Real hostile-repository tests (a filter
  with `.gitattributes`, a submodule's own fsmonitor, the repository's fsmonitor) check that none runs.
- *Terminals*: `tmux list-panes -a` (one fixed read-only argv, short deadline, scrubbed environment).
  The agent's process, or an ancestor, is matched to a pane's process. Every pane id is validated
  before it is put in a command line a person copies. `cmux surface` and `rex session` exist as
  `term.kind` values for later; v1 detects tmux only.

**Live monitor** (built in, optional): a pinned release of `abtop` (MIT) is carried inside abacus
like the other built-in tools (pinned version and sha256 per platform in `bundled-tools.json`, its
license shipped verbatim). It adds the real process behind a session, context use, rate limits, child
processes and the ports they hold, subagents, and orphaned ports. abacus runs exactly one argv,
`--json`, with a deadline, an 8 MiB output cap and an environment cut to the variables it needs to find
its files. Its other modes (a terminal UI, a hook installer, an updater) cannot be reached.
`ABACUS_ABTOP_BIN` overrides the location. Without it everything above still works.

The monitor's JSON has no schema version. The shape this build reads is that of the pinned release
(`internal/agents/testdata/monitor-v0.5.5.json`, an invented snapshot in that shape). Unknown fields
are ignored and missing ones are unknown; an answer with no `sessions` list, or not JSON, becomes a
note and the native sources carry on. An activity word the build has not heard of is `unknown`.
Re-pinning the monitor means re-reading its `snapshot.rs` and updating the fixture and the tests.

## Privacy and safety

- Every string that comes from outside abacus (session files, the process list, the monitor's JSON,
  git files) is cleaned in one place before any front end sees it: control and escape sequences, bidi
  and zero-width characters are removed, text is one line and bounded. That covers agents, ports,
  child processes, rate-limit labels, the cwd and harness ids, so the CLI, `--json`, the TUI and search
  all get safe text.
- Credentials are masked on a best-effort basis: well-known token shapes (`sk-`, `ghp_`, `xox`, AWS
  keys, `Bearer ...`), a flag that names one (`--api-key`, `--token`, `--password`, `--secret`, ...),
  `NAME=VALUE` where the name contains KEY, TOKEN, SECRET, PASSWORD or CREDENTIAL, and a password inside
  a URL. A secret in plain prose, behind a flag with an ordinary name, or as a bare `-p VALUE` is not
  recognized. Treat `task`, commands and directories as private regardless.
- Only regular files are read. A FIFO, device or socket named like a session file (or standing in for
  `.git/HEAD`) is refused before it is opened, and the real file system opens non-blocking, so one cannot
  hang a read.
- Only agents run by the current user are listed. `abacus agents --all` includes other users' agent
  processes (the process list is world-readable on most systems; their session files are not, so those
  rows show a process only). The TUI shows your own only. The monitor, when present, lists what it can
  see as the user abacus runs as.
- Two live processes are never merged into one agent because they share a session id (a resumed session
  can be open twice); records merge on harness and pid first.
- Terminal lookup through Rex goes by name (the worktree or repository), so it is offered only when one
  agent carries that name; a tmux pane, matched by the agent's own process, is the exact route and stays
  in the detail as the line to copy.

- Everything is read only; there is no write path, no network and no new daemon.
- `task` is derived from your own prompts and can still carry project context. `--json` prints it.
  Treat the output as private.
- Tests use fixtures and temporary directories only: every outside source (process list, files,
  terminals, the monitor) is injected, so no test reads the real `~/.claude`, `~/.codex` or process list.

## Terminal kinds: tmux now, cmux and rex later (design note)

`term` is `{kind, ref, command}`. `kind` is one of `tmux pane`, `cmux surface`, `rex session`; `ref` is
the address in that tool's own terms; `command` is a line a person can copy to get there. Only
`tmux pane` is detected today, from `tmux list-panes -a` matched to the agent's process or an ancestor.

- **cmux surface**: cmux gives every terminal surface an id and a workspace, and exports them into the
  surface's shell environment. The step is to read those variables from the agent's process (or its
  ancestors) and emit `kind: cmux surface`, `ref: <workspace>/<surface>` and a focus command from
  cmux's own CLI. It is read only, like the tmux match, and needs a verified list of the variable
  names and CLI verbs before it is written; none is guessed here.
- **rex session**: wired through the workspace layer the other tabs use. An agent is a workspace item of
  kind `agent`, named by its worktree or else its repository (an agent with neither has none, and
  nothing is guessed from a path). Where a Rex tab or pane is named for that, the detail shows its
  placement, `w` offers the usual open plan, and `m` brings its pane forward after the same confirm
  screen. `W` (work on this) is refused on an agent row: it starts a new agent for a task. Two agents in
  the same repository share a name, so a placement can be ambiguous, and the layer then says so rather
  than choosing. The tmux line to copy stays in the detail as the fallback; `term.kind` is still only
  detected for tmux.
- Whatever the kind, abacus never runs `command` itself from the Agents tab, and a ref that does not
  match the kind's expected shape is dropped rather than put in a command line.

## Later: a hook receiver (design only, not built)

A harness's own hooks give the strongest evidence (`source: hook`): Claude Code and Codex can run a
command on events such as a prompt being submitted, a tool about to run, a permission being asked and
a turn ending. The step would be an explicit `abacus setup` item that shows the exact change to the
harness's settings file (the diff, the file, the command that will be added), writes it only after
the usual plan, confirm, single-use apply and audit, and can be undone by removing that one entry. The
hook command would append one line of JSON per event to a file in abacus's own state directory (mode
0600, size-capped, never in a repository); the Agents feed would read its tail like a session file
and give those states `hook` evidence. Until then nothing here edits any harness configuration.

## Why one Agents domain

A parallel "Sessions" domain (PR #57) answered the same question with a second report, tab and command.
Two places for the same agents would disagree, so its genuinely new capabilities were ported here as
additive fields and sources, rewritten to this package's conventions (injected sources, text cleaned in
core at one door, bounded reads, regular files only, no real home or process reads in tests, invented
fixtures). Not ported: the second domain, command, tab and report types, its own process discovery (the
process list, ports and the monitor already cover it), its incremental file cursors (bounded tail reads
are simpler and safe), the `abacus sessions` schema, and a per-session OpenCode turn series (the
`session_message` table was not read; OpenCode has no `turn_tokens`).

## Credits and references

- [abtop](https://github.com/graykode/abtop) (MIT): the live monitor, and the inspiration for the
  per-session context gauge, rate-limit gauges, ports, subagent tree and orphaned-port panels.
  abacus draws them natively over its own typed report and imports none of its code.
- [agent-deck](https://github.com/asheshgoplani/agent-deck) `docs/status-detection.md` and
  [agentsview](https://github.com/wesm/agentsview)'s transcript parsers (both MIT) were read as
  references for how to tell what an agent is doing from its files. No code was copied.
- [gopsutil](https://github.com/shirou/gopsutil) (BSD-3-Clause): the process list, linked into the
  binary; its license ships with abacus.
- [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) (BSD-3-Clause) and the modules it needs:
  the SQLite driver that reads OpenCode's database, linked into the binary; every license text ships
  with abacus (`abacus licenses`).
