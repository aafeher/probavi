// Package sandbox defines the contract between the Probavi core and
// sandbox providers: a provider answers one request — "give me a disposable
// runtime; destroy it afterwards, guaranteed" — and fulfills the adapter
// protocol's sandbox verbs (exec, put_file) inside that runtime. Provider
// implementations live in subpackages (docker first); no provider-specific
// concept may leak into this package or into the core.
package sandbox

import (
	"errors"
	"time"
)

// MaxCaptureBytes caps each captured exec output stream, per adapter
// protocol §4.1.
const MaxCaptureBytes = 256 * 1024

// ErrInvalidParams reports drill-config sandbox parameters the provider
// cannot accept.
var ErrInvalidParams = errors.New("invalid sandbox params")

// ExecRequest runs one command inside the sandbox (adapter protocol §4.1).
type ExecRequest struct {
	Argv    []string
	Env     map[string]string
	Stdin   []byte
	Timeout time.Duration // 0: bounded only by the caller's context
}

// ExecResult reports one executed command. A non-zero ExitCode is a valid
// result, not an error.
type ExecResult struct {
	ExitCode  int
	Stdout    []byte
	Stderr    []byte
	Truncated bool
	Duration  time.Duration
}

// PutFileResult reports one completed file copy into the sandbox.
type PutFileResult struct {
	BytesCopied int64
	Duration    time.Duration
}
