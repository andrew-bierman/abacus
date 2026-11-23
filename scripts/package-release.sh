#!/usr/bin/env bash
# Build the release archives: one self-contained abacus binary per platform, tools inside.
#
#   scripts/package-release.sh VERSION [--platforms "darwin-arm64 linux-amd64"] [--out DIR]
#
# For each platform it runs scripts/embed-tools.sh (fetch and verify bd, lambe and beads-lease, build
# the Board CLI from its pinned npm package, compress them, write the notices) and then builds abacus
# with -tags embedtools so the tools are inside the binary. Each archive is laid out as
#   abacus_VERSION_OS_ARCH/{abacus, LICENSE, THIRD_PARTY_NOTICES.md, third_party/<tool>/LICENSE,
#                           third_party/bv/LICENSE}
# and nothing else: no sidecar folder, no scripts. The notices and every license text are also inside
# the binary (`abacus licenses`). bv is linked into abacus, not run: its license ships verbatim from
# the pinned Go module (manifest "linked_licenses"; sha256 pinned, the build fails closed on any
# mismatch). Archives are built in a work directory and moved into DIR only after every platform
# succeeded, so DIR never holds a partial release. DIR also gets EMBEDDED_TOOLS.md: the sha256 of every
# executable built in, per platform, for the release notes (the Board CLI is compiled by bun, whose
# output need not be reproducible, so its hash is recorded rather than pinned). Nothing is published,
# tagged or uploaded; the maintainer does that with the files left in DIR.
#
# Environment: ABACUS_BUNDLE_CACHE (downloads and Board builds, default .cache/bundle),
# ABACUS_BUNDLE_MANIFEST (manifest path, default bundled-tools.json), ABACUS_EMBED_BLOBS_DIR (where the
# generated blobs go, default internal/embedtools/blobs) and, for tests, ABACUS_PACKAGE_PREBUILT_BIN
# (an existing binary to use instead of running go build).
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
need go

mkdir -p "$out"
rm -f "$out/SHA256SUMS" "$out/EMBEDDED_TOOLS.md" # stale files must never describe a different build
work="$(mktemp -d "${TMPDIR:-/tmp}/abacus-package.XXXXXX")"
trap 'rm -rf "$work"' EXIT
mkdir "$work/out"
tools=()
while IFS= read -r t; do tools+=("$t"); done < <(jq -r '.tools[].name' "$manifest")
check_tool_names "${tools[@]}"
blobs_base="${ABACUS_EMBED_BLOBS_DIR:-$root/internal/embedtools/blobs}"
# go:embed only reads the package's own blobs/ directory, so a real build cannot use another one.
[ -z "${ABACUS_EMBED_BLOBS_DIR:-}" ] || [ -n "${ABACUS_PACKAGE_PREBUILT_BIN:-}" ] \
  || die "ABACUS_EMBED_BLOBS_DIR is only for tests with ABACUS_PACKAGE_PREBUILT_BIN"
{
  echo "# Embedded tools, abacus $version"
  echo
  echo "sha256 of each executable built into the binary. bd, lambe and beads-lease are the upstream"
  echo "release files (pinned in bundled-tools.json). The Board CLI is compiled by bun from the npm"
  echo "package at the pinned integrity; its output may differ between builds, so record this hash."
  echo
  echo "| platform | tool | version | sha256 of the executable | compressed in the binary |"
  echo "| --- | --- | --- | --- | --- |"
} >"$work/EMBEDDED_TOOLS.md"

for p in $platforms; do
  os="${p%-*}"; arch="${p#*-}"
  name="abacus_${version}_${os}_${arch}"
  stage="$work/$name"
  blobs="$blobs_base/$p"
  mkdir -p "$stage"
  echo "== $name"

  bash "$root/scripts/embed-tools.sh" --platform "$p" --out "$work/tools-$p" --blobs "$blobs" \
    || die "embed-tools.sh failed for $p"
  # Every manifest tool must be in the blob set as a non-empty compressed file; a release never ships a gap.
  for tool in "${tools[@]}"; do
    [ -s "$blobs/$tool.zst" ] || die "$tool is missing from the embedded set for $p"
  done

  if [ -n "${ABACUS_PACKAGE_PREBUILT_BIN:-}" ]; then
    cp "$ABACUS_PACKAGE_PREBUILT_BIN" "$stage/abacus"
  else
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -C "$root" -trimpath -tags embedtools \
      -ldflags "-s -w -X main.version=$version" -o "$stage/abacus" ./cmd/abacus
  fi

  # Notices and licenses are the very files embedded in the binary.
  cp "$blobs/THIRD_PARTY_NOTICES.md" "$stage/THIRD_PARTY_NOTICES.md"
  mkdir -p "$stage/third_party"
  if [ -d "$blobs/licenses" ]; then
    for d in "$blobs"/licenses/*/; do
      [ -d "$d" ] || continue
      mkdir -p "$stage/third_party/$(basename "$d")"
      cp "$d"* "$stage/third_party/$(basename "$d")/"
    done
  fi
  for tool in "${tools[@]}"; do
    [ -s "$stage/third_party/$tool/LICENSE" ] || die "no license text for $tool in $name"
  done
  [ -s "$stage/third_party/bv/LICENSE" ] || die "no license text for bv in $name"

  while IFS= read -r tool; do
    read -r _ _ exe_sha <"$work/tools-$p/.pins/$tool"
    echo "| $p | $tool | $(tool_field "$tool" .version) | \`$exe_sha\` | $(wc -c <"$blobs/$tool.zst" | tr -d ' ') bytes |" >>"$work/EMBEDDED_TOOLS.md"
  done < <(printf '%s\n' "${tools[@]}")

  cp "$root/LICENSE" "$stage/"
  chmod 0644 "$stage/LICENSE" "$stage/THIRD_PARTY_NOTICES.md"
  chmod 0755 "$stage/abacus"
  find "$stage/third_party" -type f -exec chmod 0644 {} +
  find "$stage" -type d -exec chmod 0755 {} +
  echo "   abacus binary: $(wc -c <"$stage/abacus" | tr -d ' ') bytes"

  tar -czf "$work/out/$name.tar.gz" -C "$work" "$name"
  rm -rf "$stage" "$work/tools-$p"
done

( cd "$work/out" && for p in $platforms; do f="abacus_${version}_${p%-*}_${p#*-}.tar.gz"; echo "$(sha "$f")  $f"; done >SHA256SUMS )
cp "$work/EMBEDDED_TOOLS.md" "$work/out/"
# Every platform succeeded: only now does anything reach the output directory.
for f in "$work"/out/*; do mv -f "$f" "$out/"; done
for p in $platforms; do f="abacus_${version}_${p%-*}_${p#*-}.tar.gz"; echo "   $(du -h "$out/$f" | cut -f1)  $out/$f"; done
echo "wrote $out/SHA256SUMS and $out/EMBEDDED_TOOLS.md"
