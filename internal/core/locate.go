package core

import (
	"fmt"
	"os"
	"strings"
)

// Locate finds a tool's binary the way a user would expect. With no override it looks the tool
// up on PATH. An override containing a path separator is checked on disk. A bare-name override
// (for example ABACUS_LAMBE_BIN=lambe) is resolved on PATH, never against the working directory.
// A non-empty detail means the tool is absent and says why; the bin is then empty.
func Locate(tool, explicit string, look func(string) (string, error)) (bin, detail string) {
	switch {
	case explicit == "":
		explicit = tool
	case strings.ContainsRune(explicit, os.PathSeparator) || strings.ContainsRune(explicit, '/'):
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Sprintf("%s not found at %s", tool, explicit)
		}
		return explicit, ""
	}
	p, err := look(explicit)
	if err != nil {
		return "", fmt.Sprintf("%s not found on PATH", tool)
	}
	return p, ""
}
