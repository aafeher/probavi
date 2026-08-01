package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"regexp"
	"testing"
)

// TestHostID pins the derivation to the evidence-schema §3 env.host_id
// rule: both providers stamp this value on sandboxes, and a drift would
// break sweep scoping between mixed probavi versions sharing a runtime.
func TestHostID(t *testing.T) {
	id := HostID()
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(id) {
		t.Fatalf("HostID() = %q, want 16 lowercase hex chars", id)
	}
	if again := HostID(); again != id {
		t.Errorf("HostID is not deterministic: %q then %q", id, again)
	}
	name, err := os.Hostname()
	if err != nil {
		t.Skipf("hostname unavailable: %v", err)
	}
	sum := sha256.Sum256([]byte(name))
	if want := hex.EncodeToString(sum[:])[:16]; id != want {
		t.Errorf("HostID() = %q, want %q (first 16 hex of sha256(hostname))", id, want)
	}
}
