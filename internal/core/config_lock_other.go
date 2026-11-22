//go:build !unix

package core

func lockConfig(string) (func(), error) { return func() {}, nil }
