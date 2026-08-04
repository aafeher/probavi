package sandbox

import (
	"os"
	"os/exec"
	"testing"
)

func TestProcessAliveSelf(t *testing.T) {
	if !ProcessAlive(os.Getpid()) {
		t.Error("ProcessAlive(self) = false — the sweep would reclaim this drill's own sandbox")
	}
}

// TestProcessAliveAfterExit uses a process this test owns and has already
// reaped: its pid is genuinely gone, which is the case the sweep exists
// for.
func TestProcessAliveAfterExit(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait helper: %v", err)
	}
	if ProcessAlive(pid) {
		t.Errorf("ProcessAlive(%d) = true for a reaped process — orphans would never be swept", pid)
	}
}

// TestProcessAliveForeignProcess covers the EPERM path: pid 1 exists on
// every Unix and is almost never ours, so signalling it is refused. Refused
// is not gone, and treating it as gone would mean destroying a live
// sandbox.
func TestProcessAliveForeignProcess(t *testing.T) {
	if !ProcessAlive(1) {
		t.Error("ProcessAlive(1) = false — a process we may not signal is still running")
	}
}

func TestProcessAliveRejectsNonPositive(t *testing.T) {
	// 0 and -1 are wildcards for kill(2): signalling every process in the
	// group is not a question this function may ever ask.
	for _, pid := range []int{0, -1, -1000} {
		if ProcessAlive(pid) {
			t.Errorf("ProcessAlive(%d) = true, want false", pid)
		}
	}
}
