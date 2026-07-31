//go:build integration

package docker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aafeher/probavi/internal/sandbox"
)

// testImage must stay running with its default entrypoint (like a real
// database image does) and must carry a busybox shell for exec assertions.
const testImage = "nginx:1.27-alpine"

// TestDockerLifecycle drives a real container through the full provider
// contract: create, exec (plain, stdin, exit codes), put_file, isolation,
// idempotent destroy.
func TestDockerLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	p := New(nil)

	sbx, err := p.Create(ctx, map[string]string{"image": testImage, "env.PROBAVI_TEST": "1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	destroyed := false
	defer func() {
		if !destroyed {
			dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer dcancel()
			_ = sbx.Destroy(dctx)
		}
	}()

	res, err := sbx.Exec(ctx, sandbox.ExecRequest{Argv: []string{"echo", "hello"}})
	if err != nil {
		t.Fatalf("Exec echo: %v", err)
	}
	if res.ExitCode != 0 || strings.TrimSpace(string(res.Stdout)) != "hello" {
		t.Errorf("echo: exit=%d out=%q", res.ExitCode, res.Stdout)
	}

	res, err = sbx.Exec(ctx, sandbox.ExecRequest{Argv: []string{"cat"}, Stdin: []byte("piped-data")})
	if err != nil {
		t.Fatalf("Exec cat: %v", err)
	}
	if string(res.Stdout) != "piped-data" {
		t.Errorf("stdin roundtrip = %q", res.Stdout)
	}

	res, err = sbx.Exec(ctx, sandbox.ExecRequest{Argv: []string{"sh", "-c", "exit 7"}})
	if err != nil {
		t.Fatalf("Exec exit 7: %v", err)
	}
	if res.ExitCode != 7 {
		t.Errorf("exit code = %d, want 7", res.ExitCode)
	}

	res, err = sbx.Exec(ctx, sandbox.ExecRequest{Argv: []string{"printenv", "PROBAVI_TEST"}})
	if err != nil || strings.TrimSpace(string(res.Stdout)) != "1" {
		t.Errorf("env param did not reach the container: out=%q err=%v", res.Stdout, err)
	}

	// Zero-ingress default: network none leaves loopback only.
	res, err = sbx.Exec(ctx, sandbox.ExecRequest{Argv: []string{"ls", "/sys/class/net"}})
	if err != nil {
		t.Fatalf("Exec ls net: %v", err)
	}
	if ifaces := strings.Fields(string(res.Stdout)); !slices.Equal(ifaces, []string{"lo"}) {
		t.Errorf("interfaces = %v, want [lo] — the default must be zero network exposure", ifaces)
	}

	hostFile := filepath.Join(t.TempDir(), "payload.bin")
	if err := os.WriteFile(hostFile, []byte("payload-bytes"), 0o600); err != nil {
		t.Fatalf("write host file: %v", err)
	}
	pf, err := sbx.PutFile(ctx, hostFile, sbx.ScratchDir()+"/payload.bin", "0640")
	if err != nil {
		t.Fatalf("PutFile: %v", err)
	}
	if pf.BytesCopied != int64(len("payload-bytes")) {
		t.Errorf("BytesCopied = %d", pf.BytesCopied)
	}
	res, err = sbx.Exec(ctx, sandbox.ExecRequest{Argv: []string{"sh", "-c", "cat /tmp/payload.bin && stat -c %a /tmp/payload.bin"}})
	if err != nil {
		t.Fatalf("Exec readback: %v", err)
	}
	if !strings.Contains(string(res.Stdout), "payload-bytes") || !strings.Contains(string(res.Stdout), "640") {
		t.Errorf("readback = %q, want content and mode 640", res.Stdout)
	}

	if err := sbx.Destroy(ctx); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if err := sbx.Destroy(ctx); err != nil {
		t.Fatalf("second Destroy must be idempotent: %v", err)
	}
	destroyed = true
}

// TestSweepOrphansReal verifies the sweep removes dead-owner containers and
// spares live-owner ones, against a real daemon.
func TestSweepOrphansReal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p := New(nil)

	// A live sandbox owned by this test process.
	live, err := p.Create(ctx, map[string]string{"image": testImage})
	if err != nil {
		t.Fatalf("Create live sandbox: %v", err)
	}
	defer func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dcancel()
		_ = live.Destroy(dctx)
	}()

	// An orphan: same label, owner pid that cannot exist.
	out, err := exec.CommandContext(ctx, "docker", "run", "-d",
		"--label", LabelSandbox+"=1", "--label", labelPID+"=2147483646",
		"--network", "none", testImage, "sleep", "60").Output()
	if err != nil {
		t.Fatalf("start orphan container: %v", err)
	}
	orphanID := strings.TrimSpace(string(out))
	defer exec.Command("docker", "rm", "-f", "-v", orphanID).Run() //nolint:errcheck // best-effort cleanup if the sweep failed

	removed, err := p.SweepOrphans(ctx)
	if err != nil {
		t.Fatalf("SweepOrphans: %v", err)
	}
	removedOrphan := false
	for _, id := range removed {
		if strings.HasPrefix(orphanID, id) || strings.HasPrefix(id, orphanID) {
			removedOrphan = true
		}
		if strings.HasPrefix(live.ID(), id) || strings.HasPrefix(id, live.ID()) {
			t.Errorf("sweep removed the live sandbox %s", live.ID())
		}
	}
	if !removedOrphan {
		t.Errorf("sweep did not remove the orphan %s (removed: %v)", orphanID, removed)
	}
}
