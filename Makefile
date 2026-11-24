.PHONY: help build build-dev release check-tools test check cover

# help: list the targets (each target's comment above it is the description).
help:
	@echo "abacus make targets:"
	@echo "  build        one self-contained binary with every tool inside (needs gh, npm, bun)"
	@echo "  build-dev    quick go build with no tools built in (for working on abacus)"
	@echo "  test         go test ./... (use an isolated HOME and XDG_*; see CONTRIBUTING.md)"
	@echo "  check        what CI runs: gofmt, vet, shellcheck, tests, tagged-build check"
	@echo "  cover        per-package test coverage summary"
	@echo "  check-tools  verify the fetched tools still match their pins"
	@echo "  release      the platform archives in dist/ (make release VERSION=x.y.z)"

# The platform this machine builds for, as os-arch (the names the scripts and the blob folders use).
PLATFORM := $(shell go env GOOS)-$(shell go env GOARCH)

# build: ONE self-contained binary with every tool inside it. It fetches and verifies the pinned
# tools (bd, lambe, beads-lease from their releases, the Board CLI built from its npm package),
# compresses them into internal/embedtools/blobs/$(PLATFORM) (gitignored, never committed), and builds
# with -tags embedtools. Needs gh (a login that can read andrew-bierman/beads-lease), npm and bun.
build:
	scripts/embed-tools.sh --platform $(PLATFORM) --blobs internal/embedtools/blobs/$(PLATFORM)
	go build -tags embedtools -o bin/abacus ./cmd/abacus

# build-dev: a plain go build, quick, with no tools built in. Doctor says so; it is for working on
# abacus itself.
build-dev:
	go build -o bin/abacus ./cmd/abacus

# release: the four platform archives and SHA256SUMS in dist/. make release VERSION=0.1.0
release:
	@test -n "$(VERSION)" || { echo "usage: make release VERSION=x.y.z"; exit 2; }
	scripts/package-release.sh $(VERSION)

# check-tools: verify, without changing anything, that the fetched tools still match their pins.
check-tools:
	scripts/embed-tools.sh --check

test:
	go test ./...

# check: what CI runs, plus the tagged build when blobs exist (it says so and skips when they do not).
check:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	go vet ./...
	shellcheck -x -P SCRIPTDIR scripts/*.sh scripts/lib/*.sh
	go test ./...
	scripts/check-embedtools.sh

# cover: statement coverage per package, in an isolated HOME and XDG_*; the profile is cover.out
# (git-ignored).
cover:
	@t=$$(mktemp -d); trap 'rm -rf "$$t"' EXIT; \
	  HOME=$$t/home XDG_CONFIG_HOME=$$t/config XDG_STATE_HOME=$$t/state XDG_DATA_HOME=$$t/data \
	  go test -count=1 -coverprofile=cover.out ./... | grep -v 'no test files'
