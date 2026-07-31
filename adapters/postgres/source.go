package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// resolvedSource is a concrete backup artifact chosen for restore.
type resolvedSource struct {
	path      string
	checksum  string // "sha256:<hex>" over the artifact bytes
	sizeBytes int64
	// createdAt is the artifact's modification time (RFC 3339 UTC,
	// milliseconds) — the closest derivable stand-in for the backup's own
	// creation time; nil if unavailable.
	createdAt *string
}

// resolveSource maps a source kind to one restorable artifact.
//
//	pgdump      — path is a pg_dump custom-format file
//	pgdump_dir  — path is a directory; the newest regular file is chosen
//	pgbackrest  — path is a pgBackRest repository directory (filesystem repo)
func resolveSource(kind, path string) (*resolvedSource, *protoError) {
	switch kind {
	case "pgdump":
		return resolveFile(path)
	case "pgdump_dir":
		latest, perr := latestDumpIn(path)
		if perr != nil {
			return nil, perr
		}
		return resolveFile(latest)
	case "pgbackrest":
		return resolveRepo(path)
	default:
		return nil, protoErr("unsupported_source", false,
			"unsupported source kind: %s (supported: pgdump, pgdump_dir, pgbackrest)", kind)
	}
}

// resolveRepo resolves a directory source: the checksum is a canonical hash
// over the whole tree (documented in the adapter README), created_at is the
// newest file's mtime.
func resolveRepo(dir string) (*resolvedSource, *protoError) {
	info, err := os.Stat(dir)
	switch {
	case os.IsNotExist(err):
		return nil, protoErr("source_not_found", false, "backup repository does not exist: %s", dir)
	case err != nil:
		return nil, protoErr("source_unreadable", false, "stat backup repository: %v", err)
	case !info.IsDir():
		return nil, protoErr("invalid_request", false,
			"source path %s is a file; the pgbackrest kind expects a repository directory", dir)
	}
	checksum, size, newest, perr := dirChecksum(dir)
	if perr != nil {
		return nil, perr
	}
	created := newest.UTC().Format("2006-01-02T15:04:05.000Z")
	return &resolvedSource{path: dir, checksum: checksum, sizeBytes: size, createdAt: &created}, nil
}

// dirChecksum hashes a directory tree canonically: entries sorted by
// relative path; regular files contribute path, size, and content bytes,
// symlinks contribute path and target. The same tree always hashes the
// same, any content change changes the hash.
func dirChecksum(root string) (string, int64, time.Time, *protoError) {
	h := sha256.New()
	var total int64
	var newest time.Time
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		return hashEntry(h, path, rel, d, &total, &newest)
	})
	if err != nil {
		return "", 0, time.Time{}, protoErr("source_unreadable", false, "read backup repository: %v", err)
	}
	if newest.IsZero() {
		return "", 0, time.Time{}, protoErr("source_not_found", false, "backup repository %s contains no files", root)
	}
	return fmt.Sprintf("sha256:%s", hex.EncodeToString(h.Sum(nil))), total, newest, nil
}

func hashEntry(h io.Writer, path, rel string, d os.DirEntry, total *int64, newest *time.Time) error {
	switch {
	case d.Type().IsRegular():
		info, err := d.Info()
		if err != nil {
			return err
		}
		*total += info.Size()
		if info.ModTime().After(*newest) {
			*newest = info.ModTime()
		}
		fmt.Fprintf(h, "%s\x00%d\x00", rel, info.Size())
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, cerr := io.Copy(h, f)
		if err := f.Close(); err != nil && cerr == nil {
			cerr = err
		}
		return cerr
	case d.Type()&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00L%s\x00", rel, target)
	}
	return nil
}

func resolveFile(path string) (*resolvedSource, *protoError) {
	info, err := os.Stat(path)
	switch {
	case os.IsNotExist(err):
		return nil, protoErr("source_not_found", false, "backup source does not exist: %s", path)
	case err != nil:
		return nil, protoErr("source_unreadable", false, "stat backup source: %v", err)
	case info.IsDir():
		return nil, protoErr("invalid_request", false,
			"source path %s is a directory; use kind pgdump_dir for directories", path)
	}
	checksum, perr := fileChecksum(path)
	if perr != nil {
		return nil, perr
	}
	created := info.ModTime().UTC().Format("2006-01-02T15:04:05.000Z")
	return &resolvedSource{
		path:      path,
		checksum:  checksum,
		sizeBytes: info.Size(),
		createdAt: &created,
	}, nil
}

// latestDumpIn picks the newest regular file in dir; ties break toward the
// lexicographically larger name so the choice is deterministic.
func latestDumpIn(dir string) (string, *protoError) {
	entries, err := os.ReadDir(dir)
	switch {
	case os.IsNotExist(err):
		return "", protoErr("source_not_found", false, "backup directory does not exist: %s", dir)
	case err != nil:
		return "", protoErr("source_unreadable", false, "read backup directory: %v", err)
	}
	var (
		best     string
		bestTime time.Time
	)
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return "", protoErr("source_unreadable", false, "stat %s: %v", e.Name(), err)
		}
		if best == "" || info.ModTime().After(bestTime) ||
			(info.ModTime().Equal(bestTime) && e.Name() > filepath.Base(best)) {
			best = filepath.Join(dir, e.Name())
			bestTime = info.ModTime()
		}
	}
	if best == "" {
		return "", protoErr("source_not_found", false, "backup directory %s contains no files", dir)
	}
	return best, nil
}

// fileChecksum streams the artifact once; the hash feeds the evidence
// record's backup identity, so it must be a real measurement of the bytes
// that will be restored.
func fileChecksum(path string) (string, *protoError) {
	f, err := os.Open(path)
	if err != nil {
		return "", protoErr("source_unreadable", false, "open backup source: %v", err)
	}
	h := sha256.New()
	_, cerr := io.Copy(h, f)
	if err := f.Close(); err != nil && cerr == nil {
		cerr = err
	}
	if cerr != nil {
		return "", protoErr("source_unreadable", false, "read backup source: %v", cerr)
	}
	return fmt.Sprintf("sha256:%s", hex.EncodeToString(h.Sum(nil))), nil
}
