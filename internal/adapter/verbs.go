package adapter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/aafeher/probavi/internal/sandbox"
)

// SandboxVerbs is what this package needs from a sandbox to fulfill
// adapter sandbox calls (§4). *docker.Sandbox implements it.
type SandboxVerbs interface {
	Exec(ctx context.Context, req sandbox.ExecRequest) (*sandbox.ExecResult, error)
	PutFile(ctx context.Context, hostPath, destPath, mode string) (*sandbox.PutFileResult, error)
}

// execArgs is the wire form of the exec verb's arguments (§4.1).
type execArgs struct {
	Argv           []string          `json:"argv"`
	Env            map[string]string `json:"env"`
	StdinB64       string            `json:"stdin_b64"`
	TimeoutSeconds float64           `json:"timeout_seconds"`
}

// execValue is the wire form of the exec verb's result (§4.1).
type execValue struct {
	ExitCode        int     `json:"exit_code"`
	StdoutB64       string  `json:"stdout_b64"`
	StderrB64       string  `json:"stderr_b64"`
	Truncated       bool    `json:"truncated"`
	DurationSeconds float64 `json:"duration_seconds"`
}

// putFileArgs is the wire form of the put_file verb's arguments (§4.2).
type putFileArgs struct {
	SourcePath string `json:"source_path"`
	DestPath   string `json:"dest_path"`
	Mode       string `json:"mode"`
}

// putFileValue is the wire form of the put_file verb's result (§4.2).
type putFileValue struct {
	BytesCopied     int64   `json:"bytes_copied"`
	DurationSeconds float64 `json:"duration_seconds"`
}

// dispatchVerb fulfills one sandbox call. Errors returned as (result.ok ==
// false) do not abort the operation — the adapter decides (§3.3); only a
// nil return with error means the core itself must give up.
func dispatchVerb(ctx context.Context, verbs SandboxVerbs, guard func(string) error, call *sandboxCall) sandboxResult {
	switch call.Verb {
	case "exec":
		return dispatchExec(ctx, verbs, call)
	case "put_file":
		return dispatchPutFile(ctx, verbs, guard, call)
	default:
		return verbError(call.CallID, CodeInvalidRequest, false, "unknown sandbox verb: %s", call.Verb)
	}
}

func dispatchExec(ctx context.Context, verbs SandboxVerbs, call *sandboxCall) sandboxResult {
	args := execArgs{}
	if err := json.Unmarshal(call.Args, &args); err != nil || len(args.Argv) == 0 {
		return verbError(call.CallID, CodeInvalidRequest, false, "malformed exec args")
	}
	stdin, err := base64.StdEncoding.DecodeString(args.StdinB64)
	if err != nil {
		return verbError(call.CallID, CodeInvalidRequest, false, "exec stdin_b64 is not valid base64")
	}
	res, err := verbs.Exec(ctx, sandbox.ExecRequest{
		Argv:    args.Argv,
		Env:     args.Env,
		Stdin:   stdin,
		Timeout: time.Duration(args.TimeoutSeconds * float64(time.Second)),
	})
	if err != nil {
		return verbError(call.CallID, CodeSandboxError, true, "exec: %v", err)
	}
	return sandboxResult{CallID: call.CallID, OK: true, Value: execValue{
		ExitCode:        res.ExitCode,
		StdoutB64:       base64.StdEncoding.EncodeToString(res.Stdout),
		StderrB64:       base64.StdEncoding.EncodeToString(res.Stderr),
		Truncated:       res.Truncated,
		DurationSeconds: res.Duration.Seconds(),
	}}
}

func dispatchPutFile(ctx context.Context, verbs SandboxVerbs, guard func(string) error, call *sandboxCall) sandboxResult {
	args := putFileArgs{}
	if err := json.Unmarshal(call.Args, &args); err != nil || args.SourcePath == "" || args.DestPath == "" {
		return verbError(call.CallID, CodeInvalidRequest, false, "malformed put_file args")
	}
	if guard == nil {
		return verbError(call.CallID, CodeInvalidRequest, false, "put_file is not permitted in this operation")
	}
	if err := guard(args.SourcePath); err != nil {
		return verbError(call.CallID, CodeInvalidRequest, false, "%v", err)
	}
	res, err := verbs.PutFile(ctx, args.SourcePath, args.DestPath, args.Mode)
	if err != nil {
		return verbError(call.CallID, CodeSandboxError, true, "put_file: %v", err)
	}
	return sandboxResult{CallID: call.CallID, OK: true, Value: putFileValue{
		BytesCopied:     res.BytesCopied,
		DurationSeconds: res.Duration.Seconds(),
	}}
}

func verbError(callID, code string, retryable bool, format string, a ...any) sandboxResult {
	return sandboxResult{CallID: callID, OK: false, Error: &Error{
		Code:      code,
		Message:   fmt.Sprintf(format, a...),
		Retryable: retryable,
	}}
}

// sourceGuard permits put_file sources that are the drill's configured
// backup source path or live beneath it (§4.2). Everything else — /etc,
// key files, arbitrary host paths — is refused.
func sourceGuard(sourcePath string) func(string) error {
	root := filepath.Clean(sourcePath)
	return func(requested string) error {
		clean := filepath.Clean(requested)
		if clean == root || strings.HasPrefix(clean, root+string(filepath.Separator)) {
			return nil
		}
		// No quotes in the message: it crosses the protocol as a JSON
		// string and must stay trivially embeddable.
		return fmt.Errorf("put_file source %s is outside the drill's backup source %s", clean, root)
	}
}
