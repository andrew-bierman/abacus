# Board bundling assessment: shipping the Board domain without asking for Node

Date: 2026-11-22. Status: assessment for an owner decision, no code changed.
Subject: `@vercel/board` **0.54.0** (Apache-2.0, https://github.com/vercel/board), the CLI abacus
composes for the Board domain (`internal/board`, `docs/tool-contract.md`).

Owner rule: tools should never be missing for the user; abacus brings its own binaries. Today
Board is the one tool that cannot ride in the release archive, because it is a Node program, and
the Board tab says "not connected: npm i -g @vercel/board".

How to read the evidence tags: **[ran]** means measured on this machine on 2026-11-22;
**[read]** means read from the published package source or docs (`npm pack`-equivalent install
with `--ignore-scripts` into a temp prefix, plus its shipped `docs/`); **[inferred]** is a
judgement. Nothing was run against any real board, Vercel account or network service (the only
network use was `npm` fetching the package and `bun` downloading its own runtimes for
cross-compiling).

## Summary

- A one-file executable per platform is **technically easy**: `bun build --compile` produced a
  working linux-x64 binary in under a second, `--help` is byte-identical to abacus's identity
  fixture, no native addons. The cost is **size**: about 111 MB per platform (about 52 MB
  gzipped), because the Bun runtime is about 107 MB of it and the CLI itself is about 4 MB.
  Adding it roughly doubles the 77 MB archive.
- A **native Go read client** is small for the three reads abacus needs (3 GET routes, one
  bearer token) but the hard part is auth: the CLI's token comes from a multi-step Vercel login
  exchange abacus should not reimplement. It only works cleanly if abacus is handed a token.
  Writes (`post`, `reply`) would stay on the CLI or be a second, riskier client.
- Recommendation: **(d) hybrid, step 1 is (a) with better wording now**, then a read-only Go
  client behind the existing adapter interface for the token-handed and local-board cases, and
  decide on the 111 MB compiled binary only if the owner accepts the archive size. Details below.

## 1. What `board` does over the wire for abacus's commands

abacus runs exactly these argvs (allowlist in `internal/board/module.go`), each with an optional
trailing `--url U`:

| abacus argv | HTTP route it maps to [read] | Response keys abacus decodes |
| --- | --- | --- |
| `channels list --json --limit 50` | `GET /channels?limit=50` | `channels[].path`, `lastIndex`, `messages`, `threads`; `next` |
| `read <channel> --json --limit 5` | `GET /threads?channel=<c>&limit=5` | `threads[].message`, `replies` (count), `last` |
| `read <id> --json` | `GET /messages/:id` | `message`, `replies[]` |
| `tasks --json --limit 20` | `GET /tasks?limit=20` (state defaults to open) | `tasks[]` as thread summaries; `message.task.state`, `task.claim.actor/until` |
| `post` / `reply` (writes) | `POST /messages` with an operation id and profile headers | out of scope for a read client |

The CLI's `--json` output is, per its docs, "what's in it as the HTTP API sends it" [read];
the fixtures in `internal/board/testdata` agree (`{"channels":[...],"next":null}`,
`{"threads":[{message,replies,last}]}`).

### Protocol facts [read from `docs/04-reference/03-http.md`, `01-cli.md` and `dist/*.mjs`]

- Only GET and POST, JSON bodies, JSON responses. `GET /openapi.json` is generated from the
  server's Zod route table, so the API is machine-describable (a Go client could be generated or
  checked against it).
- Auth: every request sends `Authorization: Bearer <OIDC JWT>`. The token's `aud` must equal the
  board's URL, so a token for one board cannot be replayed on another. Issuers: Vercel
  (`https://oidc.vercel.com/<team>`), GitHub Actions, or any issuer a grant names.
- Where the CLI gets a token, in order: `BOARD_TOKEN`; a local board's own token (from
  `vb serve`, for localhost); GitHub Actions OIDC; the Vercel runtime's OIDC; then a development
  token minted from your Vercel login (`VERCEL_AUTH_TOKEN`, else `vercel login`'s credentials
  file `auth.json` under the `com.vercel.cli` data dir, else it execs the `vercel` CLI when
  credentials live in the keychain), cached 12 hours in `~/.config/board/tokens/`.
- The minting path for a person is `POST https://api.vercel.com/v1/projects/<id>/token?source=board`
  with the Vercel login, then an OIDC token exchange whose audience is the board URL, after
  resolving which Vercel project a board host belongs to (`GET /v9/projects/...`,
  `GET /v2/teams`). A team's hosted board URL is
  `https://api.vercel.com/v1/messageboard/boards/<board>?teamId=<team>`.
