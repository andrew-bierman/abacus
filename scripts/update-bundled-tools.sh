#!/usr/bin/env bash
# Pin or bump a bundled tool in bundled-tools.json.
#
#   scripts/update-bundled-tools.sh TOOL VERSION [--trust-first-download]
#   scripts/update-bundled-tools.sh --all            re-pin every tool at its current version
#
# For each platform it reads the sha256 from the tool's own upstream checksum file, downloads
# the asset with gh (your gh login, so private repos work), and fails unless the download matches.
# If upstream publishes no checksum for an asset it stops; --trust-first-download records the
# hash of that one download instead (say so in the PR). It also pins the sha256 of each tool's
# license. The manifest is rewritten atomically (temp file in the same directory, then mv).
# It never publishes or tags anything. Fields are documented in scripts/lib/bundle.sh.
#
# Environment: ABACUS_BUNDLE_CACHE (downloads) and ABACUS_BUNDLE_MANIFEST (manifest path).
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUNDLE_SCRIPT=update-bundled-tools
manifest="${ABACUS_BUNDLE_MANIFEST:-$root/bundled-tools.json}"
cache="${ABACUS_BUNDLE_CACHE:-$root/.cache/bundle}"
platforms=(darwin-amd64 darwin-arm64 linux-amd64 linux-arm64)
# shellcheck source=lib/bundle.sh
source "$root/scripts/lib/bundle.sh"
need gh; need jq

tmps=()
cleanup() { [ "${#tmps[@]}" = 0 ] || rm -rf "${tmps[@]}"; }
trap cleanup EXIT

trust=0; all=0; args=()
for a in "$@"; do
  case "$a" in
    --trust-first-download) trust=1 ;;
    --all) all=1 ;;
    -*) die "unknown flag $a" ;;
    *) args+=("$a") ;;
  esac
done

pin_tool() {
  local tool="$1" version="$2"
  jq -e --arg t "$tool" '.tools[] | select(.name==$t)' "$manifest" >/dev/null || die "unknown tool $tool"
  local repo asset_t sums member_t tag dir sumfile
  repo="$(tool_field "$tool" .repo)"; asset_t="$(tool_field "$tool" .asset_template)"
  sums="$(tool_field "$tool" .checksums_asset)"; member_t="$(tool_field "$tool" .member)"
  tag="$(tag_for "$tool" "$version")"
  dir="$cache/$tool"; mkdir -p "$dir"
  sumfile="$dir/$sums.$version"
  gh release download "$tag" -R "$repo" -p "$sums" -O "$sumfile" --clobber \
    || die "$tool: cannot read $sums from $repo $tag"

  local assets="{}" p
  for p in "${platforms[@]}"; do
    local os="${p%-*}" arch="${p#*-}" asset want got
    asset="$(expand "$asset_t" "$version" "$os" "$arch")"
    want="$(awk -v a="$asset" '$2==a || $2=="*"a {print $1}' "$sumfile" | head -n1)"
    if [ ! -f "$dir/$asset" ]; then # a cached download is still verified below
      gh release download "$tag" -R "$repo" -p "$asset" -D "$dir" || die "$tool: cannot download $asset"
    fi
    got="$(sha "$dir/$asset")"
    if [ -z "$want" ]; then
      [ "$trust" = 1 ] || die "$tool: upstream publishes no checksum for $asset (got $got). Verify it yourself, then rerun with --trust-first-download"
      want="$got"; echo "WARNING: $asset: no upstream checksum; pinned from this download ($got)" >&2
    fi
    [ "$got" = "$want" ] || die "$tool: $asset sha256 $got does not match upstream $want"
    assets="$(jq -c --arg p "$p" --arg a "$asset" --arg h "$want" --arg m "$(expand "$member_t" "$version" "$os" "$arch")" \
      '. + {($p): {asset: $a, sha256: $h, member: $m}}' <<<"$assets")"
  done

  # The license: from the first platform's archive (verified above) or fetched at the pinned tag.
  local lic lsha; lic="$(mktemp)"; tmps+=("$lic")
  if [ "$(tool_field "$tool" .license_source.from)" = archive ]; then
    extract_member "$dir/$(expand "$asset_t" "$version" darwin arm64)" "$(tool_field "$tool" .license_source.member)" "$lic"
  else
    fetch_license "$tool" "$version" "$lic"
  fi
  [ -s "$lic" ] || die "$tool: empty license"
  lsha="$(sha "$lic")"

  local tmp; tmp="$(mktemp "$manifest.XXXXXX")"; tmps+=("$tmp")
  jq --arg t "$tool" --arg v "$version" --argjson a "$assets" --arg l "$lsha" \
    '(.tools[] | select(.name==$t)) |= (.version=$v | .assets=$a | .license_sha256=$l)' "$manifest" >"$tmp"
  mv "$tmp" "$manifest"
  echo "pinned $tool $version"
}

if [ "$all" = 1 ]; then
  [ "${#args[@]}" = 0 ] || die "--all takes no arguments"
  while IFS=$'\t' read -r t v; do pin_tool "$t" "$v"; done < <(jq -r '.tools[] | [.name,.version] | @tsv' "$manifest")
else
  [ "${#args[@]}" = 2 ] || die "usage: $0 TOOL VERSION [--trust-first-download] | --all"
  pin_tool "${args[0]}" "${args[1]#v}"
fi
