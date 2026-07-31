package config

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// validYAML is a minimal drill config that must pass validation; invalid
// test cases are derived from it by targeted replacement.
const validYAML = `target:
  name: test-db
  adapter: postgres
  source:
    kind: pgdump
    path: /backups/test.dump
sandbox:
  provider: docker
  params:
    image: postgres:16
  timeout: 30m
checks:
  - builtin: service_healthy
evidence:
  path: /var/lib/probavi/evidence.jsonl
  sign_key: /etc/probavi/ed25519.key
`

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "drill.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadExampleConfig(t *testing.T) {
	// The committed example must always parse and validate: this test is
	// what keeps README/examples honest (AGENTS.md §5.5).
	cfg, err := Load(filepath.Join("..", "..", "examples", "drill.example.yaml"))
	if err != nil {
		t.Fatalf("Load(examples/drill.example.yaml): %v", err)
	}
	if cfg.Target.Name != "prod-orders-db" || cfg.Target.Adapter != "postgres" {
		t.Errorf("target = %+v, want prod-orders-db/postgres", cfg.Target)
	}
	if cfg.Sandbox.Provider != "docker" || cfg.Sandbox.Params["image"] != "postgres:16" {
		t.Errorf("sandbox = %+v, want docker with postgres:16 image", cfg.Sandbox)
	}
	if cfg.Sandbox.Timeout.Std() != 30*time.Minute {
		t.Errorf("timeout = %v, want 30m", cfg.Sandbox.Timeout.Std())
	}
	if len(cfg.Checks) != 5 {
		t.Fatalf("checks = %d, want 5", len(cfg.Checks))
	}
	sql := cfg.Checks[4]
	if sql.Name != "no-negative-totals" || !sql.Expect.IsSet() || sql.Expect.String() != "0" {
		t.Errorf("sql check = %+v, want named with expect 0", sql)
	}
	fresh := cfg.Checks[3]
	if fresh.Builtin != "freshness" || fresh.MaxAge.Std() != 24*time.Hour {
		t.Errorf("freshness check = %+v, want max_age 24h", fresh)
	}
	if cfg.Metrics == nil || cfg.Metrics.PrometheusTextfile == "" {
		t.Errorf("metrics = %+v, want prometheus_textfile set", cfg.Metrics)
	}
}

func TestLoadComputesConfigHash(t *testing.T) {
	path := writeConfig(t, validYAML)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	sum := sha256.Sum256([]byte(validYAML))
	want := "sha256:" + hex.EncodeToString(sum[:])
	if cfg.Hash != want {
		t.Errorf("Hash = %q, want %q", cfg.Hash, want)
	}
	if cfg.Path != path {
		t.Errorf("Path = %q, want %q", cfg.Path, path)
	}
}

