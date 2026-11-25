#!/usr/bin/env bash
# Pin or bump a bundled tool in bundled-tools.json.
#
#   scripts/update-bundled-tools.sh TOOL VERSION [--trust-first-download]
#   scripts/update-bundled-tools.sh --all            re-pin every tool at its current version
#
# board (the Vercel Board CLI) is not a download: the script re-resolves its npm package into the
# committed scripts/board/package-lock.json and pins the integrity, the lockfile sha256 and the license.
#   scripts/update-bundled-tools.sh --license NAME   re-pin a linked_licenses entry: the sha256 of
#                                                    the license in the module cache at the entry's version
#
# For each platform it reads the sha256 from the tool's own upstream checksum file, downloads
# the asset with gh (your gh login, so private repos work), and fails unless the download matches.
# If upstream publishes no checksum for an asset it stops; --trust-first-download records the
# hash of that one download instead (say so in the PR). It also pins the sha256 of each tool's
# license. The manifest is rewritten atomically (temp file in the same directory, then mv).
# (--all also re-pins the linked licenses.) It never publishes or tags anything. Fields are documented in scripts/lib/bundle.sh.
#
# Environment: ABACUS_BUNDLE_CACHE (downloads) and ABACUS_BUNDLE_MANIFEST (manifest path).
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck disable=SC2034  # read by scripts/lib/bundle.sh
BUNDLE_SCRIPT=update-bundled-tools
manifest="${ABACUS_BUNDLE_MANIFEST:-$root/bundled-tools.json}"
cache="${ABACUS_BUNDLE_CACHE:-$root/.cache/bundle}"
platforms=(darwin-amd64 darwin-arm64 linux-amd64 linux-arm64)
# shellcheck source=lib/bundle.sh
source "$root/scripts/lib/bundle.sh"
need jq

tmps=()
cleanup() { [ "${#tmps[@]}" = 0 ] || rm -rf "${tmps[@]}"; }
trap cleanup EXIT

trust=0; all=0; args=(); lic_name=""
while [ $# -gt 0 ]; do
  case "$1" in
    --trust-first-download) trust=1 ;;
    --all) all=1 ;;
    --license) lic_name="${2:?--license needs a linked_licenses name}"; shift ;;
    -*) die "unknown flag $1" ;;
    *) args+=("$1") ;;
  esac
  shift
done

# pin_npm TOOL VERSION: the Board CLI. Rewrites the committed package.json and package-lock.json for
# the new version (npm install --package-lock-only), then pins the package's integrity, the lockfile's
# sha256 and the license's sha256 in the manifest. The bun version and runtime license stay hand-set.
pin_npm() {
  local tool="$1" version="$2" pkg lockfile lockdir work integ lic lsha tmp
  need npm
  pkg="$(tool_field "$tool" .package)"; lockfile="$root/$(tool_field "$tool" .lockfile)"; lockdir="$(dirname "$lockfile")"
  work="$(mktemp -d)"; tmps+=("$work")
  jq --arg p "$pkg" --arg v "$version" '.dependencies[$p]=$v' "$lockdir/package.json" >"$work/package.json"
  cp "$lockfile" "$work/package-lock.json"
  ( cd "$work" && npm install --package-lock-only --ignore-scripts --no-audit --no-fund >/dev/null ) || die "$tool: npm could not resolve $pkg@$version"
  integ="$(jq -r --arg p "$pkg" '.packages["node_modules/" + $p].integrity // empty' "$work/package-lock.json")"
  [ -n "$integ" ] || die "$tool: the lockfile has no integrity for $pkg@$version"
  ( cd "$work" && npm ci --ignore-scripts --no-audit --no-fund >/dev/null ) || die "$tool: npm ci failed for $pkg@$version"
  lic="$work/node_modules/$pkg/$(tool_field "$tool" .license_source.path)"
  [ -s "$lic" ] || die "$tool: $pkg@$version has no license file"
  lsha="$(sha "$lic")"
  cp "$work/package.json" "$lockdir/package.json.new" && mv "$lockdir/package.json.new" "$lockdir/package.json"
  cp "$work/package-lock.json" "$lockdir/package-lock.json.new" && mv "$lockdir/package-lock.json.new" "$lockdir/package-lock.json"
  tmp="$(mktemp "$manifest.XXXXXX")"; tmps+=("$tmp")
  jq --arg t "$tool" --arg v "$version" --arg i "$integ" --arg ls "$(sha "$lockfile")" --arg l "$lsha" \
    '(.tools[] | select(.name==$t)) |= (.version=$v | .npm_integrity=$i | .lockfile_sha256=$ls | .license_sha256=$l)' "$manifest" >"$tmp"
  mv "$tmp" "$manifest"
  echo "pinned $tool $version ($integ)"
}

