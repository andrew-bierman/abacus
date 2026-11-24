# abacus

The fleet cockpit for Beads-powered agent fleets. See the board, the tasks, the leases and the
Symphony queues, and change the tasks that are safe to change, from one terminal tool. The tools
that supply the data are providers behind each view (see Providers).

## Install

abacus is one self-contained file. Every tool it needs (`bd`, Lambe, beads-lease and the Vercel
Board CLI) is built into it: you never install, fetch or run anything else. Download the archive
for your platform from the releases page (`abacus_VERSION_OS_ARCH.tar.gz`, for darwin or linux on
amd64 or arm64), extract it and run the one file; you can move or copy `abacus` anywhere:

```
tar -xzf abacus_VERSION_darwin_arm64.tar.gz
./abacus_VERSION_darwin_arm64/abacus
```

The archive holds `abacus`, `LICENSE`, `THIRD_PARTY_NOTICES.md` and `third_party/<tool>/LICENSE`
and nothing else (`abacus licenses` prints the same notices and license texts from inside the
binary, so a lone copy of the file still carries them).

### Supported platforms

| Platform | Status |
| --- | --- |
| Linux, amd64 | Release archive; run end to end on Arch Linux (doctor, setup, the TUI in tmux) with an empty `PATH`, the tools coming from the binary itself |
| Linux, arm64 | Release archive; cross-compiles, not yet run on real hardware |
| macOS, arm64 and amd64 | Release archive (the primary development platform) |
| Windows | Not supported yet. It compiles, but its safety checks are not implemented yet; see `docs/design/2026-11-22-1900-plan-windows-support.md` |

The size is the price of that: the file is about 80 to 90 MB, because the tools are inside it
(see Built in). On first run abacus unpacks them once into a private folder; later starts
only check them.

On first run each tab that needs one-time setup says so in one line and offers a key for it, and
shows the exact command before anything happens (see First run below). Press `Tab` to move
between tabs and `?` for keys.

On macOS, a browser-downloaded archive is quarantined by Gatekeeper; clear it once with
`xattr -dr com.apple.quarantine abacus_VERSION_darwin_arm64`.

## Domains

| Domain | What you see | What you can change today |
| --- | --- | --- |
| Board | channels, recent threads and messages, open tasks (a message board for agents, not a kanban) | post a thread or reply from the command line (`abacus board post\|reply`, with a confirm step) |
| Tasks | every task, listed, sorted (eight keys), filtered (a query language and quick filters) and shown in full: description, comments, what blocks it and what it blocks, children with progress; graph insights (next pick, blockers, critical path, cycles) in a second view | create, update, close (with a confirm step); first run: create the fleet workspace |
| Leases | held, stale and free leases | read only |
| Queues | runs, the queue, what needs attention, history | read only |

A domain with no provider shows "not connected" and how to connect; it never disappears.
`abacus tasks|leases|queues|board [--json]` print the same typed reports the TUI renders, and
`abacus tasks list|show|board|graph|history` print the task list, one task in full, the kanban columns and the dependency graph (`--query`, `--sort`, `--json`) and a task's history, and
`abacus search "QUERY"` (or `ctrl+k` in the TUI) searches every domain at once. In the TUI, `:` opens a k9s-style command prompt (`:tasks`, `:graph`, `:filter label:ui`, `:search foo`, `:help`) that only navigates, and `d` and `y` describe the selected task or show it as JSON.

## First run

A fresh machine has no tracker, no actor name and no supervisor config. abacus never creates any
of them silently. Each is a setup step with a plain one-line prompt, a confirm screen that shows
the exact command, and an audit entry; none starts a daemon.

| Tab | Prompt | Key | What it does |
| --- | --- | --- | --- |
| Tasks | No tracker here, press `i` to create your fleet workspace | `i` | `bd init` in `$XDG_DATA_HOME/abacus/workspace` (or `~/.local/share/abacus/workspace`) with the fixed flags `--non-interactive --quiet --skip-agents --skip-hooks --prefix=abacus`; the folder is then remembered in the config |
| Leases | Leases need an actor name | `s` | saves a suggested actor name (user and machine) in the config; `abacus config set actor NAME` changes it |
| Queues | Queues need a supervisor config | `s` | `lambe init -non-interactive -config <abacus config dir>/lambe.yaml`, run in the workspace: writes a starter config and a starter WORKFLOW.md and formula there. It starts nothing; you run `lambe run -config ...` yourself. Needs the workspace first |

