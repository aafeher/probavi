// Package docker implements the Probavi sandbox provider on top of the
// docker CLI (deliberately not the Docker SDK: the CLI is a boring, already
// verified dependency of the host, and the SDK would drag a large module
// tree into a trust product).
//
// Security defaults (AGENTS.md §3.3): no published ports — the provider
// never emits -p; network defaults to "none" (loopback only); containers
// are labeled and removed with their volumes. Restored sandboxes contain
// production data.
//
// The provider works unchanged against a remote daemon selected with
// DOCKER_HOST=ssh://user@host (the CLI's native SSH transport): exec
// streams stdin through the client and put_file uses docker cp, so backup
// bytes travel over the SSH connection, never a published port. The
// endpoint stays in the environment on purpose — sandbox params are
// recorded verbatim in evidence records, and connection details must
// never appear there (evidence-schema.md §8). Sweeps are host-scoped (see
// isOrphan), so several drill hosts may safely share one daemon.
package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aafeher/probavi/internal/sandbox"
	"github.com/aafeher/probavi/internal/sandbox/cli"
)

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

const (
	// LabelSandbox marks every container this provider creates; the orphan
	// sweep matches on it. Never remove containers by name pattern.
	LabelSandbox = "com.probavi.sandbox"
	// labelPID records the creating process, so the sweep can tell an
	// orphan (owner dead) from a concurrently running drill's sandbox.
	labelPID = "com.probavi.pid"
	// labelHost scopes the sweep: pid liveness is only checkable on the
	// host that created the sandbox, and with DOCKER_HOST=ssh://… several
	// drill hosts may share one daemon. The sweep never touches other
	// hosts' containers.
	labelHost = "com.probavi.host"

	scratchDir     = "/tmp"
	awaitInterval  = 250 * time.Millisecond
	maxAwaitUptime = 2 * time.Minute
)

// Provider creates and destroys Docker-backed sandboxes.
type Provider struct {
	bin    string
	run    cli.Runner
	logger *slog.Logger
	pid    int
	hostID string

	awaitInterval time.Duration
	awaitCap      time.Duration
}

// New returns a Provider shelling out to the "docker" binary.
func New(logger *slog.Logger) *Provider {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Provider{
		bin:           "docker",
		run:           cli.ExecRunner{},
		logger:        logger,
		pid:           os.Getpid(),
		hostID:        sandbox.HostID(),
		awaitInterval: awaitInterval,
		awaitCap:      maxAwaitUptime,
	}
}

// Sandbox is one running disposable container.
type Sandbox struct {
	id string
	p  *Provider
}

// Create starts a container from drill-config sandbox params and waits
// until the runtime is up. Engine readiness inside the sandbox is the
// adapter's job, not the provider's. The context must carry the drill's
// deadline; a hard internal cap bounds the wait regardless.
func (p *Provider) Create(ctx context.Context, params map[string]string) (*Sandbox, error) {
	args, err := p.runArgs(params)
	if err != nil {
		return nil, err
	}
	stdout, stderr, _, exit, err := p.run.Run(ctx, nil, p.bin, args...)
	if err != nil {
		return nil, fmt.Errorf("create sandbox: %w", err)
	}
	if exit != 0 {
		return nil, fmt.Errorf("create sandbox: docker run exited %d: %s", exit, firstLine(stderr))
	}
	sbx := &Sandbox{id: strings.TrimSpace(string(stdout)), p: p}
	if err := p.awaitRunning(ctx, sbx.id); err != nil {
		// Cleanup on the failure path runs on a fresh context: the caller's
		// context may already be dead (PoC finding 3).
		dctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if derr := sbx.Destroy(dctx); derr != nil {
			p.logger.Error("destroy after failed create", "id", sbx.id, "err", derr)
		}
		return nil, err
	}
	p.logger.Info("sandbox created", "id", sbx.id)
	return sbx, nil
}

