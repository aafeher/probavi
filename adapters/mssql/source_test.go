package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolveSourceKinds(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "a-old.archive")
	latest := filepath.Join(dir, "b-latest.archive")
	if err := os.WriteFile(old, []byte("OLD"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(latest, []byte("NEW"), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}

	t.Run("bak file", func(t *testing.T) {
		src, perr := resolveSource("bak", latest, nil)
		if perr != nil {
			t.Fatalf("resolve: %+v", perr)
		}
		if src.path != latest || src.sizeBytes != 3 || src.createdAt == nil {
			t.Errorf("src = %+v", src)
		}
	})

	t.Run("bak_dir picks the newest", func(t *testing.T) {
		src, perr := resolveSource("bak_dir", dir, nil)
		if perr != nil {
			t.Fatalf("resolve: %+v", perr)
		}
		if src.path != latest {
			t.Errorf("path = %s, want the newest file %s", src.path, latest)
		}
	})

	t.Run("unknown kind", func(t *testing.T) {
		if _, perr := resolveSource("pgdump", latest, nil); perr == nil || perr.Code != "unsupported_source" {
			t.Errorf("perr = %+v, want unsupported_source", perr)
		}
	})
}

func TestResolveSourceErrors(t *testing.T) {
	dir := t.TempDir()

	t.Run("missing file", func(t *testing.T) {
		if _, perr := resolveSource("bak", filepath.Join(dir, "gone"), nil); perr == nil || perr.Code != "source_not_found" {
			t.Errorf("perr = %+v, want source_not_found", perr)
		}
	})
	t.Run("directory for the file kind", func(t *testing.T) {
		if _, perr := resolveSource("bak", dir, nil); perr == nil || perr.Code != "invalid_request" {
			t.Errorf("perr = %+v, want invalid_request pointing at bak_dir", perr)
		}
	})
	t.Run("missing directory", func(t *testing.T) {
		if _, perr := resolveSource("bak_dir", filepath.Join(dir, "gone"), nil); perr == nil || perr.Code != "source_not_found" {
			t.Errorf("perr = %+v, want source_not_found", perr)
		}
	})
	t.Run("empty directory", func(t *testing.T) {
		empty := filepath.Join(dir, "empty")
		if err := os.Mkdir(empty, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, perr := resolveSource("bak_dir", empty, nil); perr == nil || perr.Code != "source_not_found" {
			t.Errorf("perr = %+v, want source_not_found", perr)
		}
	})
	t.Run("file for the dir kind", func(t *testing.T) {
		file := filepath.Join(dir, "file")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, perr := resolveSource("bak_dir", file, nil); perr == nil || perr.Code != "source_unreadable" {
			t.Errorf("perr = %+v, want source_unreadable", perr)
		}
	})
}

// withLoginsDir builds a two-member source directory: logins.sql (older)
// and orders.bak (newer).
func withLoginsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "logins.sql"), []byte("CREATE LOGIN"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "orders.bak"), []byte("BAK-BYTES"), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "logins.sql"), past, past); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestResolveWithLoginsErrors(t *testing.T) {
	dir := withLoginsDir(t)
	file := filepath.Join(t.TempDir(), "plain.bak")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		path     string
		params   map[string]string
		wantCode string
	}{
		{"missing params entirely", dir, nil, "invalid_request"},
		{"missing logins param", dir, map[string]string{"bak": "orders.bak"}, "invalid_request"},
		{"logins param is a path", dir, map[string]string{"logins": "../logins.sql"}, "invalid_request"},
		{"logins param is absolute", dir, map[string]string{"logins": "/etc/passwd"}, "invalid_request"},
		{"logins param is dot", dir, map[string]string{"logins": "."}, "invalid_request"},
		{"bak param is a path", dir, map[string]string{"logins": "logins.sql", "bak": "x/y.bak"}, "invalid_request"},
		{"both members name one file", dir, map[string]string{"logins": "logins.sql", "bak": "logins.sql"}, "invalid_request"},
		{"logins file missing", dir, map[string]string{"logins": "gone.sql"}, "source_not_found"},
		{"bak file missing", dir, map[string]string{"logins": "logins.sql", "bak": "gone.bak"}, "source_not_found"},
		{"logins member is a directory", dir, map[string]string{"logins": "sub"}, "invalid_request"},
		{"source path is a file", file, map[string]string{"logins": "logins.sql"}, "invalid_request"},
		{"source directory missing", filepath.Join(dir, "gone"), map[string]string{"logins": "logins.sql"}, "source_not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, perr := resolveSource("bak_with_logins", tt.path, tt.params); perr == nil || perr.Code != tt.wantCode {
				t.Errorf("perr = %+v, want %s", perr, tt.wantCode)
			}
		})
	}

	t.Run("no bak beside the logins script", func(t *testing.T) {
		lone := t.TempDir()
		if err := os.WriteFile(filepath.Join(lone, "logins.sql"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, perr := resolveSource("bak_with_logins", lone, map[string]string{"logins": "logins.sql"}); perr == nil || perr.Code != "source_not_found" {
			t.Errorf("perr = %+v, want source_not_found", perr)
		}
	})
}

func TestResolveWithLoginsIdentity(t *testing.T) {
	dir := withLoginsDir(t)
	params := map[string]string{"logins": "logins.sql", "bak": "orders.bak"}

	src, perr := resolveSource("bak_with_logins", dir, params)
	if perr != nil {
		t.Fatalf("resolve: %+v", perr)
	}
	if src.path != filepath.Join(dir, "orders.bak") {
		t.Errorf("path = %s, want the bak member", src.path)
	}
	if src.loginsPath != filepath.Join(dir, "logins.sql") {
		t.Errorf("loginsPath = %s", src.loginsPath)
	}
	if src.sizeBytes != int64(len("CREATE LOGIN")+len("BAK-BYTES")) {
		t.Errorf("sizeBytes = %d, want the sum of both members", src.sizeBytes)
	}

	// created_at is the OLDER member's mtime: the set is only as current
	// as its stalest member.
	older, err := os.Stat(filepath.Join(dir, "logins.sql"))
	if err != nil {
		t.Fatal(err)
	}
	want := older.ModTime().UTC().Format("2006-01-02T15:04:05.000Z")
	if src.createdAt == nil || *src.createdAt != want {
		t.Errorf("createdAt = %v, want the older member's mtime %s", src.createdAt, want)
	}

	again, perr := resolveSource("bak_with_logins", dir, params)
	if perr != nil {
		t.Fatalf("resolve again: %+v", perr)
	}
	if again.checksum != src.checksum {
		t.Errorf("checksum not deterministic: %s vs %s", again.checksum, src.checksum)
	}
}

func TestResolveWithLoginsCreatedAtTracksStalestMember(t *testing.T) {
	// Same two members, but now the bak is the older one — created_at
	// must follow it, not the logins script.
	dir := withLoginsDir(t)
	older := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "orders.bak"), older, older); err != nil {
		t.Fatal(err)
	}
	src, perr := resolveSource("bak_with_logins", dir, map[string]string{"logins": "logins.sql", "bak": "orders.bak"})
	if perr != nil {
		t.Fatalf("resolve: %+v", perr)
	}
	info, err := os.Stat(filepath.Join(dir, "orders.bak"))
	if err != nil {
		t.Fatal(err)
	}
	want := info.ModTime().UTC().Format("2006-01-02T15:04:05.000Z")
	if src.createdAt == nil || *src.createdAt != want {
		t.Errorf("createdAt = %v, want the now-older bak mtime %s", src.createdAt, want)
	}
}

