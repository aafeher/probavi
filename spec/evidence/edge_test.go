package evidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// canonLine builds a log line that is canonical by construction, so that
// verification reaches the structural assertions of §9 instead of stopping
// at the byte comparison. It is the only way to test what the verifier does
// with a well-formed but nonsensical record.
func canonLine(t *testing.T, rec map[string]any) []byte {
	t.Helper()
	b, err := canonicalize(rec)
	if err != nil {
		t.Fatalf("canonicalize fixture: %v", err)
	}
	return append(b, '\n')
}

// baseRecord is a canonical, correctly chained skeleton whose signature is
// wrong. Every case below breaks one thing earlier in the §9 order than the
// signature check, so the signature never gets a say.
func baseRecord() map[string]any {
	return map[string]any{
		"schema":    "probavi-evidence/1",
		"seq":       json.Number("1"),
		"prev_hash": genesisPrevHash,
		"sig": map[string]any{
			"alg":     "ed25519",
			"key_id":  "56475aa75463474c",
			"sig_b64": strings.Repeat("A", 86) + "==",
		},
	}
}

// sigOf returns the record's sig object with a checked assertion, so a
// fixture drift surfaces as a test failure rather than a nil-map panic.
func sigOf(t *testing.T, rec map[string]any) map[string]any {
	t.Helper()
	sig, ok := rec["sig"].(map[string]any)
	if !ok {
		t.Fatal("fixture has no sig object")
	}
	return sig
}

func TestStructurallyBrokenRecords(t *testing.T) {
	kr := NewKeyring(exampleKey(t))

	cases := []struct {
		name       string
		mutate     func(t *testing.T, rec map[string]any)
		wantReason string
	}{
		{
			name:       "seq absent",
			mutate:     func(_ *testing.T, rec map[string]any) { delete(rec, "seq") },
			wantReason: "seq is missing or not an integer",
		},
		{
			name:       "seq is a string",
			mutate:     func(_ *testing.T, rec map[string]any) { rec["seq"] = "1" },
			wantReason: "seq is missing or not an integer",
		},
		{
			name:       "sig absent",
			mutate:     func(_ *testing.T, rec map[string]any) { delete(rec, "sig") },
			wantReason: "sig is missing or not an object",
		},
		{
			name:       "sig is not an object",
			mutate:     func(_ *testing.T, rec map[string]any) { rec["sig"] = "signed, trust me" },
			wantReason: "sig is missing or not an object",
		},
		{
			name: "unsupported signature algorithm",
			mutate: func(t *testing.T, rec map[string]any) {
				sig := sigOf(t, rec)
				sig["alg"] = "rsa"
			},
			wantReason: `unsupported signature algorithm "rsa"`,
		},
		{
			name: "key_id absent",
			mutate: func(t *testing.T, rec map[string]any) {
				delete(sigOf(t, rec), "key_id")
			},
			wantReason: "sig.key_id is missing or not a string",
		},
		{
			name: "sig_b64 absent",
			mutate: func(t *testing.T, rec map[string]any) {
				delete(sigOf(t, rec), "sig_b64")
			},
			wantReason: "sig.sig_b64 is missing or not a string",
		},
		{
			name: "sig_b64 is not base64",
			mutate: func(t *testing.T, rec map[string]any) {
				sig := sigOf(t, rec)
				sig["sig_b64"] = "!!! not base64 !!!"
			},
			wantReason: "sig_b64 is not valid base64",
		},
		{
			name: "signature is the wrong length",
			mutate: func(t *testing.T, rec map[string]any) {
				sig := sigOf(t, rec)
				sig["sig_b64"] = "QUJD" // "ABC": valid base64, three bytes
			},
			wantReason: "signature is 3 bytes, want 64",
		},
		{
			name:       "schema absent",
			mutate:     func(_ *testing.T, rec map[string]any) { delete(rec, "schema") },
			wantReason: `unsupported schema ""`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := baseRecord()
			tc.mutate(t, rec)
			res, err := Verify(bytes.NewReader(canonLine(t, rec)), kr)
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if res.Status != StatusInvalid {
				t.Fatalf("status = %s, want INVALID", res.Status)
			}
			if !strings.Contains(res.Reason, tc.wantReason) {
				t.Errorf("reason = %q, want it to contain %q", res.Reason, tc.wantReason)
			}
			if res.Line != 1 {
				t.Errorf("line = %d, want 1", res.Line)
			}
		})
	}
}

// TestVerifyPropagatesReadErrors keeps an I/O failure from being reported as
// a verdict: a log that could not be read in full is not a log that failed
// verification, and conflating the two would let a disk error read as
// "INVALID" or, far worse, as "VALID".
func TestVerifyPropagatesReadErrors(t *testing.T) {
	want := errors.New("disk went away")
	_, err := Verify(failingReader{err: want}, NewKeyring(exampleKey(t)))
	if err == nil {
		t.Fatal("Verify swallowed a read error")
	}
	if !errors.Is(err, want) {
		t.Errorf("error = %v, want it to wrap %v", err, want)
	}
}

type failingReader struct{ err error }

func (f failingReader) Read([]byte) (int, error) { return 0, f.err }

func TestLessUTF16EdgeCases(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"", "a", true},
		{"a", "", false},
		{"a", "a", false},
		{"a", "ab", true},
		{"ab", "a", false},
	}
	for _, tc := range cases {
		if got := lessUTF16(tc.a, tc.b); got != tc.want {
			t.Errorf("lessUTF16(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestIntegerBeyondInt64 covers the literal that JSON accepts, json.Number
// holds happily, and strconv refuses — distinct from the 2^53−1 schema bound.
func TestIntegerBeyondInt64(t *testing.T) {
	if _, err := integerValue(json.Number("99999999999999999999999")); err == nil {
		t.Fatal("integerValue accepted a literal that overflows int64")
	}
}

// TestIntegerField exercises the helper directly. Reached through Verify its
// numeric-rejection branch is unreachable, because canonicalization has
// already screened every number — but the helper is what future callers will
// reuse, so it is pinned here rather than left to trust.
func TestIntegerField(t *testing.T) {
	rec := map[string]any{
		"good":       json.Number("42"),
		"fractional": json.Number("1.5"),
		"huge":       json.Number("9007199254740992"),
		"text":       "42",
	}
	if v, ok := integerField(rec, "good"); !ok || v != 42 {
		t.Errorf("integerField(good) = %d, %v; want 42, true", v, ok)
	}
	for _, name := range []string{"fractional", "huge", "text", "absent"} {
		if _, ok := integerField(rec, name); ok {
			t.Errorf("integerField(%s) accepted a value that is not a schema integer", name)
		}
	}
}

// TestCanonicalizeErrorsPropagateFromNestedValues proves the walker does not
// lose a violation buried inside an array or a nested object — the places a
// forger would most like it to be overlooked.
func TestCanonicalizeErrorsPropagateFromNestedValues(t *testing.T) {
	for name, in := range map[string]string{
		"inside an array":         `{"a":[1,2.5]}`,
		"inside a nested object":  `{"a":{"b":{"c":1e9}}}`,
		"inside an array of maps": `{"a":[{"b":-0}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec, err := parseRecord([]byte(in))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if _, err := canonicalize(rec); err == nil {
				t.Error("canonicalize missed a nested violation")
			}
		})
	}
}
