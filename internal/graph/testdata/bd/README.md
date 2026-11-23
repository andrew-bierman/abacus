# bd export fixture

`export-real.jsonl` is the real stdout of `bd --readonly export`, captured from real output once
on 2026-11-22 with bd 1.3.4 (7ef2fd05e), in a throwaway tracker (`bd init --non-interactive
--quiet --skip-agents --skip-hooks --prefix=gx`, isolated `HOME` and `XDG_*`, never a real
tracker). Four invented tasks: a chain of three with `blocks` dependencies, a label on two, a
comment on one, and one closed. No host path or user name appears in it (the actor was `fixture`).

Every record in it has `"_type": "issue"`; bd tags records and excludes memories and
infrastructure beads by default, so a default export holds no other `_type`. The skip of other
types is covered by a synthetic test, not by this file.
