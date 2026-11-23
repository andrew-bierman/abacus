# shellcheck shell=bash
# Notices for the Go code linked into the abacus binary. Sourced by scripts/package-release.sh;
# reads only go.mod, go.sum and the local module cache (no network).
#
# The set is derived from `go list -deps` of the binary for the target platform, so it is exactly
# the modules that are linked (build-tag and GOOS specific), not everything go.mod mentions.

# go_linked_modules ROOT OS ARCH -> lines "path<TAB>version<TAB>dir", sorted, std and the main
# module excluded.
go_linked_modules() {
  local root="$1" os="$2" arch="$3"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go list -C "$root" -deps \
    -f '{{with .Module}}{{if not .Main}}{{.Path}}{{"\t"}}{{.Version}}{{"\t"}}{{.Dir}}{{end}}{{end}}' ./cmd/abacus \
    | LC_ALL=C sort -u
}

# go_licenses_section ROOT OS ARCH -> the notices text on stdout. Fails (non-zero) when the module
# list cannot be read or a linked module has no license file, so a release never ships
# incomplete notices.
go_licenses_section() {
  local root="$1" os="$2" arch="$3" mods path version dir f found
  mods="$(go_linked_modules "$root" "$os" "$arch")" || return 1
  [ -n "$mods" ] || { echo "no linked Go modules found" >&2; return 1; }

  echo "## Go modules linked into abacus"
  echo
  echo "These are the Go modules compiled into the \`abacus\` binary for $os/$arch (the output of"
  echo "\`go list -deps\`), with each module's license text verbatim."
  echo
  while IFS=$'\t' read -r path version dir; do
    [ -n "$path" ] || continue
    found=0
    echo "### $path $version"
    echo
    # Every license-like file at the module root (LICENSE, LICENSE.txt, COPYING, NOTICE, PATENTS).
    for f in "$dir"/*; do
      case "$(basename "$f")" in
        LICENSE*|LICENCE*|COPYING*|NOTICE*|PATENTS*|UNLICENSE*) [ -f "$f" ] || continue ;;
        *) continue ;;
      esac
      found=1
      echo "License file \`$(basename "$f")\`:"
      echo
      echo '```text'
      cat "$f"
      [ -z "$(tail -c1 "$f")" ] || echo
      echo '```'
      echo
    done
    [ "$found" = 1 ] || { echo "$path $version has no license file in the module cache" >&2; return 1; }
  done <<<"$mods"

  # Every Go binary also contains the Go standard library and runtime.
  local goroot
  goroot="$(go env GOROOT)" && [ -f "$goroot/LICENSE" ] || { echo "no Go LICENSE found" >&2; return 1; }
  echo "### Go standard library and runtime"
  echo
  echo "License file \`LICENSE\` of the Go toolchain:"
  echo
  echo '```text'
  cat "$goroot/LICENSE"
  [ -z "$(tail -c1 "$goroot/LICENSE")" ] || echo
  echo '```'
}