func TestLoadRejects(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want []string // substrings the error must contain
	}{
		{"unknown top-level field", validYAML + "banana: yes\n", []string{"unknown field", "banana"}},
		{"unknown field with typo", strings.Replace(validYAML, "adapter:", "adaptor:", 1), []string{"unknown field", "adaptor"}},
		{"duplicate key", validYAML + "sandbox:\n  provider: docker\n", []string{"duplicate"}},
		{"missing target name", strings.Replace(validYAML, "name: test-db", "name: \"\"", 1), []string{"target.name is required"}},
		{"bad adapter name", strings.Replace(validYAML, "adapter: postgres", "adapter: Postgres_16", 1), []string{"target.adapter", "probavi-adapter-"}},
		{"missing source kind", strings.Replace(validYAML, "kind: pgdump", "kind: \"\"", 1), []string{"target.source.kind is required"}},
		{"bad credential env name", strings.Replace(validYAML, "    path: /backups/test.dump", "    path: /backups/test.dump\n    credential_env: [\"1BAD-NAME\"]", 1), []string{"credential_env", "1BAD-NAME"}},
		{"missing provider", strings.Replace(validYAML, "provider: docker", "provider: \"\"", 1), []string{"sandbox.provider is required"}},
		{"missing timeout", strings.Replace(validYAML, "  timeout: 30m\n", "", 1), []string{"sandbox.timeout is required"}},
		{"bad duration", strings.Replace(validYAML, "timeout: 30m", "timeout: half an hour", 1), []string{"invalid duration"}},
		{"negative duration", strings.Replace(validYAML, "timeout: 30m", "timeout: -5m", 1), []string{"must be positive"}},
		{"no checks", strings.Replace(validYAML, "checks:\n  - builtin: service_healthy\n", "checks: []\n", 1), []string{"at least one check"}},
		{"unknown builtin", strings.Replace(validYAML, "builtin: service_healthy", "builtin: row_cnt", 1), []string{"unknown builtin", "row_cnt", "supported:"}},
		{"builtin and sql together", strings.Replace(validYAML, "- builtin: service_healthy", "- builtin: service_healthy\n    sql: SELECT 1", 1), []string{"not both"}},
		{"neither builtin nor sql", strings.Replace(validYAML, "- builtin: service_healthy", "- table: orders", 1), []string{"exactly one of builtin or sql"}},
		{"service_healthy with table", strings.Replace(validYAML, "- builtin: service_healthy", "- builtin: service_healthy\n    table: orders", 1), []string{"table is not valid for service_healthy"}},
		{"table_exists without table", strings.Replace(validYAML, "- builtin: service_healthy", "- builtin: table_exists", 1), []string{"table_exists requires table"}},
		{"row_count without bounds", strings.Replace(validYAML, "- builtin: service_healthy", "- builtin: row_count\n    table: orders", 1), []string{"min, max, or both"}},
		{"row_count negative min", strings.Replace(validYAML, "- builtin: service_healthy", "- builtin: row_count\n    table: orders\n    min: -1", 1), []string{"must not be negative"}},
		{"row_count min above max", strings.Replace(validYAML, "- builtin: service_healthy", "- builtin: row_count\n    table: orders\n    min: 10\n    max: 5", 1), []string{"min (10) exceeds max (5)"}},
		{"freshness without column", strings.Replace(validYAML, "- builtin: service_healthy", "- builtin: freshness\n    table: orders\n    max_age: 24h", 1), []string{"freshness requires column"}},
		{"freshness without max_age", strings.Replace(validYAML, "- builtin: service_healthy", "- builtin: freshness\n    table: orders\n    column: created_at", 1), []string{"freshness requires max_age"}},
		{"sql without expect", strings.Replace(validYAML, "- builtin: service_healthy", "- sql: SELECT 1", 1), []string{"require expect"}},
		{"sql with table", strings.Replace(validYAML, "- builtin: service_healthy", "- sql: SELECT 1\n    expect: 1\n    table: orders", 1), []string{"table is not valid for sql checks"}},
		{"builtin with expect", strings.Replace(validYAML, "- builtin: service_healthy", "- builtin: service_healthy\n    expect: true", 1), []string{"expect is only valid for sql checks"}},
		{"service_healthy with all forbidden fields", strings.Replace(validYAML, "- builtin: service_healthy", "- builtin: service_healthy\n    column: c\n    min: 1\n    max_age: 1h", 1), []string{"column is not valid", "min/max are not valid", "max_age is not valid"}},
		{"sql with all forbidden fields", strings.Replace(validYAML, "- builtin: service_healthy", "- sql: SELECT 1\n    expect: 1\n    column: c\n    max: 2\n    max_age: 1h", 1), []string{"column is not valid for sql checks", "min/max are not valid for sql checks", "max_age is not valid for sql checks"}},
		{"non-string timeout", strings.Replace(validYAML, "timeout: 30m", "timeout: [30]", 1), []string{"duration must be a string"}},
		{"builtin with name", strings.Replace(validYAML, "- builtin: service_healthy", "- builtin: service_healthy\n    name: nope", 1), []string{"name is only valid for sql checks"}},
		{"expect float", strings.Replace(validYAML, "- builtin: service_healthy", "- sql: SELECT 1.5\n    expect: 1.5", 1), []string{"string, boolean, or integer"}},
		{"missing evidence path", strings.Replace(validYAML, "path: /var/lib/probavi/evidence.jsonl", "path: \"\"", 1), []string{"evidence.path is required"}},
		{"missing sign key", strings.Replace(validYAML, "sign_key: /etc/probavi/ed25519.key", "sign_key: \"\"", 1), []string{"evidence.sign_key is required", "keygen"}},
		{"empty metrics section", validYAML + "metrics:\n  prometheus_textfile: \"\"\n", []string{"metrics.prometheus_textfile is required"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.yaml))
			if err == nil {
				t.Fatal("Load accepted an invalid config")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

func TestLoadReportsAllProblemsAtOnce(t *testing.T) {
	bad := strings.Replace(validYAML, "name: test-db", "name: \"\"", 1)
	bad = strings.Replace(bad, "provider: docker", "provider: \"\"", 1)
	bad = strings.Replace(bad, "sign_key: /etc/probavi/ed25519.key", "sign_key: \"\"", 1)
	_, err := Load(writeConfig(t, bad))
	if err == nil {
		t.Fatal("Load accepted an invalid config")
	}
	for _, want := range []string{"target.name", "sandbox.provider", "evidence.sign_key"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should report %q too; got:\n%v", want, err)
		}
	}
}

func TestLoadFileProblems(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("Load accepted a missing file")
	}
	if _, err := Load(writeConfig(t, "")); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("empty config: got %v, want empty-config error", err)
	}
	if _, err := Load(writeConfig(t, "target: [broken\n")); err == nil {
		t.Error("Load accepted broken YAML syntax")
	}
}

func TestScalarNormalization(t *testing.T) {
	tests := []struct {
		yaml string
		want string
	}{
		{"expect: true", "true"},
		{"expect: false", "false"},
		{"expect: 42", "42"},
		{"expect: -3", "-3"},
		{"expect: 18446744073709551615", "18446744073709551615"},
		{"expect: hello", "hello"},
		{"expect: \"1.5\"", "1.5"}, // quoted: a string, allowed
	}
	for _, tt := range tests {
		t.Run(tt.yaml, func(t *testing.T) {
			y := strings.Replace(validYAML, "- builtin: service_healthy", "- sql: SELECT 1\n    "+tt.yaml, 1)
			cfg, err := Load(writeConfig(t, y))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := cfg.Checks[0].Expect.String(); got != tt.want {
				t.Errorf("Expect = %q, want %q", got, tt.want)
			}
		})
	}
}
