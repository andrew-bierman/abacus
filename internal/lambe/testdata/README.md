# Vendored Lambe contract fixtures

`contract/v1/*.json` are copied from `testdata/contract/v1` in the Lambe repo
(checkout at `v0.10.1-2-g7214d43`, wire contract `api_version` 1). They are the
contract the decoding tests and `--demo` mode read. Re-copy them when Lambe's contract
changes; automated drift detection is follow-up work.
