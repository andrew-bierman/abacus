# abacus (working name)

The fleet cockpit for Beads-powered agent fleets. See the board, the tasks, the leases and the
Symphony queues, and change the tasks that are safe to change, from one terminal tool. abacus
does not browse issues itself and does not wrap tools: it shows what your fleet is doing, and
the tools that supply the data are providers behind each view (see Providers).

> **Name status:** `abacus` is a placeholder. The final name and brand are undecided. The
> repo is private until then. The name lives in `internal/brand`, the module path in
> `go.mod`, and the `cmd/` directory name.

## Install

Download the archive for your platform from the releases page (`abacus_VERSION_OS_ARCH.tar.gz`,
for darwin or linux on amd64 or arm64), extract it and run it:

```
tar -xzf abacus_VERSION_darwin_arm64.tar.gz
cd abacus_VERSION_darwin_arm64
./abacus
```

Nothing else needs to be installed. The archive carries the tools abacus talks to (`bd`, `bv`,
Lambe and beads-lease) in `bundled/`, and abacus finds them there. Keep the folder together: if
you copy `abacus` somewhere else, copy `bundled/` with it (or symlink `abacus`; the link is
followed to the real folder).

On first run each tab that needs one-time setup says so in one line and offers a key for it, and
shows the exact command before anything happens (see First run below). Press `Tab` to move
between tabs and `?` for keys.

On macOS, a browser-downloaded archive is quarantined by Gatekeeper; clear it once with
`xattr -dr com.apple.quarantine abacus_VERSION_darwin_arm64`.

## Domains

| Domain | What you see | What you can change today |
| --- | --- | --- |
| Board | channels, recent threads and messages, open tasks (a message board for agents, not a kanban) | post a thread or reply from the command line (`abacus board post\|reply`, with a confirm step) |
| Tasks | counts by state, ready work, recent changes, graph insights (next pick, blockers, critical path, cycles) | create, update, close (with a confirm step); first run: create the fleet workspace |
| Leases | held, stale and free leases | read only |
| Queues | runs, the queue, what needs attention, history | read only |

A domain with no provider shows "not connected" and how to connect; it never disappears.
`abacus tasks|leases|queues|board [--json]` print the same typed reports the TUI renders.

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

An empty tracker is not an error: Insights reads "no tasks yet" and bv is not run. If bv fails on
a tracker that has tasks, the tab says so in one plain line and the reason is in Doctor.

## Bundled tools

The release archive is built by `scripts/package-release.sh VERSION` from the pins in
`bundled-tools.json`: each tool's repository, version, per-platform asset name and sha256, and
the sha256 of its license. The script downloads each asset with `gh`, checks the sha256 against
the pin (any mismatch fails the build), extracts only the one executable into `bundled/`, copies
each tool's license verbatim into `third_party/<tool>/LICENSE` and writes
`THIRD_PARTY_NOTICES.md`. It publishes nothing.

