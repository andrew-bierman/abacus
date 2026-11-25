# shellcheck shell=bash
# shellcheck disable=SC2154  # manifest and BUNDLE_SCRIPT are set by the sourcing script
# Shared helpers for package-release.sh and update-bundled-tools.sh. Source it, do not run it.
#
# bundled-tools.json, field by field. "script" fields are written by update-bundled-tools.sh and
# must not be edited by hand; "hand" fields are the ones you edit to add or reshape a tool.
#   schema                     hand    manifest format version (1)
#   note                       hand    free text
#   tools[].name               hand    the tool and the file name it is extracted as at run time
#                                      (must equal core.KnownTools; a Go test checks it)
#   tools[].kind               hand    "npm-bun" for the Vercel Board CLI: an npm package compiled to a
#                                      standalone executable with bun; absent means a GitHub release asset
#   tools[].repo               hand    GitHub owner/repo the release assets come from
#   tools[].license            hand    license name shown in THIRD_PARTY_NOTICES.md
#   tools[].tag_template       hand    release tag, {version} is replaced (for example v{version})
#   tools[].asset_template     hand    asset name; {version}, {os}, {arch} (and {triple}) are replaced;
#                                      a .tar.xz asset is read as xz, anything else as gzip
#   tools[].triples            hand    optional platform (os-arch) -> upstream target name, for {triple}
#                                      in asset_template, member and license_source.member
#   tools[].checksums_asset    hand    the upstream checksum file next to the assets
#   tools[].member             hand    the one executable inside the asset (same placeholders)
#   tools[].license_source     hand    {from: archive, member} or {from: tag, path}
#   tools[].version            script  the pinned version
#   tools[].assets             script  platform (os-arch) -> {asset, sha256, member}; for npm-bun
#                                      {bun_target}, hand-edited: the compiled output is not pinned by a
#                                      hash (bun output need not be reproducible) but by its inputs:
#   tools[].bun_runtime        hand+script  platform -> {asset, sha256, member}: the official bun release
#                                      archive (github.com/oven-sh/bun) the executable is compiled from.
#                                      Downloaded with gh and verified fail closed; its bun binary is
#                                      passed to `bun build --compile --compile-executable-path` for every
#                                      target, so the runtime inside the Board CLI is the verified one.
#   tools[].package, npm_integrity, lockfile, lockfile_sha256, bun_version, entry, runtime_license
#                              npm-bun only. The whole npm tree comes from the committed lockfile
#                              (npm ci --ignore-scripts); npm_integrity must equal the integrity the
#                              lockfile records for the package; the lockfile hash and the bun version
#                              are checked; runtime_license is {repo, tag, path, sha256} of the bun
#                              runtime's license text. The sha256 of each compiled output is recorded
#                              in the release notes instead.
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

sha_stdin() { if command -v sha256sum >/dev/null; then sha256sum | cut -d' ' -f1; else shasum -a 256 | cut -d' ' -f1; fi; }

# expand TEMPLATE VERSION OS ARCH
expand() {
  local s="$1"; s="${s//\{version\}/$2}"; s="${s//\{os\}/$3}"; echo "${s//\{arch\}/$4}"
}

# expand_tool TOOL TEMPLATE VERSION OS ARCH: expand, plus {triple} from the tool's `triples` map.
expand_tool() {
  local s; s="$(expand "$2" "$3" "$4" "$5")"
  echo "${s//\{triple\}/$(tool_field "$1" ".triples[\"$4-$5\"] // empty")}"
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

# is_npm TOOL: the Vercel Board CLI, built from an npm package instead of downloaded as an asset.
is_npm() { [ "$(tool_field "$1" '.kind // "release"')" = npm-bun ]; }

# pin_of TOOL PLATFORM: what the tool's install record is checked against: the asset sha256, or for
# an npm-bun tool the npm integrity of the package. Prints nothing when the platform has no pin.
pin_of() {
  local v
  if is_npm "$1"; then
    v="$(tool_field "$1" ".assets[\"$2\"].bun_target // empty")"
    [ -n "$v" ] || return 0
    # Every input of the build, so changing any of them invalidates an install record.
    printf '%s|%s|%s|%s|%s' "$(tool_field "$1" '.npm_integrity // empty')" "$(tool_field "$1" '.lockfile_sha256 // empty')" \
      "$(tool_field "$1" '.bun_version // empty')" "$v" "$(tool_field "$1" ".bun_runtime[\"$2\"].sha256 // empty")" | sha_stdin
  else
    v="$(tool_field "$1" ".assets[\"$2\"].sha256 // empty")"; echo "$v"
  fi
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
  local archive="$1" want="${2#./}" dest="$3" listed="" ty n z=z
  case "$archive" in *.tar.xz) z=J ;; esac # the format follows the asset's extension (needs xz)
  while IFS=$'\t' read -r ty n; do
    case "$n" in /*|..|../*|*/../*|*/..) die "unsafe member '$n' in $(basename "$archive")" ;; esac
    case "$ty" in l|h) die "link member '$n' in $(basename "$archive")" ;; esac
    if [ "${n#./}" = "$want" ]; then
      [ "$ty" = "-" ] || die "$want is not a regular file in $(basename "$archive")"
      listed="$n"
    fi
  done < <(paste -d$'\t' <(tar -tv"$z"f "$archive" | cut -c1) <(tar -t"$z"f "$archive"))
  [ -n "$listed" ] || die "no member '$want' in $(basename "$archive")"
  tar -x"$z"Of "$archive" "$listed" >"$dest" || die "cannot read $want from $(basename "$archive")"
}

