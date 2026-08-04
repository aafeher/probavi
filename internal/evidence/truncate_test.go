package evidence

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateLine(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		maxBytes int
		want     string
	}{
		{"fits", "short", 10, "short"},
		{"exact fit", "abcde", 5, "abcde"},
		{"ascii cut", "abcdefghij", 8, "abcde..."},
		{"two-byte runes cut mid-rune", strings.Repeat("é", 5), 7, "éé..."},
		{"three-byte runes cut mid-rune", strings.Repeat("€", 4), 9, "€€..."},
		{"four-byte runes cut mid-rune", strings.Repeat("𝄞", 3), 10, "𝄞..."},
		{"budget just above the ellipsis", "ábcd", 4, "..."},
		{"budget equals the ellipsis", "abcd", 3, "..."},
		{"budget below the ellipsis", "abcd", 2, ".."},
		{"zero budget", "abcd", 0, ""},
		{"negative budget", "abcd", -1, ""},
		{"empty input", "", 10, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateLine(tt.in, tt.maxBytes)
			if got != tt.want {
				t.Errorf("TruncateLine(%q, %d) = %q, want %q", tt.in, tt.maxBytes, got, tt.want)
			}
			if len(got) > tt.maxBytes && tt.maxBytes > 0 {
				t.Errorf("result is %d bytes, over the %d budget", len(got), tt.maxBytes)
			}
		})
	}
}

// TestTruncateLineNeverSplitsARune sweeps every budget across strings of
// every UTF-8 stride. A single split rune anywhere makes a record
// unwritable, so the property matters at every offset, not at a sample.
func TestTruncateLineNeverSplitsARune(t *testing.T) {
	for _, filler := range []string{"a", "é", "€", "𝄞", "aé€𝄞"} {
		s := strings.Repeat(filler, 20)
		for maxBytes := range len(s) + 4 {
			got := TruncateLine(s, maxBytes)
			if !utf8.ValidString(got) {
				t.Fatalf("TruncateLine(%q, %d) produced invalid UTF-8: %q", s, maxBytes, got)
			}
			if len(got) > maxBytes {
				t.Fatalf("TruncateLine(%q, %d) returned %d bytes", s, maxBytes, len(got))
			}
		}
	}
}

// TestTruncatedFieldsPassValidation closes the loop: whatever the helper
// produces at the published caps must satisfy the record layer, because
// the only reason the helper exists is that Validate rejects rather than
// repairs.
func TestTruncatedFieldsPassValidation(t *testing.T) {
	detail := TruncateLine(strings.Repeat("é", 500), MaxDetailBytes)
	message := TruncateLine(strings.Repeat("á", 500), MaxErrorMessageBytes)
	if err := validateLine("checks[0].detail", detail, MaxDetailBytes); err != nil {
		t.Errorf("truncated detail rejected: %v", err)
	}
	if err := validateLine("error.message", message, MaxErrorMessageBytes); err != nil {
		t.Errorf("truncated message rejected: %v", err)
	}
	if !utf8.ValidString(detail) || !utf8.ValidString(message) {
		t.Error("truncation produced invalid UTF-8")
	}
}
