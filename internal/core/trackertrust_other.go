//go:build !unix

package core

import "os"

// UntrustedDir has no ownership model off unix: nothing is refused.
func UntrustedDir(os.FileInfo, int) string { return "" }
