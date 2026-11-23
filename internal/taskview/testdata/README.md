# Fixtures captured from real output

These files are **captured from real output** of the Beads CLI, not written by hand.

| | |
|---|---|
| Tool | `bd` (Beads CLI) |
| Version | `bd version 1.3.4 (7ef2fd05e: main@7ef2fd05eb0c)` |
| Captured | 2026-11-23 |
| Where | a throwaway tracker in a `mktemp` directory, isolated `HOME`, `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_STATE_HOME` and `XDG_CACHE_HOME`; created with `bd init --non-interactive --quiet --skip-agents --skip-hooks --prefix=tk`. Never a real tracker. |

How each file was captured, from inside that tracker, after creating nine invented tasks with
the real `bd` (an epic with three children, one blocking chain, labels, two comments, one
in-progress bug, one deferred chore, one stored-blocked task, one task with design, notes and
acceptance criteria, one P4 bug, and one closed child):

| File | Command | Notes |
|---|---|---|
| `export.jsonl` | `bd --readonly export` | The read the Tasks view makes. `--readonly` is accepted. One JSON issue per line, `_type` is `issue`, **line order is not stable** (it is map order), labels are sorted, `dependency_count` and `dependent_count` count `blocks` dependencies only (not `parent-child`). Fields used: id, title, description, design, notes, acceptance_criteria, status, priority, issue_type, assignee, created_at, updated_at, closed_at, close_reason, defer_until, labels, dependencies (issue_id, depends_on_id, type), comments (author, text, created_at), dependency_count, comment_count. Priority 0 is written (it is not omitted). |
| `export-empty.jsonl` | `bd --readonly export` in a freshly initialized tracker | Zero bytes. An empty tracker exports nothing, not `[]`. |
| `ready.json` | `bd --readonly ready --json` | Used only by a parity test: the tasks this reader calls ready must be the ones `bd ready` lists. |
| `schema.json` | `bd schema` | The status enum is `open, in_progress, blocked, deferred, closed, pinned, hooked`; the dependency type enum has 19 values (`blocks`, `parent-child`, `conditional-blocks`, `waits-for`, `related`, ...). |

The author names `ada` and `mei` and every title are invented. `*.golden.json` files are this
package's own outputs over `export.jsonl` at a fixed time (`go test ./internal/taskview -update`).

## Edge cases of "ready" and "blocked" (second tracker)

`export-edges.jsonl`, `ready-edges.json` and `blocked-edges.json` come from a second throwaway tracker
(same tool, version and isolation, prefix `ed`) that holds one task per dependency kind, all pointing
at one open task, plus a blocked parent with a child and a grandchild, an unblocked epic with a child
and a closed task whose blocker is open (`bd close --force`; plain `bd close` refuses a blocked
issue). `bd --readonly ready --json` and `bd --readonly blocked --json` were captured with the
blocker open. What bd 1.3.4 did:

| Case | `bd ready` | `bd blocked` |
|---|---|---|
| `blocks` on an open task | held | listed |
| `conditional-blocks` on an open task | held (freed when the target closes, even with a "failed" reason) | listed |
| `waits-for` on an open task | **ready** | not listed |
| `related`, `discovered-from` | **ready** | not listed |
| `blocks` on `external:proj:cap` | **ready** (an external reference never holds) | not listed |
| child of a blocked parent, and its child | held (the hold is inherited down `parent-child` links) | listed |
| child of an unblocked epic | ready | not listed |
| closed task whose blocker is open | not listed (closed work is never ready) | **not listed** |

## Dependency graph fixtures (third tracker)

`graph-export.jsonl` is `bd --readonly export` from a third throwaway tracker (same tool, version
and isolation as above, prefix `gr`, captured 2026-11-23). It was built with the real `bd`: a
diamond (`Snapshot reader` closed, then `Parse export` in progress and `Sanitize text` blocking
`Graph layout`, which blocks `Graph view` and `Graph docs`), a chain of eight steps (the first two
closed, the third in progress), a hub that blocks twelve tasks, a disconnected pair, a lone task,
a task with ANSI escape and bidi characters in its title, and a wide-rune title. The file is the
export exactly as `bd` wrote it.

`graph-export-edges.jsonl` is **not** a bd capture of those edges. bd 1.3.4 refuses to create
them, so the file is `graph-export.jsonl` with three dependency records added by hand (the same
record shape bd writes): a cycle (`Cycle alpha` also depends on `Cycle gamma`; `bd dep add` fails
with "would create a cycle", with or without `--no-cycle-check`), a self-dependency (`bd dep add`
fails with "cannot add self-dependency"), and a dependency on a task that is not in the tracker
(`bd delete` removes the links of the task it deletes, so a dangling one cannot be made). `bd sql`
is "not yet supported in embedded mode", so there was no other way to write them.
