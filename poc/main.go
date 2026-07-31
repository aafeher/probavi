// Package main is the ROADMAP.md Phase 0 proof of concept (throwaway by
// design): restore a pg_dump custom-format file into a disposable Docker
// container, wait for readiness, run one validation query, tear down.
// Its findings feed docs/adapter-protocol.md and docs/evidence-schema.md.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	image      = "postgres:16"
	container  = "probavi-poc-restore"
	pocLabel   = "com.probavi.poc=1"
	dumpInCtr  = "/tmp/orders.dump"
	checkQuery = "SELECT count(*) FROM orders"
	minRows    = 100_000
)

// result is the machine-readable drill outcome, printed as one JSON line on
// stdout — a deliberate rehearsal of the evidence-record discipline (stdout
// is machine output, everything human goes to stderr).
type result struct {
	Outcome         string  `json:"outcome"`
	Rows            int     `json:"rows"`
	RestoreSeconds  float64 `json:"restore_seconds"`
	ValidateSeconds float64 `json:"validate_seconds"`
}

func main() {
	dump := flag.String("dump", "poc/testdata/orders.dump", "path to a pg_dump custom-format file")
	timeout := flag.Duration("timeout", 5*time.Minute, "wall-clock limit for the whole drill")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := drill(log, *dump, *timeout); err != nil {
		log.Error("drill failed", "err", err)
		os.Exit(1)
	}
}

// drill owns the context lifecycle so main can os.Exit without skipping
// deferred cleanup.
func drill(log *slog.Logger, dump string, timeout time.Duration) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	res, err := run(ctx, log, dump)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(os.Stdout).Encode(res); err != nil {
		return fmt.Errorf("encode result: %w", err)
	}
	return nil
}

func run(ctx context.Context, log *slog.Logger, dump string) (*result, error) {
	if _, err := os.Stat(dump); err != nil {
		return nil, fmt.Errorf("backup source: %w", err)
	}

	sweepOrphans(ctx, log)

	log.Info("provisioning sandbox", "image", image)
	restoreStart := time.Now()
	// No published ports: the restored data is treated as production data,
	// so the only way in is docker exec.
	if _, err := docker(ctx, "run", "-d", "--name", container, "--label", pocLabel,
		"-e", "POSTGRES_PASSWORD=poc", image); err != nil {
		return nil, fmt.Errorf("provision container: %w", err)
	}
	// Teardown runs on a fresh context: when the drill timed out or was
	// interrupted, ctx is already dead but cleanup still has to happen.
	defer func() {
		tctx, tcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer tcancel()
		if _, err := docker(tctx, "rm", "-f", "-v", container); err != nil {
			log.Error("teardown failed, container may be orphaned", "container", container, "err", err)
			return
		}
		log.Info("teardown complete", "container", container)
	}()

	if err := awaitReady(ctx, log); err != nil {
		return nil, fmt.Errorf("await readiness: %w", err)
	}

	log.Info("restoring backup", "dump", dump)
	if _, err := docker(ctx, "cp", dump, container+":"+dumpInCtr); err != nil {
		return nil, fmt.Errorf("copy dump into sandbox: %w", err)
	}
	if _, err := docker(ctx, "exec", container, "pg_restore",
		"-U", "postgres", "-d", "postgres", "--no-owner", "--exit-on-error", dumpInCtr); err != nil {
		return nil, fmt.Errorf("pg_restore: %w", err)
	}
	restoreDur := time.Since(restoreStart)
	log.Info("restore complete", "duration", restoreDur)

	validateStart := time.Now()
	out, err := docker(ctx, "exec", container, "psql", "-U", "postgres", "-tA", "-c", checkQuery)
	if err != nil {
		return nil, fmt.Errorf("validation query: %w", err)
	}
	rows, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return nil, fmt.Errorf("parse row count %q: %w", out, err)
	}
	validateDur := time.Since(validateStart)

	if rows < minRows {
		return nil, fmt.Errorf("check failed: row_count %d < required %d", rows, minRows)
	}
	log.Info("check passed", "rows", rows, "min", minRows)

	return &result{
		Outcome:         "pass",
		Rows:            rows,
		RestoreSeconds:  restoreDur.Seconds(),
		ValidateSeconds: validateDur.Seconds(),
	}, nil
}

// awaitReady polls pg_isready over TCP. TCP on purpose: during initdb the
// image's entrypoint runs a temporary server that listens on the unix socket
// only, so a socket-based check reports ready before the final server is up.
func awaitReady(ctx context.Context, log *slog.Logger) error {
	start := time.Now()
	for {
		if _, err := docker(ctx, "exec", container, "pg_isready", "-h", "127.0.0.1", "-U", "postgres", "-q"); err == nil {
			log.Info("sandbox ready", "wait", time.Since(start))
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("sandbox never became ready: %w", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// sweepOrphans removes containers left behind by previous crashed runs,
// identified by label — never by name pattern.
func sweepOrphans(ctx context.Context, log *slog.Logger) {
	out, err := docker(ctx, "ps", "-aq", "--filter", "label="+pocLabel)
	if err != nil || out == "" {
		return
	}
	for _, id := range strings.Fields(out) {
		if _, err := docker(ctx, "rm", "-f", "-v", id); err != nil {
			log.Warn("orphan sweep failed", "id", id, "err", err)
			continue
		}
		log.Info("swept orphan container", "id", id)
	}
}

func docker(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
