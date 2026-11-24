---
title: "feat: Agents view (what is the fleet doing) over the workspace capability"
type: feat
date: 2026-11-24
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
status: proposed
---

# feat: Agents view (what is the fleet doing) over the workspace capability

## Decision for Andrew

This is the one part of the feature that changes abacus's guarantees. Everything else in this plan is ordinary
work inside the existing design. Read this section first and approve or amend it before any code is written.

### What the argv allowlist permits today

`internal/rex/argv.go` lets abacus run exactly two shapes, each with `--autostart=false` first and an optional
`-S HOST` before it:

- `rex [-S HOST] --autostart=false ls --json`
- `rex [-S HOST] --autostart=false version --json`

### Exactly what it would permit after this change

Three additions, nothing more. Each keeps `--autostart=false` in the same position as today, keeps the optional
`-S HOST` prefix (host only from `ABACUS_REX_HOSTS`, validated as today), and takes no free-form argument:

| # | Allowed argv (after the optional `-S HOST`) | Why it is needed |
| --- | --- | --- |
| A1 | `--autostart=false block ls -s SESSION --json` | Lists the panes (blocks) of one session. Rex rejects `block ls --json -A`, so there is no one-call fleet view. Only blocks with flavor `com.superlogical.terminal.shell` are asked about further. |
| A2 | `--autostart=false block call -s SESSION -b BLOCK program_status` | Returns the OSC 7501 status records a program reported in that pane: state, kind, progress, msg, hierarchical id. This is the live signal the view exists for. |
| A3 | `--autostart=false block call -s SESSION -b BLOCK process` | Returns foreground process name and cwd (argv beyond argv0 is deliberately never returned by Rex). Gives the agent name when it reports no status, and the repo and branch source. |

`SESSION` and `BLOCK` must match the id grammar seen live (`session:` or `block:` plus hex digits and hyphens, at most
128 characters, never a leading `-`, no whitespace). No other word is accepted in any position.

Explicitly not allowed, and pinned by refused-argv tests: `block ls -A`, `block ls` without `-s`, `block inspect`,
`block new|close|send|capture`, `block call` with any other method (`write`, `reset`, `clear`, `resize`,
`set_theme`, `title`, `pwd`, screen capture and so on), any trailing argument word after the method name,
`process --json`, `api call`, `events`, a leading-dash id, `--autostart` in any other position, an argv that is a
prefix or extension of an allowed one.

### Why this is a safety decision, and the honest caveat

- `rex block call` is a generic verb. The same command with method `write` types text into a pane. The safety here
  is entirely the argv pin: the method name is a literal in the matcher, no argument word is accepted, and tests
  prove the dangerous neighbours are refused both by the matcher and through `readOnly.Run` (`ran == false`).
- **Rex publishes no machine-readable read-only guarantee** (`rex api list --json` has no read-only annotation).
  Read-only-ness rests on the method descriptions ("List the status that programs ... have reported", "Get the
  processes") and a live check on throwaway sessions only. That `program_status` and `process` never wake an idle
  terminal is **UNTESTED** (the docs say it only for `modes` and `ports`). Default assumption: they might, so the
  calls are infrequent, cached and bounded.
- Everything in a record (`msg`, `title`, `app`, `id`) is untrusted program output. It is sanitized and clipped in
  core before any layout, exactly like labels today.
- The calls are made only against this machine and hosts in `ABACUS_REX_HOSTS`. Nothing creates, focuses, closes
  or types into a pane. No `api call`, no `events` stream, no screen capture.
- With no Rex binary, no server, or a Rex whose block calls fail, the feature is silent: the tab does not appear
  and nothing is shown as an error.

### What I recommend you decide

1. **Approve A1 to A3 exactly as written**, or cut A3 (then no cwd, repo, branch, or agent-name fallback; panes
   with no OSC 7501 are invisible). Recommendation: approve all three.
2. **Tab placement** (product call, see KTD1): a sixth entry "Agents" that exists only when a workspace is
   connected, placed after Doctor so digit jumps `1` to `5` never move; or `:agents` only with no tab. Recommendation:
   the conditional tab after Doctor, jump key `6`, plus `:agents`.
3. **Cost and file-overlap** stay out of v1 (follow-ups below).

If you decline A2 or A3, this plan reduces to a placement-only view with no new rex calls (less useful, but zero
change to the guarantees). That fallback is noted in the Open Questions.

---

## Goal Capsule

- **Objective:** answer "what is the fleet doing?" in one view. For each agent pane on this machine and in
  `ABACUS_REX_HOSTS`: live OSC 7501 status (state, kind, progress, msg, id), agent name, cwd, time in state, the Beads
  task and lease it maps to, repo and branch, sorted so what needs a human is first, with a stale marker. This is
  different from Rex's own notifications (tab spinners, picker indicators, system notifications say that a tab needs
  you); this view says what everything is doing and how it relates to the work.
