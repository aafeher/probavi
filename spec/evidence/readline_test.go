package evidence

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

// TestReadCappedLine pins the bound that lets this tool be pointed at a log
// it did not produce. Without it, a file containing no newline is limited
// only by the memory of the machine running the verifier.
func TestReadCappedLine(t *testing.T) {
	tests := []struct {
		name string
		// bufSize is the reader's buffer, so a line longer than it must
		// still be assembled across reads.
		bufSize       int
		input         string
		wantLine      string
		wantOversized bool
	}{
		{"short line keeps its terminator", 4096, "abc\n", "abc\n", false},
		{"torn tail", 4096, "abc", "abc", false},
		{"empty input", 4096, "", "", false},
		{"line spanning several reads", 16, strings.Repeat("x", 1000) + "\n", strings.Repeat("x", 1000) + "\n", false},
		{"a full-size record still fits", 4096, strings.Repeat("x", maxCanonicalBytes) + "\n",
			strings.Repeat("x", maxCanonicalBytes) + "\n", false},
		{"one byte past a record", 4096, strings.Repeat("x", maxCanonicalBytes+1) + "\n", "", true},
		{"no newline at all", 4096, strings.Repeat("x", maxCanonicalBytes*2), "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			br := bufio.NewReaderSize(strings.NewReader(tt.input), tt.bufSize)
			line, oversized, err := readCappedLine(br)
			if err != nil && !errors.Is(err, io.EOF) {
				t.Fatalf("readCappedLine: %v", err)
			}
			if string(line) != tt.wantLine || oversized != tt.wantOversized {
				t.Errorf("readCappedLine = (%d bytes, oversized %v), want (%d bytes, %v)",
					len(line), oversized, len(tt.wantLine), tt.wantOversized)
			}
		})
	}
}

// TestVerifyRejectsAnEndlessLine is the verdict an auditor gets instead of
// an out-of-memory kill.
func TestVerifyRejectsAnEndlessLine(t *testing.T) {
	res, err := Verify(strings.NewReader(strings.Repeat("x", maxCanonicalBytes+64)), Keyring{})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.Status != StatusInvalid || res.Line != 1 {
		t.Errorf("status = %v line = %d, want INVALID at line 1", res.Status, res.Line)
	}
	if !strings.Contains(res.Reason, "byte limit") {
		t.Errorf("reason = %q, want it to name the size rule", res.Reason)
	}
}

// TestVerifySurfacesReadErrors keeps an I/O failure distinguishable from a
// verdict about the log's contents.
func TestVerifySurfacesReadErrors(t *testing.T) {
	want := errors.New("disk on fire")
	if _, err := Verify(iotest.ErrReader(want), Keyring{}); !errors.Is(err, want) {
		t.Errorf("err = %v, want the underlying read error", err)
	}
}
