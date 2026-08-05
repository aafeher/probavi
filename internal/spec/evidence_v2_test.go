package spec_test

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// evidenceSchemaDoc is the normative document; docs/schemas/evidence is
// derived from it (record.json says so in its own $comment).
const evidenceSchemaDoc = "../../docs/evidence-schema.md"

// docExampleRe captures the first fenced JSON block of the document, which
// is §3's worked record.
var docExampleRe = regexp.MustCompile("(?s)## 3\\. Record shape.*?```json\n(.*?)```")

// sigPlaceholder is the elision §3 uses so the example stays readable. The
// signature is illustrative there — §12 publishes byte-exact signed logs
// for the real thing — so the test swaps in a well-formed stand-in and
// checks everything else.
const sigPlaceholder = "hVb0(…88 base64 chars encoding the 64-byte signature…)Cg=="

func docExampleRecord(t *testing.T) []byte {
	t.Helper()
	doc, err := os.ReadFile(evidenceSchemaDoc)
	if err != nil {
		t.Fatalf("read %s: %v", evidenceSchemaDoc, err)
	}
	m := docExampleRe.FindSubmatch(doc)
	if m == nil {
		t.Fatalf("%s: no fenced JSON example under §3", evidenceSchemaDoc)
	}
	body := string(m[1])
	if !strings.Contains(body, sigPlaceholder) {
		t.Fatalf("%s: §3 example no longer carries the signature elision this test substitutes", evidenceSchemaDoc)
	}
	return []byte(strings.Replace(body, sigPlaceholder, strings.Repeat("A", 86)+"==", 1))
}

// TestEvidenceDocExampleValidates holds the worked record in the normative
// document to the schema derived from it.
//
// Nothing did, and the two drifted: the example declared
// probavi-evidence/0 while carrying drill.pitr_target, a combination the
// v0 branch rejects — the field was added to the example when v1 landed
// without moving its schema identifier. A reader copying the example as a
// starting point would have produced records their own verifier refuses.
func TestEvidenceDocExampleValidates(t *testing.T) {
	c, _ := newCompiler(t)
	record := compile(t, c, "evidence/record.json")
	if err := record.Validate(parseJSON(t, docExampleRecord(t))); err != nil {
		t.Errorf("the §3 example of %s does not validate against docs/schemas/evidence/record.json: %v",
			evidenceSchemaDoc, err)
	}
}

// TestEvidenceV2Shape proves the v2 branch constrains rather than merely
// existing: the digests are required members that accept a sha256
// reference or null and nothing else, and they belong to v2 alone.
func TestEvidenceV2Shape(t *testing.T) {
	c, _ := newCompiler(t)
	record := compile(t, c, "evidence/record.json")

	// The document's example is a v2 record, so it is the fixture.
	var base map[string]any
	if err := json.Unmarshal(docExampleRecord(t), &base); err != nil {
		t.Fatalf("decode §3 example: %v", err)
	}

	clone := func(t *testing.T) map[string]any {
		t.Helper()
		raw, err := json.Marshal(base)
		if err != nil {
			t.Fatalf("re-encode fixture: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("re-decode fixture: %v", err)
		}
		return m
	}

	t.Run("null digests are accepted", func(t *testing.T) {
		m := clone(t)
		child(t, m, "adapter")["digest"] = nil
		child(t, m, "env")["probavi_digest"] = nil
		if err := record.Validate(any(m)); err != nil {
			t.Errorf("a v2 record with null digests must validate — an unreadable "+
				"executable may not cost a drill its record: %v", err)
		}
	})

	rejected := []struct {
		name   string
		mutate func(t *testing.T, m map[string]any)
	}{
		{"v2 without adapter.digest", func(t *testing.T, m map[string]any) {
			delete(child(t, m, "adapter"), "digest")
		}},
		{"v2 without env.probavi_digest", func(t *testing.T, m map[string]any) {
			delete(child(t, m, "env"), "probavi_digest")
		}},
		{"adapter.digest not a sha256 reference", func(t *testing.T, m map[string]any) {
			child(t, m, "adapter")["digest"] = "4c9a1f0e"
		}},
		{"adapter.digest uppercase hex", func(t *testing.T, m map[string]any) {
			child(t, m, "adapter")["digest"] = "sha256:" + strings.Repeat("A", 64)
		}},
		{"env.probavi_digest with the wrong algorithm", func(t *testing.T, m map[string]any) {
			child(t, m, "env")["probavi_digest"] = "sha1:" + strings.Repeat("b", 40)
		}},
		{"unknown member beside the digest", func(t *testing.T, m map[string]any) {
			child(t, m, "adapter")["build"] = "x"
		}},
		{"v1 downgrade keeping the digests", func(_ *testing.T, m map[string]any) {
			m["schema"] = "probavi-evidence/1"
		}},
		{"v0 downgrade keeping the digests", func(_ *testing.T, m map[string]any) {
			m["schema"] = "probavi-evidence/0"
		}},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			m := clone(t)
			tc.mutate(t, m)
			if err := record.Validate(any(m)); err == nil {
				t.Error("mutated record validates, want rejection")
			}
		})
	}
}

// TestEvidenceV1RejectsDigests is the other direction: a v1 record must
// not smuggle in a v2 field. Fixed shape per version is what lets a
// verifier decide which branch a record belongs to (§3, §10).
func TestEvidenceV1RejectsDigests(t *testing.T) {
	c, _ := newCompiler(t)
	record := compile(t, c, "evidence/record.json")
	lines := goldenLines(t, "../../docs/schemas/evidence/examples/log_v1.jsonl")

	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, m map[string]any)
	}{
		{"v1 adapter carrying a digest", func(t *testing.T, m map[string]any) {
			child(t, m, "adapter")["digest"] = "sha256:" + strings.Repeat("a", 64)
		}},
		{"v1 env carrying a probavi_digest", func(t *testing.T, m map[string]any) {
			child(t, m, "env")["probavi_digest"] = "sha256:" + strings.Repeat("a", 64)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := parseJSON(t, lines[0])
			m, ok := doc.(map[string]any)
			if !ok {
				t.Fatal("golden line is not an object")
			}
			tc.mutate(t, m)
			if err := record.Validate(doc); err == nil {
				t.Error("a v1 record with a v2 field validates, want rejection")
			}
		})
	}
}
