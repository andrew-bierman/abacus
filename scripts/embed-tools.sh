#!/usr/bin/env bash
# The build-time step that puts every tool abacus needs INSIDE the binary: fetch, verify, compress.
#
#   scripts/embed-tools.sh [--platform linux-amd64] [--out DIR] [--blobs DIR] [--check]
#
# 1. Fetches the assets pinned in bundled-tools.json for the platform (default: this machine) with
#    gh, checks every sha256 against the pin (fail closed: any mismatch aborts and changes nothing),
#    and extracts only the one executable per tool. The Vercel Board CLI is not a download: it is
#    built from its npm package at the pinned integrity (committed lockfile, npm ci --ignore-scripts)
#    and compiled with bun to a standalone executable (see ensure_board_build in lib/bundle.sh).
# 2. With --blobs DIR (what `make build` and package-release.sh pass) it then compresses each
#    verified executable with zstd into DIR together with manifest.json, the license texts and the
#    complete THIRD_PARTY_NOTICES.md, which `go build -tags embedtools` embeds (DIR is
#    internal/embedtools/blobs/<os>-<arch>, gitignored, never committed). DIR is built next to itself
#    and swapped in only when complete.
#
# The verified executables are kept in --out (default .cache/embed/<platform>/tools), laid out as
#   {bd,lambe,beads-lease,board}   the executables
#   third_party/<tool>/LICENSE (and the Board's extra license texts), THIRD_PARTY_NOTICES.md
#   .pins/<tool>                   what was installed: platform, pin, executable sha256
#
# Idempotent: a tool whose executable still matches its pin (and the manifest's current pin) is
# kept and not fetched again. Tools the manifest no longer lists are dropped by the rebuild.
# --out must not exist, be empty, or be a previous install of this script (it holds
# THIRD_PARTY_NOTICES.md and .pins/); any other directory is refused untouched, because the swap
# replaces the whole directory. A run killed with SIGKILL can leave a .tools.* stage directory
# next to --out; that is accepted (it holds only verified files) and safe to delete. The new DIR is
# built in a temp directory next to it and moved into place only when every tool succeeded.
# --check downloads nothing and changes nothing: it exits non-zero unless every tool is verified.
#
# Environment: ABACUS_BUNDLE_CACHE (downloads and compiled Board builds, default .cache/bundle) and
# ABACUS_BUNDLE_MANIFEST (manifest path, default bundled-tools.json).
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck disable=SC2034  # read by scripts/lib/bundle.sh
BUNDLE_SCRIPT=embed-tools
manifest="${ABACUS_BUNDLE_MANIFEST:-$root/bundled-tools.json}"
cache="${ABACUS_BUNDLE_CACHE:-$root/.cache/bundle}"
out=""
blobs=""
platform=""
check=0
# shellcheck source=lib/bundle.sh
source "$root/scripts/lib/bundle.sh"
# shellcheck source=lib/golicenses.sh
source "$root/scripts/lib/golicenses.sh"

while [ $# -gt 0 ]; do
  case "$1" in
    --platform) platform="${2:?}"; shift 2 ;;
    --out) out="${2:?}"; shift 2 ;;
    --blobs) blobs="${2:?}"; shift 2 ;;
    --check) check=1; shift ;;
    -h|--help) sed -n '2,33p' "$0"; exit 0 ;;
    *) die "unknown argument $1 (usage: $0 [--platform OS-ARCH] [--out DIR] [--blobs DIR] [--check])" ;;
  esac
done
need jq

if [ -z "$platform" ]; then
  case "$(uname -s)" in Linux) os=linux ;; Darwin) os=darwin ;; *) die "unsupported OS $(uname -s); pass --platform (Windows is not bundled)" ;; esac
  case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; arm64|aarch64) arch=arm64 ;; *) die "unsupported CPU $(uname -m); pass --platform" ;; esac
  platform="$os-$arch"
