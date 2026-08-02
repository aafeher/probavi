package evidence

import (
	"encoding/json"
	"strings"
	"testing"
)

// canonOf parses a JSON document and returns its canonical form, so the
// cases below can be written as "input JSON → expected canonical bytes".
func canonOf(t *testing.T, in string) string {
	t.Helper()
	rec, err := parseRecord([]byte(in))
	if err != nil {
		t.Fatalf("parse %s: %v", in, err)
	}
	out, err := canonicalize(rec)
	if err != nil {
		t.Fatalf("canonicalize %s: %v", in, err)
	}
	return string(out)
}

func TestCanonicalForm(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "keys sorted, whitespace dropped",
			in:   `{ "b": 2,  "a": 1 }`,
			want: `{"a":1,"b":2}`,
		},
		{
			name: "nested objects and arrays keep element order",
			in:   `{"z":[3,1,2],"a":{"y":1,"x":2}}`,
			want: `{"a":{"x":2,"y":1},"z":[3,1,2]}`,
		},
		{
			name: "literals",
			in:   `{"t":true,"f":false,"n":null}`,
			want: `{"f":false,"n":null,"t":true}`,
		},
		{
			name: "negative integers",
			in:   `{"a":-1,"b":0}`,
			want: `{"a":-1,"b":0}`,
		},
		{
			// RFC 8785 escapes only what it must. Go's encoding/json would
			// additionally emit <, > and & here, which is why
			// this package does not use it for canonical bytes.
			name: "HTML-significant characters stay literal",
			in:   `{"a":"<b>&</b>"}`,
			want: `{"a":"<b>&</b>"}`,
		},
		{
			name: "solidus is not escaped",
			in:   `{"a":"a/b"}`,
			want: `{"a":"a/b"}`,
		},
		{
			name: "mandatory and short escapes",
			in:   `{"a":"q\"b\\s\bf\f n\nr\rt\t"}`,
			want: `{"a":"q\"b\\s\bf\f n\nr\rt\t"}`,
		},
		{
			// Controls without a short escape become \\u00xx, and the hex
			// digits are lowercase even when the producer wrote them in
			// uppercase — a one-byte difference in the signed message.
			name: "other C0 controls use lowercase \\u00xx",
			in:   "{\"a\":\"\\u0000\\u001F\"}",
			want: "{\"a\":\"\\u0000\\u001f\"}",
		},
		{
			name: "non-ASCII stays literal UTF-8",
			in:   `{"a":"árvíztűrő é"}`,
			want: `{"a":"árvíztűrő é"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := canonOf(t, tc.in); got != tc.want {
				t.Errorf("canonical form:\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

// TestKeyOrderIsUTF16NotCodePoint is the case where the RFC 8785 sorting
// rule actually bites. U+FFFF sorts before U+10000 by code point (and so by
// UTF-8 bytes), but in UTF-16 the astral character starts with the high
// surrogate U+D800, which sorts below U+FFFF — so the canonical order is the
// reverse of the intuitive one. Schema-defined keys are all ASCII, but
// sandbox.params keys come from user config and can be anything.
func TestKeyOrderIsUTF16NotCodePoint(t *testing.T) {
	got := canonOf(t, `{"￿":1,"𐀀":2}`)
	want := "{\"\U00010000\":2,\"￿\":1}"
	if got != want {
		t.Errorf("canonical key order:\n got %q\nwant %q", got, want)
	}
	if !lessUTF16("\U00010000", "￿") {
		t.Error("lessUTF16 does not order the astral key first")
	}
	// The naive comparison this rule exists to prevent.
	if "\U00010000" < "￿" {
		t.Error("test premise broken: Go string order should disagree with UTF-16 here")
	}
}

func TestIntegerRestriction(t *testing.T) {
	rejected := []struct {
		name string
		in   string
	}{
		{"fractional", `{"a":1.5}`},
		{"integral but fractional notation", `{"a":1.0}`},
		{"exponent notation", `{"a":1e3}`},
		{"negative zero", `{"a":-0}`},
		{"above 2^53-1", `{"a":9007199254740992}`},
		{"below -(2^53-1)", `{"a":-9007199254740992}`},
	}
	for _, tc := range rejected {
		t.Run("reject "+tc.name, func(t *testing.T) {
			rec, err := parseRecord([]byte(tc.in))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if _, err := canonicalize(rec); err == nil {
				t.Fatal("canonicalize accepted a number the schema forbids")
			}
		})
	}

	accepted := []string{
		`{"a":0}`,
		`{"a":-1}`,
		`{"a":9007199254740991}`,
		`{"a":-9007199254740991}`,
	}
	for _, in := range accepted {
		t.Run("accept "+in, func(t *testing.T) {
			rec, err := parseRecord([]byte(in))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if _, err := canonicalize(rec); err != nil {
				t.Fatalf("canonicalize rejected a legal integer: %v", err)
			}
		})
	}
}

func TestParseRecordRejectsNonObjects(t *testing.T) {
	for _, in := range []string{`[]`, `"a string"`, `42`, `null`, `{}{}`, `{`, ``} {
		if _, err := parseRecord([]byte(in)); err == nil {
			t.Errorf("parseRecord(%q) accepted a line that is not one JSON object", in)
		}
	}
}

// TestDuplicateKeysCannotSurvive documents why the verifier needs no special
// duplicate-key handling: encoding/json keeps the last value, so the
// re-canonicalized form necessarily differs from the stored bytes and the
// byte comparison in §9 rejects the line.
func TestDuplicateKeysCannotSurvive(t *testing.T) {
	const in = `{"a":1,"a":2}`
	rec, err := parseRecord([]byte(in))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out, err := canonicalize(rec)
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	if string(out) == in {
		t.Fatal("duplicate-key line round-tripped unchanged; §9's byte check would not catch it")
	}
}

func TestParsePublicKey(t *testing.T) {
	const valid = "03a107bff3ce10be1d70dd18e74bc09967e4d6309ba50d5f1ddc8664125531b8"

	pub, err := ParsePublicKey([]byte(valid + "\n"))
	if err != nil {
		t.Fatalf("ParsePublicKey: %v", err)
	}
	if len(pub) != 32 {
		t.Fatalf("key length = %d, want 32", len(pub))
	}
	if got := KeyID(pub); len(got) != 16 {
		t.Errorf("KeyID = %q, want 16 hex characters", got)
	}

	rejected := map[string]string{
		"too short":    valid[:62],
		"too long":     valid + "ab",
		"uppercase":    strings.ToUpper(valid),
		"not hex":      strings.Repeat("z", 64),
		"empty":        "",
		"only spacing": "   \n",
	}
	for name, in := range rejected {
		t.Run("reject "+name, func(t *testing.T) {
			if _, err := ParsePublicKey([]byte(in)); err == nil {
				t.Error("ParsePublicKey accepted a malformed key file")
			}
		})
	}
}

// TestCanonicalizeRejectsForeignTypes guards the walker's default branch:
// values that never come from parseRecord (here a float64 produced by a
// plain Unmarshal) must fail loudly rather than be silently serialized in
// some other shape.
func TestCanonicalizeRejectsForeignTypes(t *testing.T) {
	var v any
	if err := json.Unmarshal([]byte(`{"a":1}`), &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, err := canonicalize(v); err == nil {
		t.Fatal("canonicalize accepted a float64; only json.Number is valid here")
	}
}
