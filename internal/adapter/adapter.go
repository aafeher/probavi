// Package adapter is the core-side client of the Probavi adapter protocol
// (docs/adapter-protocol.md, normative). It launches adapter executables,
// speaks line-delimited JSON with them, mediates sandbox verbs, and
// enforces every framing rule of the spec: anything an adapter does outside
// the protocol is an adapter_crash, never undefined behavior.
package adapter

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"time"
)

// ProtocolVersion is the protocol this client speaks.
const ProtocolVersion = "probavi-adapter/0"

// maxLineBytes is the protocol's frame size limit (§2.2).
const maxLineBytes = 4 << 20

// defaultGrace is the SIGTERM→SIGKILL grace period (§2.4).
const defaultGrace = 10 * time.Second

// Error codes from the protocol registry (§5) that this client emits or
// callers commonly match on.
const (
	CodeInvalidRequest = "invalid_request"
	CodeSandboxError   = "sandbox_error"
	CodeAdapterCrash   = "adapter_crash"
	CodeCancelled      = "cancelled"
)

// Error is a protocol error object (§5): either sent by the adapter in a
// final response, or assigned by this client (adapter_crash).
type Error struct {
	Code      string          `json:"code"`
	Message   string          `json:"message"`
	Retryable bool            `json:"retryable"`
	Detail    json.RawMessage `json:"detail,omitempty"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("adapter error %s: %s", e.Code, e.Message)
}

func crashf(format string, a ...any) *Error {
	return &Error{Code: CodeAdapterCrash, Message: fmt.Sprintf(format, a...), Retryable: false}
}

// Options tune a Runner. The zero value is valid.
type Options struct {
	// CredentialEnv names variables passed through from the core's own
	// environment (drill config source.credential_env).
	CredentialEnv []string
	// Env sets explicit extra variables (e.g. PROBAVI_SANDBOX_PASSWORD).
	// Values never appear in protocol messages or logs.
	Env map[string]string
	// Grace is the SIGTERM→SIGKILL period; default 10s.
	Grace time.Duration
}

// Runner launches one adapter executable, one fresh process per operation.
type Runner struct {
	path   string
	logger *slog.Logger
	opts   Options
}

// New resolves the adapter name to the executable probavi-adapter-<name> on
// PATH (§2.1) and returns a Runner for it.
func New(name string, logger *slog.Logger, opts *Options) (*Runner, error) {
	path, err := exec.LookPath("probavi-adapter-" + name)
	if err != nil {
		return nil, fmt.Errorf("resolve adapter %q: %w", name, err)
	}
	return newRunner(path, logger, opts), nil
}

func newRunner(path string, logger *slog.Logger, opts *Options) *Runner {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	o := Options{}
	if opts != nil {
		o = *opts
	}
	if o.Grace <= 0 {
		o.Grace = defaultGrace
	}
	return &Runner{path: path, logger: logger, opts: o}
}

func newRequestID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		// The id only needs uniqueness within one adapter process; a
		// failing crypto/rand is unrecoverable anyway, panic loudly.
		panic("adapter: crypto/rand unavailable: " + err.Error())
	}
	return "r-" + hex.EncodeToString(b[:])
}
