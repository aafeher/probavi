//go:build integration

package main_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFullDrillViaCLI is the README quickstart as a test: build the real
// binaries, generate keys, run a drill against a real pg_dump in a real
// Docker sandbox, then verify the evidence log offline — including a
// second, failing drill chained onto the same log.
func TestFullDrillViaCLI(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	work := t.TempDir()

	probavi := build(t, ctx, work, "probavi", ".")
	build(t, ctx, work, "probavi-adapter-postgres", "../../adapters/postgres")
	t.Setenv("PATH", work+string(os.PathListSeparator)+os.Getenv("PATH"))

	fixture := filepath.Join(work, "orders.dump")
	makeFixture(t, ctx, fixture)

	keyPath := filepath.Join(work, "ed25519.key")
	mustRun(t, ctx, probavi, "evidence", "keygen", "--out", keyPath)

	logPath := filepath.Join(work, "evidence.jsonl")
	metricsPath := filepath.Join(work, "probavi.prom")
	configPath := writeDrillConfig(t, work, fixture, logPath, keyPath, metricsPath)

	// Drill 1: a healthy backup must prove restorable, exit 0.
	out := mustRun(t, ctx, probavi, "run", "--config", configPath)
	summary := struct {
		Outcome      string `json:"outcome"`
		Seq          int64  `json:"seq"`
		ChecksPassed int    `json:"checks_passed"`
		ChecksTotal  int    `json:"checks_total"`
		RestoreMS    *int64 `json:"restore_ms"`
	}{}
	if err := json.Unmarshal([]byte(out), &summary); err != nil {
		t.Fatalf("run summary is not JSON: %v (%q)", err, out)
	}
	if summary.Outcome != "pass" || summary.Seq != 1 || summary.ChecksPassed != summary.ChecksTotal ||
		summary.RestoreMS == nil || *summary.RestoreMS <= 0 {
		t.Fatalf("summary = %+v, want a passing drill with measured restore time", summary)
	}
	if raw, err := os.ReadFile(metricsPath); err != nil ||
		!strings.Contains(string(raw), "probavi_last_success_timestamp_seconds") ||
		!strings.Contains(string(raw), `probavi_restore_duration_rolling_seconds`) ||
		!strings.Contains(string(raw), `quantile="0.95"`) ||
		!strings.Contains(string(raw), "probavi_restore_trend_samples") {
		t.Errorf("metrics file must carry the last-run and rolling-trend series: err=%v content=%s", err, raw)
	}

	// Drill 2: a corrupt backup must fail with exit 1 — and still leave a
	// signed record on the same chain.
	corrupt := filepath.Join(work, "corrupt.dump")
	if err := os.WriteFile(corrupt, []byte("not an archive"), 0o600); err != nil {
		t.Fatalf("write corrupt dump: %v", err)
	}
	corruptConfig := writeDrillConfig(t, t.TempDir(), corrupt, logPath, keyPath, metricsPath)
	out, code := run(t, ctx, probavi, "run", "--config", corruptConfig)
	if code != 1 {
		t.Fatalf("corrupt drill exit = %d (%s), want 1 — a bad backup is a recoverability failure", code, out)
	}

	// Offline verification: two records, chained, VALID, exit 0.
	out = mustRun(t, ctx, probavi, "evidence", "verify", "--log", logPath, "--key", keyPath+".pub")
	verify := struct {
		Status  string `json:"status"`
		Records int    `json:"records"`
	}{}
	if err := json.Unmarshal([]byte(out), &verify); err != nil {
		t.Fatalf("verify output: %v", err)
	}
	if verify.Status != "VALID" || verify.Records != 2 {
		t.Fatalf("verify = %+v, want VALID with 2 records", verify)
	}

	// No ORPHANED sandbox may survive a drill: containers whose owner
	// process is dead. Live-owner containers belong to integration tests
	// of other packages running in parallel and must be tolerated.
	for _, id := range strings.Fields(dockerOut(t, ctx, "ps", "-aq", "--filter", "label=com.probavi.sandbox=1")) {
		out, err := exec.CommandContext(ctx, "docker", "inspect", "-f",
			`{{ index .Config.Labels "com.probavi.pid" }}`, id).Output()
		if err != nil {
			continue // vanished between ps and inspect — that IS the cleanup working
		}
		pid := strings.TrimSpace(string(out))
		if _, err := os.Stat("/proc/" + pid); err != nil {
			t.Errorf("orphaned sandbox %s (dead owner pid %s) survived the drill", id, pid)
		}
	}

	// probavi adapter probe round-trips through the real adapter binary.
	out = mustRun(t, ctx, probavi, "adapter", "probe", "postgres")
	if !strings.Contains(out, `"name":"postgres"`) {
		t.Errorf("adapter probe output: %s", out)
	}
}

