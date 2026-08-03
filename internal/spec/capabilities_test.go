package spec_test

import (
	"os"
	"testing"
)

// capabilitiesManifest is the committed, generated document. It is the one
// instance of this schema that exists, and it is what downstream consumers
// actually read — so it, not a hand-written sample, is the positive case.
const capabilitiesManifest = "../../docs/capabilities.json"

// TestCapabilitiesManifestValidates holds the committed capabilities
// manifest to its published schema. Together with the drift gate — which
// regenerates the file and fails on any difference — this is what lets an
// external consumer parse docs/capabilities.json against
// docs/schemas/capabilities/capabilities.json and rely on the result.
func TestCapabilitiesManifestValidates(t *testing.T) {
	c, _ := newCompiler(t)
	schema := compile(t, c, "capabilities/capabilities.json")
	raw, err := os.ReadFile(capabilitiesManifest)
	if err != nil {
		t.Fatalf("read capabilities manifest: %v", err)
	}
	if err := schema.Validate(parseJSON(t, raw)); err != nil {
		t.Fatalf("docs/capabilities.json does not validate: %v", err)
	}
}

// TestCapabilitiesViolations proves the schema actually constrains: every
// mutation of the valid manifest must be rejected. A schema that accepts
// anything would let a malformed manifest reach the website unnoticed.
func TestCapabilitiesViolations(t *testing.T) {
	c, _ := newCompiler(t)
	schema := compile(t, c, "capabilities/capabilities.json")
	raw, err := os.ReadFile(capabilitiesManifest)
	if err != nil {
		t.Fatalf("read capabilities manifest: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(t *testing.T, m map[string]any)
	}{
		{"unknown top-level field", func(_ *testing.T, m map[string]any) { m["roadmap"] = "soon" }},
		{"unversioned schema id", func(_ *testing.T, m map[string]any) { m["schema"] = "probavi-capabilities" }},
		{"missing generated marker", func(_ *testing.T, m map[string]any) { delete(m, "_generated") }},
		{"missing non_goals", func(_ *testing.T, m map[string]any) { delete(m, "non_goals") }},
		{"empty adapter list", func(_ *testing.T, m map[string]any) { m["adapters"] = []any{} }},
		{"unknown maturity value", func(t *testing.T, m map[string]any) {
			firstOf(t, m, "adapters")["status"] = "production"
		}},
		{"adapter without verified versions", func(t *testing.T, m map[string]any) {
			firstOf(t, m, "adapters")["verified"] = []any{}
		}},
		{"adapter with an undeclared field", func(t *testing.T, m map[string]any) {
			firstOf(t, m, "adapters")["supported_versions"] = []any{"9.6", "17"}
		}},
		{"omitted nullable instead of null", func(t *testing.T, m map[string]any) {
			delete(firstOf(t, m, "sandbox_providers"), "docs")
		}},
		{"unknown check kind", func(t *testing.T, m map[string]any) {
			firstOf(t, m, "checks")["kind"] = "javascript"
		}},
		{"non-integer exit code", func(t *testing.T, m map[string]any) {
			commands, ok := firstOf(t, m, "cli")["commands"].([]any)
			if !ok || len(commands) == 0 {
				t.Fatal("cli.commands is not a non-empty array")
			}
			cmd, ok := commands[0].(map[string]any)
			if !ok {
				t.Fatal("cli.commands[0] is not an object")
			}
			cmd["exit_codes"] = []any{map[string]any{"code": "zero", "meaning": "ok"}}
		}},
		{"locale tag that is not a language subtag", func(t *testing.T, m map[string]any) {
			locales, ok := m["locales"].(map[string]any)
			if !ok {
				t.Fatal("locales is not an object")
			}
			locales["available"] = []any{"hu_HU"}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, ok := parseJSON(t, raw).(map[string]any)
			if !ok {
				t.Fatal("capabilities manifest is not an object")
			}
			tc.mutate(t, doc)
			if err := schema.Validate(doc); err == nil {
				t.Error("schema accepted a manifest it must reject")
			}
		})
	}
}

// firstOf returns the first element of a top-level array, or the object at
// a top-level key, with checked type assertions so cases stay one-liners.
func firstOf(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	switch v := m[key].(type) {
	case map[string]any:
		return v
	case []any:
		if len(v) == 0 {
			t.Fatalf("%s is empty", key)
		}
		first, ok := v[0].(map[string]any)
		if !ok {
			t.Fatalf("%s[0] is not an object", key)
		}
		return first
	default:
		t.Fatalf("%s is neither an object nor an array", key)
		return nil
	}
}