// SweepOrphans removes labeled containers whose creating process no longer
// runs. Containers of live processes (concurrent drills) are kept. Returns
// the removed container ids.
func (p *Provider) SweepOrphans(ctx context.Context) ([]string, error) {
	stdout, stderr, _, exit, err := p.run.Run(ctx, nil, p.bin, "ps", "-aq", "--filter", "label="+LabelSandbox+"=1")
	if err != nil {
		return nil, fmt.Errorf("list sandbox containers: %w", err)
	}
	if exit != 0 {
		return nil, fmt.Errorf("list sandbox containers: docker ps exited %d: %s", exit, firstLine(stderr))
	}
	ids := strings.Fields(string(stdout))
	removed := make([]string, 0, len(ids))
	for _, id := range ids {
		orphan, err := p.isOrphan(ctx, id)
		if err != nil {
			return removed, err
		}
		if !orphan {
			continue
		}
		if err := p.remove(ctx, id); err != nil {
			return removed, fmt.Errorf("sweep orphan %s: %w", id, err)
		}
		p.logger.Info("swept orphan sandbox", "id", id)
		removed = append(removed, id)
	}
	return removed, nil
}

// isOrphan reports whether the container's owner process is gone. Another
// drill host's container is never our orphan: pid liveness is only
// checkable where the owner runs, so the sweep skips foreign host labels
// and leaves those containers to their own host's sweep. A missing host
// label means a pre-label probavi created the container — those were
// host-local by definition, so the pid rule still applies (upgrade every
// drill host before pointing several at one shared daemon). A missing or
// malformed pid label counts as orphaned: the container carries our label
// but lost its ownership metadata.
func (p *Provider) isOrphan(ctx context.Context, id string) (bool, error) {
	stdout, stderr, _, exit, err := p.run.Run(ctx, nil, p.bin,
		"inspect", "-f", `{{ index .Config.Labels "`+labelHost+`" }}|{{ index .Config.Labels "`+labelPID+`" }}`, id)
	if err != nil {
		return false, fmt.Errorf("inspect %s: %w", id, err)
	}
	if exit != 0 {
		// Vanished between list and inspect: a concurrent drill's teardown
		// finished first. Gone means nothing left to sweep — not an error.
		if strings.Contains(string(stderr), "No such object") {
			return false, nil
		}
		return false, fmt.Errorf("inspect %s: docker inspect exited %d: %s", id, exit, firstLine(stderr))
	}
	host, pidLabel, found := strings.Cut(strings.TrimSpace(string(stdout)), "|")
	if !found {
		// The format always emits the separator; anything else means the
		// labels are unreadable — ownership metadata is gone.
		return true, nil
	}
	if host != "" && host != p.hostID {
		return false, nil
	}
	pid, err := strconv.Atoi(pidLabel)
	if err != nil || pid <= 0 {
		return true, nil
	}
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err != nil {
		return true, nil
	}
	return false, nil
}

// ID returns the container id.
func (s *Sandbox) ID() string { return s.id }

// ScratchDir returns the writable directory guaranteed inside the sandbox
// (adapter protocol §6.2 sandbox.scratch_dir).
func (s *Sandbox) ScratchDir() string { return scratchDir }

// Exec runs one command inside the sandbox (adapter protocol §4.1).
func (s *Sandbox) Exec(ctx context.Context, req sandbox.ExecRequest) (*sandbox.ExecResult, error) {
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	args := []string{"exec"}
	if len(req.Stdin) > 0 {
		args = append(args, "-i")
	}
	for _, k := range sortedKeys(req.Env) {
		args = append(args, "-e", k+"="+req.Env[k])
	}
	args = append(args, s.id)
	args = append(args, req.Argv...)

	start := time.Now()
	stdout, stderr, truncated, exit, err := s.p.run.Run(ctx, strings.NewReader(string(req.Stdin)), s.p.bin, args...)
	if err != nil {
		return nil, fmt.Errorf("exec in sandbox %s: %w", s.id, err)
	}
	return &sandbox.ExecResult{
		ExitCode:  exit,
		Stdout:    stdout,
		Stderr:    stderr,
		Truncated: truncated,
		Duration:  time.Since(start),
	}, nil
}

