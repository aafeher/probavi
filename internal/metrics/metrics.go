// Package metrics writes drill outcomes in the Prometheus textfile
// exposition format — plain text, atomically renamed into place, no
// client library dependency (AGENTS.md: minimal and boring).
package metrics

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aafeher/probavi/internal/evidence"
)

// WriteTextfile renders the record's headline metrics and atomically
// replaces the file at path (node_exporter textfile collector contract:
// readers must never observe a half-written file).
func WriteTextfile(path string, rec *evidence.Record) error {
	content, err := render(rec)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write metrics tempfile: %w", err)
	}
	if err := os.Rename(tmp, filepath.Clean(path)); err != nil {
		return fmt.Errorf("publish metrics file: %w", err)
	}
	return nil
}

func render(rec *evidence.Record) (string, error) {
	ts, err := time.Parse(evidence.TimestampFormat, rec.TS)
	if err != nil {
		return "", fmt.Errorf("record ts: %w", err)
	}
	label := `drill="` + escapeLabel(rec.Drill.Name) + `"`
	passed := 0
	for _, c := range rec.Checks {
		if c.OK {
			passed++
		}
	}

	b := &strings.Builder{}
	if rec.Timings.Restore != nil {
		writeMetric(b, "probavi_restore_duration_seconds",
			"Duration of the engine restore phase of the last drill.",
			label, formatFloat(float64(*rec.Timings.Restore)/1000))
	}
	writeMetric(b, "probavi_checks_passed",
		"Number of checks that passed in the last drill.",
		label, strconv.Itoa(passed))
	writeMetric(b, "probavi_checks_total",
		"Number of checks executed in the last drill.",
		label, strconv.Itoa(len(rec.Checks)))
	writeMetric(b, "probavi_last_run_timestamp_seconds",
		"Unix time of the last drill's evidence record.",
		label, formatFloat(float64(ts.UnixMilli())/1000))
	if rec.Outcome == evidence.OutcomePass {
		writeMetric(b, "probavi_last_success_timestamp_seconds",
			"Unix time of the last drill that proved the backup restorable.",
			label, formatFloat(float64(ts.UnixMilli())/1000))
	}
	return b.String(), nil
}

func writeMetric(b *strings.Builder, name, help, label, value string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s gauge\n%s{%s} %s\n", name, help, name, name, label, value)
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// escapeLabel applies the exposition-format label escaping rules.
func escapeLabel(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(v)
}
