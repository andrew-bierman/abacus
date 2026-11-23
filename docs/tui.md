# abacus TUI

The TUI renders core's typed domain reports and holds no logic of its own. It is the fleet
cockpit: one tab per domain (Board, Tasks, Leases, Queues) plus Doctor. abacus deliberately has
no issue browser, kanban or dependency graph; Tasks shows a compact summary and hands off to a
full view (`o`) for the rich tree and graph. The Board is a message board for agents: channels,
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
| `j` `k`, `down` `up` | move the cursor (scroll elsewhere); on the Board it selects the next thread or task |
| `n` `e` `x` | Tasks: new task, edit, close the selected task. Each opens a form (`enter` next field and, on the last, review; `tab`/`shift+tab` move; `esc` cancels) and then a confirm screen showing the exact change and command: `y` applies, `n` or `esc` cancels |
| `o` | Tasks: open the full view in the tracker directory (probes for it first; an error notice with an install hint if it is missing). Board: open the board in the browser (the local web inspector, until Ctrl-C), only when connected. Queues: hand the selected run's machine to a session |
| `i`, `s` | a guided tab: start its setup step (workspace, actor name, Queues config) |
| `r` | Tasks, Board: refresh (Tasks also reruns graph insights). Doctor: run doctor again. Board also refreshes every 30 seconds, only while its tab is visible and never two reads at once |
| `enter`, `esc` | Queues, narrow layout only: open the run detail; `esc` closes it, a second `esc` clears the filter |
| `/` | Queues: filter runs by issue, state, phase, machine or workflow |
| `?` | full help overlay; `?` or `esc` closes it |
| `q`, `ctrl+c` | quit (while a form or the filter is open, only `ctrl+c`) |

While a form, the confirm screen or the filter is open, every key (including `q`, digits and `?`)
goes to it. Nothing writes except `y` on the confirm screen.

## Tasks

Counts by state, ready work and the most recent changes (an aligned table with a cursor, for `e` and
`x`) in a framed list panel, and the graph insights in a panel of their own: the next pick, the
issues that block the most, the critical path and dependency cycles. From 100 columns the insights
sit beside the list; below that they sit under it (the list keeps two thirds of the rows). Insights
are read on start and on `r`, not on a timer, because they run several processes. Without an
insights provider the panel says it is not connected and how to install one.

The new and edit forms, the "Checking the current state…" screen, the confirm screen and its
"applying…" state are each one framed panel with the title in the border. The form is a table: a
marker column (`▸` on the focused field, with the caret `▏` at the end of its value, so focus
shows without color), the label column, then the value column; a long value wraps in its column.
When the value column would be narrower than 12 cells (the edit form below 45 columns) the label sits above its value instead. The form error is a row under the fields.
The confirm screen shows the "changed since you opened it" note when there is one, the plan lines
and "exact command:" with the command wrapped in full, never cut. Only a body taller than the
terminal is clipped, and then a `↕ +N more` row says so. None of this changes a key or the gate:
nothing writes before `y` on the confirm screen, and a state that hides a screen still blocks keys.

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
on it (a hidden confirm answered with `y` would apply a change unseen). Global keys (tabs, help,
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
under the cursor. Tasks has no detail panel to drill into.

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
one line per area, and each tool with where it was found (`bundled`, `PATH`, `override`, `demo` or
`missing`) and its version, then the detailed checks. It runs `core.DoctorReport` on every visit and on `r` (ignored while a
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
