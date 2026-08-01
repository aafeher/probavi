//go:build integration

package main_test

import (
	"context"
	"crypto/rand"
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

const (
	mssqlImage = "mcr.microsoft.com/mssql/server:2022-latest"
	// seedPassword is only for the throwaway seed engine this test starts
	// to produce a fixture; the drill engine uses the adapter's documented
	// sandbox constant.
	seedPassword = "Probavi!Seed0"
)

// sandboxParams returns the documented drill-config sandbox params: the
// image starts idle (SQL Server cannot run without a superuser password,
// and a password in sandbox params would enter the signed evidence record
// — so the adapter starts and owns the engine).
func sandboxParams() map[string]string {
	return map[string]string{"image": mssqlImage, "command": "sleep infinity"}
}

// TestEndToEndRestoreDrill proves the fourth engine through the unchanged
// core: the docker provider, the core-side protocol client, and this
// adapter — as separate processes — restore a genuine BACKUP DATABASE
// artifact under a new name with server-side MOVEs, and validate it
// through the probe-declared sql_runner (QUOTED_IDENTIFIER + NOCOUNT
// bridges included). It also exercises put_file against a non-root image
// user for the first time.
func TestEndToEndRestoreDrill(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	buildAdapterOnPath(t, ctx)
	provider := docker.New(nil)

	fixture := filepath.Join(t.TempDir(), "shop.bak")
	makeFixture(t, ctx, provider, fixture)

	sbx, err := provider.Create(ctx, sandboxParams())
	if err != nil {
		t.Fatalf("create drill sandbox: %v", err)
	}
	defer destroy(t, sbx)

	runner, err := adapter.New("mssql", nil, nil)
	if err != nil {
		t.Fatalf("resolve adapter: %v", err)
	}

	probe, err := runner.Probe(ctx)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if probe.Name != "mssql" || len(probe.SQLRunner.Argv) == 0 {
		t.Fatalf("probe = %+v", probe)
	}

	res, err := runner.Provision(ctx, &adapter.ProvisionRequest{
		Source:  adapter.ProvisionSource{Kind: "bak", Path: fixture},
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
	if res.Connection.Database != "probavi" || res.Connection.User != "sa" {
		t.Errorf("connection = %+v, want sa on the default restore target", res.Connection)
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
	// The double-quoted identifier pins the -I bridge; the clean numeric
	// output pins the SQLCMDINI NOCOUNT bridge.
	argv := make([]string, 0, len(probe.SQLRunner.Argv))
	for _, a := range probe.SQLRunner.Argv {
		a = strings.ReplaceAll(a, "{{user}}", res.Connection.User)
		a = strings.ReplaceAll(a, "{{database}}", res.Connection.Database)
		a = strings.ReplaceAll(a, "{{sql}}", `SELECT count(*) FROM "orders"`)
		argv = append(argv, a)
	}
	out, err := sbx.Exec(ctx, sandbox.ExecRequest{Argv: argv, Env: probe.SQLRunner.Env})
	if err != nil {
		t.Fatalf("sql_runner exec: %v", err)
	}
	if count := strings.TrimSpace(string(out.Stdout)); out.ExitCode != 0 || count != "500" {
		t.Fatalf("row count = %q (exit %d, stderr %s), want exactly 500 with no decoration",
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

// TestCorruptBakVerdict proves a broken backup yields the right verdict
// through the whole stack, not a generic failure.
func TestCorruptBakVerdict(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	buildAdapterOnPath(t, ctx)

	corrupt := filepath.Join(t.TempDir(), "corrupt.bak")
	garbage := make([]byte, 64*1024)
	if _, err := rand.Read(garbage); err != nil {
		t.Fatalf("garbage: %v", err)
	}
	if err := os.WriteFile(corrupt, garbage, 0o600); err != nil {
		t.Fatalf("write corrupt fixture: %v", err)
	}

	provider := docker.New(nil)
	sbx, err := provider.Create(ctx, sandboxParams())
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	defer destroy(t, sbx)

	runner, err := adapter.New("mssql", nil, nil)
	if err != nil {
		t.Fatalf("resolve adapter: %v", err)
	}
	_, err = runner.Provision(ctx, &adapter.ProvisionRequest{
		Source:  adapter.ProvisionSource{Kind: "bak", Path: corrupt},
		Sandbox: adapter.SandboxInfo{ScratchDir: sbx.ScratchDir()},
	}, sbx)
	var aerr *adapter.Error
	if err == nil || !errors.As(err, &aerr) || aerr.Code != "source_corrupt" {
		t.Fatalf("provision error = %v, want source_corrupt", err)
	}
}

// buildAdapterOnPath builds the adapter binary and puts it on PATH under
// its protocol name.
func buildAdapterOnPath(t *testing.T, ctx context.Context) {
	t.Helper()
	binDir := t.TempDir()
	if out, err := exec.CommandContext(ctx, "go", "build", "-o",
		filepath.Join(binDir, "probavi-adapter-mssql"), ".").CombinedOutput(); err != nil {
		t.Fatalf("build adapter: %v: %s", err, out)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// makeFixture seeds a throwaway engine with 500 rows and extracts a real
// BACKUP DATABASE artifact to the host.
func makeFixture(t *testing.T, ctx context.Context, provider *docker.Provider, dest string) {
	t.Helper()
	seed, err := provider.Create(ctx, sandboxParams())
	if err != nil {
		t.Fatalf("create seed sandbox: %v", err)
	}
	defer destroy(t, seed)

	startEngine(t, ctx, seed, seedPassword)
	awaitReady(t, ctx, seed, seedPassword)
	seedSQL := []string{
		"CREATE DATABASE shop",
		"CREATE TABLE shop.dbo.orders (id INT IDENTITY PRIMARY KEY, total DECIMAL(10,2) NOT NULL)",
		"INSERT INTO shop.dbo.orders (total) SELECT TOP 500 ROUND(RAND(CHECKSUM(NEWID()))*100,2) FROM sys.all_columns",
		"BACKUP DATABASE shop TO DISK = N'/tmp/shop.bak'",
	}
	for _, stmt := range seedSQL {
		mustSQL(t, ctx, seed, seedPassword, stmt)
	}

	// The provider deliberately has no get-file verb; pulling the fixture
	// out of the seed container is test harness work, done with the CLI.
	if out, err := exec.CommandContext(ctx, "docker", "cp", seed.ID()+":/tmp/shop.bak", dest).CombinedOutput(); err != nil {
		t.Fatalf("extract fixture: %v: %s", err, out)
	}
}

// startEngine launches sqlservr in an idle container the same way the
// adapter does.
func startEngine(t *testing.T, ctx context.Context, sbx *docker.Sandbox, password string) {
	t.Helper()
	res, err := sbx.Exec(ctx, sandbox.ExecRequest{
		Argv: []string{"sh", "-c", "nohup /opt/mssql/bin/sqlservr >/tmp/seed-sqlservr.log 2>&1 &"},
		Env:  map[string]string{"ACCEPT_EULA": "Y", "MSSQL_SA_PASSWORD": password},
	})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("start seed engine: %+v, %v", res, err)
	}
}

func awaitReady(t *testing.T, ctx context.Context, sbx *docker.Sandbox, password string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for {
		res, err := sbx.Exec(ctx, sandbox.ExecRequest{
			Argv: []string{"/opt/mssql-tools18/bin/sqlcmd", "-S", "127.0.0.1,1433", "-U", "sa",
				"-C", "-b", "-l", "2", "-h", "-1", "-Q", "SELECT 1"},
			Env:     map[string]string{"SQLCMDPASSWORD": password},
			Timeout: 10 * time.Second,
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

func mustSQL(t *testing.T, ctx context.Context, sbx *docker.Sandbox, password, stmt string) {
	t.Helper()
	res, err := sbx.Exec(ctx, sandbox.ExecRequest{
		Argv: []string{"/opt/mssql-tools18/bin/sqlcmd", "-S", "127.0.0.1,1433", "-U", "sa",
			"-C", "-b", "-Q", stmt},
		Env: map[string]string{"SQLCMDPASSWORD": password},
	})
	if err != nil {
		t.Fatalf("sql %q: %v", stmt, err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("sql %q: exit %d: %s%s", stmt, res.ExitCode, res.Stdout, res.Stderr)
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
