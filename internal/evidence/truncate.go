package evidence

import "unicode/utf8"

// Byte caps on the human-readable strings a record may carry
// (evidence-schema.md §3). They are byte counts, not character counts:
// Validate measures bytes, and a producer that counts characters will
// overshoot on any non-ASCII text.
const (
	// MaxDetailBytes caps checks[].detail.
	MaxDetailBytes = maxDetailLen
	// MaxErrorMessageBytes caps error.message.
	MaxErrorMessageBytes = maxErrorMessageLen
)

// ellipsis marks a truncated string. Deliberately ASCII: it is appended
// after the budget is computed, so its own length must not depend on
// encoding.
const ellipsis = "..."

// TruncateLine shortens s to at most maxBytes bytes, never splitting a
// rune. Producers of record fields must use it rather than slicing: a cut
// inside a multi-byte sequence yields invalid UTF-8, which Validate
// rejects — and a rejected record is a drill that ran and left no proof,
// so an over-long check detail must never be able to cause one.
//
// A truncated result ends in "..." and still fits the budget. Input that
// already fits is returned unchanged.
func TruncateLine(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	if maxBytes <= len(ellipsis) {
		return ellipsis[:maxBytes]
	}
	cut := maxBytes - len(ellipsis)
	// Walk back to a rune boundary. s[cut] is in range because cut <
	// maxBytes < len(s); invalid input can only shorten the result.
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}
