# abacus TUI

The TUI renders core's typed domain reports and holds no logic of its own. It is the fleet
cockpit: one tab per domain (Board, Tasks, Leases, Queues) plus Doctor. Tasks is abacus's own work
browser: every task listed, sorted and filtered, with a detail pane (see "Tasks" below); the
tree of epics and children is the default list when a tracker has parent-child links, and the
dependency graph is the Graph view (below); the kanban view is planned in `docs/plans/2026-11-23-feat-tasks-workspace-plan.md`.
The `o` handoff to the full b9s view stays as an optional extra. The Board is a message board for agents: channels,
recent threads and open tasks, with the selected thread or task shown in full below the list. Providers (the tools behind each domain)
are named only in Doctor and in "how to connect" or error messages, never in navigation.

A domain with no usable provider is still a tab: it says "not connected", why, and how to connect.
A domain whose tool is present but not set up shows one guided line instead ("No tracker here,
press `i` to create your fleet workspace (bd init)", "Leases need an actor name, press `s`",
"Queues need a supervisor config, press `s`"). The key opens a confirm screen with the exact
command; `y` applies it once (audited), `n` or `esc` cancels. While a tab is guided, its other
keys do nothing. Nothing is created or started without that `y`.

## Keys

Footer and the `?` overlay are generated from the `key.Binding` values in
`internal/tui/keys.go`, so they match what the keys do.

| Key | Action |
| --- | --- |
| `tab`, `right` / `shift+tab`, `left` | next / previous domain |
| `1`-`5` | jump to Board, Tasks, Leases, Queues, Doctor |
| `j` `k`, `down` `up` | move the cursor (scroll elsewhere); on the Board it selects the next thread or task; on a Tasks detail they scroll it |
| `n` `e` `x` | Tasks: new task, edit, close the selected task. Each opens a form (`enter` next field and, on the last, review; `tab`/`shift+tab` move; `esc` cancels) and then a confirm screen showing the exact change and command: `y` applies, `n` or `esc` cancels |
| `o` | Tasks: open the full view in the tracker directory (probes for it first; an error notice saying b9s is optional and where to get it if it is not there). Board: open the board in the browser (the local web inspector, until Ctrl-C), only when connected. Queues: hand the selected run's machine to a session |
| `w` | Leases, Tasks: go to the selected row's workspace session (see "Workspace" below). Offered only when the row has a placement or an ambiguity |
| `i`, `s` | a guided tab: start its setup step (workspace, actor name, Queues config) |
| `r` | Tasks, Board: refresh (Tasks also reruns graph insights). Doctor: run doctor again. Board also refreshes every 30 seconds, only while its tab is visible and never two reads at once |
| `enter`, `esc` | Queues and Tasks, narrow layout only: open the detail; `esc` closes it, a second `esc` clears the filter. Tasks, wide layout: `enter` focuses the detail to scroll it, `esc` leaves |
| `/` | Queues: filter runs by issue, state, phase, machine or workflow. Tasks: the query (below); `enter` keeps it, `esc` clears it |
| `s` `S`, `f`, `v` `V` | Tasks list: next sort key, reverse the direction; next quick filter; next, previous view (List, Graph, Insights) |
| `g` | Tasks list: open the dependency graph on the selected task. In the Graph view: switch between the task's neighborhood and the whole graph. `[` `]` change the depth, `h` `l` step to a sibling (a layer in the whole graph), `enter` refocuses on the selected node (on the focus itself, or in the whole graph, it opens that task in the list), `esc` returns to the list |
| `ctrl+k` | global search from any tab (see "Global search"); not offered while a form, the confirm screen or a filter is open |
| `T`, `h` `l`, `+` `-` | Tasks list: switch between the tree of epics and children and the flat list; in the tree, `h` folds an expanded parent (on a leaf or a folded task it goes up to the parent), `l` unfolds a folded one (on an expanded one it goes to the first child), `+` expands all, `-` folds all |
| `?` | full help overlay; `?` or `esc` closes it |
| `q`, `ctrl+c` | quit (while a form or the filter is open, only `ctrl+c`) |

While a form, the confirm screen or the filter is open, every key (including `q`, digits and `?`)
goes to it. Nothing writes except `y` on the confirm screen.

## Tasks

Every task in the tracker, read as one snapshot (`bd --readonly export`, a fixed read that never
writes, with the long export deadline and the 64 MiB cap) and derived in `internal/taskview`: the
list, the detail, the strip and the Insights summary line all come from that one read, so they
agree about what is blocked or ready. (The graph analysis lines under that summary, next pick,
blockers, critical path and cycles, are computed by the graph engine from its own read and can
differ by a refresh.)

