package evidence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// integerPattern accepts exactly the decimal integer forms the schema
// permits: no fractions, no exponents, no leading zeros, no "-0".
var integerPattern = regexp.MustCompile(`^(0|-?[1-9][0-9]*)$`)

// CanonicalizeRecord returns the canonical serialization of a record
// (evidence-schema.md §4). With rec.Sig == nil the sig field is absent —
// exactly the byte string that gets signed.
//
// The record must have passed Validate: encoding/json silently replaces
// invalid UTF-8 with U+FFFD, so canonicalizing an unvalidated record could
// sign silently altered content.
func CanonicalizeRecord(rec *Record) ([]byte, error) {
	raw, err := json.Marshal(rec)
	if err != nil {
		return nil, fmt.Errorf("marshal record: %w", err)
	}
	v, err := decodeStrict(raw)
	if err != nil {
		return nil, fmt.Errorf("re-decode record: %w", err)
	}
	return Canonicalize(v)
}

// Canonicalize serializes a decoded JSON value per RFC 8785 (JCS) under the
// schema's integer-only restriction. The value must come from decodeStrict
// (numbers as json.Number).
func Canonicalize(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := appendCanonical(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decodeStrict parses exactly one JSON value, keeping numbers textual so the
// integer restriction can be enforced without float round-trips.
func decodeStrict(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("parse JSON: trailing data after value")
	}
	return v, nil
}

func appendCanonical(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		buf.WriteString(strconv.FormatBool(t))
	case json.Number:
		return appendCanonicalNumber(buf, t)
	case string:
		return appendCanonicalString(buf, t)
	case []any:
		return appendCanonicalArray(buf, t)
	case map[string]any:
		return appendCanonicalObject(buf, t)
	default:
		return fmt.Errorf("%w: unsupported JSON value of type %T", ErrInvalidRecord, v)
	}
	return nil
}

// appendCanonicalNumber enforces the integer-only restriction: under it, JCS
// number serialization degenerates to the literal decimal form.
func appendCanonicalNumber(buf *bytes.Buffer, n json.Number) error {
	s := n.String()
	if !integerPattern.MatchString(s) {
		return fmt.Errorf("%w: %q", ErrNotInteger, s)
	}
	i, err := strconv.ParseInt(s, 10, 64)
	if err != nil || i > MaxSafeInteger || i < -MaxSafeInteger {
		return fmt.Errorf("%w: %q outside |n| <= 2^53-1", ErrNotInteger, s)
	}
	buf.WriteString(s)
	return nil
}

// appendCanonicalString escapes per RFC 8785 §3.2.2.2: only the two
// mandatory characters and controls are escaped (short forms where defined,
// lowercase \u00xx otherwise); everything else is literal UTF-8.
func appendCanonicalString(buf *bytes.Buffer, s string) error {
	if !utf8.ValidString(s) {
		return fmt.Errorf("%w: string is not valid UTF-8", ErrInvalidRecord)
	}
	buf.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\t':
			buf.WriteString(`\t`)
		case '\n':
			buf.WriteString(`\n`)
		case '\f':
			buf.WriteString(`\f`)
		case '\r':
			buf.WriteString(`\r`)
		default:
			if r < 0x20 {
				fmt.Fprintf(buf, `\u%04x`, r)
			} else {
				buf.WriteRune(r)
			}
		}
	}
	buf.WriteByte('"')
	return nil
}

func appendCanonicalArray(buf *bytes.Buffer, arr []any) error {
	buf.WriteByte('[')
	for i, el := range arr {
		if i > 0 {
			buf.WriteByte(',')
		}
		if err := appendCanonical(buf, el); err != nil {
			return err
		}
	}
	buf.WriteByte(']')
	return nil
}

func appendCanonicalObject(buf *bytes.Buffer, obj map[string]any) error {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return lessUTF16(keys[i], keys[j]) })
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		if err := appendCanonicalString(buf, k); err != nil {
			return err
		}
		buf.WriteByte(':')
		if err := appendCanonical(buf, obj[k]); err != nil {
			return err
		}
	}
	buf.WriteByte('}')
	return nil
}

// lessUTF16 orders property names by UTF-16 code units as RFC 8785
// requires. This differs from byte order for non-BMP characters (encoded as
// surrogate pairs starting at 0xd800) versus code points in 0xe000–0xffff.
func lessUTF16(a, b string) bool {
	ua := utf16.Encode([]rune(a))
	ub := utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}
