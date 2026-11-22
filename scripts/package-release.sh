#!/usr/bin/env bash
# Build the release archives: abacus plus the pinned sidecar tools, one per platform.
#
#   scripts/package-release.sh VERSION [--platforms "darwin-arm64 linux-amd64"] [--out DIR]
#
# Each archive is laid out as
#   abacus_VERSION_OS_ARCH/{abacus, bundled/{bd,bv,lambe,beads-lease}, third_party/<tool>/LICENSE,
#                           THIRD_PARTY_NOTICES.md, LICENSE, README.md}
# Every tool comes from bundled-tools.json: downloaded with gh, its sha256 checked against the
# pin (the build fails closed on any mismatch), and only the one executable is extracted. Nothing
# is published, tagged or uploaded; the maintainer does that with the files this leaves in DIR.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
manifest="$root/bundled-tools.json"
cache="${ABACUS_BUNDLE_CACHE:-$root/.cache/bundle}"
out="$root/dist"
platforms="darwin-amd64 darwin-arm64 linux-amd64 linux-arm64"

die() { echo "package-release: $*" >&2; exit 1; }
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
command -v gh >/dev/null || die "gh is required"
command -v jq >/dev/null || die "jq is required"
command -v go >/dev/null || die "go is required"
if command -v sha256sum >/dev/null; then sha() { sha256sum "$1" | cut -d' ' -f1; }
else sha() { shasum -a 256 "$1" | cut -d' ' -f1; }; fi

mkdir -p "$out"
work="$(mktemp -d "${TMPDIR:-/tmp}/abacus-package.XXXXXX")"
trap 'rm -rf "$work"' EXIT
tools=()
while IFS= read -r t; do tools+=("$t"); done < <(jq -r '.tools[].name' "$manifest")

tool_field() { jq -r --arg t "$1" ".tools[] | select(.name==\$t) | $2" "$manifest"; }

# fetch TOOL PLATFORM -> path of the verified asset in the cache
fetch() {
  local tool="$1" p="$2" asset want repo tag got
  asset="$(tool_field "$tool" ".assets[\"$p\"].asset")"; want="$(tool_field "$tool" ".assets[\"$p\"].sha256")"
  [ -n "$asset" ] && [ "$asset" != null ] && [ -n "$want" ] && [ "$want" != null ] \
    || die "$tool has no pin for $p in bundled-tools.json (run scripts/update-bundled-tools.sh)"
  repo="$(tool_field "$tool" .repo)"; tag="$(tool_field "$tool" .tag_template)"; tag="${tag//\{version\}/$(tool_field "$tool" .version)}"
  mkdir -p "$cache/$tool"
  if [ ! -f "$cache/$tool/$asset" ]; then
    gh release download "$tag" -R "$repo" -p "$asset" -D "$cache/$tool" || die "cannot download $asset from $repo $tag"
  fi
  got="$(sha "$cache/$tool/$asset")"
  [ "$got" = "$want" ] || die "$asset: sha256 $got does not match the pin $want (delete $cache/$tool/$asset to refetch)"
  echo "$cache/$tool/$asset"
}

for p in $platforms; do
  os="${p%-*}"; arch="${p#*-}"
  name="abacus_${version}_${os}_${arch}"
  stage="$work/$name"
  mkdir -p "$stage/bundled" "$stage/third_party"
  echo "== $name"

  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -C "$root" -trimpath \
    -ldflags "-s -w -X main.version=$version" -o "$stage/abacus" ./cmd/abacus

  notices="$stage/THIRD_PARTY_NOTICES.md"
  {
    echo "# Third-party notices"
    echo
    echo "This archive ships these tools unmodified, as separate programs in \`bundled/\`. abacus runs"
    echo "them as subprocesses and does not link or import their code. Each tool's own license is in"
    echo "\`third_party/<tool>/LICENSE\`, copied verbatim from the pinned release."
    echo
  } >"$notices"

  for tool in "${tools[@]}"; do
    asset_path="$(fetch "$tool" "$p")"
    member="$(tool_field "$tool" ".assets[\"$p\"].member")"; as="$(tool_field "$tool" .install_as)"
    ex="$work/ex-$tool"; rm -rf "$ex"; mkdir -p "$ex"   # a fresh empty dir per untrusted archive
    tar -xzf "$asset_path" -C "$ex"
    src="$ex/$member"
    [ -f "$src" ] && [ ! -L "$src" ] || die "$tool: $member is not a regular file in $(basename "$asset_path")"
    install -m 0755 "$src" "$stage/bundled/$as"

    mkdir -p "$stage/third_party/$tool"
    lic="$stage/third_party/$tool/LICENSE"
    if [ "$(tool_field "$tool" .license_source.from)" = archive ]; then
      lm="$ex/$(tool_field "$tool" .license_source.member)"
      [ -f "$lm" ] && [ ! -L "$lm" ] || die "$tool: no license in the archive"
      cp "$lm" "$lic"
    else
      repo="$(tool_field "$tool" .repo)"; tag="$(tool_field "$tool" .tag_template)"; tag="${tag//\{version\}/$(tool_field "$tool" .version)}"
      gh api -H 'Accept: application/vnd.github.raw' "repos/$repo/contents/$(tool_field "$tool" .license_source.path)?ref=$tag" >"$lic" \
        || die "$tool: cannot fetch the license at $tag"
    fi
    [ "$(sha "$lic")" = "$(tool_field "$tool" .license_sha256)" ] || die "$tool: license changed from its pin; review it, then re-pin"
    chmod 0644 "$lic"; rm -rf "$ex"

    {
      echo "## $tool $(tool_field "$tool" .version)"
      echo
      echo "- License: $(tool_field "$tool" .license)"
      echo "- Source: https://github.com/$(tool_field "$tool" .repo)"
      echo "- Shipped as: \`bundled/$as\` (asset \`$(basename "$asset_path")\`, sha256 \`$(tool_field "$tool" ".assets[\"$p\"].sha256")\`)"
      echo "- License text: \`third_party/$tool/LICENSE\`"
      echo
    } >>"$notices"
  done
  {
    echo "abacus itself is GPL-3.0-or-later (\`LICENSE\`). lambe is GPL-3.0 and shipped as a separate program."
    echo "The bv license adds a rider about use with OpenAI and Anthropic products; read"
    echo "\`third_party/bv/LICENSE\` before redistributing."
  } >>"$notices"

  cp "$root/LICENSE" "$root/README.md" "$stage/"
  cp "$manifest" "$stage/third_party/bundled-tools.json"
  chmod 0644 "$stage/LICENSE" "$stage/README.md" "$notices" "$stage/third_party/bundled-tools.json"
  chmod 0755 "$stage/abacus"
  find "$stage" -type d -exec chmod 0755 {} +

  tar -czf "$out/$name.tar.gz" -C "$work" "$name"
  echo "   $(du -h "$out/$name.tar.gz" | cut -f1)  $out/$name.tar.gz"
done

( cd "$out" && for p in $platforms; do f="abacus_${version}_${p%-*}_${p#*-}.tar.gz"; echo "$(sha "$f")  $f"; done >SHA256SUMS )
echo "wrote $out/SHA256SUMS"
