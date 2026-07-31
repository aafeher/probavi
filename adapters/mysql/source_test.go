package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveSourceFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "orders.sql")
	if err := os.WriteFile(path, []byte("-- dump"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	src, perr := resolveSource("mysqldump", path)
	if perr != nil {
		t.Fatalf("resolveSource: %+v", perr)
	}
	if !strings.HasPrefix(src.checksum, "sha256:") || src.sizeBytes != 7 || src.createdAt == nil {
		t.Errorf("src = %+v", src)
	}

	tests := []struct {
		name     string
		kind     string
		path     string
		wantCode string
	}{
		{"missing file", "mysqldump", filepath.Join(dir, "gone.sql"), "source_not_found"},
		{"directory as file", "mysqldump", dir, "invalid_request"},
		{"unsupported kind", "xtrabackup", path, "unsupported_source"},
		{"missing directory", "mysqldump_dir", filepath.Join(dir, "gone"), "source_not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, perr := resolveSource(tt.kind, tt.path); perr == nil || perr.Code != tt.wantCode {
				t.Errorf("resolveSource(%s, %s) = %+v, want %s", tt.kind, tt.path, perr, tt.wantCode)
			}
		})
	}
}

func TestResolveSourceDirPicksNewest(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-time.Hour)
	for name, mtime := range map[string]time.Time{
		"monday.sql":  old.Add(-time.Hour),
		"tuesday.sql": old,
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}

	src, perr := resolveSource("mysqldump_dir", dir)
	if perr != nil {
		t.Fatalf("resolveSource: %+v", perr)
	}
	if filepath.Base(src.path) != "tuesday.sql" {
		t.Errorf("picked %s, want the newest file tuesday.sql", src.path)
	}

	// Equal mtimes: the lexicographically larger name must win so the
	// choice stays deterministic across runs.
	tie := filepath.Join(dir, "aaa.sql")
	if err := os.WriteFile(tie, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	newest := time.Now()
	for _, name := range []string{"tuesday.sql", "aaa.sql"} {
		if err := os.Chtimes(filepath.Join(dir, name), newest, newest); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}
	src, perr = resolveSource("mysqldump_dir", dir)
	if perr != nil {
		t.Fatalf("resolveSource: %+v", perr)
	}
	if filepath.Base(src.path) != "tuesday.sql" {
		t.Errorf("tie broke to %s, want tuesday.sql", src.path)
	}

	empty := t.TempDir()
	if _, perr := resolveSource("mysqldump_dir", empty); perr == nil || perr.Code != "source_not_found" {
		t.Errorf("empty dir: %+v, want source_not_found", perr)
	}
}
