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

Counts by state, ready work, the most recent changes (a short list with a cursor, for `e` and `x`)
and graph insights: the next pick, the issues that block the most, the critical path and dependency
cycles. Insights are read on start and on `r`, not on a timer, because they run several processes.
Without an insights provider the block says it is not connected and how to install one.

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
- `fitColumns` and `formatRow` build an aligned table: columns drop in priority order as the
  width shrinks (the last one left is squeezed, not dropped) and the slack goes to the
  flexible column.
- `panel` draws a rounded, titled frame. The focused panel carries a `▸` before its title as
  well as a brighter border, so focus is visible with `NO_COLOR`. A panel too small for a frame
  (under 4 cells wide or 3 tall) degrades to plain lines.

The help overlay is the first user of them: a focused panel with aligned key columns, and a
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
one line per area, and each tool with where it was found (`bundled`, `PATH`, `override` or
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
