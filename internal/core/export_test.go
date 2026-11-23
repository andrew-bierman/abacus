package core

import "time"

// SnippetAround exposes the excerpt function to the external tests.
var SnippetAround = snippetAround

// Test seams for the built-in tool cache.

// PruneForTest runs the pruning step of the current embedded set.
func PruneForTest() {
	if s := embeddedSetNow(); s != nil {
		root, _ := ToolsDir()
		s.pruneOld(root)
	}
}

// SetPruneForTest sets the staleness window and the number of newest other versions always kept.
func SetPruneForTest(window time.Duration, keep int) (restore func()) {
	w, k := pruneWindow, keepPrev
	pruneWindow, keepPrev = window, keep
	return func() { pruneWindow, keepPrev = w, k }
}

// SetLockTimeoutForTest shortens how long extraction waits for another abacus.
func SetLockTimeoutForTest(d time.Duration) (restore func()) {
	o := lockTimeout
	lockTimeout = d
	return func() { lockTimeout = o }
}

// SetTmpMaxAgeForTest sets how old a .tmp- leftover must be to be reaped.
func SetTmpMaxAgeForTest(d time.Duration) (restore func()) {
	o := tmpMaxAge
	tmpMaxAge = d
	return func() { tmpMaxAge = o }
}

// UsedStamp is the name of the last-used stamp inside an extraction directory.
const UsedStamp = usedStamp
