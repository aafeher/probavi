package sandbox

import (
	"strconv"
	"strings"
)

// ownerSep joins the two halves of an owner id. It is a dash rather than
// the more obvious colon because these ids become Kubernetes label values,
// where a colon is not allowed. Neither half can contain it: a pid is a
// positive decimal, and the token is digits.
const ownerSep = "-"

// OwnerID renders the identity of the process that created a sandbox, for
// the label or marker a provider stamps on it. The orphan sweep later asks
// OwnerAlive whether that same process is still running.
//
// The id is the pid, optionally followed by a token that changes when a
// pid is reused. A pid alone cannot answer the question honestly: pids are
// recycled, and an unrelated process inheriting one makes a dead owner
// look alive — so a sandbox holding restored production data survives
// until that pid happens to be free during some later sweep.
//
// The token is omitted where the operating system does not offer one
// cheaply. That is deliberate: an id without a token falls back to the pid
// rule, which is what this sweep did before — the same answer as today,
// never the destructive one.
func OwnerID(pid int) string {
	id := strconv.Itoa(pid)
	if token, ok := processStartToken(pid); ok {
		return id + ownerSep + token
	}
	return id
}

// OwnerAlive reports whether the process an OwnerID names is still
// running. It answers false for a malformed or empty id: a sandbox that
// lost its ownership metadata has no owner left to protect it.
//
// When both the id and the running process carry a start token, a mismatch
// proves the pid was recycled and the real owner is gone. When either side
// has no token, the id is trusted only as far as the pid — an answer no
// worse than the pid check alone.
func OwnerAlive(id string) bool {
	pidText, token, hasToken := strings.Cut(id, ownerSep)
	pid, err := strconv.Atoi(pidText)
	if err != nil || pid <= 0 {
		return false
	}
	if !ProcessAlive(pid) {
		return false
	}
	if !hasToken {
		return true
	}
	current, ok := processStartToken(pid)
	if !ok {
		return true // nothing to compare against; the pid is all we have
	}
	return current == token
}
