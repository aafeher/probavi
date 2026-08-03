package notify

import "github.com/probavi/probavi/internal/evidence"

// SchemaID is the notification payload version this package emits. Like
// the adapter protocol and the evidence schema, it is versioned
// independently of the binary (docs/notifications.md §5).
const SchemaID = "probavi-notification/1"

// Payload is one notification message (docs/notifications.md §5,
// probavi-notification/1). Struct order is the serialization order;
// every field is always present, nullable values are null, never omitted.
type Payload struct {
	Schema         string       `json:"schema"`
	Event          string       `json:"event"`
	TS             string       `json:"ts"`
	Drill          PayloadDrill `json:"drill"`
	Adapter        string       `json:"adapter"`
	Outcome        string       `json:"outcome"`
	Seq            int64        `json:"seq"`
	ChecksPassed   int          `json:"checks_passed"`
	ChecksTotal    int          `json:"checks_total"`
	Timings        Timings      `json:"timings_ms"`
	Error          *Error       `json:"error"`
	ProbaviVersion string       `json:"probavi_version"`
}

// PayloadDrill locates the drill definition; together with Seq it points
// receivers at the exact evidence record to verify.
type PayloadDrill struct {
	Name       string `json:"name"`
	ConfigHash string `json:"config_hash"`
}

// Timings carries the two headline durations; phases that never ran stay
// null, mirroring the evidence record.
type Timings struct {
	Restore *int64 `json:"restore"`
	Total   *int64 `json:"total"`
}

// Error mirrors the record's error: nil exactly when the outcome is pass.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// NewPayload derives the notification payload from a signed evidence
// record. Everything is copied, nothing is computed fresh — the
// notification must never disagree with the record it announces.
func NewPayload(rec *evidence.Record) Payload {
	passed := 0
	for _, c := range rec.Checks {
		if c.OK {
			passed++
		}
	}
	p := Payload{
		Schema:         SchemaID,
		Event:          Event,
		TS:             rec.TS,
		Drill:          PayloadDrill{Name: rec.Drill.Name, ConfigHash: rec.Drill.ConfigHash},
		Adapter:        rec.Adapter.Name,
		Outcome:        string(rec.Outcome),
		Seq:            rec.Seq,
		ChecksPassed:   passed,
		ChecksTotal:    len(rec.Checks),
		Timings:        Timings{Restore: rec.Timings.Restore, Total: rec.Timings.Total},
		ProbaviVersion: rec.Env.ProbaviVersion,
	}
	if rec.Error != nil {
		p.Error = &Error{Code: rec.Error.Code, Message: rec.Error.Message}
	}
	return p
}
