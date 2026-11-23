# graph fixtures

Everything here is invented and hand-written; nothing was captured from a tracker, and `bd` and
`bv` were never run to make it.

- `*.jsonl`: tracker exports in the shape `.beads/issues.jsonl` has (one issue per line:
  `id`, `title`, `status`, `priority`, `issue_type`, `updated_at`, `dependencies` as
  `{issue_id, depends_on_id, type}`). The shape follows the fields `internal/bd` reads plus the
  dependency record bv's loader documents.
  - `fx_cyclic`, `fx_acyclic`: the seven invented issues behind `internal/bv/testdata`
    (fx-1..fx-7, one chain, one cycle, one closed issue); `fx_acyclic` is the project without the
    cycle (fx-5 and fx-6 removed).
  - `chain`, `diamond`, `cycle`, `closed`: the small graphs of the assessment.
  - `blocks_form`: the same edge written on the blocker (`"blocks": [...]`).
  - `empty`: an empty tracker (a zero-byte export).
  - `big200`: 20 chains of 10 issues; chains 0-14 open, 15-17 with a closed head, 18-19 with an
    in-progress head (20 actionable, 177 blocked, 3 closed, 180 edges).
- `robot/*.json`: `bv --robot-*` documents (`triage`, `plan`, `insights`, `graph`) in bv's
  documented shape for chain, diamond, cycle, closed and big200, written by hand with values
  worked out from the graph. They are NOT recorded output. The only recorded bv output used by the
  parity tests is in `internal/bv/testdata`.
