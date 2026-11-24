//go:build !unix

package core

// lockWorkspace has no advisory lock to take off Unix; the re-inspection right before the move
// is the only guard there.
func lockWorkspace(string) (func(), error) { return func() {}, nil }
