#!/usr/bin/env bash
# The tagged build is part of what ships, so it is checked too: go vet and a cross build with
# -tags embedtools for every platform that has a generated blob folder, and the tagged tests of the
# packages that own the built-in tools. The tag needs internal/embedtools/blobs/<os>-<arch>/ (made by
# scripts/embed-tools.sh --blobs, which `make build` runs); with none present this prints why and
# exits 0, so a checkout that has never built the tools (hosted CI) is not failed for it.
#
#   scripts/check-embedtools.sh
#
# Run it with an isolated HOME and XDG_* (the tests do not touch a real one, but be safe).
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
blobs="$root/internal/embedtools/blobs"

found=()
for p in darwin-amd64 darwin-arm64 linux-amd64 linux-arm64; do
  [ -s "$blobs/$p/manifest.json" ] && found+=("$p")
done
if [ "${#found[@]}" = 0 ]; then
  echo "check-embedtools: skipped: no blobs in internal/embedtools/blobs (run make build, or scripts/embed-tools.sh --platform OS-ARCH --blobs internal/embedtools/blobs/OS-ARCH)"
  exit 0
fi

host="$(go env GOOS)-$(go env GOARCH)"
if [ -s "$blobs/$host/manifest.json" ]; then
  echo "check-embedtools: go vet -tags embedtools (host $host)"
  go -C "$root" vet -tags embedtools ./...
  echo "check-embedtools: go test -tags embedtools (core, embedtools, board)"
  go -C "$root" test -count=1 -tags embedtools ./internal/core/... ./internal/embedtools/... ./internal/board/...
else
  echo "check-embedtools: no blobs for this machine ($host): vet and tests skipped"
fi
for p in "${found[@]}"; do
  echo "check-embedtools: build -tags embedtools for $p"
  CGO_ENABLED=0 GOOS="${p%-*}" GOARCH="${p#*-}" go -C "$root" build -tags embedtools -o /dev/null ./cmd/abacus
done
echo "check-embedtools: ok (${found[*]})"
