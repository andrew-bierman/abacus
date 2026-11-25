//go:build windows

package cli

// ignoreSIGPIPE: Windows has no SIGPIPE; a closed pipe is a write error.
func ignoreSIGPIPE() {}
