package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"

	"github.com/aafeher/probavi/internal/sandbox"
)

// runner abstracts subprocess execution so provider logic is unit-testable
// without Docker; execRunner is the real implementation.
type runner interface {
	// run executes name with args. err reports spawn/context failures only;
	// a non-zero exit code is returned in exitCode with err == nil.
	run(ctx context.Context, stdin io.Reader, name string, args ...string) (stdout, stderr []byte, truncated bool, exitCode int, err error)
}

type execRunner struct{}

func (execRunner) run(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, []byte, bool, int, error) {
	var out, errOut limitedWriter
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = stdin
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	truncated := out.truncated || errOut.truncated
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return out.buf.Bytes(), errOut.buf.Bytes(), truncated, exitErr.ExitCode(), nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return out.buf.Bytes(), errOut.buf.Bytes(), truncated, 0, fmt.Errorf("%s: %w", name, ctxErr)
		}
		return out.buf.Bytes(), errOut.buf.Bytes(), truncated, 0, fmt.Errorf("run %s: %w", name, err)
	}
	return out.buf.Bytes(), errOut.buf.Bytes(), truncated, 0, nil
}

// limitedWriter keeps the first MaxCaptureBytes and drops the rest, so a
// command dumping gigabytes cannot exhaust memory (protocol §4.1 cap).
type limitedWriter struct {
	buf       bytes.Buffer
	truncated bool
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	room := sandbox.MaxCaptureBytes - w.buf.Len()
	switch {
	case room <= 0:
		w.truncated = true
	case len(p) > room:
		w.buf.Write(p[:room])
		w.truncated = true
	default:
		w.buf.Write(p)
	}
	return len(p), nil
}
