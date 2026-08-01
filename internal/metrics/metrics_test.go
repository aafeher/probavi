package metrics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aafeher/probavi/internal/evidence"
)

func i64(n int64) *int64   { return &n }
func str(s string) *string { return &s }

func sampleRecord(outcome evidence.Outcome) *evidence.Record {
	return &evidence.Record{
		TS:    "2026-07-31T02:00:11.482Z",
		Drill: evidence.Drill{Name: "prod-orders-db"},
		Timings: evidence.Timings{
			Restore: i64(190),
		},
		Checks: []evidence.Check{
			{Name: "service_healthy", OK: true, Detail: str("ok")},
			{Name: "row_count:orders", OK: outcome == evidence.OutcomePass},
		},
		Outcome: outcome,
	}
}

func TestWriteTextfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "probavi.prom")
	if err := WriteTextfile(path, sampleRecord(evidence.OutcomePass)); err != nil {
		t.Fatalf("WriteTextfile: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read metrics: %v", err)
	}
	content := string(raw)
	for _, want := range []string{
		`probavi_restore_duration_seconds{drill="prod-orders-db"} 0.19` + "\n",
		`probavi_checks_passed{drill="prod-orders-db"} 2` + "\n",
		`probavi_checks_total{drill="prod-orders-db"} 2` + "\n",
		`probavi_last_run_timestamp_seconds{drill="prod-orders-db"} 1785463211.482` + "\n",
		`probavi_last_success_timestamp_seconds{drill="prod-orders-db"} 1785463211.482` + "\n",
		"# HELP probavi_restore_duration_seconds ",
		"# TYPE probavi_checks_passed gauge",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("metrics output missing %q:\n%s", want, content)
		}
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("tempfile left behind — the rename must consume it")
	}
}

func TestWriteTextfileFailedDrill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "probavi.prom")
	rec := sampleRecord(evidence.OutcomeFail)
	rec.Timings.Restore = nil // restore never ran
	if err := WriteTextfile(path, rec); err != nil {
		t.Fatalf("WriteTextfile: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read metrics: %v", err)
	}
	content := string(raw)
	if strings.Contains(content, "probavi_last_success_timestamp_seconds") {
		t.Error("failed drill must not advance the last-success timestamp")
	}
	if strings.Contains(content, "probavi_restore_duration_seconds") {
		t.Error("a phase that never ran must not be reported")
	}
	if !strings.Contains(content, `probavi_checks_passed{drill="prod-orders-db"} 1`) {
		t.Errorf("checks_passed must count only OK checks:\n%s", content)
	}
}

func TestWriteTextfileEdges(t *testing.T) {
	rec := sampleRecord(evidence.OutcomePass)
	rec.Drill.Name = "we\"ird\\name\nx"
	path := filepath.Join(t.TempDir(), "probavi.prom")
	if err := WriteTextfile(path, rec); err != nil {
		t.Fatalf("WriteTextfile: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read metrics: %v", err)
	}
	if !strings.Contains(string(raw), `drill="we\"ird\\name\nx"`) {
		t.Errorf("label escaping broken:\n%s", raw)
	}

	rec.TS = "not-a-timestamp"
	if err := WriteTextfile(path, rec); err == nil {
		t.Error("malformed record ts must be an error")
	}
	if err := WriteTextfile(filepath.Join(t.TempDir(), "no", "dir", "x.prom"),
		sampleRecord(evidence.OutcomePass)); err == nil {
		t.Error("unwritable path must be an error")
	}
}

func TestWriteTextfilePublishFailure(t *testing.T) {
	// The tempfile write succeeds, but renaming onto an existing directory
	// cannot: the atomic-publish step must surface its own error.
	dir := t.TempDir()
	if err := WriteTextfile(dir, sampleRecord(evidence.OutcomePass)); err == nil {
		t.Error("renaming over a directory must fail loudly")
	}
}
