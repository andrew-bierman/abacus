---
title: Fleet Domains, Domain-First Navigation and the Tasks Write Path - Plan
type: feat
date: 2026-11-21
artifact_contract: ce-unified-plan/v1
product_contract_source: owner-direction-2026-11-21
execution: code
---

# Fleet Domains, Domain-First Navigation and the Tasks Write Path - Plan

**Target repo:** `abacus` (`~/Code/abacus`, private `andrew-bierman/abacus`). Branch `feat/fleet-domains`, draft PR only. Upstream repos are only read here; gaps are filed on `andrew-bierman/beads-lease`, `andrew-bierman/lambe` and `andrew-bierman/board-beads`.

## Goal Capsule

- **Objective:** abacus presents one thing to the user: the cockpit for a Beads-powered agent fleet. See the board, the tasks, the leases and the queues, and change what is safe to change. The tools that supply this data are invisible providers behind domain views.
- **Means:** a typed domain layer in core (Board, Tasks, Leases, Queues), provider interfaces, a registry that maps detected modules to domains, then domain-first CLI and TUI, then one write path (Tasks via `bd`) behind a write allowlist and a confirm step.
- **Authority:** the owner's product direction (below), then the settled constraints from the earlier plans. Nothing here reopens them.
- **Stop conditions:** stop and report if any test or command would run `beads-lease`, `lambe` or `bd` against a real hub, daemon or tracker, or if `bd`'s flags differ from `bd <cmd> --help`.

## Product Contract

### Summary

abacus should not look like it houses all our tools. Our tools solve problems; to the user abacus is a one-stop shop for Beads-powered agent fleets. Navigation, headings, header and footer use domain language only. Tool names appear only in the Doctor area, in the Providers section of the README and in error or "how to connect" messages that help a user fix a provider.

### Requirements

- R1. Four domains exist as typed core reports: Board, Tasks, Leases, Queues. Each carries a connection state (`connected`, `not-connected`, `degraded`) and a provider-neutral `Source` label (`live`, `tracker subset`, `snapshot`), never a tool name.
- R2. A domain with no usable provider is shown as "not connected" with how to connect, not hidden.
- R3. `abacus tasks|leases|queues|board [--json]` read commands render the domain reports. `status --json` gains an additive, omitempty `domains` section; `schema_version` stays 1.
- R4. The TUI navigates by domain (Board, Tasks, Leases, Queues, Doctor). No nav, header or footer text contains a tool name (tested).
- R5. Tasks can be created, updated and closed from the TUI (`n`, `e`, `x`, each with a confirmation screen) and the CLI (`abacus tasks create|update|close`, `--yes`, `--dry-run`), through `bd --json` only.
- R6. Every mutation follows the write-safety model below. Leases, Queues and Board writes are not built; their exact blockers are recorded here.

### Settled constraints (unchanged)

Go; core holds all logic type-safely and CLI/TUI/native clients render core's typed reports (`schema_version` JSON); tools are composed only through their `--json` CLIs and versioned local APIs; reuse ecosystem tools where they do better (`bd` is the Beads CLI); private repo; draft PRs; small gitmoji commits; red-first tests; no command against a real hub, daemon or tracker.

## Inventory: read and write surfaces today

Verified on 2026-11-21 with `bd 1.3.4`, `~/Code/beads-lease` (README and `internal/cmd`), `~/Code/lambe` (`docs/api/state-api.md`, `lambe --help`) and `~/Code/board-beads` (README, `src/`).

### Tasks (Beads issues via `bd`)

| | Surface |
| --- | --- |
| Read | `bd status --json --no-activity` (counts by state, ready), `bd list --json --brief --all --sort updated --flat -n N` (recent changes; newest first), `bd version --json` (`version`, `schema_version`). `bd --readonly` blocks writes, so every read runs with it. abacus shows a compact summary, not an issue browser (see Ecosystem providers). |
| Write | `bd create --json` (`--title`, `-d`, `-p`, `-t`, `--dry-run`), `bd update ID... --json` (`--title`, `-d`, `-p`, `-s`, `--if-status`, `--if-assignee`, `--claim`, labels, metadata), `bd close ID... --json` (`-r`), `bd reopen`, `bd delete`, `bd comment`, `bd label` and more. |
| Dry run | `create` only (`--dry-run`). `update` and `close` have none; `--if-status` makes a stale update a no-op (exit 13). |
| Tracker selection | `-C DIR` (like `git -C`) or `--db`. abacus uses `ABACUS_BD_DIR` or the working directory. |

Verified with an isolated temp tracker (`bd init` in a temp dir, isolated `HOME`): `create`, `update` and `close` with `--json` print the issue (create and update/close as one-element arrays or an object, handled by the decoder); a failing `close` prints `{"error", "schema_version"}` and exits 1.

### Leases (beads-lease held / stale / free)

