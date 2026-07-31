package evidence

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Outcome classifies how a drill ended (evidence-schema.md §7).
type Outcome string

// Outcome values. Fail is a recoverability verdict about the backup;
// OutcomeError means infrastructure prevented any verdict.
const (
	OutcomePass      Outcome = "pass"
	OutcomeFail      Outcome = "fail"
	OutcomeError     Outcome = "error"
	OutcomeCancelled Outcome = "cancelled"
)

// TimestampFormat is the schema's timestamp form: RFC 3339 UTC with
// exactly millisecond precision (evidence-schema.md §3).
const TimestampFormat = "2006-01-02T15:04:05.000Z"

var (
	sha256RefPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	hostIDPattern    = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

// Record is one evidence record (evidence-schema.md §3). Every field is
// always serialized; nullable values are pointers.
type Record struct {
	Schema   string      `json:"schema"`
	Seq      int64       `json:"seq"`
	PrevHash string      `json:"prev_hash"`
	TS       string      `json:"ts"`
	Drill    Drill       `json:"drill"`
	Backup   Backup      `json:"backup"`
	Adapter  Adapter     `json:"adapter"`
	Sandbox  Sandbox     `json:"sandbox"`
	Timings  Timings     `json:"timings_ms"`
	Checks   []Check     `json:"checks"`
	Outcome  Outcome     `json:"outcome"`
	Error    *DrillError `json:"error"`
	Env      Env         `json:"env"`
	Sig      *Signature  `json:"sig,omitempty"`
}

// Drill identifies the drill definition that produced a record.
type Drill struct {
	Name       string `json:"name"`
	ConfigHash string `json:"config_hash"`
}

// Backup identifies the backup source that was restored.
type Backup struct {
	Kind      string  `json:"kind"`
	Checksum  *string `json:"checksum"`
	SizeBytes *int64  `json:"size_bytes"`
	CreatedAt *string `json:"created_at"`
}

// Adapter identifies the engine adapter that performed the restore.
type Adapter struct {
	Name     string  `json:"name"`
	Version  *string `json:"version"`
	Protocol string  `json:"protocol"`
}

// Sandbox identifies the sandbox provider and its drill-config parameters.
type Sandbox struct {
	Provider string            `json:"provider"`
	Params   map[string]string `json:"params"`
}

// Timings holds per-phase durations in integer milliseconds
// (evidence-schema.md §3.1). Phases that never ran are nil.
type Timings struct {
	Provision   *int64 `json:"provision"`
	EngineReady *int64 `json:"engine_ready"`
	Transfer    *int64 `json:"transfer"`
	Restore     *int64 `json:"restore"`
	Validate    *int64 `json:"validate"`
	Total       *int64 `json:"total"`
}

// Check is the outcome of one executed validation check.
type Check struct {
	Name   string  `json:"name"`
	OK     bool    `json:"ok"`
	Detail *string `json:"detail"`
}

// DrillError describes why a drill did not pass. Message must already be
// redacted by the caller; this package enforces only shape limits.
type DrillError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Env fingerprints the environment that ran the drill.
type Env struct {
	ProbaviVersion string `json:"probavi_version"`
	OS             string `json:"os"`
	Arch           string `json:"arch"`
	HostID         string `json:"host_id"`
}

// Signature is the detached ed25519 signature over the record's canonical
// bytes with the sig field absent.
type Signature struct {
	Alg    string `json:"alg"`
	KeyID  string `json:"key_id"`
	SigB64 string `json:"sig_b64"`
}

// Validate checks the writer-side shape rules of evidence-schema.md §3.
// Chain fields (seq, prev_hash) and sig are filled by the store and are not
// validated here.
func (r *Record) Validate() error {
	if err := r.validateUTF8(); err != nil {
		return err
	}
	if err := r.validateIdentity(); err != nil {
		return err
	}
	if err := r.validateOutcome(); err != nil {
		return err
	}
	if err := r.validateChecks(); err != nil {
		return err
	}
	return r.validateNested()
}

func (r *Record) validateIdentity() error {
	if r.Schema != SchemaID {
		return fmt.Errorf("%w: schema %q, want %q", ErrInvalidRecord, r.Schema, SchemaID)
	}
	if err := validateTS("ts", r.TS); err != nil {
		return err
	}
	if r.Drill.Name == "" {
		return fmt.Errorf("%w: drill.name is empty", ErrInvalidRecord)
	}
	if !sha256RefPattern.MatchString(r.Drill.ConfigHash) {
		return fmt.Errorf("%w: drill.config_hash is not a sha256 reference", ErrInvalidRecord)
	}
	return nil
}

func (r *Record) validateOutcome() error {
	switch r.Outcome {
	case OutcomePass, OutcomeFail, OutcomeError, OutcomeCancelled:
	default:
		return fmt.Errorf("%w: unknown outcome %q", ErrInvalidRecord, r.Outcome)
	}
	if (r.Outcome == OutcomePass) != (r.Error == nil) {
		return fmt.Errorf("%w: error must be null iff outcome is pass", ErrInvalidRecord)
	}
	if r.Error != nil {
		if r.Error.Code == "" {
			return fmt.Errorf("%w: error.code is empty", ErrInvalidRecord)
		}
		if err := validateLine("error.message", r.Error.Message, maxErrorMessageLen); err != nil {
			return err
		}
	}
	return nil
}

func (r *Record) validateChecks() error {
	if r.Checks == nil {
		return fmt.Errorf("%w: checks must be an array (may be empty, not null)", ErrInvalidRecord)
	}
	for i, c := range r.Checks {
		if c.Name == "" {
			return fmt.Errorf("%w: checks[%d].name is empty", ErrInvalidRecord, i)
		}
		if c.Detail != nil {
			if err := validateLine(fmt.Sprintf("checks[%d].detail", i), *c.Detail, maxDetailLen); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Record) validateNested() error {
	if r.Backup.Kind == "" {
		return fmt.Errorf("%w: backup.kind is empty", ErrInvalidRecord)
	}
	if r.Backup.Checksum != nil && !sha256RefPattern.MatchString(*r.Backup.Checksum) {
		return fmt.Errorf("%w: backup.checksum is not a sha256 reference", ErrInvalidRecord)
	}
	if r.Backup.CreatedAt != nil {
		if err := validateTS("backup.created_at", *r.Backup.CreatedAt); err != nil {
			return err
		}
	}
	if r.Backup.SizeBytes != nil && *r.Backup.SizeBytes < 0 {
		return fmt.Errorf("%w: backup.size_bytes is negative", ErrInvalidRecord)
	}
	if r.Adapter.Name == "" || r.Adapter.Protocol == "" {
		return fmt.Errorf("%w: adapter.name and adapter.protocol are required", ErrInvalidRecord)
	}
	if r.Sandbox.Provider == "" {
		return fmt.Errorf("%w: sandbox.provider is empty", ErrInvalidRecord)
	}
	if r.Sandbox.Params == nil {
		return fmt.Errorf("%w: sandbox.params must be an object (may be empty, not null)", ErrInvalidRecord)
	}
	if err := r.Timings.validate(); err != nil {
		return err
	}
	return r.Env.validate()
}

func (t *Timings) validate() error {
	for name, v := range map[string]*int64{
		"provision": t.Provision, "engine_ready": t.EngineReady, "transfer": t.Transfer,
		"restore": t.Restore, "validate": t.Validate, "total": t.Total,
	} {
		if v != nil && *v < 0 {
			return fmt.Errorf("%w: timings_ms.%s is negative", ErrInvalidRecord, name)
		}
	}
	return nil
}

func (e *Env) validate() error {
	if e.ProbaviVersion == "" || e.OS == "" || e.Arch == "" {
		return fmt.Errorf("%w: env fields are required", ErrInvalidRecord)
	}
	if !hostIDPattern.MatchString(e.HostID) {
		return fmt.Errorf("%w: env.host_id must be 16 lowercase hex chars", ErrInvalidRecord)
	}
	return nil
}

// validateUTF8 rejects invalid UTF-8 in every free-text field. This must
// run before canonicalization: encoding/json silently replaces invalid
// bytes with U+FFFD, which would alter record content without anyone
// noticing — unacceptable for signed evidence.
func (r *Record) validateUTF8() error {
	type field struct{ name, value string }
	fields := []field{
		{"drill.name", r.Drill.Name},
		{"backup.kind", r.Backup.Kind},
		{"adapter.name", r.Adapter.Name},
		{"adapter.protocol", r.Adapter.Protocol},
		{"sandbox.provider", r.Sandbox.Provider},
		{"env.probavi_version", r.Env.ProbaviVersion},
		{"env.os", r.Env.OS},
		{"env.arch", r.Env.Arch},
	}
	if r.Adapter.Version != nil {
		fields = append(fields, field{"adapter.version", *r.Adapter.Version})
	}
	if r.Error != nil {
		fields = append(fields, field{"error.code", r.Error.Code}, field{"error.message", r.Error.Message})
	}
	for i, c := range r.Checks {
		fields = append(fields, field{fmt.Sprintf("checks[%d].name", i), c.Name})
		if c.Detail != nil {
			fields = append(fields, field{fmt.Sprintf("checks[%d].detail", i), *c.Detail})
		}
	}
	for k, v := range r.Sandbox.Params {
		// NUL cannot complete a truncated UTF-8 sequence, so joining on it
		// checks key and value in one pass without false accepts.
		fields = append(fields, field{"sandbox.params", k + "\x00" + v})
	}
	for _, f := range fields {
		if !utf8.ValidString(f.value) {
			return fmt.Errorf("%w: %s is not valid UTF-8", ErrInvalidRecord, f.name)
		}
	}
	return nil
}

// validateTS enforces RFC 3339 UTC with exactly millisecond precision.
func validateTS(field, ts string) error {
	t, err := time.Parse(TimestampFormat, ts)
	if err != nil || t.UTC().Format(TimestampFormat) != ts {
		return fmt.Errorf("%w: %s %q is not RFC 3339 UTC with millisecond precision", ErrInvalidRecord, field, ts)
	}
	return nil
}

// validateLine enforces the single-line, bounded-length rule for
// human-readable strings that enter records.
func validateLine(field, s string, maxLen int) error {
	if len(s) > maxLen {
		return fmt.Errorf("%w: %s exceeds %d bytes", ErrInvalidRecord, field, maxLen)
	}
	if strings.ContainsAny(s, "\r\n") {
		return fmt.Errorf("%w: %s must be a single line", ErrInvalidRecord, field)
	}
	return nil
}
