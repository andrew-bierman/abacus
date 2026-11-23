#!/usr/bin/env bash
# Put abacus's own copies of the bundled tools (bd, bv, lambe, beads-lease) next to a locally built
# binary, so `go build -o bin/abacus ./cmd/abacus` behaves like a release archive.
#
#   scripts/dev-bundle.sh [--platform linux-amd64] [--out DIR] [--check]
#
# Downloads the assets pinned in bundled-tools.json for the current os/arch (or --platform) with
# gh, checks every sha256 against the pin (fail closed: any mismatch aborts and changes nothing),
# extracts only the one executable per tool, and writes the licenses and THIRD_PARTY_NOTICES.md
# beside them. The default output is bin/bundled, which abacus finds next to bin/abacus.
#
# Layout of DIR:
#   {bd,bv,lambe,beads-lease}  the executables
#   third_party/<tool>/LICENSE, THIRD_PARTY_NOTICES.md
#   .pins/<tool>               what was installed: platform, asset sha256, executable sha256
#
# Idempotent: a tool whose executable still matches its pin (and the manifest's current pin) is
# kept and not downloaded again. The new DIR is built in a temp directory next to it and moved
# into place only when every tool succeeded, so DIR is never left half written.
# --check downloads nothing and changes nothing: it exits non-zero unless every tool is verified.
#
# Environment: ABACUS_BUNDLE_CACHE (downloads, default .cache/bundle) and ABACUS_BUNDLE_MANIFEST
# (manifest path, default bundled-tools.json).
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck disable=SC2034  # read by scripts/lib/bundle.sh
BUNDLE_SCRIPT=dev-bundle
manifest="${ABACUS_BUNDLE_MANIFEST:-$root/bundled-tools.json}"
cache="${ABACUS_BUNDLE_CACHE:-$root/.cache/bundle}"
out="$root/bin/bundled"
platform=""
check=0
# shellcheck source=lib/bundle.sh
source "$root/scripts/lib/bundle.sh"

while [ $# -gt 0 ]; do
  case "$1" in
    --platform) platform="${2:?}"; shift 2 ;;
    --out) out="${2:?}"; shift 2 ;;
    --check) check=1; shift ;;
    -h|--help) sed -n '2,24p' "$0"; exit 0 ;;
    *) die "unknown argument $1 (usage: $0 [--platform OS-ARCH] [--out DIR] [--check])" ;;
  esac
done
need jq

if [ -z "$platform" ]; then
  case "$(uname -s)" in Linux) os=linux ;; Darwin) os=darwin ;; *) die "unsupported OS $(uname -s); pass --platform (Windows is not bundled)" ;; esac
  case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; arm64|aarch64) arch=arm64 ;; *) die "unsupported CPU $(uname -m); pass --platform" ;; esac
  platform="$os-$arch"
fi
[[ "$platform" =~ ^[a-z0-9]+-[a-z0-9]+$ ]] || die "bad platform '$platform' (want os-arch, for example linux-amd64)"

tools=()
while IFS= read -r t; do tools+=("$t"); done < <(jq -r '.tools[].name' "$manifest")
[ "${#tools[@]}" -gt 0 ] || die "the manifest lists no tools"

# verified TOOL DIR: succeeds when DIR/TOOL is the executable installed from the manifest's
# current pin for $platform; otherwise prints why on stdout and fails.
verified() {
  local tool="$1" dir="$2" asset want rec
  asset="$(tool_field "$tool" ".assets[\"$platform\"].asset")"; want="$(tool_field "$tool" ".assets[\"$platform\"].sha256")"
  if [ -z "$asset" ] || [ "$asset" = null ] || [ -z "$want" ] || [ "$want" = null ]; then
    echo "no pin for $platform in the manifest"; return 1
  fi
  if [ -L "$dir/$tool" ] || [ ! -f "$dir/$tool" ]; then echo "not installed"; return 1; fi
  if [ ! -x "$dir/$tool" ]; then echo "not executable"; return 1; fi
  [ -f "$dir/.pins/$tool" ] && [ -f "$dir/third_party/$tool/LICENSE" ] || { echo "no install record"; return 1; }
  rec="$want $platform $(sha "$dir/$tool")"
  [ "$(cat "$dir/.pins/$tool")" = "$rec" ] || { echo "does not match the pin (changed, or a different version or platform)"; return 1; }
}

if [ "$check" = 1 ]; then
  bad=0
  for tool in "${tools[@]}"; do
    if why="$(verified "$tool" "$out")"; then echo "ok       $tool $(tool_field "$tool" .version) ($platform)"
    else echo "PROBLEM  $tool: $why" >&2; bad=1; fi
  done
  [ "$bad" = 0 ] || die "bundled tools in $out are not verified; run scripts/dev-bundle.sh"
  echo "Bundled tools: ${#tools[@]} of ${#tools[@]} verified in $out"
  exit 0
fi

mkdir -p "$(dirname "$out")"
if [ -f "$out/THIRD_PARTY_NOTICES.md" ]; then
  pending=0
  for tool in "${tools[@]}"; do verified "$tool" "$out" >/dev/null || pending=1; done
  if [ "$pending" = 0 ]; then
    echo "Bundled tools: ${#tools[@]} of ${#tools[@]} already verified in $out (nothing to do)"
    exit 0
  fi
fi

stage="$(mktemp -d "$(dirname "$out")/.bundled.XXXXXX")"
old=""
cleanup() {
  rm -rf "$stage"
  # A swap that stopped half way puts the previous directory back.
  if [ -n "$old" ] && [ -d "$old" ]; then
    if [ -e "$out" ]; then rm -rf "$old"; else mv "$old" "$out"; fi
  fi
}
trap cleanup EXIT
mkdir "$stage/third_party" "$stage/.pins"
notices_header >"$stage/THIRD_PARTY_NOTICES.md"

for tool in "${tools[@]}"; do
  if verified "$tool" "$out" >/dev/null 2>&1; then
    cp -p "$out/$tool" "$stage/$tool"
    mkdir -p "$stage/third_party/$tool"
    cp -p "$out/third_party/$tool/LICENSE" "$stage/third_party/$tool/LICENSE"
    cp -p "$out/.pins/$tool" "$stage/.pins/$tool"
    asset_path="$(tool_field "$tool" ".assets[\"$platform\"].asset")"
    echo "kept     $tool (verified)"
  else
    asset_path="$(fetch "$tool" "$platform")"
    install_tool "$stage" "$stage/third_party" "$tool" "$platform" "$asset_path"
    echo "$(tool_field "$tool" ".assets[\"$platform\"].sha256") $platform $(sha "$stage/$tool")" >"$stage/.pins/$tool"
    echo "fetched  $tool $(tool_field "$tool" .version) ($asset_path)"
  fi
  notices_entry "$tool" "$platform" "$asset_path" >>"$stage/THIRD_PARTY_NOTICES.md"
done
notices_footer >>"$stage/THIRD_PARTY_NOTICES.md"

chmod 0644 "$stage/THIRD_PARTY_NOTICES.md" "$stage"/.pins/*
find "$stage" -type d -exec chmod 0755 {} +

# Every tool is verified in the stage: only now does anything reach the output directory.
if [ -e "$out" ]; then old="$stage.old"; mv "$out" "$old"; fi
mv "$stage" "$out"
rm -rf "$old"
old=""
echo "Bundled tools: ${#tools[@]} of ${#tools[@]} verified in $out"
