# beaddeck (working name)

One terminal tool for the Beads tool family: it detects which tools are installed, runs
`doctor` and `status` across them, and shows one TUI section per detected tool. Tools are
composed only through their `--json` CLIs and versioned local APIs.

> **Name status:** `beaddeck` is a working name. The final name and brand are undecided,
> so this repo stays local (no remote) until they are chosen. The name lives in
> `internal/brand`, the module path in `go.mod`, and the `cmd/` directory name.

## Development rules

- Develop and test against an isolated `HOME`, `XDG_*` dirs and `bd` database. Never touch a
  production Lambe daemon, its state, its binary or real Beads data.
- Nothing here launches an app or takes window focus. Tests use fakes for Lambe, HTTP and Rex.
- Use a private `GOCACHE` for builds.

License: GPL-3.0-or-later, matching Lambe.
