# abacus TUI

The TUI renders core's typed reports and holds no logic of its own. abacus deliberately has
no issue browser or kanban; tools such as b9s already do that. The TUI shows what is unique
to abacus: a tab per detected module (Lambe runs, attention and queue; beads-lease held,
stale and free leases), the Rex handoff, and doctor status.

## Keys

Footer and the `?` overlay are generated from the `key.Binding` values in
`internal/tui/keys.go`, so they match what the keys do.

| Key | Action |
| --- | --- |
| `tab`, `right` / `shift+tab`, `left` | next / previous selectable tab |
| `1`-`9` | jump to tab N (ignored for a dimmed tab) |
| `j` `k`, `down` `up` | move the run cursor (scroll in the other tabs) |
| `enter`, `esc` | narrow layout only: open / close the run detail; `esc` also clears a filter |
| `/` | filter runs by issue, state, phase, machine or workflow; `enter` keeps it, `esc` clears it |
| `o` | hand the selected run's machine to Rex (`rex -S <machine>`) |
| `r` | Doctor tab: run doctor again |
| `?` | full help overlay; `?` or `esc` closes it |
| `q`, `ctrl+c` | quit (while typing a filter, only `ctrl+c`) |

While a filter is being typed, every key (including `q`, digits and `?`) goes to the filter.

## Layout

```
 abacus  [Lambe] beads-lease ghost (broken) Doctor     header: module tabs
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

Header and footer are always shown. The body is cut to the remaining height and the run list
scrolls to keep the cursor visible. Every line is truncated to the terminal width in display
cells (wide characters and emoji count as their real width), with an ellipsis. Zero or tiny
sizes never panic. Until the first size message arrives the shell assumes 80 columns.

A tab for a module that was found but cannot be used (`broken`, `incompatible`) is dimmed and
shows its state, for example `ghost (broken)`; it cannot be selected, and the Doctor tab says
why. Modules that are not installed get no tab. Note: `abacus tui` currently passes only ready
modules to the TUI, so dimmed tabs appear once the CLI passes all detected entries.

The Doctor tab runs `core.DoctorReport` the first time it is opened and on `r`; it is never
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
- Badges: doctor `ok` `warn` `fail` `skip`, lease `held` `stale` `free`, `stalled`, `blocked`
  and the `tracker subset` label.

## Tests

Golden renders live in `internal/tui/testdata/golden/`, from demo and fixture data only.
Every scenario is rendered at widths 40, 80, 120 and heights 10, 30 with colors off, plus the
Lambe tab in truecolor (light and dark, escape codes shown as `␛`).

```
go test ./internal/tui                 # compare
go test ./internal/tui -update         # rewrite goldens, then review the diff
```

Invariants checked for every scenario and many sizes: no line is wider than the terminal
(display cells), no raw control characters reach the screen, header and footer are present.
Goldens are rendered through a fixed color profile; they have not been inspected in a real
terminal emulator.