- `BOARD_URL` / `--url` select the board; `--scope <team>`, `--name`, `BOARD_NAME`,
  `VERCEL_ORG_ID` select a team board; `VERCEL_AUTOMATION_BYPASS_SECRET` is sent as
  `x-vercel-protection-bypass`. abacus already scrubs the environment to an allowlist
  (`core.ScrubBoardEnvFor`).
- Pagination: cursor based. `/channels` returns `{channels, cursor, next}` (limit up to 500);
  `/threads` returns `{threads, cursor, older}` and pages back with `before=<older>`; message and
  task lists take `after`. IDs and cursors are opaque strings of 1 to 128 characters. On Vercel's
  hosted board, listing is one channel at a time, `by`/`from` filters answer 501
  `not_supported`, and "latest" is capped at 100.
- Errors: HTTP errors are `{"error":{"code","message","hint?"}}` (401 `unauthorized`, 403
  `forbidden`, 404 `message_not_found`, 429 `rate_limited` with `Retry-After`, 501
  `not_supported`, 502 `unreachable`, ...). The CLI maps them to its own `--json` failure
  document on stderr, `{status, reason, message, next[], hint?}`, which is what abacus branches
  on. `reason` values seen in source: `missing_scope` (no board/team selected, status
  `action_required`), `login_required`, `permission_denied`, `deployment_protected`,
  `board_not_found`, `board_not_granted`, `wrong_url`, `rate_limited`, `api_error`,
  `network_error` (maps from `unreachable` or too many redirects), `timeout`
  (`wait`/`tasks claim --wait` only), `token_unavailable`. A native client would have to map
  HTTP status plus `error.code` to abacus's states itself.
- The client uses `redirect: "error"`, retries 429/503 up to four times, uses `If-None-Match`
  ETag on channel and actor reads. Each of those is behavior a Go client would copy or skip.

### Judging a native Go read client (option c)

- **Size [inferred]:** three GET routes, about 4 response structs abacus already has
  (`channelsDoc`, `threadsDoc`, `threadDoc`, `threadsDocTasks`), a bearer-token transport, error
  mapping. Roughly 400 to 700 lines of Go plus tests; the existing decoders and fixtures mostly
  carry over, since `--json` equals the HTTP body.
- **Stability [read]:** the package is 0.54.0 and pre-1.0; the docs mention a "released `vb`"
  still sending headers the server now ignores, and the HTTP routes have version segments only on
  Vercel's own API (`/v1/messageboard`). The read routes' shapes have been stable in the fixtures
  (0.54.0 only was verified; we have no history for earlier versions). `minVersion` 0.54.0 would
  become an API-shape check instead. Expect churn; `openapi.json` is the mitigation.
