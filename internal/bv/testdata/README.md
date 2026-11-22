# bv fixtures

Captured from real output of `bv v0.27.0` (installed with `brew install dicklesworthstone/tap/bv`),
run only with `--robot-triage|--robot-plan|--robot-insights|--robot-graph --format json`, against a
hand-written, isolated temp project (`.beads/issues.jsonl` with seven invented issues `fx-1`..`fx-7`,
one dependency chain fx-1 <- fx-2 <- fx-3 and one cycle fx-5 <-> fx-6, `BEADS_DIR` pointed at it,
isolated `HOME` and `XDG_*`). The temp path was rewritten to `/tmp/fixture-project`. Nothing was run
against a real project or hub.

- `triage.json`, `plan.json`, `insights.json`, `graph.json`: the cyclic project.
- `insights_acyclic.json`: the same project without the cycle (so `Keystones`, the critical path, is filled).

Observed, and used by `internal/bv`: with no tracker directory `bv --robot-triage` exits 1 with a
diagnostic on stderr and a partial body on stdout (abacus treats any non-zero exit as degraded and
ignores that body); with an empty `issues.jsonl` it exits 0.

Credit: bv is beads_viewer by Jeffrey Emanuel (Dicklesworthstone),
https://github.com/Dicklesworthstone/beads_viewer, license:
https://github.com/Dicklesworthstone/beads_viewer/blob/main/LICENSE . abacus only runs the `bv`
binary as a subprocess; no bv code is imported, vendored or copied. These fixtures are bv's JSON
output about invented issues, kept only to test decoding.
