.PHONY: build bundle dev check-bundle

# A locally built binary has no bundled/ beside it; `make bundle` fetches the pinned, sha256
# verified tools into bin/bundled (see scripts/dev-bundle.sh) so it behaves like a release.
build:
	go build -o bin/abacus ./cmd/abacus

bundle:
	scripts/dev-bundle.sh

check-bundle:
	scripts/dev-bundle.sh --check

dev: build bundle