| | Surface |
| --- | --- |
| Read | `beads-lease --json list` (envelope `schemaVersion` 1), `version --json`, `doctor --json`. Needs `BEADS_ACTOR`. |
| Write | `acquire`, `release`, `heartbeat`, `run`, all with `--json` envelopes; they write to the shared production hub (`coordPath`, default `~/Code/agent-coordination/bin/coord`). |
| Dry run | none. |

### Queues (Lambe / Symphony)

| | Surface |
| --- | --- |
| Read | state API `GET /api/v1/state` (runs, needs-human), `/queue`, `/history`; degraded `lambe status -json` (tracker subset). |
| Write | none in the state API except `POST /api/v1/refresh`, documented as a mutating operator action that "needs an explicit human decision to ship". CLI: `lambe enqueue -workflow NAME [-apply] ISSUE_ID` (previews without `-apply`, prose output only). |

### Board (Vercel Board / board-beads compat)

board-beads is a TypeScript library (a Board `Backend` over `bd`) with no CLI. Whether to ship a thin contract CLI or compose Vercel's `vb` is the open owner decision in `board-beads#15`. This plan does not decide it. The Board domain is a provider interface plus a "not connected, how to connect" state.

## CRUD coverage

| Domain | Create | Read | Update | Delete / close |
| --- | --- | --- | --- | --- |
| Board | blocked: no provider contract (board-beads#15, owner decision) | blocked: same; interface and not-connected state ship | blocked: same | blocked: same |
| Tasks | done (`bd create`) | done (`bd list`) | done (`bd update`: title, description, priority, status) | done as close (`bd close`); `delete` deliberately not exposed |
| Leases | blocked: no `--dry-run`, write envelope not under `api_version` (beads-lease#9, #10) | done | blocked: heartbeat, same gaps | blocked: release, same gaps |
| Queues | blocked: `enqueue` has no `-json` (lambe#58); no mutation route in the state API (lambe#59, owner decision) | done (runs, queue, attention, history) | blocked: same | blocked: same |

### Upstream gaps filed (our repos only)

- andrew-bierman/beads-lease#9: `--dry-run` for `acquire`, `release`, `heartbeat`.
- andrew-bierman/beads-lease#10: put the write envelopes and hub/identity selection under the `api_version` contract.
- andrew-bierman/lambe#58: `lambe enqueue -json` with a `schema_version`.
- andrew-bierman/lambe#59: decision on whether the state API offers any run-control or queue-mutation route (refresh stays unused).
- andrew-bierman/board-beads#15 (existing, not decided): a comment records what a Board provider needs for read and write.

## Ecosystem providers

abacus does not rebuild an issue browser, kanban or dependency graph. Those exist and are better elsewhere.

- **b9s** (github.com/vanderheijden86/b9s, MIT, a k9s-style Beads TUI and web by another author) covers the Tasks tree, the Board (epic lanes), the dependency graph, live reload and every write through `bd`. It has no general `--json` issue-read CLI, so abacus does not read from it. Integration is a handoff only: the Tasks and Board tabs offer "Open in b9s" on `o`, an argv (`b9s`) the front-end runs on an explicit keypress, like the Rex handoff. Detection is `exec.LookPath("b9s")` plus the `-version` probe, which `b9s -help` confirms exists (Go-style single dash; `--version` is accepted too but `-version` is what the help lists). Missing b9s is an error notice with an install hint, never a failure. abacus does not install b9s, vendor or import any of its code, and no test runs it (the handoff is tested with fakes).
- The Tasks and Board domains therefore show a compact summary (counts by state, ready, blocked, in progress, recent changes), and the Tasks write path (create, update, close with a confirm step) stays as the scriptable CLI path. b9s is the rich interactive path.
- The Tasks provider interface is a plain typed read (`Tasks(ctx) (TasksReport, error)`), so a graph-insights provider can later plug into Tasks or Board without core changes.

- **bv** (beads_viewer, Jeffrey Emanuel, https://github.com/Dicklesworthstone/beads_viewer): graph insights for Tasks and Board (next pick, blockers, critical path, cycles) with Source `graph analysis`. Owner decision (2026-11-21): integrate; the owner takes responsibility for the license question. abacus runs the `bv` binary as a subprocess only and imports, vendors or copies none of its code. Only `--robot-triage`, `--robot-plan`, `--robot-insights` and `--robot-graph`, always with an explicit `--format json` (so `BV_OUTPUT_FORMAT=toon` in the environment cannot change the output), optionally `--label X` and `--recipe X`, matched element by element; bare `bv` (a blocking TUI) is never run. stdout is data only, a non-zero exit marks that section unavailable (the provider degraded). Detection is `LookPath` alone. Verified against real `bv v0.27.0` output on an isolated temp project (see `internal/bv/testdata/README.md`).

## Write-safety model

1. **Separate write allowlist per provider.** Reads and writes are different allowlists. Reads run through a read-only runner (`bd --readonly ...`). A write argv is accepted only by the provider's write allowlist, which matches the whole argv: the verb, the fixed flags, an ID that matches `^[A-Za-z0-9][A-Za-z0-9._-]*$`, values passed as `--flag=value` so a value can never be read as a flag, and `-C DIR` only with the configured directory. Anything else is refused before it reaches a process.
2. **Explicit user action plus a confirm step.** A change is first planned (`PlanTaskChange`: validated, exact argv built, human-readable lines of what will change). A plan can be applied only after `Confirm()`. The TUI shows the confirmation screen (the exact change and argv); the CLI asks `y/N` unless `--yes`. `ApplyTaskPlan` refuses an unconfirmed plan.
3. **Dry run.** `--dry-run` on the CLI plans and prints the argv and the change, and executes nothing. `bd create --dry-run` exists but abacus does not need it for this.
4. **Idempotency.** `update` and `close` are idempotent by nature (same final state). `create` is not: a plan can be applied once (a second apply of the same plan is refused), so a retry or a double Enter cannot create two issues.
5. **Audit line.** Before a mutation runs, one JSON line (time, domain, verb, argv, tracker dir) is appended to `$XDG_STATE_HOME/abacus/audit.jsonl` (default `~/.local/state/abacus/audit.jsonl`), and a second line records ok or the error. If the audit file cannot be opened the mutation does not run (fail closed).
6. **Never the shared hub in tests.** Write tests use fakes. The optional end-to-end test runs against an isolated temp `bd` tracker (temp project dir, temp `HOME`, `XDG_*`) and skips when `bd` is not installed. No test touches `~/Code/agent-coordination`, `~/.local/state/lambe` or `~/.local/bin/lambe`.

## Planning Contract

### Key Technical Decisions

- KTD1. **Domain types, provider interfaces and the registry live in `internal/core`.** Adapters already import core, and `core.Report` needs the domain types for the additive `domains` section, so a separate package would be an import cycle. Core still imports no UI library (the architecture test covers it).
- KTD2. **Domain reports embed a common `Header`**: domain, title, state, source, summary, reason, how-to-connect. State meaning: `connected` is a full read; `degraded` is a read from a reduced source (tracker subset) or a ready provider whose read failed (reason shown); `not-connected` is no usable provider (absent, broken, incompatible).
- KTD3. **`bd` becomes a module.** It implements `core.Module` (detect through `bd version --json`, doctor, status), the Tasks provider and the task writer. `doctor` and `status` therefore list it; both stay `schema_version` 1 because modules are a list. The domains section is added only to `status --json` (doctor does not read domains).
- KTD4. **Tool names in not-connected text.** The "how to connect" body is a fix message for a provider, so it may name the provider command. Nav labels, headers, footers and domain headings never do (tested).
- KTD5. **The write gate is in core.** `ApplyTaskPlan` checks confirmation, the single-use rule and the audit open, then asks the adapter to run the argv. The adapter owns only argv building and its allowlist, so a second provider gets the same gate.
- KTD6. **Queues keeps the rich Lambe view.** The TUI Queues tab uses the existing run list, detail pane, filter and Rex handoff when the provider is the Lambe module, and the typed `QueuesReport` otherwise. `shortcut: the TUI imports the Lambe module for its rich view; replace with a typed richer QueuesReport when a second Queues provider exists`.
- KTD7. **Double read in `status`.** `status` reads each module's `Status` and the domain reports, so a provider can be read twice per run. Accepted: reads are cheap and the Lambe tracker read is cached.

### Output Structure

```text
internal/core/domains.go        # types, provider interfaces, registry mapping, ReadDomains
internal/core/taskwrite.go      # TaskChange, plan, confirm, single-use, audit, ApplyTaskPlan
internal/bd/                    # Tasks provider and writer: read and write allowlists
internal/tui/                   # domain tabs: board, tasks (+ confirm screens), leases, queues, doctor
internal/cli/                   # tasks|leases|queues|board commands, tasks create|update|close
```

## Implementation Units

- U1. Plan and upstream issues (this document).
- U2. Core domain layer and registry, `domains` in `status --json`, read commands.
- U3. Domain-first TUI, goldens, docs, README.
- U4. Tasks write path (core gate, `bd` allowlist, CLI, TUI), mutation checks.
- U5. bv graph-insights provider (subprocess, allowlist, real-output fixtures).

## Verification Contract

- `go build ./...`, `go vet ./...`, `gofmt -l .` empty, `go test ./...` under an isolated `HOME`, `XDG_STATE_HOME` and `XDG_CONFIG_HOME`.
- Domain-language test: no nav, header or footer text contains `Lambe`, `beads-lease` or `board-beads`.
- Compiling mutations of the write allowlist, the confirm gate and the bv allowlist each fail a test (15 mutants, all killed).
- Goldens updated and reviewed; the README sample is the 80-column Tasks or Queues golden.

## Open Questions

- Deferred to the owner: `board-beads#15` (Board contract), `lambe#59` (any Queues mutation route), whether Leases writes should exist for a human at all (beads-lease#9, #10).
