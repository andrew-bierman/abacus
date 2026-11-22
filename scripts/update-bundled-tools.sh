#!/usr/bin/env bash
# Pin or bump a bundled tool in bundled-tools.json.
#
#   scripts/update-bundled-tools.sh TOOL VERSION [--trust-first-download]
#   scripts/update-bundled-tools.sh --all            re-pin every tool at its current version
#
# For each platform it reads the sha256 from the tool's own upstream checksum file, downloads
# the asset with gh (which uses your gh auth, so private repos work), and fails unless the
# download matches. If upstream publishes no checksum for an asset, it stops; pass
# --trust-first-download to record the hash of this one download instead, and say so in the PR.
# It also pins the sha256 of each tool's license file. It never publishes or tags anything.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
manifest="$root/bundled-tools.json"
cache="${ABACUS_BUNDLE_CACHE:-$root/.cache/bundle}"
platforms=(darwin-amd64 darwin-arm64 linux-amd64 linux-arm64)

die() { echo "update-bundled-tools: $*" >&2; exit 1; }
command -v gh >/dev/null || die "gh is required"
command -v jq >/dev/null || die "jq is required"
if command -v sha256sum >/dev/null; then sha() { sha256sum "$1" | cut -d' ' -f1; }
else sha() { shasum -a 256 "$1" | cut -d' ' -f1; }; fi

expand() { # TEMPLATE VERSION OS ARCH
  local s="$1"; s="${s//\{version\}/$2}"; s="${s//\{os\}/$3}"; echo "${s//\{arch\}/$4}"
}

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
  field() { jq -r --arg t "$tool" ".tools[] | select(.name==\$t) | $1" "$manifest"; }
  local repo tag_t asset_t sums member_t
  repo="$(field .repo)"; tag_t="$(field .tag_template)"; asset_t="$(field .asset_template)"
  sums="$(field .checksums_asset)"; member_t="$(field .member)"
  local tag; tag="$(expand "$tag_t" "$version" x x)"
  local dir="$cache/$tool"; mkdir -p "$dir"
  local sumfile="$dir/$sums.$version"
  gh release download "$tag" -R "$repo" -p "$sums" -O "$sumfile" --clobber \
    || die "$tool: cannot read $sums from $repo $tag"

  local assets="{}"
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

  # License: from the archive (verified above) or fetched from the pinned tag.
  local lsrc lic lsha
  lsrc="$(field '.license_source.from')"; lic="$(mktemp)"
  if [ "$lsrc" = archive ]; then
    local ex; ex="$(mktemp -d)"
    tar -xzf "$dir/$(expand "$asset_t" "$version" darwin arm64)" -C "$ex"
    cp "$ex/$(field '.license_source.member')" "$lic"; rm -rf "$ex"
  else
    gh api -H 'Accept: application/vnd.github.raw' "repos/$repo/contents/$(field '.license_source.path')?ref=$tag" >"$lic" \
      || die "$tool: cannot fetch the license at $tag"
  fi
  [ -s "$lic" ] || die "$tool: empty license"
  lsha="$(sha "$lic")"; rm -f "$lic"

  local tmp; tmp="$(mktemp)"
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
