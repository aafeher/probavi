package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func touch(t *testing.T, dir, name string, mtime time.Time) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", name, err)
	}
	return path
}

func TestResolveSourceKinds(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	old := touch(t, dir, "a-old.dump", base)
	newest := touch(t, dir, "b-new.dump", base.Add(time.Hour))
	if err := os.Mkdir(filepath.Join(dir, "sub.dump"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	t.Run("pgdump file", func(t *testing.T) {
		src, perr := resolveSource("pgdump", old)
		if perr != nil {
			t.Fatalf("resolveSource: %+v", perr)
		}
		if src.path != old || src.sizeBytes != int64(len("a-old.dump")) {
			t.Errorf("src = %+v", src)
		}
		if src.createdAt == nil || *src.createdAt != "2026-07-30T12:00:00.000Z" {
			t.Errorf("createdAt = %v, want the file mtime in RFC 3339 ms", src.createdAt)
		}
	})

	t.Run("pgdump_dir picks newest and ignores directories", func(t *testing.T) {
		src, perr := resolveSource("pgdump_dir", dir)
		if perr != nil {
			t.Fatalf("resolveSource: %+v", perr)
		}
		if src.path != newest {
			t.Errorf("picked %s, want %s", src.path, newest)
		}
	})

	t.Run("pgdump_dir mtime tie breaks by name", func(t *testing.T) {
		tie := t.TempDir()
		touch(t, tie, "alpha.dump", base)
		zeta := touch(t, tie, "zeta.dump", base)
		src, perr := resolveSource("pgdump_dir", tie)
		if perr != nil {
			t.Fatalf("resolveSource: %+v", perr)
		}
		if src.path != zeta {
			t.Errorf("picked %s, want deterministic tie-break to %s", src.path, zeta)
		}
	})

}

func TestResolveSourceErrors(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "present.dump", time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC))

	tests := []struct {
		name     string
		kind     string
		path     string
		wantCode string
	}{
		{"unknown kind", "wal-g", dir, "unsupported_source"},
		{"missing file", "pgdump", filepath.Join(dir, "gone.dump"), "source_not_found"},
		{"directory as pgdump", "pgdump", dir, "invalid_request"},
		{"missing directory", "pgdump_dir", filepath.Join(dir, "nodir"), "source_not_found"},
		{"empty directory", "pgdump_dir", t.TempDir(), "source_not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, perr := resolveSource(tt.kind, tt.path)
			if perr == nil || perr.Code != tt.wantCode {
				t.Errorf("resolveSource(%s, %s) = %+v, want %s", tt.kind, tt.path, perr, tt.wantCode)
			}
		})
	}
}

func TestFileChecksumUnreadable(t *testing.T) {
	if _, perr := fileChecksum(filepath.Join(t.TempDir(), "gone")); perr == nil || perr.Code != "source_unreadable" {
		t.Errorf("perr = %+v, want source_unreadable", perr)
	}
}
