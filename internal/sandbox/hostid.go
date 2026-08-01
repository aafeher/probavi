package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
)

// HostID fingerprints this host the same way evidence records do
// (evidence-schema.md §3 env.host_id): the first 16 hex chars of SHA-256
// over the hostname. Raw hostnames never leave the host, and the value
// fits container-label and Kubernetes character rules.
//
// Providers stamp it on every sandbox and scope their orphan sweeps with
// it: owner-process liveness is only checkable on the host that created
// the sandbox, which matters as soon as two drill hosts share a runtime
// (a remote Docker daemon, a Kubernetes cluster).
func HostID() string {
	name, err := os.Hostname()
	if err != nil {
		name = "unknown-host"
	}
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:])[:16]
}