# pin_bun TOOL: pin the official bun release archives the Board CLI is compiled from (the manifest's
# bun_version): each platform's sha256 comes from upstream's SHASUMS256.txt, the archive is
# downloaded with gh and must match it.
pin_bun() {
  local tool="$1" bunv dir p asset want got rt="{}" tmp
  need gh
  bunv="$(tool_field "$tool" .bun_version)"; dir="$cache/bun-pin/$bunv"; mkdir -p "$dir"
  gh release download "bun-v$bunv" -R oven-sh/bun -p SHASUMS256.txt -D "$dir" --clobber || die "$tool: cannot read SHASUMS256.txt for bun $bunv"
  for p in "${platforms[@]}"; do
    case "$p" in
      darwin-arm64) asset=bun-darwin-aarch64.zip ;; darwin-amd64) asset=bun-darwin-x64.zip ;;
      linux-arm64) asset=bun-linux-aarch64.zip ;; linux-amd64) asset=bun-linux-x64.zip ;;
      *) die "no bun asset known for $p" ;;
    esac
    want="$(awk -v a="$asset" '$2==a || $2=="*"a {print $1}' "$dir/SHASUMS256.txt" | head -n1)"
    [ -n "$want" ] || die "$tool: bun $bunv publishes no checksum for $asset"
    [ -f "$dir/$asset" ] || gh release download "bun-v$bunv" -R oven-sh/bun -p "$asset" -D "$dir" || die "$tool: cannot download $asset"
    got="$(sha "$dir/$asset")"
    [ "$got" = "$want" ] || die "$tool: $asset sha256 $got does not match upstream $want"
    rt="$(jq -c --arg p "$p" --arg a "$asset" --arg h "$want" '. + {($p): {asset: $a, sha256: $h, member: (($a | sub("\\.zip$"; "")) + "/bun")}}' <<<"$rt")"
  done
  tmp="$(mktemp "$manifest.XXXXXX")"; tmps+=("$tmp")
  jq --arg t "$tool" --argjson r "$rt" '(.tools[] | select(.name==$t)) |= (.bun_runtime=$r)' "$manifest" >"$tmp"
  mv "$tmp" "$manifest"
  echo "pinned the bun $bunv runtime for $tool"
}

pin_tool() {
  local tool="$1" version="$2"
  jq -e --arg t "$tool" '.tools[] | select(.name==$t)' "$manifest" >/dev/null || die "unknown tool $tool"
  if is_npm "$tool"; then pin_npm "$tool" "$version"; pin_bun "$tool"; return 0; fi
  need gh
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
    asset="$(expand_tool "$tool" "$asset_t" "$version" "$os" "$arch")"
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
    assets="$(jq -c --arg p "$p" --arg a "$asset" --arg h "$want" --arg m "$(expand_tool "$tool" "$member_t" "$version" "$os" "$arch")" \
      '. + {($p): {asset: $a, sha256: $h, member: $m}}' <<<"$assets")"
  done

  # The license: from the first platform's archive (verified above) or fetched at the pinned tag.
  local lic lsha; lic="$(mktemp)"; tmps+=("$lic")
  if [ "$(tool_field "$tool" .license_source.from)" = archive ]; then
    extract_member "$dir/$(expand_tool "$tool" "$asset_t" "$version" darwin arm64)" "$(expand_tool "$tool" "$(tool_field "$tool" .license_source.member)" "$version" darwin arm64)" "$lic"
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

# pin_linked NAME: write the license sha256 of a linked module from the module cache. The version
# is hand-edited (it must equal the one in go.mod), so this never changes it.
pin_linked() {
  local name="$1" src lsha tmp
  need go
  jq -e --arg t "$name" '(.linked_licenses // [])[] | select(.name==$t)' "$manifest" >/dev/null || die "unknown linked license $name"
  src="$(module_license_file "$(linked_field "$name" .module)" "$(linked_field "$name" .version)" "$(linked_field "$name" .license_path)")"
  [ -s "$src" ] || die "$name: empty license"
  lsha="$(sha "$src")"
  tmp="$(mktemp "$manifest.XXXXXX")"; tmps+=("$tmp")
  jq --arg t "$name" --arg l "$lsha" '(.linked_licenses[] | select(.name==$t)) |= (.license_sha256=$l)' "$manifest" >"$tmp"
  mv "$tmp" "$manifest"
  echo "pinned license of $name ($lsha)"
}

if [ -n "$lic_name" ]; then
  if [[ "${#args[@]}" != 0 || "$all" != 0 ]]; then
    die "--license takes only a name"
  fi
  pin_linked "$lic_name"
elif [ "$all" = 1 ]; then
  [ "${#args[@]}" = 0 ] || die "--all takes no arguments"
  while IFS=$'\t' read -r t v; do pin_tool "$t" "$v"; done < <(jq -r '.tools[] | [.name,.version] | @tsv' "$manifest")
  while IFS= read -r t; do pin_linked "$t"; done < <(jq -r '(.linked_licenses // [])[].name' "$manifest")
else
  [ "${#args[@]}" = 2 ] || die "usage: $0 TOOL VERSION [--trust-first-download] | --all | --license NAME"
  pin_tool "${args[0]}" "${args[1]#v}"
fi
