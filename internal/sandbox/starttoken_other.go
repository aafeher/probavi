//go:build !linux

package sandbox

// processStartToken has no portable answer outside Linux: macOS keeps the
// process start time behind a sysctl that needs a struct decode, which is
// a dependency this package does not want for the benefit involved.
//
// Reporting "no token" is a safe answer rather than a hole. The sweep then
// uses the pid alone — exactly what it did before, and the direction that
// leaves a sandbox behind rather than destroying a live one. Contrast the
// bug this package fixed earlier, where a Linux-only check answered "gone"
// for every process off Linux and drills deleted each other's sandboxes.
func processStartToken(int) (string, bool) { return "", false }