| Tool | Version | License |
| --- | --- | --- |
| bd, [Beads](https://github.com/steveyegge/beads) | 1.3.4 | MIT |
| bv, [beads_viewer](https://github.com/Dicklesworthstone/beads_viewer) | 0.27.0 | MIT with an OpenAI/Anthropic rider, shipped unmodified |
| [Lambe](https://github.com/andrew-bierman/lambe) | 0.10.1 | GPL-3.0 |
| [beads-lease](https://github.com/andrew-bierman/beads-lease), installed as `beads-lease` | 0.1.0 | MIT |

The tools stay separate programs run as subprocesses; abacus imports none of their code.
Read `THIRD_PARTY_NOTICES.md` and `third_party/bv/LICENSE` before redistributing the archive.

**Where abacus looks for a tool**, first match wins: an explicit `ABACUS_*_BIN`; then the
directory of the abacus executable (symlinks resolved), `../libexec/abacus/` and `bundled/`
next to it; then your `PATH`. A bundled copy is run only if it is a regular, executable file
that is not world-writable and not in a world-writable directory. Doctor shows, for every tool,
whether it is `bundled`, from `PATH` or an override, or missing, with its version. Child tools
see the sidecar directories first on `PATH`, so bv finds the bundled bd.

**The manifest, field by field.** Hand-edited: `schema`, `note`, and per tool `name` (the file
name in `bundled/`; it must equal the tool names abacus looks for, a test checks it), `repo`,
`license` (display name), `tag_template`, `asset_template` and `member` (with `{version}`, `{os}`
and `{arch}` placeholders), `checksums_asset`, `license_source` (`{from: archive, member}` or
`{from: tag, path}`). Written only by `scripts/update-bundled-tools.sh`: `version`, `assets`
(platform to asset, sha256 and member) and `license_sha256`. The same list is at the top of
`scripts/lib/bundle.sh`, which both scripts share.

**Refreshing a pin:** `scripts/update-bundled-tools.sh TOOL VERSION` (or `--all` to re-pin the
current versions). It reads each platform's sha256 from the tool's upstream checksum file,
downloads the asset with your `gh` login and fails unless the download matches. If upstream
published no checksum for an asset it stops; `--trust-first-download` pins the hash of that one
download instead (say so in the PR; all four tools publish checksums today, so none needed it).
Then run `scripts/package-release.sh VERSION --platforms darwin-arm64 --out DIR` to check the
result. Both scripts need `jq` (and `gh` to download), and read `ABACUS_BUNDLE_CACHE` (default
`.cache/bundle`) and `ABACUS_BUNDLE_MANIFEST` (default `bundled-tools.json`). Archives are built
in a work directory and moved into the output directory only after every platform succeeded;
tar members that are links, absolute or contain `..` are refused before anything is read. The
scripts are tested offline (`internal/release`) with fake archives and a fake `gh`.

## Architecture

`internal/core` holds all the real logic: the domain reports (Board, Tasks, Leases, Queues),
provider interfaces and the registry that maps detected providers to domains, the write gate,
detection, doctor and status aggregation, the typed report JSON (`schema_version`), sanitizing and the exit-code rules.
Adapters (`internal/lambe`, `internal/beadslease`, `internal/bd`, `internal/bv`) compose the
tools only through their `--json` CLIs and versioned local APIs. `internal/cli` and `internal/tui` are thin front-ends that only parse
input and render core's typed reports; a Linux or Swift GUI would be another front-end over
the same core or the same `--json` output. A test enforces that core, adapters and the CLI
import no UI library.

## Providers

Providers are invisible in navigation and appear only here, in Doctor and in "how to connect"
or error messages that help you fix one.

- **Beads (`bd`)**: Tasks reads and writes. Reads always run `--readonly`; writes go through a
  separate allowlist (create, update, close, and the fixed `init` of the first-run workspace) and
  the confirm step.
- **beads-lease**: Leases (read only).
- **Lambe**: Queues (read only, plus the one confirmed `lambe init` of the first-run setup; demo
  and a tracker-only fallback exist).
- **bv**: graph insights for Tasks only (the Board is a message board, not a graph), run as a subprocess in `--robot-*` JSON mode only.
  Not found? Tasks says so and how to get it.
- **b9s**: works with b9s for the full tree and graph view: press `o` on Tasks to open it in the
  tracker directory (probes for it first; shows an install hint if it is missing).
- **Board**: composes the Vercel Board CLI (`@vercel/board`, bins `board` and `vb`, same CLI),
  run as a subprocess only: bounded `channels list`, `read` and `tasks` reads with `--json`,
  `--limit` and a deadline, behind an argv allowlist and a scrubbed environment (only `HOME`,
  `PATH`, the XDG dirs, `BOARD_*`, `VERCEL_AUTH_TOKEN`, `VERCEL_ORG_ID`, proxy and CA variables
  are passed on). It never runs `serve`, `tail` or `wait`. Install it with
  `npm i -g @vercel/board`, never `npx vb` or `npm i vb`: **`vb` on npm is an unrelated
  package**, so a binary is trusted only after its own `--help` identifies Vercel Board
  (`ABACUS_BOARD_BIN` overrides the lookup, which prefers `board` over `vb`). Without a board
  setting it uses the CLI's own default (a linked Vercel project, `BOARD_URL`, or your team's
  board after `vercel login`); point it elsewhere with `abacus config set board_url URL` or
  `ABACUS_BOARD_URL` (passed as `--url`), for example a local board from `vb serve`. On the
  Board tab `o` opens the CLI's local web inspector (`board inspect`) on an explicit keypress;
  Ctrl-C returns. board-beads is only the beads-backed backend a board can use; abacus needs
  nothing new from it (board-beads#15 is decided: compose the Vercel CLI).

## Credits & ecosystem

- [Beads](https://github.com/gastownhall/beads) (`bd`) is the tracker everything sits on.
- [beads_viewer](https://github.com/Dicklesworthstone/beads_viewer) (`bv`) by Jeffrey Emanuel
  supplies the graph analysis; abacus only runs the `bv` binary and imports none of its code. See
  its [license](https://github.com/Dicklesworthstone/beads_viewer/blob/main/LICENSE).
- [Vercel Board](https://github.com/vercel/board) (`@vercel/board`, Apache-2.0, by Vercel) is the
  message board the Board domain reads and posts to; abacus only runs its `board`/`vb` CLI as a
  subprocess and imports, vendors and copies none of its code. Fixtures in
  `internal/board/testdata` are its `--json` output about invented messages.
- [b9s](https://github.com/vanderheijden86/b9s) (MIT, by another author) is the full interactive
  tree and graph view abacus hands off to from Tasks.
- [k9s](https://k9scli.io) inspired the keyboard-first, terminal-native feel.

## Development rules

- Develop and test against an isolated `HOME`, `XDG_*` dirs and `bd` database. Never touch a
  production Lambe daemon, its state, its binary or real Beads data.
- Nothing here launches an app or takes window focus. Tests use fakes for Lambe, HTTP and Rex; the only real
  tools tests run are `bd` and `bv`, against temp projects with an isolated `HOME`, and they skip when absent.
  Sidecar tests use fake binaries in temp dirs.
- Use a private `GOCACHE` for builds.

Note: the two `signalZero` bodies under `internal/lambe` are kept separate on purpose; they port
Lambe's own process-liveness logic per platform, and merging them would drift from it.

License: GPL-3.0-or-later, matching Lambe.

## Usage

```
abacus board|tasks|leases|queues [--json]
abacus tasks create [--title T] [--description D] [--priority N] [--type X] [--yes] [--dry-run]
abacus tasks update ID [--title T] [--description D] [--priority N] [--status S] [--yes] [--dry-run]
abacus tasks close  ID [--reason R] [--yes] [--dry-run]
abacus tui     [--demo]
abacus setup   [--json] [--yes] [--dry-run]       report and perform first-run setup
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
`ABACUS_BV_BIN`, `ABACUS_LAMBE_BIN`, `ABACUS_LAMBE_STATE_DIR`, `ABACUS_LAMBE_CONFIG`,
`ABACUS_BEADSLEASE_BIN`, `ABACUS_BOARD_BIN`, `ABACUS_BOARD_URL` (also `abacus config set board_url`).
Leases need an actor name: `BEADS_ACTOR` in the environment, or
`abacus config set actor NAME` (the environment wins).

### Writes

Tasks can be changed, and the Board can be posted to from the command line
(`abacus board post CHANNEL "TEXT"`, `abacus board reply ID "TEXT"`; `--text` for text that starts
with `-`; no edit, delete, react, attach or task claim/complete). Every change is planned first (validated, with the exact command), then
needs an explicit yes: `y` on the TUI confirm screen, or the `[y/N]` prompt (`--yes` skips it,
`--dry-run` prints the change and the command and runs nothing). A change can be applied once, every
attempt is appended to an audit log (`$XDG_STATE_HOME/abacus/audit.jsonl`), and if the log cannot be
written nothing runs. Writes are limited by an allowlist (create, update, close; no delete; for the Board only post and reply). A board post carries an idempotency key, is re-read by id afterwards, and a timeout is "result unknown", never "not applied". The TUI Board tab is read only: composing from it is not built yet.
The first-run setup steps (workspace, actor name, Queues config) go through the same confirm,
single-use and audit gate. Leases and Queues writes are blocked upstream: see `docs/plans/2026-11-21-1700-feat-fleet-domains-plan.md`.

### TUI

`abacus tui` has one tab per domain (Board, Tasks, Leases, Queues) plus Doctor. A tab whose tool
needs one-time setup shows a single prompt (see First run) instead of an error. In Queues, below 100
columns the list fills the screen and Enter drills into a run; at 100 and wider the run detail sits
beside the list. This is the 80-column Tasks render with colors off (`NO_COLOR`), from the golden test,
using data from fixture providers:

```
abacus   Board  [Tasks]  Leases  Queues  Doctor
────────────────────────────────────────────────────────────────────────────────
Tasks
14 tasks: 6 open · 3 in progress · 2 blocked · 1 deferred · 2 closed
ready 4

Recent changes
▸ ac-12         in_progress  P1  wire the lease heartbeat
  ac-09         open  P2  queue attention digest
  ac-03         closed  P3  retire the old poller

Insights (graph analysis)
  4 actionable · 2 blocked · 1 cycles
  next     ac-12  wire the lease heartbeat  (unblocks 2)
  blocker  ac-12  wire the lease heartbeat  (unblocks 2)
  critical path  ac-12 > ac-09
  cycle  ac-20 -> ac-21 -> ac-20
? help · q quit · tab/→ next tab · ↑/↓ move · n new · e edit · x close task …
```