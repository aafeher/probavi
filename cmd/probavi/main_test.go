package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aafeher/probavi/internal/adapter"
	"github.com/aafeher/probavi/internal/evidence"
	"github.com/aafeher/probavi/internal/sandbox/remotehost"
)

// TestVersionCommand pins the version output: the binary version plus both
// contract versions, nothing on stderr, exit 0.
func TestVersionCommand(t *testing.T) {
	code, stdout, stderr := runCLI(t, "version")
	if code != 0 {
		t.Fatalf("version exit %d, want 0 (stderr: %s)", code, stderr)
	}
	if stderr != "" {
		t.Errorf("version wrote to stderr: %q", stderr)
	}
	for _, want := range []string{"probavi " + version, adapter.ProtocolVersion, evidence.SchemaID} {
		if !strings.Contains(stdout, want) {
			t.Errorf("version output %q does not contain %q", stdout, want)
		}
	}
}

func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	code = run(args, &out, &errBuf)
	return code, out.String(), errBuf.String()
}

func testRecord(ts string) *evidence.Record {
	detail := "accepting connections"
	return &evidence.Record{
		Schema:  evidence.SchemaID,
		TS:      ts,
		Drill:   evidence.Drill{Name: "cli-test", ConfigHash: "sha256:" + strings.Repeat("ab", 32)},
		Backup:  evidence.Backup{Kind: "pgdump"},
		Adapter: evidence.Adapter{Name: "postgres", Protocol: "probavi-adapter/0"},
		Sandbox: evidence.Sandbox{Provider: "docker", Params: map[string]string{}},
		Checks:  []evidence.Check{{Name: "service_healthy", OK: true, Detail: &detail}},
		Outcome: evidence.OutcomePass,
		Env:     evidence.Env{ProbaviVersion: "test", OS: "linux", Arch: "amd64", HostID: "0123456789abcdef"},
	}
}

// setupLog generates a key pair through the CLI and writes a two-record log
// with it, returning the log and public key paths.
func setupLog(t *testing.T) (logPath, keyPath, pubPath string) {
	t.Helper()
	dir := t.TempDir()
	keyPath = filepath.Join(dir, "ed25519.key")
	pubPath = keyPath + ".pub"
	logPath = filepath.Join(dir, "evidence.jsonl")

	code, stdout, stderr := runCLI(t, "evidence", "keygen", "--out", keyPath)
	if code != 0 {
		t.Fatalf("keygen exit %d, stderr: %s", code, stderr)
	}
	var kg keygenOutput
	if err := json.Unmarshal([]byte(stdout), &kg); err != nil {
		t.Fatalf("keygen output is not JSON: %v (%q)", err, stdout)
	}

	signer, err := evidence.LoadSigner(keyPath)
	if err != nil {
		t.Fatalf("LoadSigner on generated key: %v", err)
	}
	if signer.KeyID() != kg.KeyID {
		t.Fatalf("keygen reported key_id %q, key derives %q", kg.KeyID, signer.KeyID())
	}
	st, err := evidence.Open(logPath, signer, nil)
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	for _, ts := range []string{"2026-07-31T10:00:00.000Z", "2026-07-31T10:05:00.000Z"} {
		if err := st.Append(testRecord(ts)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return logPath, keyPath, pubPath
}

func TestKeygenThenVerifyRoundTrip(t *testing.T) {
	logPath, keyPath, pubPath := setupLog(t)

	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("private key mode = %04o, want 0600", perm)
	}

	code, stdout, stderr := runCLI(t, "evidence", "verify", "--log", logPath, "--key", pubPath)
	if code != exitValid {
		t.Fatalf("verify exit %d, want 0; stderr: %s", code, stderr)
	}
	var res verifyOutput
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("verify output is not JSON: %v (%q)", err, stdout)
	}
	if res.Status != "VALID" || res.Records != 2 || len(res.DamagedLines) != 0 {
		t.Errorf("verify output = %+v, want VALID with 2 records", res)
	}
}