- **Auth is the hard part.** Reimplementing the Vercel-login mint plus OIDC exchange plus project
  resolution plus the 12-hour cache is a security-sensitive surface that Vercel owns and changes.
  Delegable cases: (1) a token abacus is handed (`BOARD_TOKEN`, which the docs say a team board
  accepts), (2) a local board (`vb serve`, token from its own file; abacus would still need that
  CLI to run it, so the local case stays CLI-only unless the owner uses another host), (3) CI
  OIDC. The common human case, "I ran `vercel login`", is exactly the one that needs the
  exchange (or exec'ing the `vercel` CLI, which is Node again).
- **Writes:** `POST /messages` needs the operation id (idempotency), profile header, and exact
  409/403 handling; abacus's write gate audits around a CLI subprocess today. A read-only client
  leaves writes on the CLI, so the CLI is still required for posting. That weakens the "never
  missing" story unless writes are ported too.
- **License:** Apache-2.0 allows reimplementing a documented HTTP API; no code needs copying.
  If any source or docs text is copied into a Go file, Apache section 4 requires keeping the
  license, stating changes and carrying NOTICE content (this package ships no `NOTICE` file
  [read]). A clean-room client from `openapi.json` plus the fixtures avoids the attribution
  questions; keep the existing credit paragraph.

## 2. Standalone binary option (b)

Tools on this machine [ran]: Node v26.12.0, npm 11.19.1, Bun 1.4.4 (installed by mise, not
active; invoked by absolute path). Used an isolated HOME and cache; installed with
`npm install --ignore-scripts --save-exact @vercel/board@0.54.0` into a temp prefix. Package:
`engines.node >= 22.18`, three runtime deps (`zod`, `jose`, `@vercel/oidc`), 4 optional peer
deps not installed. No `.node` native addons anywhere in the tree [ran: `find -name '*.node'`
empty]. The only native-ish API is `node:sqlite`, used by `serve`/local-store code paths that
abacus never invokes; whether Bun's `node:sqlite` shim supports `vb serve` was **not tested**
(I did not run `serve`), so `abacus board`'s "o opens the inspector" key is **unverified** with a
compiled binary.

Compile and checks [ran]:

| Target | Compiled | Size | Run-tested |
| --- | --- | --- | --- |
| linux-x64 | yes (`bun build --compile --target=bun-linux-x64 dist/cli.mjs`, 321 modules, 0.2 s) | 111,225,008 B | yes |
| linux-arm64 | yes (cross, Bun downloaded its arm64 runtime) | 111,332,344 B | no (no emulation) |
| darwin-arm64 | yes (cross) | 91,273,442 B | no |
| darwin-x64 | yes (cross) | 100,265,440 B | no |

- linux-x64 binary, in an isolated HOME/XDG, inside `unshare -rn` (no network namespace):
  `--version` prints `0.54.0`; `--help` is **byte-identical to
  `internal/board/testdata/help.txt`**, so abacus's identity probe would accept it unchanged.
- Startup `--version`: **about 86 ms** compiled (5 runs, 85 to 88 ms), versus about 216 ms with
  `node dist/cli.mjs` and about 110 ms with `bun dist/cli.mjs`. Faster than the status quo.
- Compressed: gzip -9 52.1 MB, zstd -19 41.8 MB, xz 39.9 MB. The archives are `tar.gz`
  (`scripts/package-release.sh`), so expect about 52 MB added per linux archive, taking
  linux-amd64 from about 77 MB to about 129 MB [inferred from gzip -9 on the one binary].
  Almost all of it is the runtime: the bare Bun binary is 107,308,936 B.
- Dynamically linked against glibc (`ldd`: libc, libm, libdl, libpthread), so Alpine/musl users
  would need a musl target (Bun documents one [inferred, not checked here]; **not tested**).
- **Not reproducible byte-for-byte [ran]:** two identical compiles of linux-x64 gave different
  sha256. The pin therefore has to be of the artifact we publish, not of a recipe's output.
- Windows: Bun supports `bun-windows-x64` [inferred, not tried]; abacus has no Windows support
  yet (`docs/design/2026-11-22-1900-plan-windows-support.md`).
- macOS [inferred]: an unsigned or ad-hoc-signed Mach-O from a cross-compile; first-run
  Gatekeeper behavior for a downloaded binary was not tested. The other four tools face the same
  question, so this is not new risk, but it is unverified.
- Alternatives not tried: **Node SEA** needs a single-file CJS bundle (esbuild, not installed)
  and embeds the full Node binary (about 100 MB+, similar size) with weaker support for the
  dynamic `import()` the CLI uses [inferred]; **pkg-like tools** are largely unmaintained
  [inferred]. Bun is the only measured path.
- **Licensing of the compiled output:**
  - `@vercel/board` and its tree [ran: read each `package.json`]: Apache-2.0 (`@vercel/board`,
    `@vercel/oidc`, `@vercel/cli-config`, `@vercel/cli-exec`, `human-signals`), MIT (`zod`,
    `jose`, `execa`, `cross-spawn`, `get-stream`, `is-stream`, `merge-stream`, `mimic-fn`,
    `npm-run-path`, `onetime`, `os-paths`, `path-key`, `shebang-command`, `shebang-regex`,
    `strip-final-newline`, `xdg-app-paths`, `xdg-portable`), ISC (`isexe`, `signal-exit`,
    `which`). **Nothing copyleft or non-permissive in the Board tree.** Obligations: ship the
    license texts in THIRD_PARTY_NOTICES (abacus already generates it from the manifest's
    `license_source`; a compiled bundle needs one entry for the Board tree plus its transitive
    licenses, which a build script can collect from `node_modules/*/LICENSE`).
  - **The runtime is the flag.** Bun is MIT but statically links JavaScriptCore/WebKit
    (LGPL-2) and other components incl. `tinycc` (LGPL-2.1) [read: bun.com/docs/project/licensing].
    Bun's page does not say how that applies to executables produced by `--compile`. Shipping
    it means complying with LGPL's relinkability/notice terms for the embedded runtime (provide
    notices and the means to obtain the runtime source; which Bun version is pinned). This is a
    real legal item for the owner, not a blocker, but I did not resolve it.

## 3. Trust model for a bundled copy

Today [read: `module.go`, `docs/tool-contract.md`]: a PATH binary is trusted only after its own
`--help` identifies Vercel Board and its version is at least 0.54.0, run with a probe
environment that has no token, team or board setting. That would **still work** unchanged for a
compiled copy (help is byte-identical, [ran]) and remains the right second check.

For a bundled copy, the pin is stronger than the help check:

1. **Pin and verify like the other four tools.** Add `board` to `bundled-tools.json` with
   per-platform `sha256`, but the asset cannot come from upstream (Vercel publishes to npm, not
   GitHub release binaries [read: package metadata; not checked on GitHub Releases]). Our own
   release must **host the compiled assets** (or a `board-<ver>-<os>-<arch>` file next to the
   abacus archives), and `update-bundled-tools.sh` needs a new source type, for example
   `{"from": "abacus-release"}`, with the sha256 written by the build recipe.
