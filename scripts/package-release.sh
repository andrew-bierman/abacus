#!/usr/bin/env bash
# Build the release archives: abacus plus the pinned sidecar tools, one per platform.
#
#   scripts/package-release.sh VERSION [--platforms "darwin-arm64 linux-amd64"] [--out DIR]
#
# Each archive is laid out as
#   abacus_VERSION_OS_ARCH/{abacus, bundled/{bd,bv,lambe,beads-lease}, third_party/<tool>/LICENSE,
#                           THIRD_PARTY_NOTICES.md, LICENSE, README.md}
# Every tool comes from bundled-tools.json (fields are documented in scripts/lib/bundle.sh):
# downloaded with gh, its sha256 checked against the pin (the build fails closed on any
# mismatch), and only the one executable extracted. Archives are built in a work directory and
# moved into DIR only after every platform succeeded, so DIR never holds a partial release.
# Nothing is published, tagged or uploaded; the maintainer does that with the files left in DIR.
#
# Environment: ABACUS_BUNDLE_CACHE (downloads, default .cache/bundle), ABACUS_BUNDLE_MANIFEST
# (manifest path, default bundled-tools.json) and, for tests, ABACUS_PACKAGE_PREBUILT_BIN (an
# existing binary to use instead of running go build).
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck disable=SC2034  # read by scripts/lib/bundle.sh
BUNDLE_SCRIPT=package-release
manifest="${ABACUS_BUNDLE_MANIFEST:-$root/bundled-tools.json}"
cache="${ABACUS_BUNDLE_CACHE:-$root/.cache/bundle}"
out="$root/dist"
platforms="darwin-amd64 darwin-arm64 linux-amd64 linux-arm64"
# shellcheck source=lib/bundle.sh
source "$root/scripts/lib/bundle.sh"

[ $# -ge 1 ] || die "usage: $0 VERSION [--platforms \"darwin-arm64 ...\"] [--out DIR]"
version="${1#v}"; shift
while [ $# -gt 0 ]; do
  case "$1" in
    --platforms) platforms="${2:?}"; shift 2 ;;
    --out) out="${2:?}"; shift 2 ;;
    *) die "unknown argument $1" ;;
  esac
done
[[ "$version" =~ ^[0-9A-Za-z][0-9A-Za-z._-]*$ ]] || die "bad version '$version'"
need jq
[ -n "${ABACUS_PACKAGE_PREBUILT_BIN:-}" ] || need go

mkdir -p "$out"
rm -f "$out/SHA256SUMS" # a stale checksum file must never describe a different build
work="$(mktemp -d "${TMPDIR:-/tmp}/abacus-package.XXXXXX")"
trap 'rm -rf "$work"' EXIT
mkdir "$work/out"
tools=()
while IFS= read -r t; do tools+=("$t"); done < <(jq -r '.tools[].name' "$manifest")
check_tool_names "${tools[@]}"

for p in $platforms; do
  os="${p%-*}"; arch="${p#*-}"
  name="abacus_${version}_${os}_${arch}"
  stage="$work/$name"
  mkdir -p "$stage/bundled" "$stage/third_party"
  echo "== $name"

  if [ -n "${ABACUS_PACKAGE_PREBUILT_BIN:-}" ]; then
    cp "$ABACUS_PACKAGE_PREBUILT_BIN" "$stage/abacus"
  else
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -C "$root" -trimpath \
      -ldflags "-s -w -X main.version=$version" -o "$stage/abacus" ./cmd/abacus
  fi

  notices="$stage/THIRD_PARTY_NOTICES.md"
  notices_header >"$notices"
  for tool in "${tools[@]}"; do
    asset_path="$(fetch "$tool" "$p")"
    install_tool "$stage/bundled" "$stage/third_party" "$tool" "$p" "$asset_path"
    notices_entry "$tool" "$p" "$asset_path" >>"$notices"
  done
  notices_footer >>"$notices"
  # Every manifest tool must have landed as a non-empty executable; a release never ships a gap.
  for tool in "${tools[@]}"; do
    f="$stage/bundled/$tool"
    [ -f "$f" ] && [ ! -L "$f" ] && [ -s "$f" ] && [ -x "$f" ] || die "$tool is missing from bundled/ in $name"
  done

  cp "$root/LICENSE" "$root/README.md" "$stage/"
  cp "$manifest" "$stage/third_party/bundled-tools.json"
  chmod 0644 "$stage/LICENSE" "$stage/README.md" "$notices" "$stage/third_party/bundled-tools.json"
  chmod 0755 "$stage/abacus"
  find "$stage" -type d -exec chmod 0755 {} +

  tar -czf "$work/out/$name.tar.gz" -C "$work" "$name"
  rm -rf "$stage"
done

( cd "$work/out" && for p in $platforms; do f="abacus_${version}_${p%-*}_${p#*-}.tar.gz"; echo "$(sha "$f")  $f"; done >SHA256SUMS )
# Every platform succeeded: only now does anything reach the output directory.
for f in "$work"/out/*; do mv -f "$f" "$out/"; done
for p in $platforms; do f="abacus_${version}_${p%-*}_${p#*-}.tar.gz"; echo "   $(du -h "$out/$f" | cut -f1)  $out/$f"; done
echo "wrote $out/SHA256SUMS"
