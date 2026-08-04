package sandbox

import (
	"errors"
	"os"
	"syscall"
)

// ProcessAlive reports whether the local process that created a sandbox is
// still running. Providers use it to tell an orphan (owner gone, safe to
// reclaim) from a sandbox a concurrent drill is still using.
//
// It asks the kernel with signal 0, which delivers nothing and only
// performs the permission and existence checks. The obvious alternative —
// stat /proc/<pid> — is Linux-only, and Probavi ships macOS binaries: with
// no /proc the check fails for every pid, every labeled sandbox looks
// orphaned, and a starting drill destroys the running sandbox of a
// concurrent one. This function has no such platform hole.
//
// EPERM means the process exists and belongs to another user, which is
// still "alive" for our purpose: reclaiming a sandbox whose owner is
// running would be the destructive answer.
//
// Caveat, unchanged from the previous implementation: a recycled pid looks
// alive, so a sandbox whose owner died can survive until its pid is free
// again. That errs toward leaving a sandbox behind rather than destroying
// a live one, which is the right direction for a sweep to be wrong in.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
