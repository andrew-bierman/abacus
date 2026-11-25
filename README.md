# Abacus

Abacus is a terminal dashboard for developers running Beads-backed coding agents. Browse tasks and their dependencies, see what agents are working on, and check leases, supervisor queues, and Board messages from one place.

## Try the demo

Download the archive for your platform from [GitHub Releases](https://github.com/andrew-bierman/abacus/releases/latest). Releases provide Linux and macOS archives for amd64 and arm64, along with `SHA256SUMS`.

For Linux amd64 with v0.6.0, run this from the download directory.

```sh
tar -xzf abacus_0.6.0_linux_amd64.tar.gz
./abacus_0.6.0_linux_amd64/abacus tui --demo
```

On macOS, download the `darwin_arm64` archive for Apple silicon or `darwin_amd64` for Intel. Extract it and run `./abacus tui --demo` from the extracted directory. If Gatekeeper blocks the executable from the official release, verify the archive against `SHA256SUMS` before clearing quarantine with `xattr -d com.apple.quarantine ./abacus`.

The demo uses fixture data, so you can explore without connecting any services. Press `Tab` to switch views, `?` for help, and `q` to quit. Move the `abacus` executable onto your `PATH` for the commands below.

## Connect your workspace

```sh
abacus doctor
abacus setup --dry-run
abacus tui
```

Doctor shows which providers are ready and how to connect the others. Setup previews the managed workspace, lease actor, and queue configuration steps. Use `abacus setup` to review and confirm the steps interactively. Setup leaves starting the queue supervisor to you.

Run Abacus from an existing Beads workspace to use its tracker. For a single command, point it at another tracker with `--tracker /absolute/path/to/workspace`.

| View | What you can do |
| --- | --- |
| Board | Read channels and threads, and post or reply with confirmation. Connect using your Vercel Board configuration. |
| Tasks | Browse, filter, and update Beads tasks. Switch between the list, kanban, dependency graph, and insights. |
| Leases | See held, stale, and free leases from your coordination hub. |
| Queues | Inspect Lambe runs, queue history, and items that need attention. |
| Agents | See local coding agent sessions, repository and branch, and activity details where available. |
| Doctor | Check provider connections and troubleshoot setup. |

The release binary bundles Beads, Lambe, beads-lease, the Vercel Board CLI, and the abtop agent monitor. The built-in tools unpack into a private per-user directory on first use. Rex is optional and can connect tasks and runs to terminal sessions. See [built-in tools](docs/built-in-tools.md) and [agent monitoring](docs/agents.md) for details.

## Use Abacus from the CLI

```sh
abacus tasks list --json
abacus agents --json
abacus search "release"
abacus config show
abacus tasks --help
```

Task changes and Board posts show a plan before confirmation. Use `--dry-run` on a write command to inspect that plan. Leases and queues are read-only views.

For integrations, see the [tool contract](docs/tool-contract.md) and the [stdio JSON-RPC protocol](docs/serve-protocol.md). `abacus schema` lists the JSON schemas, and `abacus licenses` prints the bundled notices.

## Terminal preview

The Tasks tab shows data from fixture providers:

```
abacus   Board  [Tasks]  Leases  Queues  Agents  Doctor
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

## Credits

- [abtop](https://github.com/graykode/abtop) by graykode (MIT): the live agent monitor behind the Agents
  tab's richer detail, and the model for its panels. abacus runs it as a subprocess and imports none of
  its code.
- [gopsutil](https://github.com/shirou/gopsutil) (BSD-3-Clause): the process list.
- [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) (BSD-3-Clause): reads OpenCode's session database, linked in.
- [agent-deck](https://github.com/asheshgoplani/agent-deck) and [agentsview](https://github.com/wesm/agentsview)
  (MIT): read as references for telling what an agent is doing from its files; no code was used.