```
 [List] Insights                                                     14 tasks · 3 ready   view strip, then counts
 [open] /label:ui                                          3 of 14 · sort: priority ↑   quick filter, query, matches
╭─ ▸ Tasks · fleet ───────────────────╮ ╭─ ac-12 ──────────────╮
│   id    title            st P  dep ...│ │ detail of the task   │
```

- **List.** A full-row cursor over every task: `id`, title, status mark (`○` open, `◐` in progress,
  `●` blocked, `✓` closed, `◇` deferred), priority, `dep`, type, age, assignee, and from 140 columns
  the labels. `dep` reads `⊘N` held by N unfinished tasks and `→N` holds N tasks back. Columns drop
  by importance as the width shrinks (40 columns keep `id`, title, status, priority and `dep`). The
  title carries `done/total` for an epic. The panels fill the body height, with a two-row legend of
  the marks at the foot of the list. The panel title names the directory that holds the tracker (found
  the way bd finds it, so it is the project the list and the `n` `e` `x` writes really use); a read
  older than two polls adds `stale 3m`, and a snapshot that may have lost records, or that saw
  duplicate ids (the later record is used; an export's order is not stable), says so under the panel.
- **Tree.** When the tracker has at least one parent-child link the list opens as a tree (`T` switches
  to the flat list and back; with no links there is nothing to fold and `T` says so). An epic and its
  children are drawn with branch lines (`├─`, `└─`, `│`), `▾` marks an open parent and `▹` a folded
  one, and an epic row carries a four-cell progress bar of its closed direct children (`▰▰▱▱`, with
  `done/total` in the title; the bar is the first of those marks to drop as the width shrinks).
  Siblings follow the list's sort and the quick filter and query apply to the tree: a task shows when
  it matches, or when a task below it does, and then it is drawn dimmed (marked `dim: context` in the
  legend) so the branch above a match is never lost; its siblings that do not match are hidden.
  Folds are paused while a query is active (a match is never hidden inside a fold; `h` says so) and
  return when it is cleared. A task whose parent is not in the tracker is shown at the top level, a
  cycle of parent links is cut at its lowest id (shown at the top level), a task with two parents sits
  under the first, and deep nesting is drawn at most five levels wide (`… ` stands for the rest).
  Fold choices and the tree-or-flat choice are kept per tracker in abacus's own state directory
  (`$XDG_STATE_HOME/abacus/tasks-tree.json`, mode 0600; never inside the tracker, and a demo session
  keeps nothing), so they survive a restart; the selection follows the task through a refresh, a
  filter and a resize, and folding away the selected task's branch moves it to the nearest parent
  still shown. The tree is read only: reparenting is a later step. `abacus tasks list --tree` prints
  the same tree (`--json` for the typed report).
- **Detail.** Beside the list from 100 columns, otherwise `enter` opens it full width and `esc`
  returns. It shows the task, its status, labels and parent, what blocks it and what it blocks
  (finished ones are listed too, marked `✓`), the children of an epic with a progress bar, the
  description, design, notes and acceptance text as wrapped text with light markdown (headings,
  bullets, fenced and inline code, bold; not a markdown renderer), the close reason and the comment
  thread. Everything from the tracker is sanitized before it is measured or drawn. Long details
  scroll with `j` `k`.
- **Sort.** `s` cycles priority, created, updated, title, status, type, deps and deferred, each in its
  natural direction (newest first for times); `S` reverses. Ties always break by id, ascending, so a
  refresh never shuffles equal rows.
- **Quick filters.** `f` cycles open (not closed, the default), ready, blocked, closed and all; your
  choice survives a refresh. Ready is open, not deferred and not held, and it matches `bd ready` and
  `bd blocked` on every case captured from bd 1.3.4 (`internal/taskview/testdata/README.md`): a
  `blocks` or `conditional-blocks` dependency on an unfinished task holds a task; so does a held
  ancestor (the hold is inherited down parent-child links, shown as `⊘↑`); `waits-for`, `related`,
  `discovered-from` and an `external:` reference never hold; closed work is never held, ready or
  in the blocked quick filter. The blocked quick filter is held *or* stored as blocked: the stored
  status stays what a card move would write, and the derived state is the `⊘N` mark and
  `blocked:true`.
- **Query.** `/` edits a query that filters as you type; `enter` keeps it and `esc` clears it. Free
  words match the id, title and labels (every word must; a word of up to four characters matches as a
  subsequence, a longer one as contiguous text; `"quoted phrase"` is contiguous). Predicates narrow
  one field: `status:open`, `priority:<=1`, `priority:p0`, `type:bug`, `assignee:ada`,
  `assignee:none`, `label:ui`, `label:none`, `parent:ID`, `id:ac-4` (prefix), `title:text`,
  `blocked:true`, `ready:true`, `is:ready`, `has:comments` (also `description`, `labels`, `parent`,
  `children`, `assignee`), `updated>7d`, `created<2026-11-01`. Different fields AND, repeated values
  for one field OR (`status:open status:blocked` or `status:open,blocked`); `!` or `-` excludes. A
  duration is that long ago, so `updated>7d` is "touched in the last week" and `updated<7d` is
  "untouched for a week". A mistake keeps the last good filter and says what is wrong and where
  (`unknown field "foo" (col 25)`). The grammar is `internal/query`, shared with every later search.
- **Insights.** `v` switches to the graph insights panel: the counts bar, the next pick, the issues
  that block the most, the critical path and dependency cycles. Insights are read on start and on
  `r`, not on a timer, because each read analyzes the whole tracker. Without an insights provider the panel says it is not connected and what is
  wrong with abacus's own bd (a release: damaged or moved, re-extract; a dev build: run
  `scripts/dev-bundle.sh`).