func writeDrillConfig(t *testing.T, dir, source, logPath, keyPath, metricsPath string) string {
	t.Helper()
	cfg := fmt.Sprintf(`target:
  name: cli-e2e-drill
  adapter: postgres
  source:
    kind: pgdump
    path: %s
sandbox:
  provider: docker
  params:
    image: postgres:16
    env.POSTGRES_HOST_AUTH_METHOD: trust
  timeout: 5m
checks:
  - builtin: service_healthy
  - builtin: table_exists
    table: orders
  - builtin: row_count
    table: orders
    min: 1000
    max: 1000
  - name: no-negative-totals
    sql: "SELECT count(*) FROM orders WHERE total < 0"
    expect: 0
evidence:
  path: %s
  sign_key: %s
metrics:
  prometheus_textfile: %s
`, source, logPath, keyPath, metricsPath)
	path := filepath.Join(dir, "drill.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write drill config: %v", err)
	}
	return path
}

func makeFixture(t *testing.T, ctx context.Context, dest string) {
	t.Helper()
	id := dockerOut(t, ctx, "run", "-d", "--label", "com.probavi.test-seed=1",
		"-e", "POSTGRES_HOST_AUTH_METHOD=trust", "postgres:16")
	defer func() { _ = exec.Command("docker", "rm", "-f", "-v", id).Run() }() //nolint:errcheck // best-effort cleanup

	deadline := time.Now().Add(2 * time.Minute)
	for {
		if err := exec.CommandContext(ctx, "docker", "exec", id,
			"pg_isready", "-h", "127.0.0.1", "-U", "postgres", "-q").Run(); err == nil {
			break
		}
		if time.Now().After(deadline) {
			logs, lerr := exec.CommandContext(ctx, "docker", "logs", "--tail", "30", id).CombinedOutput()
			t.Fatalf("seed engine never became ready (docker logs err=%v):\n%s", lerr, logs)
		}
		time.Sleep(500 * time.Millisecond)
	}
	dockerOut(t, ctx, "exec", id, "psql", "-h", "127.0.0.1", "-U", "postgres", "-v", "ON_ERROR_STOP=1", "-c",
		`CREATE TABLE orders (id bigserial PRIMARY KEY, total numeric(10,2) NOT NULL);
INSERT INTO orders (total) SELECT (random()*100)::numeric(10,2) FROM generate_series(1,1000);`)
	dockerOut(t, ctx, "exec", id, "pg_dump", "-h", "127.0.0.1", "-U", "postgres", "-Fc", "-f", "/tmp/f.dump", "postgres")
	dockerOut(t, ctx, "cp", id+":/tmp/f.dump", dest)
}

func build(t *testing.T, ctx context.Context, dir, name, pkg string) string {
	t.Helper()
	bin := filepath.Join(dir, name)
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", bin, pkg).CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v: %s", name, err, out)
	}
	return bin
}

func run(t *testing.T, ctx context.Context, bin string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if ok := isExitError(err, &exitErr); ok {
			return string(out), exitErr.ExitCode()
		}
		t.Fatalf("run %s %v: %v", bin, args, err)
	}
	return string(out), 0
}

func mustRun(t *testing.T, ctx context.Context, bin string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		var exitErr *exec.ExitError
		if isExitError(err, &exitErr) {
			stderr = string(exitErr.Stderr)
		}
		t.Fatalf("%s %v: %v\nstderr: %s", filepath.Base(bin), args, err, stderr)
	}
	return string(out)
}

func dockerOut(t *testing.T, ctx context.Context, args ...string) string {
	t.Helper()
	// Output, not CombinedOutput: docker streams pull progress to stderr on
	// a cold image cache, and mixing it in corrupts captured container ids.
	out, err := exec.CommandContext(ctx, "docker", args...).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if isExitError(err, &exitErr) {
			t.Fatalf("docker %v: %v: %s", args, err, exitErr.Stderr)
		}
		t.Fatalf("docker %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func isExitError(err error, target **exec.ExitError) bool {
	if e, ok := err.(*exec.ExitError); ok {
		*target = e
		return true
	}
	return false
}
