# beads-lease list fixtures

Derived from the `Envelope` and `LeaseRow` types in andrew-bierman/beads-lease
(`internal/cliutil`), not captured from a hub. `list.json` has one held, one stale and one
free lease; `list-empty.json` is an empty hub (`leases` is omitted when empty).

`doctor-ok.json` and `doctor-fail.json` follow the `Check` type in
`internal/cmd/doctor.go` (checks actor, config, coord, hub; `status` ok|warn|fail|skip),
written by hand from that source, not captured from a hub.
