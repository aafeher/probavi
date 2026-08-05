package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestFileDigest(t *testing.T) {
	dir := t.TempDir()

	t.Run("hashes the bytes", func(t *testing.T) {
		path := filepath.Join(dir, "binary")
		content := []byte("not really an executable")
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		got := FileDigest(path)
		if got == nil {
			t.Fatal("FileDigest returned nil for a readable file")
		}
		sum := sha256.Sum256(content)
		if want := "sha256:" + hex.EncodeToString(sum[:]); *got != want {
			t.Errorf("digest = %q, want %q", *got, want)
		}
		if !sha256RefPattern.MatchString(*got) {
			t.Errorf("digest %q is not the reference form the schema requires", *got)
		}
	})

	t.Run("empty file still hashes", func(t *testing.T) {
		path := filepath.Join(dir, "empty")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if FileDigest(path) == nil {
			t.Error("an empty file has a digest; nil would misreport it as unreadable")
		}
	})

	// Nil is the documented answer for anything unreadable: §3 makes the
	// digest nullable so build identity never costs a drill its record.
	t.Run("missing file is null, not an error", func(t *testing.T) {
		if got := FileDigest(filepath.Join(dir, "absent")); got != nil {
			t.Errorf("digest = %q, want nil", *got)
		}
	})

	t.Run("a directory is null", func(t *testing.T) {
		if got := FileDigest(dir); got != nil {
			t.Errorf("digest = %q, want nil for a directory", *got)
		}
	})

	t.Run("a record accepts what it produces", func(t *testing.T) {
		path := filepath.Join(dir, "binary")
		rec := sampleRecordPass()
		rec.Adapter.Digest = FileDigest(path)
		rec.Env.ProbaviDigest = FileDigest(path)
		if err := rec.Validate(); err != nil {
			t.Errorf("a record carrying real digests must validate: %v", err)
		}
	})
}
