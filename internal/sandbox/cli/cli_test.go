package cli

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/probavi/probavi/internal/sandbox"
)

func TestExecRunnerReal(t *testing.T) {
	// The real runner needs no provider CLI — any binary proves the contract.
	r := ExecRunner{}

	stdout, _, truncated, exit, err := r.Run(context.Background(), nil, nil, "sh", "-c", "echo hello")
	if err != nil || exit != 0 || strings.TrimSpace(string(stdout)) != "hello" || truncated {
		t.Errorf("echo: out=%q exit=%d trunc=%v err=%v", stdout, exit, truncated, err)
	}

	_, _, _, exit, err = r.Run(context.Background(), nil, nil, "sh", "-c", "exit 3")
	if err != nil || exit != 3 {
		t.Errorf("exit 3: exit=%d err=%v (non-zero exit is a result, not an error)", exit, err)
	}

	stdout, _, _, _, err = r.Run(context.Background(), strings.NewReader("piped"), nil, "cat")
	if err != nil || string(stdout) != "piped" {
		t.Errorf("stdin: out=%q err=%v", stdout, err)
	}

	big := "yes x | head -c " + strconv.Itoa(sandbox.MaxCaptureBytes*2)
	stdout, _, truncated, _, err = r.Run(context.Background(), nil, nil, "sh", "-c", big)
	if err != nil || !truncated || len(stdout) != sandbox.MaxCaptureBytes {
		t.Errorf("truncation: len=%d trunc=%v err=%v, want capped at %d", len(stdout), truncated, err, sandbox.MaxCaptureBytes)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, _, err := r.Run(ctx, nil, nil, "sleep", "1"); err == nil {
		t.Error("canceled context must be an error")
	}

	if _, _, _, _, err := r.Run(context.Background(), nil, nil, "/no/such/binary-probavi"); err == nil {
		t.Error("missing binary must be an error")
	}
}

func TestLimitedWriter(t *testing.T) {
	var w limitedWriter
	n, err := w.Write(make([]byte, sandbox.MaxCaptureBytes+1))
	if err != nil || n != sandbox.MaxCaptureBytes+1 {
		t.Errorf("Write = %d,%v — must report full length to keep io.Copy happy", n, err)
	}
	if !w.truncated || w.buf.Len() != sandbox.MaxCaptureBytes {
		t.Errorf("buf=%d truncated=%v, want cap and flag", w.buf.Len(), w.truncated)
	}
	if _, err := w.Write([]byte("more")); err != nil || w.buf.Len() != sandbox.MaxCaptureBytes {
		t.Error("writes past the cap must be swallowed")
	}
}