- **Fallbacks.** A provider that cannot read the whole tracker, or a read that failed before the
  first snapshot, lists the summary's recent changes (`recent only` in the panel title) with the
  reason. Dependencies were not read, so that list shows no ready count, no `⊘` marks, no blockers
  and no ready or blocked quick filter. A read that fails after a good one keeps the last list and says
  `refresh failed`. A read that times out (or is over 64 MiB) is not retried on the 30 s poll: the
  notice says `tracker too large to load quickly` and the next try is five minutes later, or at once on
  `r` or after a write. A write that lands while a read is in flight triggers one more read at once,
  and until it lands the notice says the list may not show your last change.
- **Graph.** `g` on a list row (or `v`) shows what blocks the task and what it blocks, from the same
  snapshot. From 100 columns the neighborhood is boxes with connectors (the focus in a double
  border, the selected node heavy, closed work muted) beside a Chain panel; below 100 columns it is
  an indented outline that never overflows. `g` again shows the whole graph: every task with a blocking
  link in topological layers (layer 0 is what nothing blocks), the longest chain of unfinished work
  marked `*`, cycles listed explicitly (`~`), a task that depends on itself (`!`) and a blocker that
  is not in the tracker (`?`). A task with many links collapses into `+N more`. Layout is
  `internal/taskview/graphlayout.go`: deterministic (the same tasks in any order draw the same), ids
  break every tie, connectors are drawn only between neighboring layers (other links are counted),
  and the graph engine's bottlenecks are marked `^`. `abacus tasks graph [ID | --whole] [--depth N]
  [--json]` prints the same view.
- **Insights view.** `n` works from any view; `e`, `x` and the workspace key act on the list's task,
  which this view does not show, so they only say `switch to the List view (v)`.
- **Read only.** Nothing in the list, sort, query or detail writes. The `n`, `e` and `x` forms and
  the confirm gate below are unchanged and now act on the task under the cursor.

`abacus tasks list [--query Q] [--sort KEY] [--reverse] [--preset P] [--tree] [--json]` and
`abacus tasks show ID [--json]` print the same typed reports (`schema_version` 1, additive keys
only) the TUI renders.

The new and edit forms, the "Checking the current state…" screen, the confirm screen and its
"applying…" state are each one framed panel with the title in the border. The form is a table: a
marker column (`▸` on the focused field, with the caret `▏` at the end of its value, so focus
shows without color), the label column, then the value column; a long value wraps in its column.
When the value column would be narrower than 12 cells (the edit form below 45 columns) the label sits above its value instead. The form error is a row under the fields.
The confirm screen shows the "changed since you opened it" note when there is one, the plan lines
and "exact command:" with the command wrapped in full, never cut. Only a body taller than the
terminal is clipped, and then a `↕ +N more` row says so. A confirm that is clipped cannot be
answered with `y`: the row reads "enlarge the terminal to review", `y apply` leaves the footer, and
the key does nothing until the whole plan and command are on screen (the same for the first-run
setup confirm). The command shown is the command run: an argument with an invisible, bidi or
control character is shown quoted, as an escape, so nothing is hidden between the screen and what
runs. While a form or confirm screen is open, its own keys lead the footer, so a narrow footer cuts
"? help" and the tab hint first. None of this changes the gate: nothing writes before `y` on the
confirm screen, and a state that hides a screen still blocks keys.

## Global search

`ctrl+k` opens a search box over every domain: Tasks (id, title, labels, description and comment
text), Board (threads, their latest reply and open tasks), Leases and Queues (runs, queued items,
what needs attention, history). It is live as you type, groups the hits by domain (best 20 each,
`+N more` for the rest) and uses the same query grammar as the Tasks `/` filter: words, `"a phrase"`,
`field:value`, `!field:value`, `status:open,blocked`, `priority:<=1`, `updated>7d`, `in:board`,
`who:ada` (also `assignee:`, `author:`, `holder:`, `machine:`), `is:blocked`, `is:ready`,
`is:stale`, `expires<30m`. A field a domain does not have excludes that domain, so `label:ui` finds tasks only. A duration on a time field points back (`updated>7d`: touched in the last week) except on `expires`, where it points ahead (`expires>2h`: more than two hours from now).

| Key (while the box is open) | Action |
| --- | --- |
| type, `backspace`, `ctrl+u` | edit the query (up to 256 characters) |
| `up` `down` | move the cursor over the hits |
| `tab` `shift+tab` | next, previous domain group |
| `enter` | jump to the hit's tab with the row selected; if that tab's own filter hides it, the filter is cleared and the footer says so |
| `ctrl+p` `ctrl+n` | step through earlier queries (with an empty query the box lists them: `enter` reuses one) |
| `esc`, `ctrl+k` | close |

Search reads what each tab already holds; opening it runs nothing, except one read of a domain
that never loaded (the Board, until its tab is first shown). A domain read longer ago than twice
its refresh interval is marked "may be out of date". A domain that is not connected, still loading
or unreadable is named in one muted line, and a domain searched only in part says how (only recent changes for Tasks when the full list could not be read, only recent threads for a capped Board, the first 20,000 documents). The last 100 queries that led to a jump are kept in the
abacus state directory (`search-history.json`, mode 0600, never in a tracker directory; a demo
session keeps none). The queries are stored as plain text: `abacus config set search_history off`
keeps none. If the history file is busy the jump still happens and the footer says it was not saved. `abacus search "QUERY" [--domain D] [--limit N] [--json]` prints the same hits
(exit 0 with an answer, 1 when no domain could be searched, 2 for a bad invocation or query; nothing
on stdout on a failure under `--json`).

## Board

A framed list panel with the summary, the channels and two aligned tables (recent threads and open
tasks), and the selected message or task in a detail panel beside it from 100 columns; below that
`enter` drills into the detail and `esc` comes back. The channel, author and claimant columns drop
first as the panel narrows; ids and text stay. Selection follows the thread or task, not its row,
across refreshes.

## Layout

```
 abacus  Board [Tasks] Leases Queues Doctor           header: domain tabs
 ───────────────────────────────────────────────
 list pane            │ detail pane                     body
 ───────────────────────────────────────────────
 ? help · q quit · ...                                 footer: short help
