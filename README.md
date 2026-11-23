# abacus

The fleet cockpit for [Beads](https://github.com/steveyegge/beads)-powered agent fleets: one
terminal tool that shows what your agents are doing and lets you change the tasks that are safe
to change.

- **Board**: channels, recent threads and open tasks (a message board for agents).
- **Tasks**: counts by state, ready work, recent changes, graph insights (next pick, blockers,
  critical path, cycles). Create, update and close tasks behind a confirm step.
- **Leases**: held, stale and free leases.
- **Queues**: runs, the queue, what needs attention, history.
- **Doctor**: whether every tool abacus talks to is present, where it came from and which version.

It is for people who run several coding agents against a shared Beads tracker and want one place
to look and one safe place to act. abacus does not browse issues itself and does not wrap tools:
the tools that supply the data are providers behind each view (see [Providers](#providers)).

Status: pre-1.0 (current release 0.3.0). Minor versions may change behavior; patch versions are
fixes and small additions. The name `abacus` is a working name and may change; see the
[CHANGELOG](CHANGELOG.md).

## Try it in seconds

No fleet, no tracker and no setup needed. The demo shows every tab with invented data, reads
nothing from your machine and never runs a tool or writes anything:

```
./abacus tui --demo          # from an extracted release archive
```

From a source checkout (needs Go 1.26.11 or newer, `gh` logged in, and `jq`):

```
make dev                     # build bin/abacus and fetch the pinned tools into bin/bundled/
./bin/abacus tui --demo
./bin/abacus doctor
```

Press `Tab` or `1`-`5` to switch tabs, `?` for every key, `q` to quit.

## Install

Download `abacus_VERSION_OS_ARCH.tar.gz` from the releases page, extract it and run it:

```
tar -xzf abacus_VERSION_linux_amd64.tar.gz
cd abacus_VERSION_linux_amd64
./abacus tui
```

Running `abacus` with no command prints the command list. Nothing else needs to be installed:
the archive carries the tools abacus talks to (`bd`, `bv`, Lambe and beads-lease) in `bundled/`,
and abacus finds them there. Keep the folder together: if you copy `abacus` somewhere else, copy
`bundled/` with it (or symlink `abacus`; the link is followed to the real folder).

On macOS, a browser-downloaded archive is quarantined by Gatekeeper; clear it once with
`xattr -dr com.apple.quarantine abacus_VERSION_darwin_arm64`.

### Platforms

| Platform | Status |
| --- | --- |
| Linux amd64 | The test target. CI (build, vet, tests) runs on Ubuntu, and the TUI has been used on Arch Linux. |
| Linux arm64 | Release archive, cross-compiled. Not run on real hardware. |
| macOS arm64 and amd64 | Release archive, cross-compiled. Assumed to work; not verified by CI or by a maintainer test run. |
| Windows | Not supported. It compiles, but its safety checks are placeholders; see [the Windows plan](docs/plans/2026-11-22-1900-plan-windows-support.md). |

## What you see

```
abacus tasks|leases|queues|board [--json]     the same typed reports the TUI renders
abacus tui [--demo]
```

| Tab | What you see | What you can change from abacus |
| --- | --- | --- |
| Board | channels, recent threads and messages, open tasks (a message board for agents, not a kanban) | post a thread or reply from the command line (`abacus board post\|reply`, with a confirm step); the Board tab itself only reads |
| Tasks | counts by state, ready work, recent changes, graph insights | create, update, close (TUI `n` `e` `x` or `abacus tasks create\|update\|close`), each with a confirm step |
| Leases | held, stale and free leases | nothing yet, apart from the one-time actor name in first-run setup |
| Queues | runs, the queue, what needs attention, history | nothing yet, apart from the one-time supervisor config in first-run setup |
| Doctor | readiness, every tool with its source and version, detailed checks | nothing; `r` reruns it |

A domain with no provider shows "not connected" and how to connect; it never disappears. Leases
and Queues writes are blocked upstream, see
[the fleet domains plan](docs/plans/2026-11-21-1700-feat-fleet-domains-plan.md).

This is the 80-column Tasks render with colors off (`NO_COLOR`), taken from the golden test,
using data from fixture providers:

```
abacus   Board  [Tasks]  Leases  Queues  Doctor
────────────────────────────────────────────────────────────────────────────────
╭─ ▸ Tasks ────────────────────────────────────────────────────────────────────╮
│ ▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒█████████▓▓▓▓▓▓░░░······                                     │
│ 14 tasks: 6 open · 3 in progress · 2 blocked · 1 deferred · 2 closed         │
│ ready 4                                                                      │
│                                                                              │
│ Recent changes                                                               │
│ ▸ ac-12  in_progress  P1  wire the lease heartbeat                           │
│   ac-09  open         P2  queue attention digest                             │
│   ac-03  closed       P3  retire the old poller                              │
│                                                                              │
│                                                                              │
╰──────────────────────────────────────────────────────────────────────────────╯
╭─ Insights · graph analysis ──────────────────────────────────────────────────╮
│ 4 actionable · 2 blocked · 1 cycles                                          │
│ next     ac-12  wire the lease heartbeat  (unblocks 2)                       │
│ blocker  ac-12  wire the lease heartbeat  (unblocks 2)                       │
│ critical path  ac-12 > ac-09                                                 │
│ cycle  ac-20 -> ac-21 -> ac-20                                               │
╰──────────────────────────────────────────────────────────────────────────────╯
? help · q quit · tab/→ next tab · ↑/↓ move · n new · e edit · x close task …
```

### Keys

The footer and the `?` overlay are generated from the key bindings, so they match what the keys
do. The full table is in [docs/tui.md](docs/tui.md).

| Key | Action |
| --- | --- |
| `tab` / `shift+tab`, `1`-`5` | next or previous tab; jump to Board, Tasks, Leases, Queues, Doctor |
| `j` `k` or arrows | move the cursor |
| `n` `e` `x` | Tasks: new, edit, close. A form, then a confirm screen: `y` applies, `n` or `esc` cancels |
| `r` | refresh (Tasks also reruns graph insights; Doctor reruns doctor) |
| `o` | Tasks: open the full tree view in b9s (optional). Board: local web inspector. Queues: hand off to the run's session |
| `w` | Leases, Tasks: go to the item's Rex session (optional, only when the row has a placement) |
| `i`, `s` | on a tab that needs setup: start its setup step |
| `enter` / `esc`, `/` | narrow layout: open or close the detail; Queues: filter runs |
| `?`, `q` | help overlay, quit |

Layout adapts to the terminal: below 100 columns the list fills the screen and `enter` drills
into the detail; at 100 and wider the detail sits beside the list; from 140 columns Rex-aware
lists add a `where` column. Under 40 columns or 12 rows the body says "Terminal too small".
Every color is backed by a word or glyph, so `NO_COLOR` loses nothing.

## First run

A fresh machine has no tracker, no actor name and no supervisor config. abacus never creates any
of them silently. Each is a setup step with a one-line prompt, a confirm screen that shows the
exact command, and an audit entry; none starts a daemon.

| Tab | Prompt | Key | What it does |
| --- | --- | --- | --- |
| Tasks | No tracker here, press `i` to create your fleet workspace | `i` | `bd init` in `$XDG_DATA_HOME/abacus/workspace` (or `~/.local/share/abacus/workspace`) with the fixed flags `--non-interactive --quiet --skip-agents --skip-hooks --prefix=abacus`; the folder is then remembered in the config |
| Leases | Leases need an actor name | `s` | saves a suggested actor name (user and machine) in the config; `abacus config set actor NAME` changes it |
| Queues | Queues need a supervisor config | `s` | `lambe init -non-interactive -config <abacus config dir>/lambe.yaml`, run in the workspace: writes a starter config, WORKFLOW.md and formula. It starts nothing; you run `lambe run -config ...` yourself. Needs the workspace first |

`abacus setup [--json] [--yes] [--dry-run]` reports the same steps and, with `--yes` (or a `y` at
the prompt on a terminal), performs them through the same gate. Without input or `--yes` it only
reports. Leases also need a coordination hub (beads-lease reads one through its `coord` wrapper,
`BEADS_LEASE_COORD`); abacus cannot create a shared hub, so on a machine without one Leases says
so plainly and stays out of the way.

Settings live in `$XDG_CONFIG_HOME/abacus/config.json` (written atomically, mode 0600):
`abacus config show|get KEY|set KEY VALUE|path`, with the keys `actor`, `tracker_dir`,
`lambe_config` and `board_url`. Tasks picks its tracker in this order: `ABACUS_BD_DIR`, a tracker
at or above the working directory, `tracker_dir` from the config, an existing managed workspace.

An empty tracker is not an error: Insights reads "no tasks yet" and bv is not run. If bv fails
on a tracker that has tasks, the tab says so in one line and the reason is in Doctor.

## Safety model

Everything abacus can change goes through one write gate, for the CLI and the TUI alike:

1. **Plan**: the change is validated and the exact command is built and shown.
2. **Confirm**: nothing runs without an explicit yes (`y` on the TUI confirm screen, the `[y/N]`
   prompt, or `--yes`). `--dry-run` prints the plan and runs nothing. A confirm that does not fit
   on screen cannot be answered with `y`.
3. **Apply once**: a plan can be applied a single time, through an allowlist (create, update,
   close; no delete; for the Board only post and reply).
4. **Audit**: every attempt is appended to `$XDG_STATE_HOME/abacus/audit.jsonl`. If the log
   cannot be written, nothing runs.

A write whose result cannot be known (a timeout or cancel) reports "result unknown ... refresh
before retrying", never "not applied", and the audit phase is `unknown`. Reads of the tracker
always run `bd --readonly`. The demo never executes anything: a confirmed demo change is applied
to nothing, reports a made-up id and writes no real audit entry.

Board posts carry an idempotency key derived from (board, op, destination, text, 10-minute
window), so a retry posts at most once, and are re-read by id afterwards. The confirm shows the
board URL and where it came from, what a reply answers, and whether a post creates a new channel.

## Command line

```
abacus board|tasks|leases|queues [--json]
abacus board post CHANNEL "TEXT" | reply ID "TEXT"      (--text for text that starts with -)
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
was not applied or its result is unknown; any command exits 2 on a usage error. The domain reads
and `status` exit 0 whatever the connection state: the outcome is in each domain's `state`
(`connected`, `not-connected`, `degraded`) and `reason`. Every read command's JSON carries
`schema_version` (currently 1). A not-connected or degraded domain keeps the same shape
(`leases: []`, zeroed `counts`). JSON key naming is mixed by origin (`issue` in Queues rows, `id`
in Tasks) and is not renamed within schema version 1.

`abacus tasks <verb> --json` prints exactly one JSON document on stdout and nothing else, and
needs `--yes` or `--dry-run` (it cannot prompt); on failure stdout is empty and the error is on
stderr.

## Environment variables

| Variable | Purpose |
| --- | --- |
| `ABACUS_BD_DIR` | the tracker directory Tasks uses (passed as `bd -C`) |
| `ABACUS_BD_BIN`, `ABACUS_BV_BIN`, `ABACUS_LAMBE_BIN`, `ABACUS_BEADSLEASE_BIN` | use this executable instead of the bundled or `PATH` copy |
| `ABACUS_LAMBE_CONFIG`, `ABACUS_LAMBE_STATE_DIR` | which Lambe config and state directory Queues reads |
| `ABACUS_BOARD_BIN` | the Vercel Board CLI to use (optional) |
| `ABACUS_BOARD_URL` | board URL (also `abacus config set board_url`; the shell's `BOARD_URL` is used last). https only, or http for a local board, no query or fragment |
| `ABACUS_GRAPH_ENGINE` | `library` runs bv's analysis in-process for Tasks insights (opt-in). Unset or `subprocess` is the default. Any other value falls back to the default and Doctor reports it |
| `ABACUS_REX_BIN` | the `rex` executable to use (optional) |
| `ABACUS_REX_HOSTS` | comma-separated hosts, besides this machine, that Rex reads may contact with `-S` (at most 16) |
| `ABACUS_REX_PASS_ENV` | comma-separated variable names to pass on to `rex`, for example `SSH_AUTH_SOCK` (at most 8) |
| `BEADS_ACTOR` | actor name for Leases; wins over `abacus config set actor` |
| `NO_COLOR` | turn styling off |

For development and packaging only: `ABACUS_BUNDLE_CACHE` (default `.cache/bundle`),
`ABACUS_BUNDLE_MANIFEST` (default `bundled-tools.json`) and `ABACUS_PACKAGE_PREBUILT_BIN`.

## Providers

Providers are invisible in navigation and appear only here, in Doctor and in "how to connect" or
error messages that help you fix one.

| Provider | Role | Bundled |
| --- | --- | --- |
| `bd` (Beads) | Tasks reads and writes | yes |
| `bv` | graph insights for Tasks | yes (optional) |
| beads-lease | Leases | yes |
| Lambe | Queues | yes |
| Board CLI | Board tab | no, optional |
| Rex | workspace layer | no, optional |
| b9s | full tree and graph view | no, optional |

- **Beads (`bd`)**: Tasks reads and writes. Reads always run `--readonly`; writes go through a
  separate allowlist (create, update, close, and the fixed `init` of the first-run workspace) and
  the confirm step.
- **beads-lease**: Leases (reads; no writes from abacus).
- **Lambe**: Queues (reads, plus the one confirmed `lambe init` of the first-run setup; demo and a
  tracker-only fallback exist).
- **bv**: graph insights for Tasks only, by default as a subprocess in `--robot-*` JSON mode. With
  `ABACUS_GRAPH_ENGINE=library` the same insights come from bv's Go analysis package linked into
  the abacus binary (only `internal/graph/bvanalyzer.go` imports it). If abacus's own bv is
  unusable, Tasks says so and how to restore it.
- **b9s** (optional, not bundled): the full tree and graph view. Press `o` on Tasks to open it in
  the tracker directory (probes for it first). Without it `o` says what b9s adds and where to
  get it.
- **Board** (optional, not bundled): composes the Vercel Board CLI (`@vercel/board`, bins `board`
  and `vb`, same CLI), run as a subprocess only: bounded `channels list`, `read` and `tasks`
  reads with `--json`, `--limit` and a deadline, behind an argv allowlist and a scrubbed
  environment (only `HOME`, `PATH`, the XDG dirs, `BOARD_*`, `VERCEL_AUTH_TOKEN`,
  `VERCEL_ORG_ID`, proxy and CA variables are passed on). It never runs `serve`, `tail` or
  `wait`. Install it with `npm i -g @vercel/board`, never `npx vb` or `npm i vb`: **`vb` on npm
  is an unrelated package**, so a binary is trusted only after its own `--help` identifies
  Vercel Board (`ABACUS_BOARD_BIN` overrides the lookup, which prefers `board` over `vb`).
  Without a board setting it uses the CLI's own default (a linked Vercel project, `BOARD_URL`,
  or your team's board after `vercel login`); point it elsewhere with
  `abacus config set board_url URL` or `ABACUS_BOARD_URL`, for example a local board from
  `vb serve`. On the Board tab `o` opens the CLI's local web inspector (`board inspect`) on an
  explicit keypress; Ctrl-C returns. Posting from the TUI Board tab is not built yet; use
  `abacus board post|reply`.
- **Rex** (optional, not bundled): a terminal workspace. When a Rex server is reachable abacus
  can say which terminal session a run, lease or task lives in (the "workspace" layer; it is not
  a fifth tab). `w` on Leases and Tasks and `o` on Queues jump to that session on an explicit
  keypress, only when the placement is unambiguous, as the existing `rex -S <machine>` handoff;
  otherwise you get a notice with the candidates or the command to copy. abacus composes only
  the `rex` CLI (`ls --json` and `version --json`, always `--autostart=false`), behind an argv
  allowlist and a scrubbed environment (`HOME`, `PATH`, the XDG dirs and `REX_*`), and never
  starts a server or types into a session. Only this machine and the hosts in
  `ABACUS_REX_HOSTS` are contacted (`-S` can open an ssh connection);
  `ABACUS_REX_PASS_ENV=SSH_AUTH_SOCK` opts in to passing a variable on. Detection is
  `REX_SESSION`/`REX_SERVER` in the environment, else a short probe; no server is "not
  connected", not an error, and an absent Rex is invisible in Doctor. See
  [docs/tool-contract.md](docs/tool-contract.md).

## Bundled tools

The release archive is built by `scripts/package-release.sh VERSION` from the pins in
[bundled-tools.json](bundled-tools.json): each tool's repository, version, per-platform asset name and sha256, and
the sha256 of its license. The script downloads each asset with `gh`, checks the sha256 against
the pin (any mismatch fails the build), extracts only the one executable into `bundled/`, copies
each tool's license verbatim into `third_party/<tool>/LICENSE` and writes
`THIRD_PARTY_NOTICES.md`. It publishes nothing. The four tools run as separate programs,
unmodified. Separately, the `abacus` binary statically links parts of bv's Go packages (bv
v0.27.0 `pkg/analysis` and dependencies) for the optional in-process graph engine, under bv's
license (`third_party/bv/LICENSE`). The notices say so and end with every Go module linked into
the binary and its license text, generated offline from `go list -deps`
(`scripts/lib/golicenses.sh`).

| Tool | Version | License |
| --- | --- | --- |
| bd, [Beads](https://github.com/steveyegge/beads) | 1.3.4 | MIT |
| bv, [beads_viewer](https://github.com/Dicklesworthstone/beads_viewer) | 0.27.0 | MIT with an OpenAI/Anthropic rider |
| [Lambe](https://github.com/andrew-bierman/lambe) | 0.10.1 | GPL-3.0 |
| [beads-lease](https://github.com/andrew-bierman/beads-lease), installed as `beads-lease` | 0.1.0 | MIT |

Read `THIRD_PARTY_NOTICES.md` and `third_party/bv/LICENSE` (both in the release archive) before
redistributing it.

**Where abacus looks for a tool**, first match wins: an explicit `ABACUS_*_BIN`; then the
directory of the abacus executable (symlinks resolved), `../libexec/abacus/` and `bundled/` next
to it; then your `PATH`. A bundled copy is run only if it resolves (through symlinks) to a
regular, executable file that is not group- or world-writable, is owned by you or root, and sits
(with its link, if any) in a directory nobody else can write to. Doctor shows, for every tool,
whether it is `bundled`, from `PATH` or an override, or `unavailable` (abacus's own copy is
damaged or was moved), with its version, and leads with "Bundled tools: 4 of 4 verified". Doctor
is `Ready` only when bd, lambe and beads-lease are usable (bv adds graph insights and is optional:
it counts in the line but never blocks `Ready`); the Vercel Board CLI is reported separately as
`optional`. Child tools see the sidecar directories first on `PATH`, so bv finds the bundled bd.

A missing bundled tool is never your job. In a release, abacus says its own copy is damaged or
was moved (with the reason when a copy was refused) and to re-extract the archive and keep the
folder together. In a locally built binary it says to run `scripts/dev-bundle.sh`. Only tools
abacus does not ship (the Vercel Board CLI, Rex, b9s) are optional extras, and each says what it
adds.

**Local builds: `scripts/dev-bundle.sh`.** `go build` leaves no `bundled/` beside the binary, so
the tools would look unavailable. `scripts/dev-bundle.sh` (or `make bundle`; `make dev` builds
and bundles) downloads the same pinned assets with `gh` for this OS and CPU (`--platform
linux-amd64` for another), checks every sha256 against `bundled-tools.json` (any mismatch aborts
and changes nothing), and puts the four executables, their licenses and `THIRD_PARTY_NOTICES.md`
in `bin/bundled/`, where `bin/abacus` finds them. It is idempotent, and `--check` (or
`make check-bundle`) only verifies. `--out` must be new, empty or a previous install (anything
else is refused untouched, because the directory is replaced as a whole). Linux and macOS on
amd64 and arm64 have pins; Windows has none.

**The manifest, field by field.** Hand-edited: `schema`, `note`, and per tool `name` (the file
name in `bundled/`; it must equal the tool names abacus looks for, a test checks it), `repo`,
`license` (display name), `tag_template`, `asset_template` and `member` (with `{version}`,
`{os}` and `{arch}` placeholders), `checksums_asset`, `license_source` (`{from: archive, member}`
or `{from: tag, path}`). Written only by `scripts/update-bundled-tools.sh`: `version`, `assets`
(platform to asset, sha256 and member) and `license_sha256`. The same list is at the top of
`scripts/lib/bundle.sh`, which both scripts share.

**Refreshing a pin:** `scripts/update-bundled-tools.sh TOOL VERSION` (or `--all` to re-pin the
current versions). It reads each platform's sha256 from the tool's upstream checksum file,
downloads the asset with your `gh` login and fails unless the download matches. If upstream
published no checksum for an asset it stops; `--trust-first-download` pins the hash of that one
download instead (say so in the PR; all four tools publish checksums today, so none needed it).
Then run `scripts/package-release.sh VERSION --platforms linux-amd64 --out DIR` to check the
result. Both scripts need `jq` (and `gh` to download). Archives are built in a work directory
and moved into the output directory only after every platform succeeded; tar members that are
links, absolute or contain `..` are refused before anything is read. The scripts are tested
offline (`internal/release`) with fake archives and a fake `gh`.

## Architecture

`internal/core` holds all the real logic: the domain reports (Board, Tasks, Leases, Queues),
provider interfaces and the registry that maps detected providers to domains, the write gate,
detection, doctor and status aggregation, the typed report JSON (`schema_version`), sanitizing
and the exit-code rules. Adapters (`internal/lambe`, `internal/beadslease`, `internal/bd`,
`internal/bv`, `internal/board`, `internal/rex`, `internal/graph`) compose the tools through their `--json` CLIs
and versioned local APIs, or, for the optional graph engine, bv's Go package. `internal/cli` and
`internal/tui` are thin front-ends that only parse input and render core's typed reports; a
Linux or Swift GUI would be another front-end over the same core or the same `--json` output. A
test enforces that core, adapters and the CLI import no UI library.

## Development

Building needs Go 1.26.11 or newer (`go.mod` names it, because the optional in-process graph
engine links bv's analysis package). Go's default `GOTOOLCHAIN=auto` downloads that toolchain the
first time you build (it needs network access); offline, install it first, for example
`mise use go@1.26.11`.

| Command | What it does |
| --- | --- |
| `make build` | `go build -o bin/abacus ./cmd/abacus` |
| `make bundle` | fetch the pinned tools into `bin/bundled/` (needs `gh` logged in, and `jq`) |
| `make dev` | build and bundle |
| `make check-bundle` | verify `bin/bundled/` without downloading |
| `go test ./...` | all tests (fakes, fixtures and temp dirs; the real-`bd`/`bv` tests skip when absent) |
| `go test ./internal/tui -update` | rewrite the TUI goldens and the README sample, then review the diff |
| `gofmt -l .` and `go vet ./...` | formatting and vet, both enforced by CI |

Rules for working on abacus:

- Develop and test against an isolated `HOME`, `XDG_*` dirs and `bd` database. Never touch a
  production Lambe daemon, its state, its binary or real Beads data.
- Nothing here launches an app or takes window focus. Tests use fakes for Lambe, HTTP and Rex;
  the only real tools tests run are `bd` and `bv`, against temp projects with an isolated
  `HOME`. Sidecar tests use fake binaries in temp dirs.
- Use a private `GOCACHE` for builds.
- The sample above is checked against `internal/tui/testdata/golden/tasks.ascii.golden` by
  `TestReadmeSampleMatchesTheGolden`; change the TUI, run the `-update` command, and the sample
  follows.
- The two `signalZero` bodies under `internal/lambe` are kept separate on purpose; they port
  Lambe's own process-liveness logic per platform, and merging them would drift from it.

## Docs

- [docs/tui.md](docs/tui.md): the TUI in detail (keys, layout, theme, workspace, demo, tests).
- [docs/tool-contract.md](docs/tool-contract.md): how abacus composes each tool.
- [docs/plans/](docs/plans/): design plans and assessments, including
  [the Tasks workspace plan](docs/plans/2026-11-23-feat-tasks-workspace-plan.md).
- [CHANGELOG.md](CHANGELOG.md): what changed in each release.

## License and credits

abacus is GPL-3.0-or-later, matching Lambe; see [LICENSE](LICENSE). Release archives also carry
`THIRD_PARTY_NOTICES.md` and each bundled tool's license in `third_party/<tool>/LICENSE`.

- [Beads](https://github.com/steveyegge/beads) (`bd`) is the tracker everything sits on.
- [beads_viewer](https://github.com/Dicklesworthstone/beads_viewer) (`bv`) by Jeffrey Emanuel
  supplies the graph analysis. The `bv` binary is bundled and run as a separate program, and the
  abacus binary also links bv's analysis packages for the optional library engine (see
  [Bundled tools](#bundled-tools)). See its
  [license](https://github.com/Dicklesworthstone/beads_viewer/blob/main/LICENSE).
- [Vercel Board](https://github.com/vercel/board) (`@vercel/board`, Apache-2.0, by Vercel) is
  the message board the Board domain reads and posts to; abacus only runs its `board`/`vb` CLI
  as a subprocess and imports, vendors and copies none of its code. Fixtures in
  `internal/board/testdata` are its `--json` output about invented messages.
- [b9s](https://github.com/vanderheijden86/b9s) (MIT, by another author) is the full interactive
  tree and graph view abacus hands off to from Tasks.
- [k9s](https://k9scli.io) inspired the keyboard-first, terminal-native feel.
