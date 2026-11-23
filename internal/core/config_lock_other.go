//go:build !unix

package core

import "time"

func lockConfig(string) (func(), error) { return func() {}, nil }

func lockWithin(string, time.Duration) (func(), error) { return func() {}, nil }