fi
[[ "$platform" =~ ^[a-z0-9]+-[a-z0-9]+$ ]] || die "bad platform '$platform' (want os-arch, for example linux-amd64)"
[ -n "$out" ] || out="$root/.cache/embed/$platform/tools"

tools=()
while IFS= read -r t; do tools+=("$t"); done < <(jq -r '.tools[].name' "$manifest")
[ "${#tools[@]}" -gt 0 ] || die "the manifest lists no tools"
check_tool_names "${tools[@]}"

# writable_by_others PATH: true when group or other may write to PATH (the same refusal abacus
# itself applies to a bundled tool, so a copy that passes here is accepted at run time).
writable_by_others() {
  [ -n "$(find "$1" -maxdepth 0 \( -perm -020 -o -perm -002 \) -print)" ]
}

# verified TOOL DIR: succeeds when DIR/TOOL is the executable installed from the manifest's
# current pin for $platform, its license still matches license_sha256 and the modes are ones abacus
# accepts; otherwise prints why on stdout and fails.
verified() {
  local tool="$1" dir="$2" want rec lic
  want="$(pin_of "$tool" "$platform")"
  if [ -z "$want" ]; then
    echo "no pin for $platform in the manifest"; return 1
  fi
  if [ -L "$dir/$tool" ] || [ ! -f "$dir/$tool" ]; then echo "not built in yet"; return 1; fi
  if [ ! -x "$dir/$tool" ]; then echo "not executable"; return 1; fi
  if writable_by_others "$dir" || writable_by_others "$dir/$tool"; then echo "writable by group or others (abacus would refuse it)"; return 1; fi
  lic="$dir/third_party/$tool/LICENSE"
  if [[ ! -f "$dir/.pins/$tool" || ! -f "$lic" || -L "$lic" ]]; then
    echo "no install record"
    return 1
  fi
  rec="$want $platform $(sha "$dir/$tool")"
  [ "$(cat "$dir/.pins/$tool")" = "$rec" ] || { echo "does not match the pin (changed, or a different version or platform)"; return 1; }
  if is_npm "$tool" && [ ! -s "$dir/third_party/$tool/BUN_LICENSE.md" ]; then echo "its runtime license is missing"; return 1; fi
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
  [ "$bad" = 0 ] || die "the tools in $out are not verified; run scripts/embed-tools.sh"
  echo "Tools: ${#tools[@]} of ${#tools[@]} verified in $out"
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
# One EXIT trap for every temp directory this script makes (RETURN traps do not fire on die/exit):
# the build stage, and the blob work directory of pack_blobs. Nothing is left behind on failure.
stage=""; old=""; pack_work=""
on_exit() {
  [ -z "$pack_work" ] || rm -rf "$pack_work"
  if declare -F cleanup >/dev/null; then cleanup; fi
}
trap on_exit EXIT
built=0
if [ -f "$out/THIRD_PARTY_NOTICES.md" ]; then
  pending=0
  [ -z "$(stale_tools "$out")" ] || pending=1 # the rebuild below drops tools the manifest no longer lists
  for tool in "${tools[@]}"; do verified "$tool" "$out" >/dev/null || pending=1; done
  if [ "$pending" = 0 ]; then
    echo "Tools: ${#tools[@]} of ${#tools[@]} already verified in $out (nothing to do)"
    built=1
  fi
fi

build_raw() {
  stage="$(mktemp -d "$(dirname "$out")/.tools.XXXXXX")"
  old=""
  cleanup() {
    rm -rf "${stage:-}"
    # A swap that stopped half way puts the previous directory back.
    if [ -n "$old" ] && [ -d "$old" ]; then
      if [ -e "$out" ]; then rm -rf "$old"; else mv "$old" "$out"; fi
    fi
  }
  mkdir "$stage/third_party" "$stage/.pins"
  notices_header >"$stage/THIRD_PARTY_NOTICES.md"

  for tool in "${tools[@]}"; do
    if verified "$tool" "$out" >/dev/null 2>&1; then
      cp -p "$out/$tool" "$stage/$tool"
      mkdir -p "$stage/third_party/$tool"
      cp -p "$out/third_party/$tool"/* "$stage/third_party/$tool/"
      cp -p "$out/.pins/$tool" "$stage/.pins/$tool"
      asset_path="$(tool_field "$tool" ".assets[\"$platform\"].asset // \"$tool\"")"
      echo "kept     $tool (verified)"
    else
      asset_path="$(fetch "$tool" "$platform")"
      install_tool "$stage" "$stage/third_party" "$tool" "$platform" "$asset_path"
      echo "$(pin_of "$tool" "$platform") $platform $(sha "$stage/$tool")" >"$stage/.pins/$tool"
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
  echo "Tools: ${#tools[@]} of ${#tools[@]} verified in $out"
}
[ "$built" = 1 ] || build_raw

# pack_blobs: compress the verified executables into $blobs with the complete notices. Built in a
# temp directory beside it and swapped in only when complete; refuses to run without a manifest
# entry for every Go module whose license must ship verbatim (the same rule as the release).
must_ship=(github.com/Dicklesworthstone/beads_viewer)
pack_blobs() {
  local os="${platform%-*}" arch="${platform#*-}" mods work lname m notices args=() t
  need go
  mods="$(go_linked_modules "$root" "$os" "$arch")" \
    || die "cannot list the Go modules linked into abacus (run go mod download first)"
  for m in "${must_ship[@]}"; do
    printf '%s\n' "$mods" | awk -F'\t' -v m="$m" '$1==m{f=1} END{exit !f}' || continue
    [ "$(jq -r --arg m "$m" '[(.linked_licenses // [])[] | select(.module==$m)] | length' "$manifest")" != 0 ] \
      || die "$m is linked into abacus but the manifest has no linked_licenses entry for it"
  done
  mkdir -p "$(dirname "$blobs")"
  work="$(mktemp -d "$(dirname "$blobs")/.blobs.XXXXXX")"
  pack_work="$work"
  mkdir -p "$work/lic"
  cp -R "$out/third_party/." "$work/lic/"
  notices="$work/THIRD_PARTY_NOTICES.md"
  notices_header >"$notices"
  for t in "${tools[@]}"; do
    notices_entry "$t" "$platform" "$(tool_field "$t" ".assets[\"$platform\"].asset // \"$t\"")" >>"$notices"
  done
  while IFS= read -r lname; do
    [ -n "$lname" ] || continue
    install_linked_license "$work/lic" "$lname" "$mods"
    linked_notices_entry "$lname" >>"$notices"
  done < <(jq -r '(.linked_licenses // [])[].name' "$manifest")
  notices_footer >>"$notices"
  go_licenses_section "$root" "$os" "$arch" >>"$notices" \
    || die "cannot list the Go modules linked into abacus (run go mod download first)"
  for t in "${tools[@]}"; do args+=("$t=$(tool_field "$t" .version)"); done
  go run -C "$root" ./internal/embedtools/cmd/pack -src "$out" -licenses "$work/lic" -notices "$notices" \
    -out "$work/blobs" "${args[@]}" || die "packing the tools failed"
  # What was packed must be exactly the tools the manifest lists, at the versions it pins.
  [ "$(jq -S -c '[.tools[] | {name, version}] | sort_by(.name)' "$work/blobs/manifest.json")" = \
    "$(jq -S -c '[.tools[] | {name, version}] | sort_by(.name)' "$manifest")" ] \
    || die "the packed tool list does not match bundled-tools.json"
  rm -rf "$blobs"
  mv "$work/blobs" "$blobs"
  rm -rf "$work"; pack_work=""
  echo "Packed ${#tools[@]} tools for $platform into $blobs ($(du -sh "$blobs" | cut -f1))"
}
[ -z "$blobs" ] || pack_blobs