- **Means:** an optional core capability `AgentProvider` next to `WorkspaceProvider`, implemented by the rex module
  with three newly allowlisted read-only calls, a bounded sweep of its own, pure sort and collapse functions in
  core, the existing label matcher and Open plan reused unchanged, and one thin TUI view.
- **Authority:** the owner's request in this session ("extend abacus with the agents view"), then the existing
  rules (core holds logic, UI libraries only in `internal/tui`, read-only and bounded Rex, no tool names in view
  bodies, text sanitized, fakes and goldens, no write verbs outside the gate).
- **Stop conditions:** stop and report if a unit needs any argv beyond A1 to A3; if a live check shows A2 or A3
  mutates anything; if an existing allowlist or guard test would have to be weakened; if a unit would type into,
  focus, or close a pane.
- **Finishes and ships:** implementation and review of each unit on `feat/agents-view`. Pushing, the PR, and the
  allowlist approval are owner actions. Nothing is pushed by this plan.

Evidence legend: **[verified]** read in this repo or confirmed live on throwaway sessions labelled
`spike-abacus-agents` (since removed; none remain) with rex 0.1.0-preview.22, api_revision 19; **[inferred]**
reasoned; **UNTESTED** not checked.

---

## Product Contract

### Problem Frame

abacus shows Beads tasks, leases and queues and can say which Rex session a work item lives in (`rex ls --json`,
match by session label, [verified] `internal/rex/match.go`). It cannot say what the agent in that pane is doing, or
that three panes have nothing to do with any task. Rex's own notifications answer "a tab needs you" per tab; nothing
answers "what is the whole fleet doing, and what is stuck".

### Requirements

- R1. One row per agent pane across this machine and `ABACUS_REX_HOSTS`: host, session, agent name, state (working,
  blocked, done, error, idle), kind (permission, question, auth), progress, msg, record id, foreground process and
  cwd, time in state, stale marker.
