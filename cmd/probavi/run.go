package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/aafeher/probavi/internal/adapter"
	"github.com/aafeher/probavi/internal/config"
	"github.com/aafeher/probavi/internal/core"
	"github.com/aafeher/probavi/internal/evidence"
	"github.com/aafeher/probavi/internal/metrics"
	"github.com/aafeher/probavi/internal/sandbox/docker"
)

// version is stamped into evidence records; releases will set it via
// -ldflags.
const version = "0.1.0-dev"

// Exit codes for `probavi run` (cron/CI contract, documented in usage).
const (
	exitPass         = 0
	exitFail         = 1
	exitError        = 2
	exitEvidenceLost = 5
)

// runSummary is the machine-readable drill summary printed on stdout.
type runSummary struct {
	Outcome      string `json:"outcome"`
	Seq          int64  `json:"seq"`
	EvidencePath string `json:"evidence_path"`
	ChecksPassed int    `json:"checks_passed"`
	ChecksTotal  int    `json:"checks_total"`
	RestoreMS    *int64 `json:"restore_ms"`
	TotalMS      *int64 `json:"total_ms"`
	ErrorCode    string `json:"error_code,omitempty"`
}

func runDrill(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "path to the drill configuration YAML (required)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *configPath == "" {
		fmt.Fprintln(stderr, "probavi run: --config is required")
		return exitUsage
	}
	logger := slog.New(slog.NewTextHandler(stderr, nil))

	drill, evidencePath, cleanup, err := wireDrill(*configPath, logger)
	if err != nil {
		fmt.Fprintf(stderr, "probavi run: %v\n", err)
		return exitUsage
	}
	defer cleanup()

	// The drill's hard wall-clock limit comes from the config; SIGTERM and
	// Ctrl-C turn into a cancelled drill with a signed record, not a crash.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, drill.Config.Sandbox.Timeout.Std())
	defer cancel()

	rec, err := drill.Run(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "probavi run: %v\n", err)
		return exitEvidenceLost
	}
	if drill.Config.Metrics != nil {
		if merr := metrics.WriteTextfile(drill.Config.Metrics.PrometheusTextfile, rec); merr != nil {
			// Metrics are observability, not evidence: failure is loud but
			// never changes the drill verdict.
			logger.Error("write metrics textfile", "err", merr)
		}
	}
	if err := json.NewEncoder(stdout).Encode(summarize(rec, evidencePath)); err != nil {
		fmt.Fprintf(stderr, "probavi run: encode summary: %v\n", err)
	}
	switch rec.Outcome {
	case evidence.OutcomePass:
		return exitPass
	case evidence.OutcomeFail:
		return exitFail
	default:
		return exitError
	}
}

// wireDrill builds the object graph for one drill run: config, evidence
// store, adapter runner, sandbox provider.
func wireDrill(configPath string, logger *slog.Logger) (*core.Drill, string, func(), error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, "", nil, err
	}
	if cfg.Sandbox.Provider != "docker" {
		return nil, "", nil, fmt.Errorf("unsupported sandbox provider %q (supported: docker)", cfg.Sandbox.Provider)
	}
	signer, err := evidence.LoadSigner(cfg.Evidence.SignKey)
	if err != nil {
		return nil, "", nil, err
	}
	store, err := evidence.Open(cfg.Evidence.Path, signer, logger)
	if err != nil {
		return nil, "", nil, err
	}
	password := randomHex(16)
	runner, err := adapter.New(cfg.Target.Adapter, logger, &adapter.Options{
		CredentialEnv: cfg.Target.Source.CredentialEnv,
		Env:           map[string]string{"PROBAVI_SANDBOX_PASSWORD": password},
	})
	if err != nil {
		if cerr := store.Close(); cerr != nil {
			logger.Error("close evidence store", "err", cerr)
		}
		return nil, "", nil, err
	}
	drill := &core.Drill{
		Config:          cfg,
		Adapter:         runner,
		Provider:        dockerProvider{docker.New(logger)},
		Store:           store,
		Logger:          logger,
		Version:         version,
		SandboxPassword: password,
	}
	cleanup := func() {
		if cerr := store.Close(); cerr != nil {
			logger.Error("close evidence store", "err", cerr)
		}
	}
	return drill, cfg.Evidence.Path, cleanup, nil
}

func summarize(rec *evidence.Record, evidencePath string) runSummary {
	passed := 0
	for _, c := range rec.Checks {
		if c.OK {
			passed++
		}
	}
	s := runSummary{
		Outcome:      string(rec.Outcome),
		Seq:          rec.Seq,
		EvidencePath: evidencePath,
		ChecksPassed: passed,
		ChecksTotal:  len(rec.Checks),
		RestoreMS:    rec.Timings.Restore,
		TotalMS:      rec.Timings.Total,
	}
	if rec.Error != nil {
		s.ErrorCode = rec.Error.Code
	}
	return s
}

// dockerProvider adapts *docker.Provider to core.Provider (Go interfaces
// do not covariantly match the concrete sandbox return type).
type dockerProvider struct {
	p *docker.Provider
}

func (d dockerProvider) Create(ctx context.Context, params map[string]string) (core.Sandbox, error) {
	sbx, err := d.p.Create(ctx, params)
	if err != nil {
		return nil, err
	}
	return sbx, nil
}

func (d dockerProvider) SweepOrphans(ctx context.Context) ([]string, error) {
	return d.p.SweepOrphans(ctx)
}

// runAdapterProbe implements `probavi adapter probe <name>`: resolve the
// adapter and print its probe response as JSON.
func runAdapterProbe(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] == "" {
		fmt.Fprintln(stderr, "probavi adapter probe: exactly one adapter name is required")
		return exitUsage
	}
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	runner, err := adapter.New(args[0], logger, nil)
	if err != nil {
		fmt.Fprintf(stderr, "probavi adapter probe: %v\n", err)
		return exitUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res, err := runner.Probe(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "probavi adapter probe: %v\n", err)
		return exitError
	}
	if err := json.NewEncoder(stdout).Encode(res); err != nil {
		fmt.Fprintf(stderr, "probavi adapter probe: encode: %v\n", err)
		return exitError
	}
	return exitPass
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// Unrecoverable: an ephemeral sandbox secret must never be
		// predictable.
		panic("probavi: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}
