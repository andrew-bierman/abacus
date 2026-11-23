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
#   linked_licenses[]          Go modules linked into the binary whose license must ship verbatim
#                              (no program is bundled for them; package-release.sh only)
#   linked_licenses[].name     hand    the name shown in the notices and the directory under third_party/
#   linked_licenses[].module   hand    the Go module path
#   linked_licenses[].version  hand    the module version; must equal the version linked into the binary
#                                      (go.mod), checked by package-release.sh and a Go test
#   linked_licenses[].license  hand    license name shown in THIRD_PARTY_NOTICES.md
#   linked_licenses[].license_path hand  the license file at the module root
#   linked_licenses[].linked_what hand   what of the module is linked, in the notices
#   linked_licenses[].license_sha256 script  sha256 of that license file at the pinned version
#                              (update-bundled-tools.sh --license NAME writes it from the module cache)
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

# check_tool_names NAME...: the names become file names under bundled/ and .pins/, so only
# lowercase letters, digits and dashes are accepted (no path separators, no leading surprises).
check_tool_names() {
  local n
  for n in "$@"; do
    [[ "$n" =~ ^[a-z0-9-]+$ ]] || die "unsafe tool name '$n' in the manifest (allowed: lowercase letters, digits and dashes)"
  done
}

# linked_field NAME JQ_EXPR  (the expression is applied to that linked_licenses entry)
linked_field() { jq -r --arg t "$1" ".linked_licenses[] | select(.name==\$t) | $2" "$manifest"; }

# module_license_file MODULE VERSION LICENSE_PATH -> prints the path of that file in the module
# download cache at exactly that version (go mod download -json; no network when it is cached).
# The caller sets `root` (the module root, for go.mod's toolchain and checksums).
module_license_file() {
  local dir
  [[ "$3" =~ ^[A-Za-z0-9._-]+$ ]] || die "unsafe license path '$3' in the manifest"
  dir="$(go -C "$root" mod download -json "$1@$2" | jq -r .Dir)" && [ -d "$dir" ] \
    || die "cannot read $1@$2 from the module cache (run go mod download first)"
  [ -f "$dir/$3" ] && [ ! -L "$dir/$3" ] || die "$1@$2 has no regular file $3"
  echo "$dir/$3"
}

# install_linked_license LICDIR NAME MODS
# MODS is the go_linked_modules output for the target. The module must be linked at exactly the
# pinned version; its license is copied from the module cache to LICDIR/NAME/LICENSE and checked
# against the pin (fails closed on any mismatch).
install_linked_license() {
  local licdir="$1" name="$2" mods="$3" module version linked src lic
  module="$(linked_field "$name" .module)"; version="$(linked_field "$name" .version)"
  linked="$(printf '%s\n' "$mods" | awk -F'\t' -v m="$module" '$1==m{print $2}')"
  [ -n "$linked" ] || die "$name: $module is not linked into abacus; remove it from linked_licenses"
  [ "$linked" = "$version" ] || die "$name: $module is linked at $linked but linked_licenses pins $version; update both together"
  src="$(module_license_file "$module" "$version" "$(linked_field "$name" .license_path)")"
  lic="$licdir/$name/LICENSE"
  mkdir -p "$licdir/$name"
  cp "$src" "$lic"
  [ "$(sha "$lic")" = "$(linked_field "$name" .license_sha256)" ] || die "$name: license changed from its pin; review it, then re-pin"
  chmod 0644 "$lic"
}

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
  echo "them as subprocesses. Each tool's own license is in \`third_party/<tool>/LICENSE\`, copied"
  echo "verbatim from the pinned release."
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

# linked_notices_entry NAME: a Go module linked into the binary, with no program shipped for it.
linked_notices_entry() {
  local v; v="$(linked_field "$1" .version)"
  echo "## $1 $v (linked library)"
  echo
  echo "- License: $(linked_field "$1" .license)"
  echo "- Source: https://$(linked_field "$1" .module)"
  echo "- Linked: the \`abacus\` binary statically links parts of $1's Go packages ($1 $v,"
  echo "  $(linked_field "$1" .linked_what)). No $1 program is shipped or run."
  echo "- License text: \`third_party/$1/LICENSE\`, copied verbatim from the Go module at that version."
  echo "  The $1 license adds a rider about use with OpenAI and Anthropic products; read it before"
  echo "  redistributing."
  echo
}

notices_footer() {
  echo "abacus itself is GPL-3.0-or-later (\`LICENSE\`). lambe is GPL-3.0 and shipped as a separate program."
  echo
}