- R2. Each row maps to a Beads task and lease through the existing workspace matching (whole-word id in session or
  block label, record title or msg, or the cwd's last path element), with the existing ambiguity rule: a pane
  naming two items is shown as ambiguous, never silently picked.
- R3. Repo and branch from cwd, read-only, local panes only, with no `git` process (read `.git/HEAD`).
- R4. Sorting for attention: blocked first (permission, then auth, then question), then error, then done, then
  working, then idle; within a group longest-waiting first; stale marked. "Unseen done" is not derivable (Rex has
  no seen flag); v1 shows plain "done".
- R5. Actions stay inside the existing gated jump: the `rex -S HOST` handoff or the command to copy. Nothing is typed
  into a pane. No new action kind.
- R6. Works with no Rex (the view is absent and nothing breaks), with Rex but no agents (a clear empty state that
  says how to publish status), and with Rex and agents.
- R7. Refresh is bounded: poll only while the view is visible, one sweep in flight, interval backs off, hard
  budget per sweep, manual refresh key.
- R8. Core holds the logic and a typed `AgentReport` (`schema_version` 1, additive JSON); the TUI renders it.
  View bodies say "workspace" and "terminal", never "Rex".

### Scope Boundaries

**In this plan:** the units below.

**Follow-ups, not in v1:**

- Cost and usage per agent (needs a source Rex does not give through these calls).
- File-overlap detection (two agents editing the same files).
- A one-glyph agent state on Tasks and Leases rows that map to a pane (v1.1, `AgentReport` already carries the join).
- True "unseen done" (needs client-side seen tracking; at most "first observed by this process").
- Focusing or jumping into a specific pane (a write, unverified).
- Reading screen text, acting on blocked panes, `rex events` streaming.

---

## Key Technical Decisions

- **KTD1. A view over the workspace capability, not a fifth Beads domain.** The README says the workspace "is not
  a fifth tab" and the 2026-11-22-1905 plan KTD6 says "capability, not a fifth domain; four tabs plus Doctor", and
  explicitly deferred polling `program_status`. This plan consciously revisits that: the earlier reasoning was
  that placements annotate work items, so they belong on the item rows. That still holds for the join. It does not
  hold for the question asked here. A pane with no task, a task with two panes, and a blocked pane that maps to
  nothing have no row to live on in Tasks, Leases or Queues, so enriching those rows cannot answer "what is the
  fleet doing". Resolution: Agents is not a domain and has no Beads data; it exists only when a workspace provider
  is connected (same quiet-when-absent rule as the workspace layer's `Quiet()`), so an install without Rex is
  byte-for-byte unchanged and the `1` to `5` jumps do not move. Placement after Doctor (jump `6`) or `:agents`
  only is Andrew's call. The README sentence is rewritten to "it adds an Agents view only when a terminal workspace
  is connected" (coordinate with #44's README edits). v1.1 adds the row glyph. Evidence: [verified] README.md and
  the plan text above.
- **KTD2. New optional core capability, existing types untouched.** `AgentProvider{Agents(ctx, []WorkspaceItem)
  (AgentReport, error)}` beside `WorkspaceProvider`; the rex module implements both; the app finds it with
  `pick[AgentProvider]` like `pick[WorkspaceProvider]`. `WorkspaceProvider` and `Placement` are unchanged. Reason:
  the smallest extension that carries pane-level state, with no change to existing JSON or the demo provider's
  contract. Alternative rejected: adding fields to `Placement`, which would force every consumer and golden of
  the workspace layer to change.
- **KTD3. `AgentPane` and report shape (core, UI-free).** `AgentReport{State, Reason, HowToConnect, Stale,
  Truncated, Panes}`; `AgentPane{Host, Session, SessionLabel, Block, Label, Agent, State, Kind, Progress, Msg, Title,
  RecordID, NRecords, UpdatedAt, Foreground{Name, Cwd}, Repo, Branch, Item *WorkspaceItem, Match, Open *OpenPlan}`.
  Core sanitizes every text field (`core.Sanitize`, `core.ClipRunes`), maps an unknown `state` to idle and an unknown
  `kind` to none (the OSC 7501 spec says so), caps panes, keeps at most 32 records per pane (most severe first),
  and collapses a pane's records to one row, most severe wins, "+N records". Name "agents"/`AgentPane`, not
  "board": Board is the agents' message board (`internal/board`, `core.BoardReport`).
- **KTD4. Matching reuses `core.MatchSessionLabel`.** Run against session label, block label, record title and msg,
  and the cwd's last element (worktree directory name), whole-word, same ambiguity rule. The lease or task link is
  then the existing `WorkspaceItem` kinds; no new join logic. `Open` per pane is the existing
  `Module.Open(Placement{Host, Session, Label})`, whose rung `wsLayer.runnable` already re-validates as exactly
  `[bin, "-S", host]`. [verified] `internal/rex/match.go`, `internal/tui/workspace.go`.
- **KTD5. The sweep.** One `ls --json` per host (existing call), then per session `block ls` (A1), then per
  terminal block `program_status` (A2) and `process` (A3). Measured: ~35 ms per spawn; 20 throwaway sessions took
  2.2 s sequentially (60 spawns) and 0.42 s at 8-way; the real fleet is roughly 170 spawns, about 6 s sequential and
  about 1 s at 8-way [verified]. Bounds: concurrency 8, hard 4 s budget per sweep (its own; the existing 1 s
  workspace budget and 2 s `CallTimeout` per call stay for the existing calls), one sweep in flight, at most 500
  sessions (existing), 64 blocks per session, 256 panes per host, output per call under the existing 1 MiB cap.
  `process` is cached 10 to 15 s (foreground changes rarely); `program_status` is the live part with the existing
  3 s per-host TTL. Truncation is marked, not silent.
- **KTD6. Per-host circuit breaker and error semantics.** The first call to a host is the existing `ls --json`; if
  it fails or times out, that host's block calls are skipped for the sweep (else N panes times 2 s). Remote hosts
  only from `ABACUS_REX_HOSTS`. New calls treat any non-zero exit as "pane or session gone, skip": the live exit for
  an unknown session or block is 3, which in this package already means "more than one server" for `ls`
  (`exitAmbiguous`), so the exit-code mapping for the new calls is separate and never reused. Schema drift (records
  not an array) is an error for that pane, not "no agents". A failing block call is "no agent data", never an error
  banner (Rex revision window is UNTESTED, see Open Questions).
- **KTD7. Refresh.** Poll only while the view is visible. Interval 5 s while any pane is blocked or working, else
  15 s; back off to 60 s after a slow (over 2 s) or failed sweep; manual `r`; one sweep in flight; a cancelled sweep
  is not cached. Mirrors `tasksPollEvery`, `tasksPollMax`, `tasksSlowRead` and `taskview.Poller`. `rex events`
  streaming is rejected for v1: an unbounded child process and outside the argv contract.
- **KTD8. Repo and branch without `git`.** For local panes only, walk up from `foreground.cwd` (at most 12
  levels, regular files, size-capped) to a `.git` directory or `.git` file (`gitdir:` for worktrees) and read
  `HEAD` (`ref: refs/heads/x` gives `x`, else a short hash). A remote cwd is shown as text and never read.
  Rationale: spawning `git` in an agent-controlled directory can run repo-configured helpers; reading a file
  cannot. Output sanitized and clipped.
- **KTD9. Time in state and stale.** `now - record.updated_at` using the injectable `nowFn` clock the goldens
  already use. Rex's `updated_at` is the server clock: exact for local, shown with `~` for remote hosts (skew
  UNTESTED), and a negative duration is hidden. `stale` = newest record older than 10 minutes for working or
  blocked (the protocol has no heartbeat, so "working" only means "last report"), plus the report-level
  cache-replay `Stale`. Threshold is a core constant, tested.
- **KTD10. Agent name.** Record `effective_app` first, else the foreground script, argv0 or name (Rex's own
  preference order). A pane with no OSC 7501 whose foreground is in a small named set (claude, codex, and so on) is
  shown as "agent, no status". Real claude and codex process names are UNTESTED.
- **KTD11. Done is not unseen.** The server has no seen flag. v1 shows "done" (a result is waiting), ordered after
  error and before working. No claim of unseen.

---

## Output Structure

    internal/core/agents.go            AgentProvider, AgentReport, AgentPane, sort, collapse, clean
    internal/core/agents_test.go
    internal/rex/agents.go             sweep, parsing, matching glue, breaker, caps
    internal/rex/argv.go               + A1 to A3 (the safety-critical change)
    internal/rex/testdata/             + recorded fixtures, README updated
    internal/app/                      + AgentProvider pick, `abacus agents` (optional) JSON
    internal/tui/agents_view.go        the view, poll, keys
    internal/demo/agents.go            fake provider with every state
    internal/tui/testdata/golden/      + agents-* goldens
    docs/tui.md, docs/tool-contract.md, README.md, CHANGELOG.md   small, local edits

---

## Implementation Units

Each unit is one commit-sized reviewable step in this order. Test files are named per unit.

### U1. Allowlist additions A1 to A3 (the safety unit, first, alone)

- **Goal:** extend `internal/rex/argv.go` with exactly the three shapes and nothing else, with the id grammar.
- **Approach:** extend the existing matcher (do not add a second path); literal method names `program_status` and
  `process`; no extra word; keep `--autostart=false` first and the `-S` validation as is. Do not weaken any check.
- **Tests (`internal/rex/argv_test.go`, `main_test.go`):** copy the shape of `TestOnlyTheReadArgvsEverExecute`,
  `TestEveryCallTheModuleMakesIsAllowed`, `TestOnlyReadArgvsWithTheAutostartGuardInEveryPositionRun`, and
  `TestOnlyThisMachineAndConfiguredHostsAreEverContacted`. New ok list: the three shapes, each with and without
  `-S HOST`. New refused list (each must also fail through `readOnly.Run` with `ran == false`): `block call ... write|
  reset|clear|resize|set_theme|title|pwd`, an args word after the method, `program_status '{"x":1}'`, `process --json`,
  `block ls -A`, `block ls` without `-s`, `block inspect|new|close|send|capture`, ids with a leading `-`, spaces,
  newline, wrong prefix, over-length, `--autostart` elsewhere, invalid `-S`, prefix or extension of an allowed argv,
  `api call`, `events`. A mutant check as the repo does for gates (loosen the method pin, test must fail).
- **Done when:** the refused list is longer than the ok list and every current test passes unchanged.

### U2. Fixtures and Rex testdata README

- **Goal:** recorded real shapes, scrubbed (pids, ids, paths, hostnames), plus hand-written hostile and drift cases.
- **Files:** `internal/rex/testdata/block-ls.json`, `program-status-*.json`, `process-*.json`; update
  `internal/rex/testdata/README.md`, which today says nothing came from a real Rex run: now state which fixtures are
  real (recorded from rex 0.1.0-preview.22, api_revision 19, throwaway sessions) and which are hand-written
  (hostile ANSI/bidi/newline/10 KB text, drift, oversized, unknown state or kind).
- **Done when:** fixture parse tests pass and the README is accurate. Source shapes: `~/.cache/abacus-agents/*.json`
  (spike scratch, not committed).

### U3. Core: types, sort, collapse, clean

- **Files:** `internal/core/agents.go`, `agents_test.go`; assertion in `arch_test.go` style that core stays UI-free.
- **Approach:** pure functions: `CollapseRecords`, `Attention` ordering (blocked: permission, auth, question, unknown;
  then error, done, working, idle; no-status shells last and hidden unless agent by process name), `IsStale(now)`,
  `CleanAgentPane` (Sanitize, ClipRunes, unknown state and kind mapping, record and pane caps, `Truncated`).
- **Tests:** table tests per state and kind, tie-breaking by wait time, hostile text, caps, determinism, JSON
  `schema_version` 1 golden.

### U4. Rex adapter sweep

- **Files:** `internal/rex/agents.go`, tests with the existing `Runner` fakes (`fleetFake`, `readyMod`, `resp{out}`).
- **Approach:** the bounded sweep of KTD5 and KTD6, repo and branch reader of KTD8, matching of KTD4, `Open` via the
  existing `Module.Open`. Own budget and concurrency limiter; one sweep in flight (singleflight); breaker per host.
- **Degradation tests (fakes):** no rex binary; no server (exit 4); zero sessions; sessions but no panes; panes
  with no status; exit 3 mid-sweep (pane vanished); one host timing out while another answers; hostile `msg`,
  `title`, `app`, `id`; schema drift is an error not "no agents"; unknown state and kind; over-cap panes and
  records (truncated marker); hung rex bounded by ctx; cancel not cached; breaker skips a host's block calls after
  the first failure; only configured hosts contacted.

### U5. App wiring and CLI/JSON

- **Files:** `internal/app/` capability pick; optional `abacus agents --json` printing the same `AgentReport`
  (decide in U5; JSON additive, schema_version 1; update `docs/tool-contract.md`).
- **Done when:** with no provider the app is byte-for-byte unchanged (existing goldens untouched).

### U6. Demo provider

- **Files:** `internal/demo/agents.go`; `var _ core.AgentProvider = ...` assertion next to the existing ones in
  `internal/demo/demo_test.go`.
- **Content:** every state and kind, progress, a child id, hostile text, an ambiguous placement, a stale pane,
  a degraded host, a truncated report. Names follow whatever #44 settles for demo data.

### U7. TUI view

- **Files:** `internal/tui/agents_view.go` (and model wiring); thin, renders only core data.
- **Approach:** list sorted by core, columns collapse with width (40/80/120), detail on `enter`/`d`, polling per
  KTD7 with `r`, jump via the existing gated handoff (`wsLayer.runnable` unchanged), copy-command fallback. Binds no
  key that types or focuses; no collision with `w` (workspace jump) or digit tab jumps. Empty states: no workspace
  (view absent), workspace but no agents (says how to publish status, in "workspace/terminal" words), degraded host,
  stale.
- **Tests:** golden scenarios `agents`, `agents-empty`, `agents-no-panes`, `agents-degraded`, `agents-detail`,
  `agents-hostile`, at `goldenWidths` 40/80/120 and `goldenHeights` 12/30 plus framed sizes, using fixed `goldenNow`;
  extend the size property test (no line wider than the terminal, no panic at tiny sizes); a test that bodies never
  say "Rex"; a key-collision test; a test that the view's only runnable action equals the existing handoff.

### U8. Docs and release notes

- **Files:** `README.md` (rewrite the "not a fifth tab" sentence and the Rex bullet to list the three added calls),
  `docs/tool-contract.md` (the allowlist table), `docs/tui.md` (the view, key map, refresh), `CHANGELOG.md` draft
  that begins with the same "Decision for Andrew" content. Mark every UNTESTED item as such.

---

## Verification Plan

- `go build ./... && go test ./...` baseline first (not yet run; spike did not run it), then per unit; golden
  update workflow per `docs/tui.md`: `go test ./internal/tui -update`, review the diff by eye.
- Live check, throwaway sessions only, labelled `spike-abacus-agents`, killed afterwards: emit OSC 7501 with printf
  and compare the view to the records. Never other sessions, never other hosts.
- Safety review of U1 by the owner before U4 onward (the approval gate).

## Risks and Dependencies

- **Draft PR #44** (open-source readiness, 77 files) renames `docs/plans` to `docs/design`, edits README,
  tool-contract, CHANGELOG, `internal/demo/*`, about 35 goldens, `workspace_test.go`, `app/rex_test.go`. It does
  not touch `internal/rex` or `core/workspace*.go`. Mitigation: rebase after it merges and regenerate goldens with
  `-update` rather than resolve by hand; if it lands first this plan moves to `docs/design/`; keep README and
  tool-contract edits small and local.
- `abacus-24` may be working in the primary checkout; scope unknown, not contacted. All work is in the
  `agents-view` worktree.
- The workspace layer (#23, #27) is already on main; `feat/workspace-rex-core` and `feat/tui-workspace` are older
  squash-merged originals and must not be branched from.
- Fleet size makes the sweep the cost center (about 170 spawns); breaker, concurrency and budget are the control.

## Open Questions (conservative default in parentheses)

1. Do `program_status` or `process` wake an idle pane? UNTESTED. (Assume yes; cached, infrequent, bounded.)
2. `-S HOST` per-call cost for block calls, an unreachable host's cost, and remote clock skew. UNTESTED, no remote
   host contacted. (Circuit-break, cache, `~` durations; verify on a host Andrew names.)
3. Rex revision window: abacus gates `api_revision` 10..13 and fails only below 10 or when `api_min_revision` is
   above 13; this server is revision 19 and passes. Whether `block call` and these methods exist at 10 to 13 is
   UNTESTED. (A failing block call means "no agent data", not an error.)
4. No-server exit code 4 on the new calls was not re-tested. (Any non-zero exit means skip.)
5. Real claude and codex process names and `app` values. UNTESTED. (Prefer OSC 7501 `effective_app`; process name is
   a fallback set that is easy to extend.)
6. Tab number and position, and whether a tab or only `:agents`. (Conditional "Agents" entry after Doctor, key `6`,
   plus `:agents`; digits `1` to `5` unchanged.)
7. Stale threshold (10 minutes) and poll intervals (5 / 15 / 60 s) are proposals. (Constants in core, tested.)
8. If Andrew declines A2 or A3: fall back to placement-only rows from the existing `ls --json`, no new calls.
   (Default: do not start U4 until A1 to A3 are approved.)
9. Plan file location if #44 merges first. (`docs/design/`.)

## Sources and Evidence

- Spike notes: `~/.cache/abacus-agents/spike.md` (live throwaway-session results, shapes, timings).
- Repo: `internal/rex/argv.go`, `module.go`, `match.go`; `internal/core/workspace*.go`; `internal/tui/workspace.go`;
  `docs/plans/2026-11-22-1905-feat-tui-redesign-and-workspace-plan.md` (KTD6);
  `docs/plans/2026-11-23-feat-tasks-workspace-plan.md`; README Rex bullet.
- Rex docs: `~/.cache/rex-docs/llms-full.txt` (program status spec around lines 2625 to 2945; block methods around
  10425).
