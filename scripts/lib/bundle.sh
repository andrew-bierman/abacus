# shellcheck shell=bash
# shellcheck disable=SC2154  # manifest and BUNDLE_SCRIPT are set by the sourcing script
# Shared helpers for package-release.sh and update-bundled-tools.sh. Source it, do not run it.
#
# bundled-tools.json, field by field. "script" fields are written by update-bundled-tools.sh and
# must not be edited by hand; "hand" fields are the ones you edit to add or reshape a tool.
#   schema                     hand    manifest format version (1)
#   note                       hand    free text
#   tools[].name               hand    the tool and the file name it is installed as in bundled/
#                                      (must equal core.KnownTools; a Go test checks it)
#   tools[].repo               hand    GitHub owner/repo the release assets come from
#   tools[].license            hand    license name shown in THIRD_PARTY_NOTICES.md
#   tools[].tag_template       hand    release tag, {version} is replaced (for example v{version})
#   tools[].asset_template     hand    asset name; {version}, {os}, {arch} are replaced
#   tools[].checksums_asset    hand    the upstream checksum file next to the assets
#   tools[].member             hand    the one executable inside the asset (same placeholders)
#   tools[].license_source     hand    {from: archive, member} or {from: tag, path}
#   tools[].version            script  the pinned version
#   tools[].assets             script  platform (os-arch) -> {asset, sha256, member}
#   tools[].license_sha256     script  sha256 of the license file at the pin
#
# The caller sets BUNDLE_SCRIPT (for messages) and `manifest` (path to the manifest).

die() { echo "${BUNDLE_SCRIPT:-bundle}: $*" >&2; exit 1; }

need() { command -v "$1" >/dev/null || die "$1 is required"; }

if command -v sha256sum >/dev/null; then sha() { sha256sum "$1" | cut -d' ' -f1; }
else sha() { shasum -a 256 "$1" | cut -d' ' -f1; }; fi

# expand TEMPLATE VERSION OS ARCH
expand() {
  local s="$1"; s="${s//\{version\}/$2}"; s="${s//\{os\}/$3}"; echo "${s//\{arch\}/$4}"
}

# tool_field TOOL JQ_EXPR  (the expression is applied to that tool's object)
tool_field() { jq -r --arg t "$1" ".tools[] | select(.name==\$t) | $2" "$manifest"; }

# tag_for TOOL VERSION
tag_for() { expand "$(tool_field "$1" .tag_template)" "$2" x x; }

# fetch_license TOOL VERSION DEST: the license file at the pinned tag, via gh (so private repos work)
fetch_license() {
  need gh
  gh api -H 'Accept: application/vnd.github.raw' \
    "repos/$(tool_field "$1" .repo)/contents/$(tool_field "$1" .license_source.path)?ref=$(tag_for "$1" "$2")" >"$3" \
    || die "$1: cannot fetch the license at $(tag_for "$1" "$2")"
}

# extract_member ARCHIVE MEMBER DEST
# Refuses an archive that has any link, absolute or `..` member, then writes exactly one regular
# file from it to DEST (through stdout, so nothing is ever extracted to a path the archive picks).
extract_member() {
  local archive="$1" want="${2#./}" dest="$3" listed="" ty n
  while IFS=$'\t' read -r ty n; do
    case "$n" in /*|..|../*|*/../*|*/..) die "unsafe member '$n' in $(basename "$archive")" ;; esac
    case "$ty" in l|h) die "link member '$n' in $(basename "$archive")" ;; esac
    if [ "${n#./}" = "$want" ]; then
      [ "$ty" = "-" ] || die "$want is not a regular file in $(basename "$archive")"
      listed="$n"
    fi
  done < <(paste -d$'\t' <(tar -tvzf "$archive" | cut -c1) <(tar -tzf "$archive"))
  [ -n "$listed" ] || die "no member '$want' in $(basename "$archive")"
  tar -xzOf "$archive" "$listed" >"$dest" || die "cannot read $want from $(basename "$archive")"
}

# fetch TOOL PLATFORM -> prints the path of the verified asset in $cache (download with gh when it
# is not cached; fails closed when the manifest has no pin or the sha256 differs from the pin).
# The caller sets `cache` (download directory).
fetch() {
  local tool="$1" p="$2" asset want got
  asset="$(tool_field "$tool" ".assets[\"$p\"].asset")"; want="$(tool_field "$tool" ".assets[\"$p\"].sha256")"
  [ -n "$asset" ] && [ "$asset" != null ] && [ -n "$want" ] && [ "$want" != null ] \
    || die "$tool has no pin for $p in the manifest (run scripts/update-bundled-tools.sh)"
  mkdir -p "$cache/$tool"
  if [ ! -f "$cache/$tool/$asset" ]; then
    need gh
    gh release download "$(tag_for "$tool" "$(tool_field "$tool" .version)")" -R "$(tool_field "$tool" .repo)" \
      -p "$asset" -D "$cache/$tool" || die "cannot download $asset"
  fi
  got="$(sha "$cache/$tool/$asset")"
  [ "$got" = "$want" ] || die "$asset: sha256 $got does not match the pin $want (delete $cache/$tool/$asset to refetch)"
  echo "$cache/$tool/$asset"
}

# install_tool BINDIR LICDIR TOOL PLATFORM ASSET_PATH
# Puts the one executable in BINDIR/TOOL and its license (verified against the pin) in
# LICDIR/TOOL/LICENSE.
install_tool() {
  local bindir="$1" licdir="$2" tool="$3" p="$4" asset_path="$5" lic
  mkdir -p "$bindir" "$licdir/$tool"
  extract_member "$asset_path" "$(tool_field "$tool" ".assets[\"$p\"].member")" "$bindir/$tool"
  chmod 0755 "$bindir/$tool"
  lic="$licdir/$tool/LICENSE"
  if [ "$(tool_field "$tool" .license_source.from)" = archive ]; then
    extract_member "$asset_path" "$(tool_field "$tool" .license_source.member)" "$lic"
  else
    fetch_license "$tool" "$(tool_field "$tool" .version)" "$lic"
  fi
  [ "$(sha "$lic")" = "$(tool_field "$tool" .license_sha256)" ] || die "$tool: license changed from its pin; review it, then re-pin"
  chmod 0644 "$lic"
}

# The THIRD_PARTY_NOTICES.md text, in three pieces: header, one entry per tool, footer.
notices_header() {
  echo "# Third-party notices"
  echo
  echo "This archive ships these tools unmodified, as separate programs in \`bundled/\`. abacus runs"
  echo "them as subprocesses and does not link or import their code. Each tool's own license is in"
  echo "\`third_party/<tool>/LICENSE\`, copied verbatim from the pinned release."
  echo
}

# notices_entry TOOL PLATFORM ASSET_PATH
notices_entry() {
  echo "## $1 $(tool_field "$1" .version)"
  echo
  echo "- License: $(tool_field "$1" .license)"
  echo "- Source: https://github.com/$(tool_field "$1" .repo)"
  echo "- Shipped as: \`bundled/$1\` (asset \`$(basename "$3")\`, sha256 \`$(tool_field "$1" ".assets[\"$2\"].sha256")\`)"
  echo "- License text: \`third_party/$1/LICENSE\`"
  echo
}

notices_footer() {
  echo "abacus itself is GPL-3.0-or-later (\`LICENSE\`). lambe is GPL-3.0 and shipped as a separate program."
  echo "The bv license adds a rider about use with OpenAI and Anthropic products; read"
  echo "\`third_party/bv/LICENSE\` before redistributing."
}
