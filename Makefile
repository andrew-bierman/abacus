.PHONY: build build-dev release check-tools test check

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
