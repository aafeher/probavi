package capabilities

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Render serializes the document to its committed byte form: two-space
// indentation so a diff is line-oriented and reviewable, HTML escaping off
// so keys like env.<NAME> stay readable, and a trailing newline.
//
// The bytes are deterministic. Object keys follow struct declaration
// order, every slice is ordered at build time, and no field carries a
// timestamp or build metadata — so the file changes when a capability
// changes and at no other time, which is what makes the CI drift gate
// worth reading.
func Render(doc *Document) ([]byte, error) {
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("render capabilities document: %w", err)
	}
	return buf.Bytes(), nil
}