```

| Width | Body |
| --- | --- |
| >= 100 columns | list and detail side by side (list is 40% of the width, at least 30) |
| < 100 columns | list only; `enter` shows the detail full width, `esc` goes back |
| < 60 columns | the brand name is dropped from the header |
| tabs do not fit | header shrinks to the active tab and `n/m` |
| height < 16 | the rule under the header is dropped |
| < 40 columns or < 12 rows | the body is a "Terminal too small" message that says what is needed; the tab bar and footer stay around it |

Every threshold has a name in `internal/tui/layout.go` (`bpMin` 40, `bpCompact` 60, `bpWide` 100,
`bpUltra` 140; `hMin` 12, `hRule` 16, `hSummary` 24, `hTall` 40), and a test pins each value.
`bpUltra`, `hSummary` and `hTall` are reserved for the wide and tall layouts the tabs adopt in the
next steps.

Tabs are built from three small helpers, each tested at every size so no line can exceed the
terminal:

- `split` divides a width (or height) between fixed, minimum and weighted slots, like a ratatui
  layout, and never overshoots.
- `table` (`fit`, `fitRows` and `row`) builds an aligned table: columns drop in priority order as the
  width shrinks (the last one left is squeezed, not dropped) and the slack goes to the
  flexible column.
- `panel` draws a rounded, titled frame. The focused panel carries a `▸` before its title as
  well as a brighter border, so focus is visible with `NO_COLOR`. A panel too small for a frame
  (under 4 cells wide or 3 tall) degrades to plain lines.

Rule for any state that hides a screen (too small, an overlay, a collapsed pane): it must also stop
input reaching that screen, because a key pressed on a screen the user cannot see can still act
on it (a hidden confirm answered with `y` would apply a change unseen). A clipped confirm is a
partly hidden screen: it blocks `y` the same way (`TestConfirmAppliesOnlyWhenTheWholeCommandIsVisible`).
Global keys (tabs, help,
quit) keep working; keys for the hidden section are dropped. Each input path needs its own test,
capturing and non-capturing, since a guard on one path alone survives mutation of the other
(`TestTooSmallTerminalCannotApplyAHiddenConfirm`, `TestTooSmallTerminalGivesAHiddenListNoKeys`).

Leases, Queues and Doctor are built from them, with two more pieces:

- `duo` lays out a list panel and a detail panel. From `bpWide` (100) columns the list takes
  three fifths and the detail the rest, side by side; below that only the list is drawn, and
  `enter` drills into the detail and `esc` comes back. The list panel is the one focused panel;
  the detail is read-only. Panels are as tall as their content plus a little air (at least ten
  rows), never taller than the body, so a four-row list in a tall terminal is a compact box.
- `table.fitRows` sizes columns to the widest cell, so short content takes little room and a
  column is dropped only when it truly does not fit. Ids and states keep their whole width, up to 24 cells for a lease id and 12 for a lease state (longer text is cut with an ellipsis and is whole in the detail panel); the
  low-priority columns (holder and expiry, phase and machine, workflow and last event) go first.

The four list sections (Leases, Queues, Board, Tasks) share one piece, `listNav` (`listnav.go`):
the cursor, the scroll offset, the selected row's identity and the drill state. A section hands it
one stable id per selectable row (a lease resource, a run key, "t:"/"k:" plus a message id, a task
ID). When the selected row disappears on a refresh, Leases and Queues keep the selection pinned to
it (it is selected again if the row comes back) and Board and Tasks select the clamped row now
under the cursor. Tasks drills into its detail below 100 columns, like the Board.

Not-connected and unavailable states (and the first-run setup prompt) are one framed panel with the
domain in the border; the "how to connect:" text is a block of its own, set off by blank rows, which
the tool-name test exempts. Loading is a plain line.

The help overlay was the first user of the helpers: a focused panel with aligned key columns, and a
plain indented list when the frame would cost rows the text needs.

Header and footer are always shown (down to a height of 2). The body is cut to the remaining
height and the run list scrolls to keep the cursor visible; clipped content ends in a
`↕ +N more` row. The Needs attention block is pinned below the scrolling list (at most a third
of the height, at least its header with the count), so a long run list cannot push it out of
reach. Every line is truncated to the terminal width in display
cells (wide characters and emoji count as their real width), with an ellipsis. Zero or tiny
sizes never panic. Until the first size message arrives the shell assumes 80 columns.

A provider that was found but cannot be used (`broken`, `incompatible`) leaves its domain
"not connected" with the reason; the Doctor tab says why. `abacus tui` always hands the TUI every
detected provider.

The Doctor tab (the one place providers are listed by name) leads with "Ready" or "Needs attention",
one line per area, a "Bundled tools: 4 of 4 verified" line, and each tool with where it was found
(`bundled`, `PATH`, `override`, `demo`; or `unavailable` for abacus's own copy that cannot be used,
`optional` for the Board CLI, which abacus does not ship) and its version, then the detailed checks.
"Ready" needs bd, lambe and beads-lease usable. It runs `core.DoctorReport` on every visit and on `r` (ignored while a
run is pending; a report from an older run is dropped); it is never
polled because doctor shells out to each tool. Lambe polls every 5 seconds (not in demo),
other modules every 30 seconds.

## Theme and NO_COLOR

All styles and colors live in `internal/tui/theme.go`; sections ask for a semantic role (ok,
warn, fail, skip, muted, accent) and never for a color. A test fails if a color literal
appears in any other file.

- Colors adapt to a light or dark terminal background (lipgloss adaptive colors).
- Each role has truecolor, 256-color and 16-color values, so a 16-color terminal gets real
  ANSI colors.
- `NO_COLOR` (or a terminal without color) turns styling off entirely. Structure then does
  not depend on color: badges become `(ok)`, `(held)`, `(stalled)`, the active tab is
  `[Lambe]`, the selected run has a `▸` marker, and dimmed tabs carry their state in text.
- Badges: doctor `ok` `warn` `fail` `skip`, lease `held` `stale` `free`, `stalled`, `blocked`,
  `not connected` and the source labels (`tracker subset`, `graph analysis`).

## Visual language

Color means something, and every color is backed by a word or glyph so `NO_COLOR` loses nothing.

- **Red, amber, green** are states only: failed or blocked or P0, attention or deferred, ok or
  running. Names never use them: the tints for a person or machine come from a separate set (purple,
  teal, magenta, lilac; just the first two on a 16-color terminal), chosen by a hash of the name, so
  `ada` is the same color as a lease holder, a board author or claimant and a run's machine. Channels
  are not tinted, so a channel and a person never share a color by accident.
- **Accent** (blue) marks focus (the focused panel's border and title), section headings and the
  in-progress state.
- **Tasks bar**: the states in proportion across the panel, one glyph per state (`▒` open, `█` in
  progress, `▓` blocked, `░` deferred, `·` closed), so it reads in monochrome.
- **Queues mark**: a shape per state before its name (`●` running, `✕` failed, `◐` deferred, `◌` pending, `○` settled or canceled), colored by its tone.
- The selected row is drawn plain on the highlight so the highlight stays whole.

## Workspace

When an optional terminal workspace is connected (and in the demo), Queues, Leases and Tasks show
where the selected item lives. Nothing changes anywhere without workspace data.

- **Detail.** The selected run, lease or task gets a `where` block: the session, its machine, the
  match (`exact`, `by label`, `machine not configured`), `ambiguous: N claim it` with the
  candidates, or `not placed`. A cached read, a truncated read and a degraded workspace are marked.
  On Tasks the block is the last section of the detail pane.
- **Where column.** At 140 columns and wider the lists get an aligned `where` column as the last,
  first-dropped column; it appears only if no other column is dropped and the identifying columns
  (id, state) keep their width.
- **Jump.** `o` on Queues (as before) and `w` on Leases and Tasks use the open plan core built. On
  the keypress, and only when the placement is unambiguous and the plan's first step is an
  interactive `<workspace tool> -S <that placement's machine>`, that one step is run with the same
  runner `o` always used; no later step is tried, the command must be the located workspace tool
  with a safe machine name (the same check the adapter uses), and a second press while the handoff
  is open does nothing. Anything else is a framed notice that any key
  closes: the candidates when ambiguous, or the command to copy (machine not configured, a session
  on this machine, a step that cannot run from here). The key is absent from the footer and does
  nothing when there is no workspace data or the row has no placement. Like every section key it
  is dropped when the body is too small to draw.
