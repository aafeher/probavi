//go:build integration

package conformance

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestInRepoAdaptersAreConformant is the Phase 2 exit criterion's other
// half: both shipped adapters pass the frozen §10 list. No container
// runtime is involved — conformance runs against the simulated sandbox.
func TestInRepoAdaptersAreConformant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	for _, name := range []string{"postgres", "mysql"} {
		t.Run(name, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), "probavi-adapter-"+name)
			if out, err := exec.CommandContext(ctx, "go", "build", "-o", bin,
				"../../adapters/"+name).CombinedOutput(); err != nil {
				t.Fatalf("build adapter: %v: %s", err, out)
			}
			report, err := Run(ctx, bin, Options{})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			for _, c := range report.Checks {
				if !c.OK {
					t.Errorf("FAIL %s: %s", c.Name, c.Detail)
				}
			}
			if report.Failed != 0 || report.Passed != 15 {
				t.Fatalf("report: %d passed / %d failed", report.Passed, report.Failed)
			}
		})
	}
}
