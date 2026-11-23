//go:build !unix

package core

import (
	"context"
	"os"
	"time"
)

func tryFlock(path string, exclusive bool) (*os.File, bool, error) { return nil, true, nil }
func closeLock(*os.File)                                           {}
func lockWait(string, time.Duration) (func(), error)               { return func() {}, nil }
func holdInUse(string)                                             {}
func interruptContext(p context.Context) (context.Context, func()) { return p, func() {} }
