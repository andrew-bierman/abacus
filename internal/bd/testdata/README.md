# Fixtures captured from real output

| | |
|---|---|
| Tool | `bd` (Beads CLI) `bd version 1.3.4 (7ef2fd05e: main@7ef2fd05eb0c)` |
| Captured | 2026-11-23 |
| Where | a throwaway embedded tracker in a `mktemp` directory (isolated `HOME`, `XDG_*`), prefix `hs`. Never a real tracker. |

`manifest-before.txt` and `manifest-after.txt` are the bytes of
`.beads/embeddeddolt/hs/.dolt/noms/manifest`, the small file the embedded Dolt store rewrites on
every write: right after `bd init` and two creates, then after `bd update ID --status=in_progress`.

What was measured (fingerprint choice for the live refresh): the manifest content changes on every
write (update, comment, dependency, close, create, including a write made with
`--dolt-auto-commit=batch`) and never on a read (`export`, `status`). The modification times of the
journal files change on reads as well, and `.beads/last-touched` does not change on every write, so
neither is a fingerprint. Reading the manifest costs one 150-byte file read; `bd --readonly status`
and `bd --readonly count` each cost a process start (about 130 ms here) and `bd --readonly export`
about 360 ms for a tracker of three tasks.
