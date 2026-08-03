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

	"github.com/probavi/probavi/internal/adapter"
	"github.com/probavi/probavi/internal/capabilities"
	"github.com/probavi/probavi/internal/sandbox"
	"github.com/probavi/probavi/internal/sandbox/docker"
)

// verifiedImage is the engine image adapter.json declares this adapter
// verified against. The manifest and this suite read the same value, so
// docs/capabilities.json can never claim an engine version CI does not
// actually restore from (docs/capabilities.md §1).
func verifiedImage(t *testing.T) string {
	t.Helper()
	m, err := capabilities.LoadAdapterManifest(".")
	if err != nil {
		t.Fatalf("load adapter manifest: %v", err)
	}
	image, err := m.VerifiedImage()
	if err != nil {
		t.Fatalf("adapter manifest: %v", err)
	}
	return image
}

// sandboxParams returns the documented drill-config sandbox params: the
// image runs bare — no MONGO_INITDB_* variables — so mongod starts without
// access control and without the first-boot temporary server. Zero-ingress
// sandboxes (--network none, no ports expressible) are the only reason
// that is acceptable.
func sandboxParams(t *testing.T) map[string]string {
	return map[string]string{"image": verifiedImage(t)}
}

// TestEndToEndRestoreDrill proves the third engine through the unchanged
// core: the docker provider, the core-side protocol client, and this
// adapter — as separate processes — restore genuine mongodump archives
// (plain and gzip, distinguished only by their bytes) and validate them
// through the probe-declared sql_runner's mongosh --eval bridge.
func TestEndToEndRestoreDrill(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	buildAdapterOnPath(t, ctx)
	provider := docker.New(nil)

	// Phase A: seed a database and take real mongodump fixtures — one
	// plain archive, one gzip — from the same 500 documents.
	fixtureDir := t.TempDir()
	plain := filepath.Join(fixtureDir, "orders.archive")
	gzipped := filepath.Join(fixtureDir, "orders.archive.gz")
	makeFixtures(t, ctx, provider, plain, gzipped)

	for name, fixture := range map[string]string{"plain": plain, "gzip": gzipped} {
		t.Run(name, func(t *testing.T) {
			driveDrill(t, ctx, provider, fixture)
		})
	}
}

// driveDrill runs one full drill against a fresh sandbox and validates the
// restored data through the sql_runner.
func driveDrill(t *testing.T, ctx context.Context, provider *docker.Provider, fixture string) {
	t.Helper()
	sbx, err := provider.Create(ctx, sandboxParams(t))
	if err != nil {
		t.Fatalf("create drill sandbox: %v", err)
	}
	defer destroy(t, sbx)

	runner, err := adapter.New("mongodb", nil, nil)
	if err != nil {
		t.Fatalf("resolve adapter: %v", err)
	}

	probe, err := runner.Probe(ctx)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if probe.Name != "mongodb" || len(probe.SQLRunner.Argv) == 0 {
		t.Fatalf("probe = %+v", probe)
	}

	res, err := runner.Provision(ctx, &adapter.ProvisionRequest{
		Source:  adapter.ProvisionSource{Kind: "mongodump", Path: fixture},
		Sandbox: adapter.SandboxInfo{ScratchDir: sbx.ScratchDir()},
		Options: map[string]string{"database": "probavi"},
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
	// The check text is a mongosh --eval expression: that is this
	// adapter's documented check dialect.
	argv := make([]string, 0, len(probe.SQLRunner.Argv))
	for _, a := range probe.SQLRunner.Argv {
		a = strings.ReplaceAll(a, "{{user}}", res.Connection.User)
		a = strings.ReplaceAll(a, "{{database}}", res.Connection.Database)
		a = strings.ReplaceAll(a, "{{sql}}", "db.orders.countDocuments({})")
		argv = append(argv, a)
	}
	out, err := sbx.Exec(ctx, sandbox.ExecRequest{Argv: argv})
	if err != nil {
		t.Fatalf("sql_runner exec: %v", err)
	}
	if count := strings.TrimSpace(string(out.Stdout)); out.ExitCode != 0 || count != "500" {
		t.Fatalf("document count = %q (exit %d, stderr %s), want 500 — the restore did not carry the data",
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

// TestCorruptArchiveVerdict proves a broken backup yields the right
// verdict through the whole stack, not a generic failure.
func TestCorruptArchiveVerdict(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	buildAdapterOnPath(t, ctx)

	corrupt := filepath.Join(t.TempDir(), "corrupt.archive")
	if err := os.WriteFile(corrupt, []byte("this is not a mongodump archive"), 0o600); err != nil {
		t.Fatalf("write corrupt fixture: %v", err)
	}

	provider := docker.New(nil)
	sbx, err := provider.Create(ctx, sandboxParams(t))
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	defer destroy(t, sbx)

	runner, err := adapter.New("mongodb", nil, nil)
	if err != nil {
		t.Fatalf("resolve adapter: %v", err)
	}
	_, err = runner.Provision(ctx, &adapter.ProvisionRequest{
		Source:  adapter.ProvisionSource{Kind: "mongodump", Path: corrupt},
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
		filepath.Join(binDir, "probavi-adapter-mongodb"), ".").CombinedOutput(); err != nil {
		t.Fatalf("build adapter: %v: %s", err, out)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// makeFixtures seeds 500 documents and extracts real mongodump archives
// (plain and gzip) to the host.
func makeFixtures(t *testing.T, ctx context.Context, provider *docker.Provider, plainDest, gzipDest string) {
	t.Helper()
	seed, err := provider.Create(ctx, sandboxParams(t))
	if err != nil {
		t.Fatalf("create seed sandbox: %v", err)
	}
	defer destroy(t, seed)

	awaitReady(t, ctx, seed)
	seedJS := `const docs = [];
for (let i = 1; i <= 500; i++) docs.push({_id: i, total: Math.round(Math.random()*10000)/100});
db.orders.insertMany(docs);`
	mustExec(t, ctx, seed, "mongosh", "--quiet", "--norc", "--host", "127.0.0.1", "probavi", "--eval", seedJS)
	mustExec(t, ctx, seed, "mongodump", "--host", "127.0.0.1", "--db", "probavi",
		"--archive=/tmp/fixture.archive")
	mustExec(t, ctx, seed, "mongodump", "--host", "127.0.0.1", "--db", "probavi",
		"--archive=/tmp/fixture.archive.gz", "--gzip")

	// The provider deliberately has no get-file verb; pulling the fixtures
	// out of the seed container is test harness work, done with the CLI.
	for containerPath, dest := range map[string]string{
		"/tmp/fixture.archive":    plainDest,
		"/tmp/fixture.archive.gz": gzipDest,
	} {
		if out, err := exec.CommandContext(ctx, "docker", "cp", seed.ID()+":"+containerPath, dest).CombinedOutput(); err != nil {
			t.Fatalf("extract fixture: %v: %s", err, out)
		}
	}
}

// awaitReady polls a ping until the seed engine answers commands.
func awaitReady(t *testing.T, ctx context.Context, sbx *docker.Sandbox) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for {
		res, err := sbx.Exec(ctx, sandbox.ExecRequest{
			Argv: []string{"mongosh", "--quiet", "--norc",
				"mongodb://127.0.0.1:27017/admin?serverSelectionTimeoutMS=2000&connectTimeoutMS=2000",
				"--eval", "db.runCommand({ping:1}).ok"},
			Timeout: 5 * time.Second,
		})
		if err == nil && res.ExitCode == 0 && strings.TrimSpace(string(res.Stdout)) == "1" {
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
