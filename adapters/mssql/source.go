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
	// loginsPath is the server-logins script to replay before the restore,
	// for the bak_with_logins kind; empty for every other kind.
	loginsPath string
}

// resolveSource maps a source kind to one restorable artifact.
//
//	bak             — path is a native BACKUP DATABASE file
//	bak_dir         — path is a directory; the newest regular file is chosen
//	bak_with_logins — path is a directory holding a server-logins T-SQL
//	                  script (params.logins) and one .bak
func resolveSource(kind, path string, params map[string]string) (*resolvedSource, *protoError) {
	switch kind {
	case "bak":
		return resolveFile(path)
	case "bak_dir":
		latest, perr := latestDumpIn(path)
		if perr != nil {
			return nil, perr
		}
		return resolveFile(latest)
	case "bak_with_logins":
		return resolveWithLogins(path, params)
	default:
		return nil, protoErr("unsupported_source", false,
			"unsupported source kind: %s (supported: bak, bak_dir, bak_with_logins)", kind)
	}
}

// resolveWithLogins resolves the two-member source of the bak_with_logins
// kind: a server-logins script and one .bak, both named inside one source
// directory.
//
// One directory rather than two independent paths because the core only
// hands an adapter files belonging to the drill's configured backup source
// (protocol §4.2) — a guard that exists so an adapter, which is a
// third-party binary, cannot copy arbitrary host files into a sandbox it
// controls. The members are named explicitly in params rather than
// recognised by filename pattern: renaming a backup file must not silently
// change what a drill proves.
//
// Both members are restored, so both must be in the backup identity — a
// checksum covering only the .bak would let the logins change without the
// evidence record noticing, and the logins are exactly what this kind
// exists to prove present. Only the two chosen members are hashed, not the
// whole directory: one directory may hold the logins script beside several
// databases' backups, each drilled separately, and a drill's identity must
// cover what that drill restored and nothing else. The construction
// mirrors the postgres adapter's two-member framing (role NUL size NUL
// content, fixed order), so the same pair always hashes the same and any
// change to either member changes the hash.
func resolveWithLogins(dir string, params map[string]string) (*resolvedSource, *protoError) {
	info, err := os.Stat(dir)
	switch {
	case os.IsNotExist(err):
		return nil, protoErr("source_not_found", false, "backup directory does not exist: %s", dir)
	case err != nil:
		return nil, protoErr("source_unreadable", false, "stat backup directory: %v", err)
	case !info.IsDir():
		return nil, protoErr("invalid_request", false,
			"source path %s is a file; the bak_with_logins kind expects a directory "+
				"holding the logins script and the backup", dir)
	}

	loginsName, perr := memberName(params["logins"], "logins")
	if perr != nil {
		return nil, perr
	}
	loginsPath := filepath.Join(dir, loginsName)
	logins, perr := statRegularFile(loginsPath, "logins script")
	if perr != nil {
		return nil, perr
	}

	bakPath, perr := chooseBak(dir, params["bak"], loginsName)
	if perr != nil {
		return nil, perr
	}
	bak, perr := statRegularFile(bakPath, "backup source")
	if perr != nil {
		return nil, perr
	}

	h := sha256.New()
	for _, m := range []struct {
		role string
		path string
		info os.FileInfo
	}{
		{"logins", loginsPath, logins},
		{"bak", bakPath, bak},
	} {
		fmt.Fprintf(h, "%s\x00%d\x00", m.role, m.info.Size())
		if perr := copyInto(h, m.path); perr != nil {
			return nil, perr
		}
	}

	// The older mtime, not the newer: a two-member set is only as current
	// as its stalest member. Stale logins are precisely the failure this
	// kind exists to surface — a login created after the script was taken
	// is missing, and its restored database user is orphaned. (bak_dir
	// takes the newest file for the opposite and equally deliberate
	// reason: a rotation directory's newest file is its latest backup.)
	created := logins.ModTime()
	if bak.ModTime().Before(created) {
		created = bak.ModTime()
	}
	stamp := created.UTC().Format("2006-01-02T15:04:05.000Z")
	return &resolvedSource{
		path:       bakPath,
		checksum:   fmt.Sprintf("sha256:%s", hex.EncodeToString(h.Sum(nil))),
		sizeBytes:  logins.Size() + bak.Size(),
		createdAt:  &stamp,
		loginsPath: loginsPath,
	}, nil
}

