# Vendored Lambe contract fixtures

`contract/v1/*.json` are copied from `testdata/contract/v1` in the Lambe repo
(checkout at `v0.10.1-2-g7214d43`, wire contract `api_version` 1). They are the
contract the decoding tests and `--demo` mode read. Re-copy them when Lambe's contract
changes; automated drift detection is follow-up work.

`tracker/status.json` is the documented `lambe status -json` subset (docs/REFERENCE.md in
Lambe): tracker in-progress claims as `running` runs, no phase, `last_event_age` "n/a",
`gate_rejections` empty, `daemon.pid` 0. Written by hand from that doc, not captured.
