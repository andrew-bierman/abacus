# rex fixtures

Nothing here came from a real Rex run; no Rex was run to make them.

- `ls-help-example.json` is the `response_example` of `session.list` in the Rex server API
  reference (`help.json`, API revision 13), verbatim. `rex ls --json` printing exactly the
  `session.list` result is itself unverified (see `docs/tool-contract.md`).
- `version-13.json` is the `response_example` of `server.version` in the same reference plus
  the two fields that reference documents (`api_revision`, `api_min_revision`). That
  `rex version --json` prints this shape is second-hand (from Lambe's research notes).
- `ls-fleet.json`, `ls-null.json` and `version-no-revision.json` are hand-written to exercise
  Lambe's window naming convention (`Lambe · <ISSUE>`), a null session list and a server from
  before `api_revision` existed ("a missing `api_revision` means 0").