func TestResolveWithLoginsChecksumCoversBothMembers(t *testing.T) {
	dir := withLoginsDir(t)
	params := map[string]string{"logins": "logins.sql", "bak": "orders.bak"}
	base, perr := resolveSource("bak_with_logins", dir, params)
	if perr != nil {
		t.Fatalf("resolve: %+v", perr)
	}

	if err := os.WriteFile(filepath.Join(dir, "logins.sql"), []byte("CREATE LOGIM"), 0o600); err != nil {
		t.Fatal(err)
	}
	loginsChanged, perr := resolveSource("bak_with_logins", dir, params)
	if perr != nil {
		t.Fatalf("resolve: %+v", perr)
	}
	if loginsChanged.checksum == base.checksum {
		t.Error("checksum ignored a logins change — the identity must cover both members")
	}

	if err := os.WriteFile(filepath.Join(dir, "logins.sql"), []byte("CREATE LOGIN"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "orders.bak"), []byte("BAK-BYTEZ"), 0o600); err != nil {
		t.Fatal(err)
	}
	bakChanged, perr := resolveSource("bak_with_logins", dir, params)
	if perr != nil {
		t.Fatalf("resolve: %+v", perr)
	}
	if bakChanged.checksum == base.checksum {
		t.Error("checksum ignored a bak change — the identity must cover both members")
	}
}