func TestKeygenRefusesOverwrite(t *testing.T) {
	_, keyPath, _ := setupLog(t)
	code, _, stderr := runCLI(t, "evidence", "keygen", "--out", keyPath)
	if code != exitUsage {
		t.Fatalf("keygen over existing key: exit %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, keyPath) {
		t.Errorf("stderr should name the offending file, got: %s", stderr)
	}
}

func TestVerifyDamagedLog(t *testing.T) {
	logPath, _, pubPath := setupLog(t)
	f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	if _, err := f.WriteString(`{"torn":`); err != nil {
		t.Fatalf("append fragment: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	code, stdout, _ := runCLI(t, "evidence", "verify", "--log", logPath, "--key", pubPath)
	if code != exitValidWithDamage {
		t.Fatalf("verify exit %d, want %d (stdout: %s)", code, exitValidWithDamage, stdout)
	}
}

func TestVerifyTamperedLog(t *testing.T) {
	logPath, _, pubPath := setupLog(t)
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	tampered := strings.Replace(string(raw), "cli-test", "cli-tesx", 1)
	if err := os.WriteFile(logPath, []byte(tampered), 0o600); err != nil {
		t.Fatalf("write tampered log: %v", err)
	}

	code, stdout, _ := runCLI(t, "evidence", "verify", "--log", logPath, "--key", pubPath)
	if code != exitInvalid {
		t.Fatalf("verify exit %d, want %d (stdout: %s)", code, exitInvalid, stdout)
	}
	var res verifyOutput
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("verify output is not JSON: %v", err)
	}
	if res.Status != "INVALID" || res.FailedLine != 1 || res.Reason == "" {
		t.Errorf("verify output = %+v, want INVALID at line 1 with a reason", res)
	}
}

func TestSandboxProviderResolution(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	for _, name := range []string{"docker", "k8s"} {
		if p, err := sandboxProvider(name, nil, logger); err != nil || p == nil {
			t.Errorf("sandboxProvider(%q) = %v, %v", name, p, err)
		}
	}
	if _, err := sandboxProvider("nomad", nil, logger); err == nil || !strings.Contains(err.Error(), "supported: docker, k8s, remotehost") {
		t.Errorf("unknown provider: %v, want the supported list", err)
	}

	// remotehost needs its ssh target from the environment — never from
	// config, which is recorded verbatim in evidence records.
	t.Setenv(remotehost.EnvTarget, "")
	if _, err := sandboxProvider("remotehost", nil, logger); err == nil || !strings.Contains(err.Error(), remotehost.EnvTarget) {
		t.Errorf("remotehost without %s: %v, want a clear error", remotehost.EnvTarget, err)
	}
	t.Setenv(remotehost.EnvTarget, "drill@target.example")
	if p, err := sandboxProvider("remotehost", map[string]string{"memory": "1G"}, logger); err != nil || p == nil {
		t.Errorf("sandboxProvider(remotehost) = %v, %v", p, err)
	}
	if _, err := sandboxProvider("remotehost", map[string]string{"image": "x"}, logger); err == nil {
		t.Error("remotehost with invalid params must fail at wiring time")
	}
}

func TestUsageErrors(t *testing.T) {
	logPath, _, pubPath := setupLog(t)
	missing := filepath.Join(t.TempDir(), "nope")

	tests := []struct {
		name string
		args []string
	}{
		{"no args", nil},
		{"unknown command", []string{"restore"}},
		{"evidence without subcommand", []string{"evidence"}},
		{"evidence unknown subcommand", []string{"evidence", "sign"}},
		{"verify without flags", []string{"evidence", "verify"}},
		{"verify without key", []string{"evidence", "verify", "--log", logPath}},
		{"verify missing log file", []string{"evidence", "verify", "--log", missing, "--key", pubPath}},
		{"verify unreadable key", []string{"evidence", "verify", "--log", logPath, "--key", missing}},
		{"verify bad flag", []string{"evidence", "verify", "--no-such-flag"}},
		{"keygen without out", []string{"evidence", "keygen"}},
		{"keygen bad flag", []string{"evidence", "keygen", "--no-such-flag"}},
		{"keygen uncreatable path", []string{"evidence", "keygen", "--out", filepath.Join(missing, "sub", "k")}},
		{"adapter without subcommand", []string{"adapter"}},
		{"adapter unknown subcommand", []string{"adapter", "fuzz"}},
		{"conformance without adapter", []string{"adapter", "conformance"}},
		{"conformance bad source-param", []string{"adapter", "conformance", "--source-param", "novalue", "x"}},
		{"conformance unresolvable adapter", []string{"adapter", "conformance", "no-such-adapter-installed"}},
		{"conformance bad flag", []string{"adapter", "conformance", "--no-such-flag", "x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _, stderr := runCLI(t, tt.args...)
			if code != exitUsage {
				t.Errorf("exit %d, want %d (stderr: %s)", code, exitUsage, stderr)
			}
		})
	}
}

// TestRunRejectsUnresolvedWebhookEnv proves webhook environment variables
// are resolved at wiring time: a drill with an unset url_env must abort
// before any sandbox or adapter work, naming the missing variable.
func TestRunRejectsUnresolvedWebhookEnv(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "drill.yaml")
	cfg := `target:
  name: notify-env-test
  adapter: postgres
  source:
    kind: pgdump
    path: /backups/test.dump
sandbox:
  provider: docker
  timeout: 5m
checks:
  - builtin: service_healthy
evidence:
  path: ` + filepath.Join(dir, "evidence.jsonl") + `
  sign_key: ` + filepath.Join(dir, "unused.key") + `
notify:
  webhooks:
    - url_env: PROBAVI_TEST_UNSET_WEBHOOK_URL
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PROBAVI_TEST_UNSET_WEBHOOK_URL", "")

	code, _, stderr := runCLI(t, "run", "--config", cfgPath)
	if code != exitUsage {
		t.Fatalf("exit %d, want %d (stderr: %s)", code, exitUsage, stderr)
	}
	if !strings.Contains(stderr, "PROBAVI_TEST_UNSET_WEBHOOK_URL") {
		t.Errorf("stderr should name the missing variable, got: %s", stderr)
	}
}
