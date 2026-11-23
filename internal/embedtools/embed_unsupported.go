//go:build embedtools && !((darwin || linux) && (amd64 || arm64))

package embedtools

// Fails the build on a platform with no embedded tool set (Windows is out of scope).
var _ int = "embedtools has no tool set for this platform"
