# Abacus

**A terminal dashboard for your coding agents and the work they share.**

See which Beads tasks are ready, where work is blocked, and which agents are on it. Follow leases, queues, and Board conversations from the same terminal.

[Install](#install-the-terminal-app) · [Connect a workspace](#connect-a-workspace) · [Read the docs](docs/README.md) · [See what changed](CHANGELOG.md)

![Abacus Tasks view showing a Beads dependency tree, with the lease heartbeat task selected](docs/images/abacus-tasks.png)

## Install the terminal app

On macOS or Linux, run:

```sh
curl -fsSL https://raw.githubusercontent.com/andrew-bierman/abacus/main/scripts/install.sh | bash
```

The installer verifies the release checksum and puts `abacus` in `~/.local/bin`. It prints the PATH command if your shell needs it. You can also [browse the installer](scripts/install.sh) or download a platform archive from [Releases](https://github.com/andrew-bierman/abacus/releases/latest).

```sh
abacus tui --demo
```

The demo runs on sample data. Press **Tab** to explore, **?** for shortcuts, and **q** to quit. Run the installer again to update.

This install is for the terminal app. The planned [native GUI](docs/design/2026-11-24-plan-native-clients.md) will ship separately with its own Abacus helper bundled inside. No terminal install will be required for the GUI.

<details>
<summary>Tasks preview as text</summary>

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

</details>

## Connect a workspace

Open a terminal in your workspace. Start with Doctor if you want to see which connections need setup.

```sh
abacus doctor
abacus tui --tab tasks
```

Abacus detects an existing Beads tracker in your workspace. For a new workspace, run `abacus setup` and review the setup steps before confirming. Use `abacus setup --dry-run` to preview them in your terminal. Queue supervision is optional and starts separately.

| View | What you can see or do |
| --- | --- |
| **Tasks** | Find ready work and blockers in the list, kanban, dependency graph, and insights. |
| **Agents** | See local coding sessions, their repositories and branches, and available activity and quota data. |
| **Leases** | Check which leases are held, stale, or free. |
| **Queues** | Follow Lambe runs and spot failures that need attention. |
| **Board** | Catch up on channels and threads, then preview and confirm posts or replies. |
| **Doctor** | Check provider connections and find setup steps. |

The release bundles Beads, Lambe, beads-lease, the Vercel Board CLI, and abtop. Optional Rex integration connects tasks and runs to terminal sessions. See [built-in tools](docs/built-in-tools.md) and [agent monitoring](docs/agents.md).

## Use Abacus from the CLI

```sh
abacus tasks list --json
abacus agents --json
abacus search release
```

Task changes and Board posts show a plan for confirmation. Add `--dry-run` to preview a write. The [tool contract](docs/tool-contract.md) describes the JSON reports, and the [stdio protocol](docs/serve-protocol.md) covers native clients.

## Contribute

Start with the [development guide](docs/development.md) and [contribution guide](CONTRIBUTING.md). Abacus is licensed under [GPL-3.0-or-later](LICENSE).

## Credits

- [abtop](https://github.com/graykode/abtop) by graykode (MIT): the live agent monitor behind the Agents
  tab's richer detail, and the model for its panels. abacus runs it as a subprocess and imports none of
  its code.
- [gopsutil](https://github.com/shirou/gopsutil) (BSD-3-Clause): the process list.
- [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) (BSD-3-Clause): reads OpenCode's session database, linked in.
- [agent-deck](https://github.com/asheshgoplani/agent-deck) and [agentsview](https://github.com/wesm/agentsview)
  (MIT): read as references for telling what an agent is doing from its files; no code was used.
