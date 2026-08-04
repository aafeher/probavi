package adapter

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/probavi/probavi/internal/evidence"
)

// TestEvidenceTimestampFormatMatches pins the local copy of the evidence
// schema's timestamp layout. The protocol client deliberately does not
// import the evidence package; this test is what keeps the duplication
// honest.
func TestEvidenceTimestampFormatMatches(t *testing.T) {
	if evidenceTimestampFormat != evidence.TimestampFormat {
		t.Fatalf("evidenceTimestampFormat = %q, evidence.TimestampFormat = %q",
			evidenceTimestampFormat, evidence.TimestampFormat)
	}
}

// goodResult is a provision response that passes every boundary rule; each
// test mutates exactly one field away from it.
func goodResult() *ProvisionResult {
	created := "2026-07-30T01:58:02.000Z"
	return &ProvisionResult{
		Connection: Connection{Scheme: "postgresql"},
		SourceIdentity: SourceIdentity{
			Checksum:  "sha256:" + strings.Repeat("9f", 32),
			SizeBytes: 565248,
			CreatedAt: &created,
		},
		Timings: Timings{EngineReadySeconds: 1.17, TransferSeconds: 0.11, RestoreSeconds: 0.19},
		State:   json.RawMessage(`{}`),
	}
}

// TestCreatedAtNormalization covers the §6.2 contract: any RFC 3339
// instant is accepted and lands in the record's millisecond UTC form,
// truncated rather than rounded.
func TestCreatedAtNormalization(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"already canonical", "2026-07-30T01:58:02.000Z", "2026-07-30T01:58:02.000Z"},
		{"second precision", "2026-07-30T01:58:02Z", "2026-07-30T01:58:02.000Z"},
		{"tenths", "2026-07-30T01:58:02.7Z", "2026-07-30T01:58:02.700Z"},
		{"nanoseconds truncate down", "2026-07-30T01:58:02.789999999Z", "2026-07-30T01:58:02.789Z"},
		{"never rounds up", "2026-07-30T01:58:02.9999Z", "2026-07-30T01:58:02.999Z"},
		{"positive offset", "2026-07-30T03:58:02+02:00", "2026-07-30T01:58:02.000Z"},
		{"negative offset", "2026-07-29T21:58:02.25-04:00", "2026-07-30T01:58:02.250Z"},
		{"lowercase t and z", "2026-07-30t01:58:02.5z", "2026-07-30T01:58:02.500Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := goodResult()
			res.SourceIdentity.CreatedAt = &tt.in
			if err := validateProvisionResult(res); err != nil {
				t.Fatalf("validateProvisionResult(%q): %v", tt.in, err)
			}
			got := *res.SourceIdentity.CreatedAt
			if got != tt.want {
				t.Errorf("created_at %q normalized to %q, want %q", tt.in, got, tt.want)
			}
			// The whole point: what comes out must satisfy the record.
			rec := recordWith(got)
			if err := rec.Validate(); err != nil {
				t.Errorf("normalized %q still fails the evidence schema: %v", got, err)
			}
		})
	}

	t.Run("null stays null", func(t *testing.T) {
		res := goodResult()
		res.SourceIdentity.CreatedAt = nil
		if err := validateProvisionResult(res); err != nil {
			t.Fatalf("validateProvisionResult: %v", err)
		}
		if res.SourceIdentity.CreatedAt != nil {
			t.Errorf("created_at = %q, want nil — not derivable is a legitimate answer", *res.SourceIdentity.CreatedAt)
		}
	})
}

// TestProvisionResultRejects proves that every value the evidence record
// would refuse is caught here instead, where it becomes an adapter_crash
// verdict and the drill still leaves a signed record.
func TestProvisionResultRejects(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*ProvisionResult)
		wantMsg string
	}{
		{"negative size", func(r *ProvisionResult) { r.SourceIdentity.SizeBytes = -1 }, "size_bytes is negative"},
		{"unparseable created_at", func(r *ProvisionResult) {
			s := "yesterday"
			r.SourceIdentity.CreatedAt = &s
		}, "not an RFC 3339 instant"},
		{"date-only created_at", func(r *ProvisionResult) {
			s := "2026-07-30"
			r.SourceIdentity.CreatedAt = &s
		}, "not an RFC 3339 instant"},
		{"zoneless created_at", func(r *ProvisionResult) {
			s := "2026-07-30T01:58:02"
			r.SourceIdentity.CreatedAt = &s
		}, "not an RFC 3339 instant"},
		{"NaN restore", func(r *ProvisionResult) { r.Timings.RestoreSeconds = math.NaN() }, "restore_seconds is NaN"},
		{"NaN engine_ready", func(r *ProvisionResult) { r.Timings.EngineReadySeconds = math.NaN() }, "engine_ready_seconds is NaN"},
		{"+Inf transfer", func(r *ProvisionResult) { r.Timings.TransferSeconds = math.Inf(1) }, "transfer_seconds is infinite"},
		{"-Inf restore", func(r *ProvisionResult) { r.Timings.RestoreSeconds = math.Inf(-1) }, "restore_seconds is infinite"},
		{"negative timing", func(r *ProvisionResult) { r.Timings.RestoreSeconds = -0.5 }, "negative timings"},
		{"timing beyond the schema", func(r *ProvisionResult) {
			r.Timings.RestoreSeconds = maxTimingSeconds * 2
		}, "exceeds what an evidence record can represent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := goodResult()
			tt.mutate(res)
			err := validateProvisionResult(res)
			if err == nil {
				t.Fatalf("accepted a value the evidence record would refuse")
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error = %v, want it to mention %q", err, tt.wantMsg)
			}
			var aerr *Error
			if !errors.As(err, &aerr) || aerr.Code != CodeAdapterCrash {
				t.Errorf("error = %v, want an adapter_crash verdict — the drill must still leave a record", err)
			}
		})
	}
}

// recordWith builds the minimal valid record carrying one created_at, so
// the normalization tests assert against the real schema rather than a
// restated pattern.
func recordWith(createdAt string) *evidence.Record {
	return &evidence.Record{
		Schema: evidence.SchemaID,
		TS:     "2026-07-31T02:00:00.000Z",
		Drill:  evidence.Drill{Name: "d", ConfigHash: "sha256:" + strings.Repeat("7d", 32)},
		Backup: evidence.Backup{Kind: "pgdump", CreatedAt: &createdAt},
		Adapter: evidence.Adapter{
			Name: "postgres", Protocol: ProtocolVersion,
		},
		Sandbox: evidence.Sandbox{Provider: "docker", Params: map[string]string{}},
		Checks:  []evidence.Check{},
		Outcome: evidence.OutcomePass,
		Env: evidence.Env{
			ProbaviVersion: "test", OS: "linux", Arch: "amd64",
			HostID: strings.Repeat("a", 16),
		},
	}
}
