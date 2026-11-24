# Agents

One dashboard of every coding agent running on this machine, across harnesses: Claude Code and
Codex first, with OpenCode, Gemini CLI and Aider seen as processes. It is read only. abacus never
starts, stops, signals or types into an agent.

`abacus agents [--json] [--harness H] [--state S] [--repo R] [WORDS...]` prints the report, the
Agents tab shows it live, `abacus search` and `ctrl+k` cover it, and `:agents` jumps to it.

## What an agent is

The typed report (`AgentsReport`, `schema_version` 1, additive changes only) holds `agents[]` and,
when the live monitor supplied them, `rate_limits[]`, `orphan_ports[]` and `token_rate`.
`notes[]` says which source could not be read; the other sources still count.

| Field | Meaning |
| --- | --- |
| `id`, `harness`, `pid`, `session_id` | `claude-code`, `codex`, `opencode`, `gemini-cli`, `aider`; the id is harness plus pid, else harness plus session |
| `repo`, `branch`, `worktree`, `cwd` | from the directory's own git files (`.git`, `HEAD`, `commondir`); `worktree` is set only for a linked worktree |
| `state` | `running`, `idle`, `waiting` (wants a person), `error`, `stopped`, `unknown`: a closed set, anything else becomes `unknown` |
| `source`, `sources` | how `state` was learned and every source that saw the agent (below) |
| `task` | the last prompt a person typed (or the monitor's title), one line, at most 160 characters, obvious credentials masked |
| `last_activity` | when its session file last changed (RFC 3339, UTC) |
| `tokens`, `cost_usd`, `context_pct` | pointers: absent means unknown, never zero |
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

A session with no process is `stopped` (shown for 15 minutes after its last change); when the
process list itself could not be read it is not called stopped.

## Sources

**Native** (always there, in this process, no helper and no daemon):

- *Processes*: the process list through `gopsutil` (BSD-3-Clause, maintained), keeping processes whose
  name or first arguments match a known harness. A helper whose parent is the same harness is not a
  second agent. The directory of each match is read for git facts.
- *Session files*: `~/.claude/projects/*/*.jsonl` and `~/.codex/sessions/YYYY/MM/DD/*.jsonl`. Reads are
  bounded: the last 512 KiB of a file (plus the first line of a Codex file, where its identity is), at
  most 64 files and 24 MiB per read, under a deadline. Text is sanitized (control characters removed,
  one line, credentials masked) before anything shows it. On Windows only the process list is read.
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

- Everything is read only; there is no write path, no network and no new daemon.
- `task` is derived from your own prompts and can still carry project context. `--json` prints it.
  Treat the output as private.
- Tests use fixtures and temporary directories only: every outside source (process list, files,
  terminals, the monitor) is injected, so no test reads the real `~/.claude`, `~/.codex` or process list.

## Later: a hook receiver (design only, not built)

A harness's own hooks give the strongest evidence (`source: hook`): Claude Code and Codex can run a
command on events such as a prompt being submitted, a tool about to run, a permission being asked and
a turn ending. The step would be an explicit `abacus setup` item that shows the exact change to the
harness's settings file (the diff, the file, the command that will be added), writes it only after
the usual plan, confirm, single-use apply and audit, and can be undone by removing that one entry. The
hook command would append one line of JSON per event to a file in abacus's own state directory (mode
0600, size-capped, never in a repository); the Agents feed would read its tail like a session file
and give those states `hook` evidence. Until then nothing here edits any harness configuration.

## Credits and references

- [abtop](https://github.com/graykode/abtop) (MIT): the live monitor, and the inspiration for the
  per-session context gauge, rate-limit gauges, ports, subagent tree and orphaned-port panels.
  abacus draws them natively over its own typed report and imports none of its code.
- [agent-deck](https://github.com/asheshgoplani/agent-deck) `docs/status-detection.md` and
  [agentsview](https://github.com/wesm/agentsview)'s transcript parsers (both MIT) were read as
  references for how to tell what an agent is doing from its files. No code was copied.
- [gopsutil](https://github.com/shirou/gopsutil) (BSD-3-Clause): the process list, linked into the
  binary; its license ships with abacus.