# fetch TOOL PLATFORM -> prints the path of the verified asset in $cache (download with gh when it
# is not cached; fails closed when the manifest has no pin or the sha256 differs from the pin).
# The caller sets `cache` (download directory).
fetch() {
  local tool="$1" p="$2" asset want got
  if is_npm "$tool"; then
    [ -n "$(pin_of "$tool" "$p")" ] || die "$tool has no pin for $p in the manifest (run scripts/update-bundled-tools.sh)"
    echo "npm:$(tool_field "$tool" .package)@$(tool_field "$tool" .version)"; return 0
  fi
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
  if is_npm "$tool"; then
    install_board "$bindir" "$licdir" "$tool" "$p"
    return 0
  fi
  extract_member "$asset_path" "$(tool_field "$tool" ".assets[\"$p\"].member")" "$bindir/$tool"
  chmod 0755 "$bindir/$tool"
  lic="$licdir/$tool/LICENSE"
  if [ "$(tool_field "$tool" .license_source.from)" = archive ]; then
    extract_member "$asset_path" "$(expand_tool "$tool" "$(tool_field "$tool" .license_source.member)" "$(tool_field "$tool" .version)" "${p%-*}" "${p#*-}")" "$lic"
  else
    fetch_license "$tool" "$(tool_field "$tool" .version)" "$lic"
  fi
  [ "$(sha "$lic")" = "$(tool_field "$tool" .license_sha256)" ] || die "$tool: license changed from its pin; review it, then re-pin"
  chmod 0644 "$lic"
}

# fetch_bun TOOL PLATFORM -> prints the path of the verified bun executable for PLATFORM (the
# runtime the Board CLI is compiled from): the official release archive, downloaded with gh, its
# sha256 checked against the pin (fail closed), only the one named member extracted.
fetch_bun() {
  local tool="$1" p="$2" asset want member bunv dir zip exe
  asset="$(tool_field "$tool" ".bun_runtime[\"$p\"].asset // empty")"; want="$(tool_field "$tool" ".bun_runtime[\"$p\"].sha256 // empty")"
  member="$(tool_field "$tool" ".bun_runtime[\"$p\"].member // empty")"; bunv="$(tool_field "$tool" .bun_version)"
  [ -n "$asset" ] && [ -n "$want" ] && [ -n "$member" ] || die "$tool has no bun runtime pin for $p in the manifest (run scripts/update-bundled-tools.sh)"
  [[ "$asset" =~ ^bun-[a-z0-9-]+\.zip$ ]] && [[ "$member" =~ ^bun-[a-z0-9-]+/bun$ ]] || die "$tool: unsafe bun runtime asset or member in the manifest"
  dir="$cache/bun/$bunv-$p"; zip="$dir/$asset"; exe="$dir/bun"
  mkdir -p "$dir"
  if [ ! -f "$zip" ]; then
    need gh
    gh release download "bun-v$bunv" -R oven-sh/bun -p "$asset" -D "$dir" || die "$tool: cannot download $asset"
  fi
  [ "$(sha "$zip")" = "$want" ] || die "$tool: $asset sha256 $(sha "$zip") does not match the pin $want (delete $zip to refetch)"
  need unzip
  rm -f "$exe"
  unzip -p "$zip" "$member" >"$exe" || die "$tool: cannot read $member from $asset"
  [ -s "$exe" ] || die "$tool: empty bun runtime in $asset"
  chmod 0755 "$exe"
  echo "$exe"
}

# ensure_board_build TOOL PLATFORM -> prints the cache directory holding the compiled executable
# and its licenses. Recipe (fail closed): copy the committed package.json and package-lock.json to a
# temp dir; check the lockfile's sha256 and that the lockfile records the pinned npm integrity for the
# package; `npm ci --ignore-scripts` (npm verifies every tarball against the lockfile); check bun's
# version; `bun build --compile` for the platform's target. Never `npx vb` (unrelated package).
# A cached build is reused only when its record says it came from the same inputs.
ensure_board_build() {
  local tool="$1" p="$2" version integ lock lock_sha bunv entry target pkg key rec work lockdir bunexe
  version="$(tool_field "$tool" .version)"; integ="$(tool_field "$tool" .npm_integrity)"
  lock="$root/$(tool_field "$tool" .lockfile)"; lock_sha="$(tool_field "$tool" .lockfile_sha256)"
  bunv="$(tool_field "$tool" .bun_version)"; entry="$(tool_field "$tool" .entry)"
  target="$(tool_field "$tool" ".assets[\"$p\"].bun_target")"; pkg="$(tool_field "$tool" .package)"
  [[ "$target" =~ ^bun-[a-z0-9-]+$ ]] || die "$tool: unsafe bun target '$target' in the manifest"
  [[ "$entry" =~ ^[A-Za-z0-9._/-]+$ ]] && [[ "$entry" != *..* ]] || die "$tool: unsafe entry '$entry' in the manifest"
  [ -f "$lock" ] && [ "$(sha "$lock")" = "$lock_sha" ] || die "$tool: $(tool_field "$tool" .lockfile) does not match its pin lockfile_sha256; review it, then re-pin"
  jq -e --arg pkg "$pkg" --arg i "$integ" '.packages["node_modules/" + $pkg].integrity == $i' "$lock" >/dev/null \
    || die "$tool: the lockfile does not record the pinned npm integrity for $pkg"
  key="$cache/$tool/$version-$p"; rec="$integ $lock_sha $bunv $target $(tool_field "$tool" ".bun_runtime[\"$p\"].sha256 // empty")"
  if [ -f "$key/board.build" ] && [ "$(cut -d' ' -f1-5 "$key/board.build")" = "$rec" ] \
     && [ "$(sha "$key/$tool")" = "$(cut -d' ' -f6 "$key/board.build")" ] && [ -f "$key/LICENSE" ] && [ -f "$key/DEPENDENCY_LICENSES.txt" ] && [ -f "$key/BUN_LICENSE.md" ]; then
    echo "$key"; return 0
  fi
  need npm; need bun
  bunexe="$(fetch_bun "$tool" "$p")" || exit 1
  [ "$(bun --version)" = "$bunv" ] || die "$tool: bun $(bun --version) is installed but the manifest pins bun $bunv"
  work="$(mktemp -d "${TMPDIR:-/tmp}/abacus-board.XXXXXX")"
  lockdir="$(dirname "$lock")"
  cp "$lockdir/package.json" "$lockdir/package-lock.json" "$work/"
  ( cd "$work" && npm ci --ignore-scripts --no-audit --no-fund >/dev/null ) || { rm -rf "$work"; die "$tool: npm ci failed"; }
  [ -f "$work/node_modules/$pkg/$entry" ] || { rm -rf "$work"; die "$tool: $pkg has no $entry"; }
  bun build --compile --minify --no-compile-autoload-dotenv --no-compile-autoload-bunfig \
    --target="$target" --compile-executable-path="$bunexe" "$work/node_modules/$pkg/$entry" --outfile "$work/$tool.out" >/dev/null \
    || { rm -rf "$work"; die "$tool: bun build failed"; }
  [ -s "$work/$tool.out" ] || { rm -rf "$work"; die "$tool: bun produced no executable"; }
  mkdir -p "$key.tmp"
  cp "$work/$tool.out" "$key.tmp/$tool"
  cp "$work/node_modules/$pkg/$(tool_field "$tool" .license_source.path)" "$key.tmp/LICENSE"
  board_dependency_licenses "$work" >"$key.tmp/DEPENDENCY_LICENSES.txt"
  fetch_runtime_license "$tool" >"$key.tmp/BUN_LICENSE.md" || { rm -rf "$work" "$key.tmp"; exit 1; }
  echo "$rec $(sha "$key.tmp/$tool")" >"$key.tmp/board.build"
  rm -rf "$work" "$key"; mv "$key.tmp" "$key"
  echo "$key"
}

# board_dependency_licenses WORKDIR: every package npm installed (the whole tree is compiled in),
# with its license field and license text, from the installed packages.
board_dependency_licenses() {
  local work="$1" pj dir name ver lic f
  echo "Licenses of the npm packages compiled into the Board CLI executable (from the committed lockfile)."
  for pj in "$work"/node_modules/*/package.json "$work"/node_modules/@*/*/package.json; do
    [ -f "$pj" ] || continue
    dir="$(dirname "$pj")"
    name="$(jq -r .name "$pj")"; ver="$(jq -r .version "$pj")"
    lic="$(jq -r 'if (.license|type)=="string" then .license else (.license.type // "see its license file") end' "$pj")"
    echo
    echo "================ $name $ver ($lic) ================"
    for f in "$dir"/LICENSE* "$dir"/LICENCE* "$dir"/COPYING* "$dir"/NOTICE*; do
      [ -f "$f" ] || continue
      echo "--- $(basename "$f") ---"; cat "$f"; echo
    done
  done
}

# fetch_runtime_license TOOL: the bun runtime's license text at the pinned tag, checked against its pin.
fetch_runtime_license() {
  local tool="$1" tmp repo tag path want
  need gh
  repo="$(tool_field "$tool" .runtime_license.repo)"; tag="$(tool_field "$tool" .runtime_license.tag)"
  path="$(tool_field "$tool" .runtime_license.path)"; want="$(tool_field "$tool" .runtime_license.sha256)"
  tmp="$(mktemp)"
  gh api -H 'Accept: application/vnd.github.raw' "repos/$repo/contents/$path?ref=$tag" >"$tmp" \
    || { rm -f "$tmp"; die "$tool: cannot fetch the runtime license from $repo at $tag"; }
  [ "$(sha "$tmp")" = "$want" ] || { rm -f "$tmp"; die "$tool: the runtime license changed from its pin; review it, then re-pin"; }
  cat "$tmp"; rm -f "$tmp"
}

# install_board BINDIR LICDIR TOOL PLATFORM
install_board() {
  local bindir="$1" licdir="$2" tool="$3" p="$4" key
  key="$(ensure_board_build "$tool" "$p")"
  cp "$key/$tool" "$bindir/$tool"; chmod 0755 "$bindir/$tool"
  cp "$key/LICENSE" "$licdir/$tool/LICENSE"
  [ "$(sha "$licdir/$tool/LICENSE")" = "$(tool_field "$tool" .license_sha256)" ] || die "$tool: license changed from its pin; review it, then re-pin"
  cp "$key/DEPENDENCY_LICENSES.txt" "$licdir/$tool/DEPENDENCY_LICENSES.txt"
  cp "$key/BUN_LICENSE.md" "$licdir/$tool/BUN_LICENSE.md"
  chmod 0644 "$licdir/$tool"/*
}

# The THIRD_PARTY_NOTICES.md text, in three pieces: header, one entry per tool, footer.
notices_header() {
  echo "# Third-party notices"
  echo
  echo "The abacus program is a single file. It carries these tools inside it, unmodified, and runs"
  echo "each one as a separate program (a subprocess) after extracting it to a private folder. Each"
  echo "tool's own license text is printed by \`abacus licenses\` and is in \`third_party/<tool>/\` in"
  echo "the release archive, copied verbatim from the pinned release."
  echo
}

# notices_entry TOOL PLATFORM ASSET_PATH
notices_entry() {
  echo "## $1 $(tool_field "$1" .version)"
  echo
  echo "- License: $(tool_field "$1" .license)"
  echo "- Source: https://github.com/$(tool_field "$1" .repo)"
  if is_npm "$1"; then
    echo "- Built in as: a standalone executable compiled by bun $(tool_field "$1" .bun_version) from the npm package \`$(tool_field "$1" .package)\`"
    echo "  at the pinned integrity \`$(tool_field "$1" .npm_integrity)\`, with every npm dependency taken from a"
    echo "  committed lockfile. The executable includes the bun runtime (MIT, with LGPL-2 JavaScriptCore;"
    echo "  its license is in \`third_party/$1/BUN_LICENSE.md\`) and the npm packages whose licenses are in"
    echo "  \`third_party/$1/DEPENDENCY_LICENSES.txt\`."
  else
    echo "- Built in as: asset \`$(basename "$3")\`, sha256 \`$(tool_field "$1" ".assets[\"$2\"].sha256")\`"
  fi
  echo "- License text: \`third_party/$1/LICENSE\`"
  echo
}

# linked_notices_entry NAME: a Go module linked into the binary, with no program shipped for it.
linked_notices_entry() {
  local v module
  v="$(linked_field "$1" .version)"
  module="$(linked_field "$1" .module)"
  echo "## $1 $v (linked library)"
  echo
  echo "- License: $(linked_field "$1" .license)"
  echo "- Source: https://$module"
  echo "- Linked: the \`abacus\` binary statically links parts of $1's Go packages ($1 $v,"
  echo "  $(linked_field "$1" .linked_what)). No $1 program is shipped or run."
  echo "- License text: \`third_party/$1/LICENSE\`, copied verbatim from the Go module at that version."
  if [[ "$module" == "github.com/Dicklesworthstone/beads_viewer" ]]; then
    echo "  The $1 license adds a rider about use with OpenAI and Anthropic products; read it before"
    echo "  redistributing."
  fi
  echo
}

notices_footer() {
  echo "abacus itself is GPL-3.0-or-later (\`LICENSE\`). lambe is GPL-3.0 and built in as a separate program."
  echo
}
