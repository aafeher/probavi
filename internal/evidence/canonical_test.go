package evidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func TestCanonicalizeStrings(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "abc", `"abc"`},
		{"quote and backslash", `a"b\c`, `"a\"b\\c"`},
		{"short control escapes", "\b\t\n\f\r", `"\b\t\n\f\r"`},
		{"other controls lowercase hex", "\x01\x1f", `"\u0001\u001f"`},
		{"solidus not escaped", "a/b", `"a/b"`},
		{"unicode literal utf-8", "árvíztűrő 😀", `"árvíztűrő 😀"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Canonicalize(tt.in)
			if err != nil {
				t.Fatalf("Canonicalize(%q): %v", tt.in, err)
			}
			if string(got) != tt.want {
				t.Errorf("Canonicalize(%q) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}

	if _, err := Canonicalize("\xff invalid"); err == nil {
		t.Error("Canonicalize accepted invalid UTF-8")
	}
}

func TestCanonicalizeNumbers(t *testing.T) {
	valid := []string{"0", "-1", "42", "9007199254740991", "-9007199254740991"}
	for _, s := range valid {
		if _, err := Canonicalize(json.Number(s)); err != nil {
			t.Errorf("Canonicalize(%s): unexpected error %v", s, err)
		}
	}
	invalid := []string{"1.5", "10.0", "1e2", "1E2", "-0", "9007199254740992", "-9007199254740992"}
	for _, s := range invalid {
		if _, err := Canonicalize(json.Number(s)); !errors.Is(err, ErrNotInteger) {
			t.Errorf("Canonicalize(%s): got %v, want ErrNotInteger", s, err)
		}
	}
}

func TestCanonicalizeSortsKeysByUTF16(t *testing.T) {
	// U+FFFD encodes in UTF-8 as EF BF BD, U+1F600 as F0 9F 98 80: byte
	// order puts U+FFFD first. In UTF-16, U+1F600 starts with surrogate
	// 0xd83d < 0xfffd, so RFC 8785 order puts the emoji first.
	in := map[string]any{"�": json.Number("1"), "😀": json.Number("2")}
	got, err := Canonicalize(in)
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	want := `{"😀":2,"` + "�" + `":1}`
	if string(got) != want {
		t.Errorf("Canonicalize = %s, want %s", got, want)
	}
}

func TestCanonicalizeCompositesAndErrors(t *testing.T) {
	v, err := decodeStrict([]byte(`{"b":[1,null,true,"x"],"a":{"z":0,"y":-3}}`))
	if err != nil {
		t.Fatalf("decodeStrict: %v", err)
	}
	got, err := Canonicalize(v)
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	want := `{"a":{"y":-3,"z":0},"b":[1,null,true,"x"]}`
	if string(got) != want {
		t.Errorf("Canonicalize = %s, want %s", got, want)
	}

	if _, err := Canonicalize(struct{}{}); err == nil {
		t.Error("Canonicalize accepted an unsupported Go type")
	}
	if _, err := Canonicalize([]any{json.Number("1.5")}); !errors.Is(err, ErrNotInteger) {
		t.Errorf("nested float: got %v, want ErrNotInteger", err)
	}
	if _, err := Canonicalize(map[string]any{"k": json.Number("2e3")}); !errors.Is(err, ErrNotInteger) {
		t.Errorf("nested exponent: got %v, want ErrNotInteger", err)
	}
}

func TestDecodeStrict(t *testing.T) {
	if _, err := decodeStrict([]byte(`{"a":1} {"b":2}`)); err == nil {
		t.Error("decodeStrict accepted trailing data")
	}
	if _, err := decodeStrict([]byte(`{"a":`)); err == nil {
		t.Error("decodeStrict accepted truncated JSON")
	}
	v, err := decodeStrict([]byte(`123`))
	if err != nil {
		t.Fatalf("decodeStrict: %v", err)
	}
	if n, ok := v.(json.Number); !ok || n.String() != "123" {
		t.Errorf("decodeStrict(123) = %#v, want json.Number(123)", v)
	}
}

func TestCanonicalizeRecordStability(t *testing.T) {
	rec := sampleRecordPass()
	a, err := CanonicalizeRecord(rec)
	if err != nil {
		t.Fatalf("CanonicalizeRecord: %v", err)
	}
	b, err := CanonicalizeRecord(rec)
	if err != nil {
		t.Fatalf("CanonicalizeRecord: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Error("CanonicalizeRecord is not deterministic")
	}
	if bytes.Contains(a, []byte("sig")) {
		t.Error("canonical bytes of an unsigned record must not contain a sig field")
	}
}
