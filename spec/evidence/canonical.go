// Package evidence is an independent verifier for the Probavi evidence log
// format, schema versions probavi-evidence/0, /1 and /2.
//
// It is deliberately a *second* implementation. It was written from
// docs/evidence-schema.md alone and shares no code with the Probavi core:
// this is a separate Go module, so the language itself forbids importing
// github.com/probavi/probavi/internal/... . When this package and the core
// disagree about a log, either the specification is ambiguous or one of the
// two is wrong — catching that is the entire point of the package existing.
//
// The dependency list is empty on purpose. A verifier whose supply chain an
// auditor cannot inspect in an afternoon is not much of a verifier.
package evidence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

// maxCanonicalBytes is the per-record ceiling of evidence-schema.md §4,
// which verifiers MUST enforce.
const maxCanonicalBytes = 64 << 10

// maxSafeInteger is 2^53−1, the numeric bound of §4.
const maxSafeInteger = 1<<53 - 1

// parseRecord decodes one stored line into the generic value tree the
// canonicalizer walks. Numbers stay json.Number so that §4's integer
// restriction is checked against the literal the writer actually emitted
// rather than against a float that has already lost the evidence.
func parseRecord(line []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing content after the JSON value")
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("line is not a JSON object")
	}
	return obj, nil
}

// canonicalize renders v as RFC 8785 (JCS) bytes under the integer-only
// restriction of §4, and fails rather than guessing if the restriction is
// violated.
func canonicalize(v any) ([]byte, error) {
	var b bytes.Buffer
	if err := writeCanonical(&b, v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func writeCanonical(b *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		writeCanonicalString(b, t)
	case json.Number:
		n, err := integerValue(t)
		if err != nil {
			return err
		}
		b.WriteString(strconv.FormatInt(n, 10))
	case []any:
		return writeCanonicalArray(b, t)
	case map[string]any:
		return writeCanonicalObject(b, t)
	default:
		return fmt.Errorf("unsupported JSON value of type %T", v)
	}
	return nil
}

// writeCanonicalArray emits array elements in their given order: RFC 8785
// sorts object members, never array elements.
func writeCanonicalArray(b *bytes.Buffer, a []any) error {
	b.WriteByte('[')
	for i, e := range a {
		if i > 0 {
			b.WriteByte(',')
		}
		if err := writeCanonical(b, e); err != nil {
			return err
		}
	}
	b.WriteByte(']')
	return nil
}

func writeCanonicalObject(b *bytes.Buffer, m map[string]any) error {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortUTF16(keys)
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		writeCanonicalString(b, k)
		b.WriteByte(':')
		if err := writeCanonical(b, m[k]); err != nil {
			return err
		}
	}
	b.WriteByte('}')
	return nil
}

// integerValue enforces §4: every number is an integer with |n| ≤ 2^53−1,
// written in plain decimal. Fractional and exponent literals are rejected
// outright instead of being rounded — a verifier that quietly accepted 1.0
// for 1 would be accepting two different byte sequences for one record,
// which is precisely the ambiguity canonicalization exists to remove.
func integerValue(n json.Number) (int64, error) {
	s := n.String()
	if strings.ContainsAny(s, ".eE") {
		return 0, fmt.Errorf("number %s is not an integer literal", s)
	}
	if s == "-0" {
		return 0, fmt.Errorf("number -0 is not permitted")
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("number %s: %w", s, err)
	}
	if v > maxSafeInteger || v < -maxSafeInteger {
		return 0, fmt.Errorf("number %s exceeds the 2^53-1 bound", s)
	}
	return v, nil
}

// sortUTF16 orders keys by UTF-16 code unit, the RFC 8785 rule. Ordering by
// UTF-8 bytes would agree for every ASCII key this schema defines, but
// sandbox.params keys come from user config and may hold anything: above the
// BMP the two orders genuinely diverge, because surrogate code units
// (U+D800–U+DFFF) sort below U+E000–U+FFFF.
func sortUTF16(keys []string) {
	sort.Slice(keys, func(i, j int) bool { return lessUTF16(keys[i], keys[j]) })
}

func lessUTF16(a, b string) bool {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

// writeCanonicalString escapes per RFC 8785 §3.2.2.2: the two mandatory
// escapes, the five short control escapes, \u00xx with lowercase hex for the
// remaining C0 controls, and literal UTF-8 for everything else.
//
// Writing this by hand rather than calling encoding/json is not
// gold-plating: Go's encoder also escapes '<', '>' and '&' as < and
// friends, which produces bytes no conforming JCS implementation would emit
// and would break verification of any record containing those characters.
func writeCanonicalString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}
