#!/usr/bin/env bash
# Put abacus's own copies of the bundled tools (bd, lambe, beads-lease) next to a locally built
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
#   {bd,lambe,beads-lease}     the executables
#   third_party/<tool>/LICENSE, THIRD_PARTY_NOTICES.md
#   .pins/<tool>               what was installed: platform, asset sha256, executable sha256
#
# Idempotent: a tool whose executable still matches its pin (and the manifest's current pin) is
# kept and not downloaded again. Tools the manifest no longer lists are dropped by the rebuild.
# --out must not exist, be empty, or be a previous install of this script (it holds
# THIRD_PARTY_NOTICES.md and .pins/); any other directory is refused untouched, because the swap
# replaces the whole directory. A run killed with SIGKILL can leave a .bundled.* stage directory
# next to --out; that is accepted (it holds only downloads of verified files) and safe to delete. The new DIR is built in a temp directory next to it and moved
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
    -h|--help) sed -n '2,29p' "$0"; exit 0 ;;
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
check_tool_names "${tools[@]}"

# writable_by_others PATH: true when group or other may write to PATH (the same refusal abacus
# itself applies to a bundled tool, so a copy that passes here is accepted at run time).
writable_by_others() {
  local perms
  perms="$(ls -ld "$1" | cut -c1-10)"
  [ "${perms:5:1}" = w ] || [ "${perms:8:1}" = w ]
}

# verified TOOL DIR: succeeds when DIR/TOOL is the executable installed from the manifest's
# current pin for $platform, its license still matches license_sha256 and the modes are ones abacus
# accepts; otherwise prints why on stdout and fails.
verified() {
  local tool="$1" dir="$2" asset want rec lic
  asset="$(tool_field "$tool" ".assets[\"$platform\"].asset")"; want="$(tool_field "$tool" ".assets[\"$platform\"].sha256")"
  if [ -z "$asset" ] || [ "$asset" = null ] || [ -z "$want" ] || [ "$want" = null ]; then
    echo "no pin for $platform in the manifest"; return 1
  fi
  if [ -L "$dir/$tool" ] || [ ! -f "$dir/$tool" ]; then echo "not installed"; return 1; fi
  if [ ! -x "$dir/$tool" ]; then echo "not executable"; return 1; fi
  if writable_by_others "$dir" || writable_by_others "$dir/$tool"; then echo "writable by group or others (abacus would refuse it)"; return 1; fi
  lic="$dir/third_party/$tool/LICENSE"
  [ -f "$dir/.pins/$tool" ] && [ -f "$lic" ] && [ ! -L "$lic" ] || { echo "no install record"; return 1; }
  rec="$want $platform $(sha "$dir/$tool")"
  [ "$(cat "$dir/.pins/$tool")" = "$rec" ] || { echo "does not match the pin (changed, or a different version or platform)"; return 1; }
  [ "$(sha "$lic")" = "$(tool_field "$tool" .license_sha256)" ] || { echo "license does not match license_sha256 in the manifest"; return 1; }
}

# stale_tools DIR: install records for tools the manifest no longer lists, one per line.
stale_tools() {
  local f n
  for f in "$1"/.pins/*; do
    [ -e "$f" ] || continue
    n="$(basename "$f")"
    contains_tool "$n" || echo "$n"
  done
}
contains_tool() { local t; for t in "${tools[@]}"; do [ "$t" = "$1" ] && return 0; done; return 1; }

if [ "$check" = 1 ]; then
  bad=0
  for tool in "${tools[@]}"; do
    if why="$(verified "$tool" "$out")"; then echo "ok       $tool $(tool_field "$tool" .version) ($platform)"
    else echo "PROBLEM  $tool: $why" >&2; bad=1; fi
  done
  while IFS= read -r n; do [ -n "$n" ] || continue; echo "PROBLEM  $n: installed, but no longer in the manifest" >&2; bad=1; done < <(stale_tools "$out")
  [ "$bad" = 0 ] || die "bundled tools in $out are not verified; run scripts/dev-bundle.sh"
  echo "Bundled tools: ${#tools[@]} of ${#tools[@]} verified in $out"
  exit 0
fi

mkdir -p "$(dirname "$out")"
# The swap below replaces DIR wholesale, so DIR must be empty or one of our own installs.
if [ -L "$out" ]; then die "$out is a symlink; pass the real directory"; fi
if [ -e "$out" ]; then
  [ -d "$out" ] || die "$out exists and is not a directory"
  if [ -n "$(ls -A "$out")" ] && { [ ! -f "$out/THIRD_PARTY_NOTICES.md" ] || [ ! -d "$out/.pins" ]; }; then
    die "$out is not empty and does not look like an abacus bundle (no THIRD_PARTY_NOTICES.md and .pins/); refusing to replace it. Pass an empty or new --out directory."
  fi
fi
if [ -f "$out/THIRD_PARTY_NOTICES.md" ]; then
  pending=0
  [ -z "$(stale_tools "$out")" ] || pending=1 # the rebuild below drops tools the manifest no longer lists
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
