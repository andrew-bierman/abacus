//go:build !unix

package core

import "time"

func lockTree(string, time.Duration) (func(), error) { return func() {}, nil }
