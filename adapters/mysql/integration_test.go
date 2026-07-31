//go:build integration

package main_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aafeher/probavi/internal/adapter"
	"github.com/aafeher/probavi/internal/sandbox"
	"github.com/aafeher/probavi/internal/sandbox/docker"
)

const mysqlImage = "mysql:8.4"

// sandboxParams returns the documented drill-config sandbox params: an
// empty root password is acceptable only because the sandbox has zero
// ingress (--network none, no ports expressible).
func sandboxParams() map[string]string {
	return map[string]string{"image": mysqlImage, "env.MYSQL_ALLOW_EMPTY_PASSWORD": "yes"}
}

// TestEndToEndRestoreDrill proves the second engine through the unchanged
// core: the docker provider, the core-side protocol client, and this
// adapter — as separate processes — restore a genuine mysqldump and
// validate it through the probe-declared sql_runner, including the
// ANSI_QUOTES bridge for the core's SQL-standard quoted identifiers.
func TestEndToEndRestoreDrill(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Build the adapter binary and put it on PATH under its protocol name.
	binDir := t.TempDir()
	bin := filepath.Join(binDir, "probavi-adapter-mysql")
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build adapter: %v: %s", err, out)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	provider := docker.New(nil)

	// Phase A: seed a database and take a real mysqldump fixture.
	fixture := filepath.Join(t.TempDir(), "orders.sql")
	makeFixture(t, ctx, provider, fixture)

	// Phase B: the drill — fresh sandbox, restore through the protocol.
	sbx, err := provider.Create(ctx, sandboxParams())
	if err != nil {
		t.Fatalf("create drill sandbox: %v", err)
	}
	defer destroy(t, sbx)

	runner, err := adapter.New("mysql", nil, nil)
	if err != nil {
		t.Fatalf("resolve adapter: %v", err)
	}

	probe, err := runner.Probe(ctx)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if probe.Name != "mysql" || len(probe.SQLRunner.Argv) == 0 {
		t.Fatalf("probe = %+v", probe)
	}

	// No options: the defaults (root, probavi) must carry the drill, and
	// the seed dumped exactly the default database name.
	res, err := runner.Provision(ctx, &adapter.ProvisionRequest{
		Source:  adapter.ProvisionSource{Kind: "mysqldump", Path: fixture},
		Sandbox: adapter.SandboxInfo{ScratchDir: sbx.ScratchDir()},
	}, sbx)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if res.Timings.RestoreSeconds <= 0 || res.Timings.EngineReadySeconds <= 0 {
		t.Errorf("timings = %+v, want real measurements", res.Timings)
	}
	if !strings.HasPrefix(res.SourceIdentity.Checksum, "sha256:") || res.SourceIdentity.SizeBytes == 0 {
		t.Errorf("source identity = %+v", res.SourceIdentity)
	}

	health, err := runner.Healthcheck(ctx, &res.Connection, res.State, sbx)
	if err != nil {
		t.Fatalf("healthcheck: %v", err)
	}
	if !health.Healthy {
		t.Fatalf("healthcheck = %+v, want healthy", health)
	}

	// Validate the restored data through the probe-declared sql_runner —
	// exactly how internal/checks runs checks without engine knowledge.
	// The double-quoted identifier is the point: the core emits
	// SQL-standard quoting, and the declared template must make the
	// engine accept it.
	argv := make([]string, 0, len(probe.SQLRunner.Argv))
	for _, a := range probe.SQLRunner.Argv {
		a = strings.ReplaceAll(a, "{{user}}", res.Connection.User)
		a = strings.ReplaceAll(a, "{{database}}", res.Connection.Database)
		a = strings.ReplaceAll(a, "{{sql}}", `SELECT count(*) FROM "orders"`)
		argv = append(argv, a)
	}
	out, err := sbx.Exec(ctx, sandbox.ExecRequest{Argv: argv})
	if err != nil {
		t.Fatalf("sql_runner exec: %v", err)
	}
	if count := strings.TrimSpace(string(out.Stdout)); out.ExitCode != 0 || count != "500" {
		t.Fatalf("row count = %q (exit %d, stderr %s), want 500 — the restore did not carry the data",
			count, out.ExitCode, out.Stderr)
	}

	teardown, err := runner.Teardown(ctx, res.State, "completed", sbx)
	if err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if !teardown.Released {
		t.Errorf("teardown = %+v", teardown)
	}
}

