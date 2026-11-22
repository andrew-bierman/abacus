//go:build !unix

package core

import "os"

func ownerUID(os.FileInfo) (int, bool) { return 0, false }
