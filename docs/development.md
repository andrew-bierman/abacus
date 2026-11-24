# Development

- Develop and test against an isolated `HOME`, `XDG_*` dirs and `bd` database. Never touch a
  running Lambe daemon, its state, or real Beads data.
- Nothing here launches an app or takes window focus. Tests use fakes for Lambe, HTTP and Rex; the only real
  tool tests run is `bd`, against temp projects with an isolated `HOME`, and they skip when absent.
  Sidecar tests use fake binaries in temp dirs.
- Use a private `GOCACHE` for builds.
- Building needs Go 1.26.11 or newer (`go.mod` names it, because the in-process graph
  engine links bv's analysis package). Go's default `GOTOOLCHAIN=auto` downloads that
  toolchain for you the first time you build (it needs network access); offline, or with
  `GOTOOLCHAIN=local`, install it first: `mise use go@1.26.11`. CI and
  the release workflow read the version from `go.mod`.

## Building from source

```
make build-dev               # go build -o bin/abacus ./cmd/abacus: quick, no tools built in
make build                   # bin/abacus with every tool inside (needs gh, npm, bun)
./bin/abacus doctor          # leads with: Built-in tools: 4 of 4 ready
```

`make build` fetches and verifies the pinned tools into `.cache/` and `internal/embedtools/blobs/`
(both git-ignored), so `make build-dev` and `go test ./...` need none of that.

The tagged build is checked too: `make check` runs gofmt, vet, shellcheck and the tests, then
`scripts/check-embedtools.sh`, which (when `internal/embedtools/blobs/<os>-<arch>/` exists, that is
after `make build`) runs `go vet -tags embedtools ./...`, builds `-tags embedtools` for every platform
that has blobs, and runs `go test -tags embedtools` for the core, embedtools and board packages.
Without blobs it says so and skips (hosted CI does this). The unit tests describe a development build
(each package's `TestMain` sets the tool set to none) and use fake tool sets, so they pass with or
without the tag. `ABACUS_REAL_SIZE=1 go test ./internal/core -run RealSize -v` runs a 100 MB blob
through the real extraction path and logs its timings.

Note: the two `signalZero` bodies under `internal/lambe` are kept separate on purpose; they port
Lambe's own process-liveness logic per platform, and merging them would drift from it.