2. **Build recipe** (all done by hand here [ran], to be scripted if chosen): pin the npm version
   and the lockfile integrity (0.54.0 was `sha512-JPsKFnPVgSQpZGxtj/8TrPbHnJzebIMXXdzDeAZkfkXq9B0d0L3diAq5TByWtvP+4nDgrOqCHQxnzxdvsFNJ2A==`
   per `testdata/README.md`), `npm install --ignore-scripts --save-exact` into a temp prefix,
   `npm audit signatures`-style provenance check if available [inferred], `bun build --compile
   --target=<t> dist/cli.mjs` per target with a pinned Bun version, record sha256 of each output.
   Because outputs are not byte-reproducible, a third party cannot verify the binary from the
   recipe; they verify the sha256 in the signed release's `SHA256SUMS`.
3. **Keep the `--help` identity check** as defense in depth (it also guards `ABACUS_BOARD_BIN`).
4. **Upstream tracking:** a bump means re-running the recipe, re-capturing fixtures (the
   fixtures were captured with a local `board serve`, which is allowed to a human maintainer,
   not run in this assessment), and re-pinning. Decide whether abacus auto-updates the pin or
   only on explicit maintainer action (recommended: explicit).

## 4. Recommendation matrix

| | (a) Optional npm install, better wording | (b) Bundle compiled binary | (c) Native Go read client | (d) Hybrid |
| --- | --- | --- | --- | --- |
| Effort | tiny: change text in Doctor, the Board tab empty state and docs | medium: build script, new `bundled-tools.json` source type, release hosting, notices, per-platform smoke tests | medium-large: client, error mapping, token handoff, tests; writes still on CLI | (a) now, then (c) read path, then optionally (b) |
| Risk | none | LGPL runtime notice; macOS/arm64 runs untested; glibc only; non-reproducible build | auth and API drift in a pre-1.0 upstream; two code paths to keep equal | spread over steps, each reversible |
| User experience | still asks for Node + npm (violates the owner rule, but honest) | zero install; startup 86 ms; archive grows 77 to about 129 MB per linux archive | no install for the token-handed or local cases; the usual `vercel login` user still needs the CLI | best path per case |
| Maintenance | none | re-run recipe on each upstream release, re-pin, re-capture fixtures | track API changes, regenerate from `openapi.json`, version-check | highest total, lowest per step |
| License | none new | Apache-2.0 plus MIT/ISC notices for the tree; LGPL-2 notice for the Bun runtime | Apache-2.0 fine for a clean-room client; credit already in README | union of the above for whatever ships |
| Owner decides | whether Node-required is acceptable meanwhile | whether about +50 MB per archive is OK; LGPL runtime comfort; who hosts assets | whether read-only without the CLI is worth it; accept `BOARD_TOKEN` as the supported credential | the order and whether (b) is ever done |

**Recommendation: (d), starting with (a).** Reasons: the owner rule says bundle, and (b) is the
only option that fully satisfies it today (it works, [ran]); its price is size and an LGPL
runtime notice, which are owner decisions, not engineering ones. (c) does not satisfy the rule
for the normal `vercel login` user and leaves writes on Node. So: improve wording now, ask the
owner two questions (size budget, LGPL comfort), and if the answer is yes, ship (b); (c) becomes
a later optimization only if the archive size is rejected.

**First concrete small step (docs/copy only, no new risk):** change the not-connected text from
"npm i -g @vercel/board" to name what the Board needs and that abacus does not need Node for
anything else, and record the two owner decisions below in the handoff. If the owner says yes to
(b), the next step is a `scripts/` build recipe for the compiled binary plus a measured smoke
test (`--version`, `--help` equals the identity fixture), not wired into `package-release.sh`
until the hosting question is settled.

## Owner decisions needed

1. Is +50 MB gzipped per platform archive acceptable (77 to about 129 MB for linux-amd64)?
2. Is shipping a Bun-compiled binary (LGPL-2 JavaScriptCore inside) acceptable, with notices?
3. Is a hosted asset in abacus's own release acceptable as the source for a pinned tool (no
   upstream binary exists)?
4. Should `BOARD_TOKEN`-only read access (no CLI) be a supported mode, or must `vercel login`
   users work without Node?

## What was not verified

- The compiled binary against any real or local board (no `serve`, no network); `inspect`/`serve`
  under Bun were not run.
- Run-tests of the linux-arm64, darwin-arm64, darwin-x64 outputs.
- Alpine/musl, Windows, Node SEA, pkg-like tools.
- The legal reading of LGPL for `bun build --compile` output; Bun's page is silent on it.
- Whether the HTTP route to argv mapping in section 1 matches the CLI byte-for-byte: it is read
  from the docs and source, and confirmed only against the fixtures' shapes.