func TestResolveWithLoginsChecksumIsUnambiguous(t *testing.T) {
	// "A"+"B" and "AB"+"" concatenate identically; the size framing must
	// keep their identities apart.
	dirA := t.TempDir()
	if err := os.WriteFile(filepath.Join(dirA, "logins.sql"), []byte("A"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirA, "x.bak"), []byte("B"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirB := t.TempDir()
	if err := os.WriteFile(filepath.Join(dirB, "logins.sql"), []byte("AB"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "x.bak"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	params := map[string]string{"logins": "logins.sql", "bak": "x.bak"}
	a, perr := resolveSource("bak_with_logins", dirA, params)
	if perr != nil {
		t.Fatalf("resolve a: %+v", perr)
	}
	b, perr := resolveSource("bak_with_logins", dirB, params)
	if perr != nil {
		t.Fatalf("resolve b: %+v", perr)
	}
	if a.checksum == b.checksum {
		t.Error("checksum collides across member boundaries — framing must include sizes")
	}
}

func TestResolveWithLoginsIgnoresSiblings(t *testing.T) {
	dir := withLoginsDir(t)
	params := map[string]string{"logins": "logins.sql", "bak": "orders.bak"}
	base, perr := resolveSource("bak_with_logins", dir, params)
	if perr != nil {
		t.Fatalf("resolve: %+v", perr)
	}
	// A half-written temp file beside the members must not change the
	// drill's backup identity.
	if err := os.WriteFile(filepath.Join(dir, "in-flight.tmp"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, perr := resolveSource("bak_with_logins", dir, params)
	if perr != nil {
		t.Fatalf("resolve: %+v", perr)
	}
	if after.checksum != base.checksum {
		t.Error("a sibling file changed the checksum — only the two members are the backup")
	}
}

func TestResolveWithLoginsPicksNewestNonLogins(t *testing.T) {
	dir := withLoginsDir(t)
	// Make the logins script the newest file in the directory: the
	// implicit bak choice must still skip it.
	now := time.Now()
	if err := os.Chtimes(filepath.Join(dir, "logins.sql"), now, now); err != nil {
		t.Fatal(err)
	}
	past := now.Add(-time.Minute)
	if err := os.Chtimes(filepath.Join(dir, "orders.bak"), past, past); err != nil {
		t.Fatal(err)
	}
	src, perr := resolveSource("bak_with_logins", dir, map[string]string{"logins": "logins.sql"})
	if perr != nil {
		t.Fatalf("resolve: %+v", perr)
	}
	if src.path != filepath.Join(dir, "orders.bak") {
		t.Errorf("path = %s, want the newest non-logins file", src.path)
	}
}

func TestLatestDumpTieBreak(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	for _, name := range []string{"a.archive", "b.archive"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, now, now); err != nil {
			t.Fatal(err)
		}
	}
	latest, perr := latestDumpIn(dir)
	if perr != nil {
		t.Fatalf("latestDumpIn: %+v", perr)
	}
	if filepath.Base(latest) != "b.archive" {
		t.Errorf("latest = %s, want the lexicographically larger name on an mtime tie", latest)
	}
}
