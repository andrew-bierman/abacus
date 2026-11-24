# rex fixtures

Where each file came from. "Recorded" means captured from a real Rex on this machine on
2026-11-24 (`rex 0.1.0-preview.22`, API revision 19), on throwaway sessions created for the
purpose (labelled `spike-abacus-agents`, killed afterwards) with `printf` emitting OSC 7501
status. Process ids, user names, paths, session, block, window and host ids were replaced with
fixed fake values; nothing else was edited. Everything else is hand-written.

Not recorded, and so UNTESTED against a real Rex: `-S HOST` for any call, a real claude or codex
pane (their process names and `app` values), exit code 4 (no server) on the block calls, and any
other Rex version or API revision (abacus accepts 10 to 13; the one tested is 19).

## Session list and version (the workspace layer)

- `ls-help-example.json` is the `response_example` of `session.list` in the Rex server API
  reference (`help.json`, API revision 13), verbatim. `rex ls --json` printing exactly the
  `session.list` result was not re-verified against a live server (see `docs/tool-contract.md`).
- `version-13.json` is the `response_example` of `server.version` in the same reference plus
  the two fields that reference documents (`api_revision`, `api_min_revision`). That
  `rex version --json` prints this shape is second-hand (from Lambe's research notes).
- `ls-fleet.json`, `ls-null.json` and `version-no-revision.json` are hand-written to exercise
  Lambe's window naming convention (`Lambe · <ISSUE>`), a null session list and a server from
  before `api_revision` existed ("a missing `api_revision` means 0").

## Panes and status (the Agents view)

Recorded, scrubbed:

- `block-ls.json` is `rex block ls -s SESSION --json` for a session with one shell.
- `program-status.json` is `rex block call -s SESSION -b BLOCK program_status` after a script
  printed three OSC 7501 reports: a blocked permission request, a working child with progress 40,
  and an error. `msg` and `title` come back already decoded.
- `process-exited.json` is the reply for a keep-open pane whose shell has exited (`last_exit` set, no
  `foreground`, no `child`), seen on a live Rex and scrubbed; the exact `last_exit` fields are approximated.
- `process.json` is `rex block call -s SESSION -b BLOCK process` for a pane running `sleep`.

Hand-written:

- `block-ls-mixed.json` has terminal blocks beside the other flavors Rex has (tunnel, onboarding).
- `program-status-empty.json` and `-null.json` are a pane that never reported anything.
- `program-status-hostile.json` carries terminal escapes, bidi overrides, a line break and an out of
  range progress in every text field; `-oversized.json` has 10 KB titles and messages;
  `-unknown.json` has a state and a kind this build does not know and an unparsable time.
- `program-status-drift.json` and `-drift-types.json` are output that is not the documented shape
  (a missing or mistyped `records`); `process-drift.json` (not an object) and `block-ls-drift.json` likewise.
- `process-agent.json` is what a node script agent would plausibly report (`script` set). It is a
  guess at the real shape of a codex process, not a recording. `process-child-only.json` has no
  `foreground` and an argv0 with a leading dash, as a login shell has.
