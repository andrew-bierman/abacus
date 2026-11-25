#!/usr/bin/env bash
# Install the latest Abacus terminal release into ~/.local/bin.
set -euo pipefail

die() {
  printf 'Abacus install: %s\n' "$*" >&2
  exit 1
}

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) die "releases are available for Linux and macOS" ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) die "releases are available for x86-64 and ARM64" ;;
esac

bin_dir="${HOME:?}/.local/bin"
target="$bin_dir/abacus"
mkdir -p "$bin_dir"
[ ! -d "$target" ] || die "$target is a directory"
stage="$(mktemp -d "$bin_dir/.abacus-install.XXXXXXXX")"
trap 'rm -rf "$stage"' EXIT

releases=https://github.com/andrew-bierman/abacus/releases
download() {
  curl --disable --proto '=https' --proto-redir '=https' --fail --silent --show-error --location --retry 2 "$@"
}

download "$releases/latest/download/SHA256SUMS" -o "$stage/SHA256SUMS"
# Select a single manifest entry, then pin the archive URL to that entry's version.
entry="$(awk -v suffix="_${os}_${arch}.tar.gz" '
  NF == 2 && length($1) == 64 && $1 ~ /^[[:xdigit:]]+$/ &&
    substr($2, length($2) - length(suffix) + 1) == suffix { print tolower($1), $2 }
' "$stage/SHA256SUMS")"
[[ -n "$entry" && "$entry" != *$'\n'* ]] || die "expected one checksum for $os/$arch"
read -r expected archive <<< "$entry"
[[ "$archive" =~ ^abacus_([0-9]+\.[0-9]+\.[0-9]+)_${os}_${arch}\.tar\.gz$ ]] ||
  die "unrecognized release archive name"
version="${BASH_REMATCH[1]}"
printf 'Downloading Abacus %s for %s/%s…\n' "$version" "$os" "$arch"
download "$releases/download/v$version/$archive" -o "$stage/$archive"

if command -v sha256sum >/dev/null 2>&1; then
  checksum="$(sha256sum < "$stage/$archive")"
else
  checksum="$(shasum -a 256 < "$stage/$archive")"
fi
[ "${checksum%% *}" = "$expected" ] || die "checksum mismatch for $archive"

# Existing releases contain harmless macOS metadata that GNU tar prints for each member.
if [[ "$(tar --version 2>/dev/null || true)" == "tar (GNU tar)"* ]]; then
  tar --warning=no-unknown-keyword -xOzf "$stage/$archive" "${archive%.tar.gz}/abacus" > "$stage/abacus"
else
  tar -xOzf "$stage/$archive" "${archive%.tar.gz}/abacus" > "$stage/abacus"
fi
[ -s "$stage/abacus" ] || die "release contains an empty binary"
chmod 0755 "$stage/abacus"
mv -f "$stage/abacus" "$target"
printf 'Installed Abacus %s at %s\n' "$version" "$target"

if [ "$(command -v abacus || true)" = "$target" ]; then
  printf 'Try: abacus tui --demo\n'
else
  cat <<'PATH_HELP'

Put Abacus first on PATH, then try the demo:

  export PATH="$HOME/.local/bin:$PATH"
  abacus tui --demo

Add that export to your shell startup file to keep it for future sessions.
PATH_HELP
fi
