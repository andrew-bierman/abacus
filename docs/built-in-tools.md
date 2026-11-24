# Built-in tools

abacus is a single binary. The tools it composes are inside it, pinned and sha256-verified at
build time, compressed with zstd, and unpacked on first use. Nothing is fetched at run time and
nothing is installed.

| Tool | Version | License | Built in as |
| --- | --- | --- | --- |
| bd, [Beads](https://github.com/steveyegge/beads) | 1.3.4 | MIT | the upstream release executable |
| [Lambe](https://github.com/andrew-bierman/lambe) | 0.10.1 | GPL-3.0 | the upstream release executable |
| [beads-lease](https://github.com/andrew-bierman/beads-lease) | 0.1.0 | MIT | the release executable |
| Vercel Board CLI, [`@vercel/board`](https://github.com/vercel/board) | 0.54.0 | Apache-2.0 | compiled with bun from the npm package at a pinned integrity |

Linked into the binary as a Go library, not run as a program:

| Library | Version | License |
| --- | --- | --- |
| bv, [beads_viewer](https://github.com/Dicklesworthstone/beads_viewer) (`pkg/analysis` and dependencies) | 0.27.0 | MIT (with an additional AI-provider clause); license text shipped unmodified |

The tools stay separate programs run as subprocesses, unmodified; abacus imports none of their
code. bv is different: the `abacus` binary statically links parts of its Go packages for the graph
insights, so its license is copied verbatim from the pinned Go module (the `linked_licenses` entry
in `bundled-tools.json`, sha256 pinned, the build fails closed on a mismatch). The Board CLI
executable also contains the bun runtime (MIT, with LGPL-2 JavaScriptCore) and its npm dependency
tree; their license texts ship too. `THIRD_PARTY_NOTICES.md`, every license text and the license
of each linked Go module are inside the binary: run `abacus licenses`.

**Where a tool comes from**, first match wins, one resolver for all of them:

1. an explicit `ABACUS_BD_BIN`, `ABACUS_LAMBE_BIN`, `ABACUS_BEADSLEASE_BIN` or `ABACUS_BOARD_BIN`
   (a path); you chose it, so abacus runs it as is;
2. the built-in copy, extracted to `$ABACUS_TOOLS_DIR/<content hash>/<name>`, which defaults to
   `$XDG_DATA_HOME/abacus/tools` (`~/.local/share/abacus/tools`);
3. a development build only: a sidecar folder next to the executable (`bundled/`, `../libexec/abacus`),
   then the `PATH`.

A binary that has its tools built in never falls back to a `bd` (or any other built-in tool) of
another version, whatever is on `PATH`; if the built-in copy cannot be set up, the tool is
unavailable and Doctor says why.

**The private cache.** The folder is mode 0700 and owned by you, and so is every file in it (0700).
Extraction writes a temp file in the same folder, checks its length and sha256 against the manifest
inside the binary, syncs it and renames it into place; a lock file keeps two abacus processes from
racing (a start waits up to 30 seconds for another abacus that is preparing the tools, then says
which lock file to delete). Names come from a closed manifest (lowercase letters, digits and
dashes), so an extraction path cannot escape the folder. A link, a file or folder owned by someone
else, a folder others can write to, or a file with other permissions is refused, never used and never
"repaired" in place; so is a tools folder below a directory that others can write to (a sticky one
such as `/tmp` is fine) or that another user owns. A folder chosen with `ABACUS_TOOLS_DIR` must be
private to you: its ancestors may not be group- or world-writable either. The default folder under
your data directory is checked for the owner and world-write only.

Every start does stat checks (type, owner, mode, size); the sha256 is computed at extraction and
again whenever the size or modification time no longer matches a marker written beside the file,
because hashing a 144 MB file on every start would add about a quarter of a second to each one. The
path is checked again (one stat) right before each program is run, so a copy that was replaced,
deleted or loosened after it was verified is not run; a deleted one is unpacked again. Interrupted
extractions leave nothing (a Ctrl-C removes the partial file; the leftover of a killed process is
removed by the next extraction), a failed extraction is remembered for the rest of the run (a
refresh, which Doctor does, tries again), and a full disk is reported as a write failure with the
folder, not as a damaged download.

Older versions are removed only when all of these hold: the new version is complete; the old one is
not among the two most recently used others; it has not been used for 14 days (every start stamps the
folder it runs from); and nobody is preparing it or running from it (a non-blocking lock on each of
its two lock files, which a running abacus holds for its life). Lock files themselves are never
removed. The hashes come from the binary; no environment variable can replace them. The Board CLI
is the largest tool and the optional one, so it is unpacked the first time the Board is read (or
Doctor runs), not at start: `abacus tasks` never unpacks it. The very first start prints "Preparing
built-in tools (first run only)…" to stderr; it takes about 3 seconds in total for the roughly
250 MB of tools, about 0.2 s on later starts.

If the cache folder cannot be used, Doctor says what failed and the path; set `ABACUS_TOOLS_DIR` to a
folder you can write to (an absolute path), or, if the message says this copy of abacus is damaged,
download it again. If the system refuses to run the unpacked programs (a folder on a mount that
forbids running programs, or macOS blocking them), Doctor says so with the folder, and `ABACUS_TOOLS_DIR`
chooses one that allows it; on macOS the message also gives `xattr -dr com.apple.quarantine <folder>`.

**Threat model, honestly.** The cache is a private folder of one user, so the protection is against
other users, other processes of other users and a swapped folder, not against the same user: an
attacker who is already you can change abacus itself. Two consequences are accepted by design: a
file changed in place without changing its size and modification time is not noticed by a start
(only a full hash would, see above), and a program is started by path, not by an already-open file
handle, so a replacement between the one-stat check and the start is a same-user race. Setting
`ABACUS_BD_BIN`, `ABACUS_LAMBE_BIN`, `ABACUS_BEADSLEASE_BIN` or `ABACUS_BOARD_BIN` runs that file
**without any of these checks** (you chose it): it is unverified code, and abacus runs it with your
rights. The Board CLI's `--help` identity check still runs on an override, with a token-free
environment, before it is trusted.

**Doctor** shows each tool as `built in` with its version and leads with `Built-in tools: 4 of 4
ready`. It is Ready when bd, lambe and beads-lease are usable; the Board CLI is built in too but
never blocks Ready: the Board tab works once a board is configured (`abacus config set board_url
URL`, then `vercel login` or `BOARD_TOKEN`) and otherwise says how to point it at one.

**Development builds.** A plain `go build` has no tools built in (so it is quick to compile), and
Doctor says so in one line: "Development build: tools are not built in; build with make build".
It then looks for tools next to the executable and on the `PATH`, which is for working on abacus.
`make build` makes the single binary (below). Only Rex and b9s are not built in; they are
optional separate programs abacus can hand off to, and say so.

## How the binary is built

`bundled-tools.json` pins every tool. `scripts/embed-tools.sh` (run by `make build` and by the
release) fetches each release asset with `gh` (your login, so the private beads-lease repo works),
fails unless its sha256 equals the pin, extracts only the one executable, builds the Board CLI
(`npm ci --ignore-scripts` from the committed `scripts/board/package-lock.json`, whose sha256 and
the package's npm integrity are pinned; then `bun build --compile` for the platform), compresses
them with zstd into `internal/embedtools/blobs/<os>-<arch>/` (git-ignored: these are tens of
megabytes and are never committed) and writes the complete notices. `go build -tags embedtools`
embeds that folder. The Board executable is compiled, and bun's output need not be reproducible,
so it is pinned by its inputs and the sha256 of each compiled output is recorded for the release
notes (`EMBEDDED_TOOLS.md`, written next to the archives).

```
make build                    # bin/abacus, one file, tools inside (needs gh, npm, bun)
make build-dev                # plain go build: quick, no tools built in
make release VERSION=0.1.0    # dist/: four archives, SHA256SUMS, EMBEDDED_TOOLS.md
make check-tools              # verify the fetched tools still match their pins
```

`scripts/package-release.sh VERSION [--platforms ...] [--out DIR]` builds darwin and linux on
amd64 and arm64, each with its own pinned tools. It publishes, tags and uploads nothing. Windows
is out of scope for now (see `docs/design/2026-11-22-1900-plan-windows-support.md`).

**The manifest, field by field.** The list is at the top of `scripts/lib/bundle.sh`, which the
scripts share. Hand-edited: `schema`, `note`, and per tool `name` (the file name when extracted;
it must equal the tool names abacus looks for, a test checks it), `repo`, `license`, `tag_template`,
`asset_template`, `member`, `checksums_asset`, `license_source`; for the Board CLI `kind`,
`package`, `bun_version`, `entry`, `runtime_license` and the bun target of each platform. Written
only by `scripts/update-bundled-tools.sh`: `version`, `assets`, `license_sha256`, and for the Board
CLI `npm_integrity` and `lockfile_sha256`.

**Refreshing a pin:** `scripts/update-bundled-tools.sh TOOL VERSION` (or `--all`). It reads each
platform's sha256 from the tool's upstream checksum file, downloads the asset with your `gh` login
and fails unless the download matches; `--trust-first-download` pins the hash of one download when
upstream publishes none (say so in the PR). For `board` it re-resolves the npm package into the
committed lockfile and pins the integrity and license. The scripts need `jq` (plus `gh`, and `npm`
and `bun` for the Board CLI), and read `ABACUS_BUNDLE_CACHE` (downloads and Board builds, default
`.cache/bundle`) and `ABACUS_BUNDLE_MANIFEST`. Output directories are built next to themselves and
swapped in only when complete; archive members that are links, absolute or contain `..` are
refused. The scripts are tested offline (`internal/release`) with fake archives, a fake `gh`,
`npm` and `bun`.

**Size tradeoff.** The tools are the size: bd alone is 144 MB uncompressed and the Board CLI is
90 to 110 MB (the bun runtime is most of it). Measured for version 0.0.0-embed1 (a real build of
each platform); a build without the tools is about 10 MB:

| Platform | Tools compressed in the binary | `abacus` binary | Release archive (.tar.gz) |
| --- | --- | --- | --- |
| darwin-arm64 | 76.4 MB | 87.5 MB | 79.5 MB |
| darwin-amd64 | 84.3 MB | 95.6 MB | 87.2 MB |
| linux-arm64 | 86.3 MB | 96.6 MB | 88.6 MB |
| linux-amd64 | 91.5 MB | 102.6 MB | 94.3 MB |

On darwin-arm64 (the only platform run): the four tools are 254.8 MB extracted, 243 MB on disk. A
cold first run (extraction included) took 3.3 s for `abacus doctor` and showed the TUI's first
frame after 2.9 s; every later start is about 0.3 s. zstd was chosen over xz: xz (`-9e`) made the
two big tools about 17% smaller, but zstd unpacks the biggest tool in about 0.1 s and needs no
second compression library.
