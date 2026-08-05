package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
)

// FileDigest returns the record-shaped sha256 reference of a file, or nil
// when it cannot be read.
//
// Nil is a legitimate answer, not an error to propagate: the digests this
// produces (adapter.digest, env.probavi_digest — schema §3) are nullable
// precisely so that an unreadable executable never costs a drill its
// signed record. A record with a null digest still proves the restore; a
// drill that failed because a hash could not be taken proves nothing.
//
// It hashes the file at the path given, which is what the schema says the
// field attests: the bytes the core selected, not the instructions that
// ran. A file replaced between this call and exec would go unnoticed, and
// closing that window means reading /proc/<pid>/exe, which does not exist
// on every platform Probavi supports.
func FileDigest(path string) *string {
	// Read rather than stream: the inputs are executables the process is
	// about to run or is running, so their size is already bounded by what
	// this machine loads anyway, and there is no open file to close on a
	// path that must never fail loudly.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	return &digest
}
