package spec_test

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/probavi/probavi/internal/evidence"
)

// TestErrorCodeVocabularyMatchesSchema pins the Go vocabulary to the
// published enum. The core normalizes every error.code against
// evidence.ErrorCodes() before signing, so if that list ever grew past what
// docs/schemas/evidence/record.json permits, Probavi would sign records
// that fail the schema its own consumers validate against — `evidence
// verify` reporting VALID while a schema check reports INVALID is the one
// contradiction a trust product cannot afford.
func TestErrorCodeVocabularyMatchesSchema(t *testing.T) {
	raw, err := os.ReadFile("../../docs/schemas/evidence/record.json")
	if err != nil {
		t.Fatalf("read record schema: %v", err)
	}
	var doc struct {
		Defs struct {
			DrillError struct {
				Properties struct {
					Code struct {
						Enum []string `json:"enum"`
					} `json:"code"`
				} `json:"properties"`
			} `json:"drillError"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse record schema: %v", err)
	}

	schema := doc.Defs.DrillError.Properties.Code.Enum
	if len(schema) == 0 {
		t.Fatal("record schema declares no error.code enum — the gate would pass vacuously")
	}
	code := evidence.ErrorCodes()
	if !slices.Equal(schema, code) {
		t.Errorf("error.code vocabulary differs from the published enum\n  schema: %v\n  Go:     %v", schema, code)
	}

	for _, c := range code {
		if !evidence.IsErrorCode(c) {
			t.Errorf("IsErrorCode(%q) = false for a code in its own list", c)
		}
	}
	for _, c := range []string{"", "banana_peel", "setup_error", "evidence_lost", "INTERNAL"} {
		if evidence.IsErrorCode(c) {
			t.Errorf("IsErrorCode(%q) = true, want false", c)
		}
	}
}
