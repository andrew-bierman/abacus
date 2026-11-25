# Development

Start with the [isolated test commands in Contributing](../CONTRIBUTING.md#test-isolation-rules). Use them for focused tests and builds too. Tests use fake Lambe, HTTP, Rex, and sidecar providers. The Beads integration tests use `bd` against a temporary project and skip if it is unavailable.

- The Agents domain reads the process list and the session folders of Claude Code and Codex. Tests never do:
  `app.Env.Agents` is nil unless `DefaultEnv` sets it, every source is injected, and the `internal/agents`
  tests make reaching for the real ones panic.
- Building needs Go 1.26.11 or newer (`go.mod` names it, because the in-process graph
  engine links bv's analysis package). Go's default `GOTOOLCHAIN=auto` downloads that
  toolchain for you the first time you build (it needs network access); offline, or with
  `GOTOOLCHAIN=local`, install it first: `mise use go@1.26.11`. CI and
  the release workflow read the version from `go.mod`.

## Contract goldens and schemas

`go test ./internal/cli -update-golden` rewrites the `--json` goldens (`internal/cli/testdata`, the
`contract/` folder); `go test ./internal/core -update-schema` rewrites `docs/schema/v1` from the Go
types. Review the diff either way: a key may be added, never renamed or removed, within
`schema_version` 1. See `docs/tool-contract.md`, "Schemas and fixtures for clients".

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