`abacus setup [--json] [--yes] [--dry-run]` reports the same steps and, with `--yes` (or a `y` at
the prompt on a terminal), performs them through the same gate. Without input or `--yes` it only
reports. Leases also need a coordination hub (beads-lease reads one through its `coord`
wrapper, `BEADS_LEASE_COORD`); abacus cannot create a shared hub, so on a machine without one
Leases says so plainly and stays out of the way.

Settings live in `$XDG_CONFIG_HOME/abacus/config.json` (written atomically, mode 0600):
`abacus config show|get KEY|set KEY VALUE|path` with the keys `actor`, `tracker_dir` and
`lambe_config`. The tracker used by Tasks is chosen in this order: `ABACUS_BD_DIR`, a tracker at
or above the working directory, `tracker_dir` from the config, an existing managed workspace.

An empty tracker is not an error: Insights reads "no tasks yet" and nothing is analyzed. If the
analysis fails on a tracker that has tasks, the tab says so in one plain line and the reason is in Doctor.

## Built in

abacus is one self-contained binary: `bd`, Lambe, beads-lease and the Vercel Board CLI are inside
it, pinned and sha256-verified at build time, and unpacked on first use into a private per-user
folder. Nothing is fetched at run time. Graph analysis comes from
[beads_viewer](https://github.com/Dicklesworthstone/beads_viewer)'s analysis package, linked in.
`abacus licenses` prints every notice and license text. How it works, the threat model and how the
binary is built: [docs/built-in-tools.md](docs/built-in-tools.md).

| Tool | Version | License |
| --- | --- | --- |
| [Beads](https://github.com/steveyegge/beads) (`bd`) | 1.3.4 | MIT |
| [Lambe](https://github.com/andrew-bierman/lambe) | 0.10.1 | GPL-3.0 |
| [beads-lease](https://github.com/andrew-bierman/beads-lease) | 0.1.0 | MIT |
| Vercel Board CLI ([`@vercel/board`](https://github.com/vercel/board)) | 0.54.0 | Apache-2.0 |
| [beads_viewer](https://github.com/Dicklesworthstone/beads_viewer) (linked library) | 0.27.0 | MIT, see its LICENSE |

Doctor shows each tool as `built in` with its version. If the tools folder cannot be used it says
which folder and the one fix; set `ABACUS_TOOLS_DIR` to a writable folder.

## Architecture

`internal/core` holds all the real logic: the domain reports (Board, Tasks, Leases, Queues),
provider interfaces and the registry that maps detected providers to domains, the write gate,
detection, doctor and status aggregation, the typed report JSON (`schema_version`), sanitizing and the exit-code rules.
Adapters (`internal/lambe`, `internal/beadslease`, `internal/bd`, `internal/rex`) compose the
tools only through their `--json` CLIs and versioned local APIs. `internal/cli` and `internal/tui` are thin front-ends that only parse
input and render core's typed reports; a Linux or Swift GUI would be another front-end over
the same core or the same `--json` output. A test enforces that core, adapters and the CLI
import no UI library.

## Providers

Providers are invisible in navigation and appear only here, in Doctor and in "how to connect"
or error messages that help you fix one.

- **Beads (`bd`)**: Tasks reads and writes. Reads always run `--readonly` (the task list is one
  `bd --readonly export` of the tracker, a fixed argv); writes go through a
  separate allowlist (create, update, close, and the fixed `init` of the first-run workspace) and
  the confirm step.
- **beads-lease**: Leases (read only).
- **Lambe**: Queues (read only, plus the one confirmed `lambe init` of the first-run setup; demo
  and a tracker-only fallback exist).
- **Graph engine** (`internal/graph`): graph insights for Tasks only (the Board is a message board,
  not a graph), computed in this process with bv's analysis package over the tracker's
  `.beads/issues.jsonl`. It is not a tool and has no switch: there is no bv program to bundle,
  find or run.
- **b9s**: an optional extra for its full tree and graph view: press `o` on Tasks to open it in the
  tracker directory (probes for it first). abacus's own Tasks list and detail do not need it: without
  it `o` says that b9s is an optional separate program abacus did not find.
- **Board** (built in): composes the Vercel Board CLI (`@vercel/board`, binary `board`),
  run as a subprocess only: bounded `channels list`, `read` and `tasks` reads with `--json`,
  `--limit` and a deadline, behind an argv allowlist and a scrubbed environment (only `HOME`,
  `PATH`, the XDG dirs, `BOARD_*`, `VERCEL_AUTH_TOKEN`, `VERCEL_ORG_ID`, proxy and CA variables
  are passed on). It never runs `serve`, `tail` or `wait`. The extracted binary is trusted only
  after its own `--help`, run with a token-free environment, identifies Vercel Board at 0.54.0 or
  newer (`ABACUS_BOARD_BIN` overrides the built-in copy). The npm package named `vb` is an
  unrelated project and is never looked for. Without a board
  setting it uses the CLI's own default (a linked Vercel project, `BOARD_URL`, or your team's
  board after `vercel login`); point it elsewhere with `abacus config set board_url URL` or
  `ABACUS_BOARD_URL` (passed as `--url`), for example a local board from `vb serve`. On the
  Board tab `o` opens the CLI's local web inspector (`board inspect`) on an explicit keypress;
  Ctrl-C returns. board-beads is only the beads-backed backend a board can use; abacus needs
  nothing new from it (board-beads#15 is decided: compose the Vercel CLI).
- **Rex** (optional): a terminal workspace. When a Rex server is reachable abacus can say which
  terminal session a run, lease or task lives in; the interface calls this the "workspace" and
  it is not a fifth tab. abacus composes only the `rex` CLI (reads, `capture`, the `events` change
  feed, and `focus`, `run`, `split`, `new` and `window new` writes), always with
  `--autostart=false`, behind exact argv allowlists and a scrubbed environment
  (`HOME`, `PATH`, the XDG dirs and `REX_*`), and never starts a server or types into a
  terminal. On a row, `t` brings its terminal forward and `W` opens one running your `agent_command`
  (`abacus config set agent_command '["prog","Work on {task_id}: {title}"]'`), each after a confirm
  screen; `abacus rex layout` opens the cockpit tabs. Only this machine and the hosts you list in `ABACUS_REX_HOSTS` are ever contacted
  (`-S` can open an ssh connection); `ABACUS_REX_PASS_ENV=SSH_AUTH_SOCK` opts in to passing a
  variable on. Detection is `REX_SESSION`/`REX_SERVER` in the environment, else a short probe; no
  server is "not connected", not an error, and an absent Rex is invisible in Doctor.
  `ABACUS_REX_BIN` overrides the lookup. Opening a placement runs only on an explicit keypress
  and is the existing `rex -S <machine>` handoff, else the command to copy. See
  `docs/tool-contract.md`.

## Usage

```
abacus board|tasks|leases|queues [--json]
abacus tasks create [--title T] [--description D] [--priority N] [--type X] [--yes] [--dry-run]
abacus tasks update ID [--title T] [--description D] [--priority N] [--status S] [--yes] [--dry-run]
abacus tasks close  ID [--reason R] [--yes] [--dry-run]
abacus tasks label add|remove ID LABEL [--yes] [--dry-run]
abacus tasks assign ID NAME | ID --clear [--yes] [--dry-run]
abacus tasks priority ID 0-4 [--yes] [--dry-run]
abacus tasks dep add|remove ID --blocked-by OTHER | --blocks OTHER [--yes] [--dry-run]
abacus tasks comment ID TEXT | ID --text TEXT [--yes] [--dry-run]
abacus search QUERY [--domain D] [--limit N] [--json]   search tasks, board, leases and queues
abacus tui     [--demo]
abacus setup   [--json] [--yes] [--dry-run]       report and perform first-run setup
abacus setup --repair [--yes] [--dry-run]       fix a half-created workspace (nothing is deleted)
abacus config  show [--json] | get KEY | set KEY VALUE | path
abacus status  [--json] [--lambe-config FILE]     domains plus provider state
abacus doctor  [--json] [--lambe-config FILE]     leads with ready or needs attention
abacus version [--json]
```

Exit codes: `doctor` exits 1 when a check failed or a provider is broken; a write exits 1 when it
was not applied or its result is unknown; any command exits 2 on a usage error (including a stray
positional argument). The domain reads (`board`, `tasks`, `leases`, `queues`) and `status` exit 0
whatever the connection state: the outcome is in each domain's `state` (`connected`,
`not-connected`, `degraded`) and `reason`. `status --json` and `doctor --json` keep
`schema_version` 1; `status --json` gains an additive `domains` section. Every read command's JSON
carries `schema_version`. A not-connected or degraded domain keeps the same shape (`leases: []`,
zeroed `counts`). JSON key naming is mixed by origin (`issue` in Queues rows, `id` in Tasks) and is
not renamed within `schema_version` 1.

`abacus tasks <verb> --json` prints exactly one JSON document on stdout and nothing else, and needs
`--yes` or `--dry-run` (it cannot prompt); on failure stdout is empty and the error is on stderr. A
write whose result cannot be known (a timeout or cancel) says "result unknown ... refresh before
retrying", never "not applied", and the audit log records phase `unknown`.

Overrides: `ABACUS_BD_DIR` (the tracker directory, passed as `bd -C`), `ABACUS_BD_BIN`,
`ABACUS_LAMBE_BIN`, `ABACUS_LAMBE_STATE_DIR`, `ABACUS_LAMBE_CONFIG`,
`ABACUS_BEADSLEASE_BIN`, `ABACUS_REX_BIN`, `ABACUS_BOARD_BIN`, `ABACUS_BOARD_URL` (also `abacus config set board_url`; the shell's `BOARD_URL` is used last). A board URL must be https, or http only for a local board, with no query or fragment. The Board JSON no longer has `columns` or `insights` (never populated with Board data): see the CHANGELOG.
Leases need an actor name: `BEADS_ACTOR` in the environment, or
`abacus config set actor NAME` (the environment wins).

### Writes

Tasks can be changed, and the Board can be posted to from the command line
(`abacus board post CHANNEL "TEXT"`, `abacus board reply ID "TEXT"`; `--text` for text that starts
with `-`; no edit, delete, react, attach or task claim/complete). Every change is planned first (validated, with the exact command), then
needs an explicit yes: `y` on the TUI confirm screen, or the `[y/N]` prompt (`--yes` skips it,
`--dry-run` prints the change and the command and runs nothing). A change can be applied once, every
attempt is appended to an audit log (`$XDG_STATE_HOME/abacus/audit.jsonl`), and if the log cannot be
written nothing runs. Writes are limited by an allowlist (create, update, close; no delete; for the Board only post and reply). A board post carries an idempotency key derived from (board, op, destination, text, 10-minute window) so a retry posts at most once (and an identical post within ten minutes counts as that retry), is re-read by id afterwards, and anything unclear (timeout, cancel, network error, unreadable answer) is "result unknown", never "not applied". The confirm shows the board URL and where it came from, what a reply answers, and whether a post creates a new channel. The TUI Board tab is read only: composing from it is not built yet.
The first-run setup steps (workspace, actor name, Queues config) go through the same confirm,
single-use and audit gate. Leases and Queues writes are blocked upstream: see `docs/design/2026-11-21-1700-feat-fleet-domains-plan.md`.

### TUI

`abacus tui` has one tab per domain (Board, Tasks, Leases, Queues) plus Doctor. A tab whose tool
needs one-time setup shows a single prompt (see First run) instead of an error. In Queues, Leases and Board, below
100 columns the list fills the screen and Enter drills into the detail; at 100 and wider the detail
sits beside the list. This is the 80-column Tasks render with colors off (`NO_COLOR`), from the golden test,
using data from fixture providers:

```
abacus   Board  [Tasks]  Leases  Queues  Doctor
────────────────────────────────────────────────────────────────────────────────
[List] Kanban  Graph  Insights                                14 tasks · 3 ready
[open] / to filter                                   12 of 14 · sort: priority ↑
╭─ ▸ Tasks · fleet ────────────────────────────────────────────────────────────╮
│   id    title                                  done st P  dep  type  age who │
│   ac-01 ▾ Fleet workspace 1/5                  ▰▱▱▱ ○  P1      epic  30d mei │
│ ▸ ac-12 ├── wire the lease heartbeat                ◐  P0 →1   bug   9d  ada │
│   ac-15 ├── lease holder column                     ○  P1 →1   feat  6d  mei │
│   ac-09 ├── queue attention digest                  ○  P2 ⊘1   feat  12d -   │
│   ac-16 └── stale lease warning                     ○  P2 ⊘1   task  5d  -   │
│   ac-20   split graph source from export            ◐  P1 ⊘1→1 task  4d  ada │
│   ac-21   export reader                             ○  P2 ⊘1→1 task  4d  -   │
│   ac-25   doctor: stale export warning              ●  P2      bug   3d  ada │
│   ac-27   board thread digest                       ●  P3      feat  2d  -   │
│   ac-30   saved filters                             ○  P3      feat  2d  -   │
│   ac-33   help overlay per view                     ◐  P3      task  1d  mei │
│   ac-36   verify tool flags against help            ◇  P3      chore 40d mei │
│                                                                              │
│                                                                              │
│                                                                              │
│                                                                              │
│                                                                              │
│                                                                              │
│                                                                              │
│ ▾ open ▹ folded                                                              │
│ ⊘N held by N · →N holds N · ⊘↑ held by parent                                │
│ ○ open ◐ active ● blocked ✓ closed ◇ deferred                                │
╰──────────────────────────────────────────────────────────────────────────────╯
? help · q quit · tab/→ next tab · : command · / filter · h/l fold · s/S sort …
```
