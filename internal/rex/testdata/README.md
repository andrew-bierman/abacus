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

## Captured from a real Rex (2026-11-23)

These came from a real Rex, 0.1.0-preview.19 (API revision 14, macOS arm64), run as an isolated
server on a temporary socket with its own state directory, so no real session was read or changed:
`version-14.json`, `ls-14.json`, `block-ls.json`, `window-ls.json`, `run-json.json`,
`split-json.json`, `new-json.json` (the `--json` answers of `run`, `split` and `new`),
`capture.txt` (`capture --trim` of one of its panes) and `events.jsonl` (`events --json` while a
session and its panes were created, renamed and killed; client connections are in it on purpose).
