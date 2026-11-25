# Local release demo acceptance

On Linux amd64, point this opt-in test at an existing trusted Abacus release
archive and its `SHA256SUMS`. It verifies the checksum entry for that archive
before extracting the binary, checks `status --demo --json` for `demo: true`,
and waits for the Board fixture to render in a private PTY before sending `q`
and checking for exit status 0. Both commands use an isolated home and XDG
directories. The TUI has a ten-second deadline.

From the repository root, set the paths to your local artifacts. The test uses
`sha256sum`, `tar`, `stty`, `timeout`, and util-linux `script`.

```sh
mkdir -p "$HOME/.cache/abacus-release-acceptance/tmp"
TMPDIR="$HOME/.cache/abacus-release-acceptance/tmp" \
  ABACUS_RELEASE_ARCHIVE=/path/to/out/abacus_VERSION_linux_amd64.tar.gz \
  ABACUS_RELEASE_SHA256SUMS=/path/to/out/SHA256SUMS \
  go test ./internal/release -run '^TestPackagedDemo$' -count=1 -v
```

The default Go suite skips this test when both environment variables are unset.
The check uses demo fixtures and reads an existing archive from disk.