// PutFile copies a host file into the sandbox and applies mode (octal
// string, default "0600") — adapter protocol §4.2. Path allow-listing is
// the core's responsibility; the provider only moves bytes.
func (s *Sandbox) PutFile(ctx context.Context, hostPath, destPath, mode string) (*sandbox.PutFileResult, error) {
	if mode == "" {
		mode = "0600"
	}
	if _, err := strconv.ParseUint(mode, 8, 32); err != nil {
		return nil, fmt.Errorf("%w: mode %q is not octal", sandbox.ErrInvalidParams, mode)
	}
	info, err := os.Stat(hostPath)
	if err != nil {
		return nil, fmt.Errorf("put_file source: %w", err)
	}

	start := time.Now()
	if _, stderr, _, exit, err := s.p.run.Run(ctx, nil, s.p.bin, "cp", hostPath, s.id+":"+destPath); err != nil {
		return nil, fmt.Errorf("copy into sandbox %s: %w", s.id, err)
	} else if exit != 0 {
		return nil, fmt.Errorf("copy into sandbox %s: docker cp exited %d: %s", s.id, exit, firstLine(stderr))
	}
	if _, stderr, _, exit, err := s.p.run.Run(ctx, nil, s.p.bin, "exec", s.id, "chmod", mode, destPath); err != nil {
		return nil, fmt.Errorf("chmod in sandbox %s: %w", s.id, err)
	} else if exit != 0 {
		return nil, fmt.Errorf("chmod in sandbox %s: exited %d: %s", s.id, exit, firstLine(stderr))
	}
	return &sandbox.PutFileResult{BytesCopied: info.Size(), Duration: time.Since(start)}, nil
}

// Destroy force-removes the container and its volumes. It is idempotent:
// destroying an already-removed sandbox succeeds.
func (s *Sandbox) Destroy(ctx context.Context) error {
	if err := s.p.remove(ctx, s.id); err != nil {
		return fmt.Errorf("destroy sandbox: %w", err)
	}
	s.p.logger.Info("sandbox destroyed", "id", s.id)
	return nil
}

func (p *Provider) remove(ctx context.Context, id string) error {
	_, stderr, _, exit, err := p.run.Run(ctx, nil, p.bin, "rm", "-f", "-v", id)
	if err != nil {
		return err
	}
	if exit != 0 && !strings.Contains(string(stderr), "No such container") {
		return fmt.Errorf("docker rm exited %d: %s", exit, firstLine(stderr))
	}
	return nil
}

// runArgs builds the docker run arguments from drill-config sandbox params.
// Recognized: image (required), network (default "none"), memory, cpus,
// command (whitespace-split argv appended after the image — for engines
// restored physically, where the adapter must start the server itself),
// env.<NAME>. Anything else is an error — typos must not silently weaken a
// sandbox. Publishing ports is not expressible at all.
func (p *Provider) runArgs(params map[string]string) ([]string, error) {
	image := params["image"]
	if image == "" {
		return nil, fmt.Errorf(`%w: "image" is required for the docker provider`, sandbox.ErrInvalidParams)
	}
	network := "none"
	if n, ok := params["network"]; ok {
		network = n
	}
	args := []string{
		"run", "-d",
		"--name", "probavi-sbx-" + randomSuffix(),
		"--label", LabelSandbox + "=1",
		"--label", labelPID + "=" + strconv.Itoa(p.pid),
		"--label", labelHost + "=" + p.hostID,
		"--network", network,
	}
	for _, k := range sortedKeys(params) {
		v := params[k]
		switch {
		case k == "image" || k == "network" || k == "command":
		case k == "memory":
			args = append(args, "--memory", v)
		case k == "cpus":
			args = append(args, "--cpus", v)
		case strings.HasPrefix(k, "env."):
			name := strings.TrimPrefix(k, "env.")
			if !envNamePattern.MatchString(name) {
				return nil, fmt.Errorf("%w: %q is not a valid environment variable name", sandbox.ErrInvalidParams, name)
			}
			args = append(args, "-e", name+"="+v)
		default:
			return nil, fmt.Errorf("%w: unknown docker sandbox param %q (supported: image, network, memory, cpus, command, env.<NAME>)", sandbox.ErrInvalidParams, k)
		}
	}
	args = append(args, image)
	// Split on whitespace, executed directly by the runtime — no shell.
	return append(args, strings.Fields(params["command"])...), nil
}

// awaitRunning waits until the container runtime is up (not the engine —
// that is the adapter's readiness job, see PoC finding 1).
func (p *Provider) awaitRunning(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, p.awaitCap)
	defer cancel()
	for {
		stdout, _, _, exit, err := p.run.Run(ctx, nil, p.bin, "inspect", "-f", "{{.State.Running}}", id)
		if err != nil {
			return fmt.Errorf("await sandbox %s: %w", id, err)
		}
		if exit == 0 && strings.TrimSpace(string(stdout)) == "true" {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("sandbox %s never reached running state: %w", id, ctx.Err())
		case <-time.After(p.awaitInterval):
		}
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func randomSuffix() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is unrecoverable; the name is cosmetic, the
		// container id is authoritative — fall back to the pid.
		return "p" + strconv.Itoa(os.Getpid())
	}
	return hex.EncodeToString(b[:])
}
