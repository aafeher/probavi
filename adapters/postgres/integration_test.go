//go:build integration

package main_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aafeher/probavi/internal/adapter"
	"github.com/aafeher/probavi/internal/sandbox"
	"github.com/aafeher/probavi/internal/sandbox/docker"
)

const pgImage = "postgres:16"

// TestEndToEndRestoreDrill is the first real vertical slice: the docker
// provider, the core-side protocol client, and this adapter — as separate
// processes — prove a genuine pg_dump restorable, end to end.
func TestEndToEndRestoreDrill(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Build the adapter binary and put it on PATH under its protocol name.
	binDir := t.TempDir()
	bin := filepath.Join(binDir, "probavi-adapter-postgres")
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build adapter: %v: %s", err, out)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	provider := docker.New(nil)
	params := map[string]string{"image": pgImage, "env.POSTGRES_HOST_AUTH_METHOD": "trust"}

	// Phase A: seed a database and take a real pg_dump fixture.
	fixture := filepath.Join(t.TempDir(), "orders.dump")
	makeFixture(t, ctx, provider, params, fixture)

	// Phase B: the drill — fresh sandbox, restore through the protocol.
	sbx, err := provider.Create(ctx, params)
	if err != nil {
		t.Fatalf("create drill sandbox: %v", err)
	}
	defer destroy(t, sbx)

	runner, err := adapter.New("postgres", nil, nil)
	if err != nil {
		t.Fatalf("resolve adapter: %v", err)
	}

	probe, err := runner.Probe(ctx)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if probe.Name != "postgres" || len(probe.SQLRunner.Argv) == 0 {
		t.Fatalf("probe = %+v", probe)
	}

	res, err := runner.Provision(ctx, &adapter.ProvisionRequest{
		Source:  adapter.ProvisionSource{Kind: "pgdump", Path: fixture},
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
	// exactly how internal/checks will run checks without engine knowledge.
	argv := make([]string, 0, len(probe.SQLRunner.Argv))
	for _, a := range probe.SQLRunner.Argv {
		a = strings.ReplaceAll(a, "{{user}}", res.Connection.User)
		a = strings.ReplaceAll(a, "{{database}}", res.Connection.Database)
		a = strings.ReplaceAll(a, "{{sql}}", "SELECT count(*) FROM orders")
		argv = append(argv, a)
	}
	out, err := sbx.Exec(ctx, sandbox.ExecRequest{Argv: argv})
	if err != nil {
		t.Fatalf("sql_runner exec: %v", err)
	}
	if count := strings.TrimSpace(string(out.Stdout)); out.ExitCode != 0 || count != "1000" {
		t.Fatalf("row count = %q (exit %d), want 1000 — the restore did not carry the data", count, out.ExitCode)
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
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	binDir := t.TempDir()
	if out, err := exec.CommandContext(ctx, "go", "build", "-o",
		filepath.Join(binDir, "probavi-adapter-postgres"), ".").CombinedOutput(); err != nil {
		t.Fatalf("build adapter: %v: %s", err, out)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	corrupt := filepath.Join(t.TempDir(), "corrupt.dump")
	if err := os.WriteFile(corrupt, []byte("this is not a pg_dump archive"), 0o600); err != nil {
		t.Fatalf("write corrupt fixture: %v", err)
	}

	provider := docker.New(nil)
	sbx, err := provider.Create(ctx, map[string]string{
		"image": pgImage, "env.POSTGRES_HOST_AUTH_METHOD": "trust",
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	defer destroy(t, sbx)

	runner, err := adapter.New("postgres", nil, nil)
	if err != nil {
		t.Fatalf("resolve adapter: %v", err)
	}
	_, err = runner.Provision(ctx, &adapter.ProvisionRequest{
		Source:  adapter.ProvisionSource{Kind: "pgdump", Path: corrupt},
		Sandbox: adapter.SandboxInfo{ScratchDir: sbx.ScratchDir()},
	}, sbx)
	var aerr *adapter.Error
	if err == nil || !errors.As(err, &aerr) || aerr.Code != "source_corrupt" {
		t.Fatalf("provision error = %v, want source_corrupt", err)
	}
}

// TestPgBackRestEndToEnd proves the physical-restore path: a real
// pgBackRest repository (stanza-create + full backup on a seed cluster) is
// restored through the full stack into an idle sandbox, recovery replays
// the WAL archive, and the data comes back queryable.
func TestPgBackRestEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	image := buildPgBackRestImage(t, ctx)

	binDir := t.TempDir()
	if out, err := exec.CommandContext(ctx, "go", "build", "-o",
		filepath.Join(binDir, "probavi-adapter-postgres"), ".").CombinedOutput(); err != nil {
		t.Fatalf("build adapter: %v: %s", err, out)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	hostRepo := filepath.Join(t.TempDir(), "repo")
	makeBackRestRepo(t, ctx, image, hostRepo)

	provider := docker.New(nil)
	sbx, err := provider.Create(ctx, map[string]string{"image": image, "command": "sleep infinity"})
	if err != nil {
		t.Fatalf("create idle sandbox: %v", err)
	}
	defer destroy(t, sbx)

	runner, err := adapter.New("postgres", nil, nil)
	if err != nil {
		t.Fatalf("resolve adapter: %v", err)
	}
	res, err := runner.Provision(ctx, &adapter.ProvisionRequest{
		Source: adapter.ProvisionSource{
			Kind: "pgbackrest", Path: hostRepo, Params: map[string]string{"stanza": "demo"},
		},
		Sandbox: adapter.SandboxInfo{ScratchDir: sbx.ScratchDir()},
	}, sbx)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if res.Timings.RestoreSeconds <= 0 || res.Timings.EngineReadySeconds <= 0 {
		t.Errorf("timings = %+v, want real measurements", res.Timings)
	}

	health, err := runner.Healthcheck(ctx, &res.Connection, res.State, sbx)
	if err != nil || !health.Healthy {
		t.Fatalf("healthcheck = %+v err=%v", health, err)
	}
	out, err := sbx.Exec(ctx, sandbox.ExecRequest{Argv: []string{
		"psql", "-h", "127.0.0.1", "-U", "postgres", "-d", "postgres", "-tA", "-c",
		"SELECT count(*) FROM orders"}})
	if err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count := strings.TrimSpace(string(out.Stdout)); out.ExitCode != 0 || count != "500" {
		t.Fatalf("row count = %q (exit %d, stderr %s), want 500", count, out.ExitCode, out.Stderr)
	}
	if _, err := runner.Teardown(ctx, res.State, "completed", sbx); err != nil {
		t.Fatalf("teardown: %v", err)
	}
}

// buildPgBackRestImage builds (once, cached afterwards) a postgres image
// with pgbackrest installed — the documented requirement for the
// pgbackrest source kind.
func buildPgBackRestImage(t *testing.T, ctx context.Context) string {
	t.Helper()
	const tag = "probavi-it-pgbackrest:16"
	dir := t.TempDir()
	dockerfile := "FROM " + pgImage + "\n" +
		"RUN apt-get update && apt-get install -y --no-install-recommends pgbackrest && rm -rf /var/lib/apt/lists/*\n"
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {
		t.Fatalf("write dockerfile: %v", err)
	}
	if out, err := exec.CommandContext(ctx, "docker", "build", "-q", "-t", tag, dir).CombinedOutput(); err != nil {
		t.Fatalf("build test image: %v: %s", err, out)
	}
	return tag
}

// makeBackRestRepo seeds a real cluster in an idle container, configures
// WAL archiving into a filesystem repo, takes a full backup, and copies the
// repo to the host.
func makeBackRestRepo(t *testing.T, ctx context.Context, image, dest string) {
	t.Helper()
	// The owner-pid label carries the REAL test process: a concurrent
	// sweep must spare the live seed; if this process dies, the next
	// sweep reaps the leftover.
	out, err := exec.CommandContext(ctx, "docker", "run", "-d",
		"--label", docker.LabelSandbox+"=1", "--label", "com.probavi.pid="+strconv.Itoa(os.Getpid()),
		"--network", "none", image, "sleep", "infinity").Output()
	if err != nil {
		t.Fatalf("start seed container: %v", err)
	}
	id := strings.TrimSpace(string(out))
	defer exec.Command("docker", "rm", "-f", "-v", id).Run() //nolint:errcheck // best-effort cleanup

	seedScript := `set -e
mkdir -p /tmp/repo /etc/pgbackrest
printf '[global]\nrepo1-path=/tmp/repo\n\n[demo]\npg1-path=/var/lib/postgresql/data\n' > /etc/pgbackrest/pgbackrest.conf
chown -R postgres:postgres /tmp/repo /etc/pgbackrest /var/lib/postgresql/data
gosu postgres initdb -D /var/lib/postgresql/data
printf "archive_mode=on\narchive_command='pgbackrest --stanza=demo archive-push %%p'\n" >> /var/lib/postgresql/data/postgresql.conf
gosu postgres pg_ctl -D /var/lib/postgresql/data -w -l /tmp/pg.log start
gosu postgres psql -v ON_ERROR_STOP=1 -c "CREATE TABLE orders (id bigserial PRIMARY KEY, total numeric(10,2)); INSERT INTO orders (total) SELECT (random()*100)::numeric(10,2) FROM generate_series(1,500);"
gosu postgres pgbackrest --stanza=demo stanza-create
gosu postgres pgbackrest --stanza=demo --type=full backup
gosu postgres pg_ctl -D /var/lib/postgresql/data -w stop`
	if out, err := exec.CommandContext(ctx, "docker", "exec", id, "sh", "-c", seedScript).CombinedOutput(); err != nil {
		t.Fatalf("seed pgbackrest repo: %v: %s", err, out)
	}
	if out, err := exec.CommandContext(ctx, "docker", "cp", id+":/tmp/repo", dest).CombinedOutput(); err != nil {
		t.Fatalf("extract repo: %v: %s", err, out)
	}
}

func makeFixture(t *testing.T, ctx context.Context, provider *docker.Provider, params map[string]string, dest string) {
	t.Helper()
	seed, err := provider.Create(ctx, params)
	if err != nil {
		t.Fatalf("create seed sandbox: %v", err)
	}
	defer destroy(t, seed)

	awaitReady(t, ctx, seed)
	seedSQL := `CREATE TABLE orders (id bigserial PRIMARY KEY, total numeric(10,2) NOT NULL);
INSERT INTO orders (total) SELECT (random()*100)::numeric(10,2) FROM generate_series(1,1000);`
	mustExec(t, ctx, seed, "psql", "-h", "127.0.0.1", "-U", "postgres", "-v", "ON_ERROR_STOP=1", "-c", seedSQL)
	mustExec(t, ctx, seed, "pg_dump", "-h", "127.0.0.1", "-U", "postgres", "-Fc", "-f", "/tmp/fixture.dump", "postgres")

	// The provider deliberately has no get-file verb; pulling the fixture
	// out of the seed container is test harness work, done with the CLI.
	if out, err := exec.CommandContext(ctx, "docker", "cp", seed.ID()+":/tmp/fixture.dump", dest).CombinedOutput(); err != nil {
		t.Fatalf("extract fixture: %v: %s", err, out)
	}
}

func awaitReady(t *testing.T, ctx context.Context, sbx *docker.Sandbox) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		res, err := sbx.Exec(ctx, sandbox.ExecRequest{
			Argv: []string{"pg_isready", "-h", "127.0.0.1", "-U", "postgres", "-q"}, Timeout: 5 * time.Second,
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