// TestCorruptDumpVerdict proves a broken backup yields the right verdict
// through the whole stack, not a generic failure.
func TestCorruptDumpVerdict(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	binDir := t.TempDir()
	if out, err := exec.CommandContext(ctx, "go", "build", "-o",
		filepath.Join(binDir, "probavi-adapter-mysql"), ".").CombinedOutput(); err != nil {
		t.Fatalf("build adapter: %v: %s", err, out)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	corrupt := filepath.Join(t.TempDir(), "corrupt.sql")
	if err := os.WriteFile(corrupt, []byte("this is not a mysql dump"), 0o600); err != nil {
		t.Fatalf("write corrupt fixture: %v", err)
	}

	provider := docker.New(nil)
	sbx, err := provider.Create(ctx, sandboxParams())
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	defer destroy(t, sbx)

	runner, err := adapter.New("mysql", nil, nil)
	if err != nil {
		t.Fatalf("resolve adapter: %v", err)
	}
	_, err = runner.Provision(ctx, &adapter.ProvisionRequest{
		Source:  adapter.ProvisionSource{Kind: "mysqldump", Path: corrupt},
		Sandbox: adapter.SandboxInfo{ScratchDir: sbx.ScratchDir()},
	}, sbx)
	var aerr *adapter.Error
	if err == nil || !errors.As(err, &aerr) || aerr.Code != "source_corrupt" {
		t.Fatalf("provision error = %v, want source_corrupt", err)
	}
}

// makeFixture seeds the default database with 500 rows and extracts a real
// mysqldump file to the host.
func makeFixture(t *testing.T, ctx context.Context, provider *docker.Provider, dest string) {
	t.Helper()
	seed, err := provider.Create(ctx, sandboxParams())
	if err != nil {
		t.Fatalf("create seed sandbox: %v", err)
	}
	defer destroy(t, seed)

	awaitReady(t, ctx, seed)
	seedSQL := `CREATE DATABASE probavi;
USE probavi;
CREATE TABLE orders (id BIGINT AUTO_INCREMENT PRIMARY KEY, total DECIMAL(10,2) NOT NULL);
INSERT INTO orders (total)
WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n < 500)
SELECT ROUND(RAND()*100, 2) FROM seq;`
	mustExec(t, ctx, seed, "mysql", "-h", "127.0.0.1", "-u", "root", "-e", seedSQL)
	mustExec(t, ctx, seed, "mysqldump", "-h", "127.0.0.1", "-u", "root",
		"--result-file=/tmp/fixture.sql", "probavi")

	// The provider deliberately has no get-file verb; pulling the fixture
	// out of the seed container is test harness work, done with the CLI.
	if out, err := exec.CommandContext(ctx, "docker", "cp", seed.ID()+":/tmp/fixture.sql", dest).CombinedOutput(); err != nil {
		t.Fatalf("extract fixture: %v: %s", err, out)
	}
}

// awaitReady polls a TCP SELECT 1 until the seed engine serves queries.
// The first boot initializes the datadir, which takes markedly longer than
// postgres — hence the generous deadline.
func awaitReady(t *testing.T, ctx context.Context, sbx *docker.Sandbox) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for {
		res, err := sbx.Exec(ctx, sandbox.ExecRequest{
			Argv:    []string{"mysql", "-h", "127.0.0.1", "-u", "root", "-N", "-B", "-e", "SELECT 1"},
			Timeout: 5 * time.Second,
		})
		if err == nil && res.ExitCode == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("seed engine never became ready")
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func mustExec(t *testing.T, ctx context.Context, sbx *docker.Sandbox, argv ...string) {
	t.Helper()
	res, err := sbx.Exec(ctx, sandbox.ExecRequest{Argv: argv})
	if err != nil {
		t.Fatalf("exec %v: %v", argv, err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exec %v: exit %d: %s", argv, res.ExitCode, res.Stderr)
	}
}

func destroy(t *testing.T, sbx *docker.Sandbox) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := sbx.Destroy(ctx); err != nil {
		t.Errorf("destroy sandbox: %v", err)
	}
}