- **Demo.** The demo workspace places some demo items (one ambiguous). A demo session never runs a
  step: `o` and `w` only show the command, including `o` on a run the workspace does not place.

## Demo

`abacus tui --demo` shows every tab with invented data (the fixtures in `internal/demo`, the same
ones the goldens use): Board, Tasks with graph insights and the confirm flow, Leases, Queues and
Doctor. It reads nothing from your machine. A confirmed change is applied to nothing and reports a
made-up id, `o` on Tasks says there is no tracker to open (the real full-view tool is never probed
or run), and nothing is recorded in the real audit log. A test drives all of this with a fake tool
on `PATH` that leaves a marker if it is ever run.

## Tests

Golden renders live in `internal/tui/testdata/golden/`, from demo and fixture data only (fake
providers; nothing runs a tool).
Every scenario is rendered at widths 40, 80, 120 and heights 10, 30 with colors off, plus the
Queues tab in truecolor (light and dark, escape codes shown as `␛`).

```
go test ./internal/tui                 # compare
go test ./internal/tui -update         # rewrite goldens, then review the diff
```

Invariants checked for every scenario and many sizes: no line is wider than the terminal
(display cells), no raw control characters reach the screen, header and footer are present.
Goldens are rendered through a fixed color profile; they have not been inspected in a real
terminal emulator.

A domain-language test asserts that no header, rule or footer line on any tab, at any width,
contains a provider name (Lambe, beads-lease, board-beads, b9s, Rex, the Board CLI), connected or not. Tasks
tests assert the confirm gate: no key path writes before `y`.