// memberName validates a params entry naming a file inside the source
// directory. It is a bare filename, never a path: the core's put_file
// guard confines transfers to the configured backup source, and a plain
// name keeps a config's reach obvious to whoever reviews it.
func memberName(value, param string) (string, *protoError) {
	if value == "" {
		return "", protoErr("invalid_request", false,
			"the bak_with_logins kind requires source.params.%s: the name of the %s file "+
				"inside the source directory", param, param)
	}
	if value != filepath.Base(value) || value == "." || value == ".." {
		return "", protoErr("invalid_request", false,
			"source.params.%s must be a filename inside the source directory, not a path: %s",
			param, value)
	}
	return value, nil
}

// chooseBak resolves which backup the drill restores: the one params.bak
// names, or — so a drill against a rotating backup directory keeps working
// unattended — the newest file that is not the logins script.
func chooseBak(dir, requested, loginsName string) (string, *protoError) {
	if requested != "" {
		name, perr := memberName(requested, "bak")
		if perr != nil {
			return "", perr
		}
		if name == loginsName {
			return "", protoErr("invalid_request", false,
				"source.params.bak and source.params.logins both name %s", name)
		}
		return filepath.Join(dir, name), nil
	}
	newest, perr := newestFileIn(dir, loginsName)
	if perr != nil {
		return "", perr
	}
	if newest == "" {
		return "", protoErr("source_not_found", false,
			"backup directory %s holds no backup beside the logins script %s", dir, loginsName)
	}
	return newest, nil
}

// statRegularFile stats a source member that must exist as a plain file;
// what names it in diagnostics.
func statRegularFile(path, what string) (os.FileInfo, *protoError) {
	info, err := os.Stat(path)
	switch {
	case os.IsNotExist(err):
		return nil, protoErr("source_not_found", false, "%s does not exist: %s", what, path)
	case err != nil:
		return nil, protoErr("source_unreadable", false, "stat %s: %v", what, err)
	case info.IsDir():
		return nil, protoErr("invalid_request", false, "%s %s is a directory, not a file", what, path)
	}
	return info, nil
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
			"source path %s is a directory; use kind bak_dir for directories", path)
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

// latestDumpIn picks the newest regular file in dir.
func latestDumpIn(dir string) (string, *protoError) {
	best, perr := newestFileIn(dir, "")
	if perr != nil {
		return "", perr
	}
	if best == "" {
		return "", protoErr("source_not_found", false, "backup directory %s contains no files", dir)
	}
	return best, nil
}

// newestFileIn returns the newest regular file in dir, skipping the entry
// named except; ties break toward the lexicographically larger name so the
// choice is deterministic. An empty result means the directory is readable
// but holds no candidate — the caller says what that means.
func newestFileIn(dir, except string) (string, *protoError) {
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
		if !e.Type().IsRegular() || e.Name() == except {
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
	return best, nil
}

// copyInto streams a file's bytes into h.
func copyInto(h io.Writer, path string) *protoError {
	f, err := os.Open(path)
	if err != nil {
		return protoErr("source_unreadable", false, "open backup source: %v", err)
	}
	_, cerr := io.Copy(h, f)
	if err := f.Close(); err != nil && cerr == nil {
		cerr = err
	}
	if cerr != nil {
		return protoErr("source_unreadable", false, "read backup source: %v", cerr)
	}
	return nil
}

// fileChecksum streams the artifact once; the hash feeds the evidence
// record's backup identity, so it must be a real measurement of the bytes
// that will be restored.
func fileChecksum(path string) (string, *protoError) {
	h := sha256.New()
	if perr := copyInto(h, path); perr != nil {
		return "", perr
	}
	return fmt.Sprintf("sha256:%s", hex.EncodeToString(h.Sum(nil))), nil
}
