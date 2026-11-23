# bv fixtures (recorded output, static)

Captured once from real output of `bv v0.27.0`, run only with `--robot-triage|--robot-plan|--robot-insights|--robot-graph --format json`, against a
hand-written, isolated temp project (`.beads/issues.jsonl` with seven invented issues `fx-1`..`fx-7`,
one dependency chain fx-1 <- fx-2 <- fx-3 and one cycle fx-5 <-> fx-6, `BEADS_DIR` pointed at it,
isolated `HOME` and `XDG_*`). The temp path was rewritten to `/tmp/fixture-project`. Nothing was run
against a real project or hub.

- `triage.json`, `plan.json`, `insights.json`, `graph.json`: the cyclic project.
- `insights_acyclic.json`: the same project without the cycle (so `Keystones`, the critical path, is filled).

These are static fixtures. The bv binary is not part of abacus and is never run by this repository
or its tests; `internal/graph/parity_test.go` only reads these files and compares the facts in them
with what the in-process engine computes for the same invented issues.

Credit: bv is beads_viewer by Jeffrey Emanuel (Dicklesworthstone),
https://github.com/Dicklesworthstone/beads_viewer, license:
https://github.com/Dicklesworthstone/beads_viewer/blob/main/LICENSE . abacus links bv's analysis
packages (see THIRD_PARTY_NOTICES.md in a release archive). These fixtures are bv's JSON output
about invented issues, kept only as a reference for the engine's behavior.
