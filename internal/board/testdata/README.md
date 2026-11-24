# Vercel Board fixtures

Captured from real output of `@vercel/board` **0.54.0** (Apache-2.0, https://github.com/vercel/board),
installed with `npm install --ignore-scripts --save-exact @vercel/board@0.54.0` into a temp prefix
(lockfile integrity `sha512-JPsKFnPVgSQpZGxtj/8TrPbHnJzebIMXXdzDeAZkfkXq9B0d0L3diAq5TByWtvP+4nDgrOqCHQxnzxdvsFNJ2A==`,
no global install, no lifecycle scripts). Captured on 2026-11-22 against a local board started with
`board serve --port <random> --dir <temp> --no-inspect --no-open` and an isolated `HOME`/`XDG_*`; every
command was run with `--json` and an explicit `--url` (or none, for the `err-no-board` case) and read
the output of the real CLI. Nothing was run against Vercel's hosted API, a team board or any token.

The board holds invented messages (`dev`, `dev/web`, `work` channels, a few tasks, one message with
control characters in its text). The only edit is that the OS user name was replaced by `ada`.

- `channels.json`: `channels list --json --limit 50`.
- `read-channel.json`: `read dev/web --json --limit 5` (includes a message with ESC and BEL in it).
- `read-thread.json`: `read 3 --json` (a thread with replies from the user and from a profile).
- `tasks.json`, `tasks-all.json`: `tasks --json --limit 20` and `--state all` (blocked and claimed tasks).
- `post.json`: `post dev --json --idempotency-key ... -- TEXT`.
- `help.txt`, `version.txt`: `--help` and `--version`, used for the identity check.
- `err-*.stderr.json`: the failure document the CLI prints on stderr with `--json` (`no board`
  exit 2, `unreachable` exit 1, `channel not found` exit 1). stdout is empty in those cases.

- `err-login-required.stderr.json`: `channels list --json --scope acme` with no Vercel login (exit 1,
  no network needed). Captured 2026-11-24 from the same 0.54.0 binary, isolated `HOME`/`XDG_*`.
- `channels-page1.json`, `channels-page2.json`, `channels-page3.json`: `channels list --json --limit 5`
  and `--after <next>` on a local board with 12 channels (`team/ch01`..`team/ch12`). The cursor is the
  last channel path there; an unknown cursor restarts at the first page; `next` is `null` on the last page.
- No `timeout` failure document exists to capture: the CLI has no client deadline (against a listener
  that never answers it was still waiting after 100 s), so a stalled board is cut by abacus's own
  deadline (`TestRealBoardCliAStalledBoardIsCutByAbacusDeadline`).

Note: the CLI's failure document has `reason` (not `class`), `message`, `next[]` and sometimes
`hint`; abacus branches on `reason`.

Credit: Vercel Board is by Vercel, Apache-2.0. abacus only runs its CLI as a subprocess; no code is
imported, vendored or copied. These files are its JSON output about invented messages, kept only to
test decoding.
