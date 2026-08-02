// Package main is the probavi CLI entry point. It contains flag parsing,
// wiring, and exit codes only — all logic lives in internal packages
// (AGENTS.md layout rule).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/aafeher/probavi/internal/adapter"
	"github.com/aafeher/probavi/internal/evidence"
)

// Exit codes. Verify verdicts follow evidence-schema.md §9; exitUsage
// covers usage and I/O errors, distinct from any verdict.
const (
	exitValid           = 0
	exitValidWithDamage = 1
	exitInvalid         = 2
	exitUsage           = 3
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitUsage
	}
	switch args[0] {
	case "run":
		return runDrill(args[1:], stdout, stderr)
	case "gameday":
		return runGameDay(args[1:], stdout, stderr)
	case "evidence":
		return runEvidence(args[1:], stdout, stderr)
	case "adapter":
		return runAdapter(args[1:], stdout, stderr)
	case "version":
		return runVersion(stdout)
	default:
		fmt.Fprintf(stderr, "probavi: unknown command %q\n\n", args[0])
		usage(stderr)
		return exitUsage
	}
}

func runAdapter(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitUsage
	}
	switch args[0] {
	case "probe":
		return runAdapterProbe(args[1:], stdout, stderr)
	case "conformance":
		return runAdapterConformance(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "probavi adapter: unknown subcommand %q\n\n", args[0])
		usage(stderr)
		return exitUsage
	}
}

func runEvidence(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitUsage
	}
	switch args[0] {
	case "verify":
		return runEvidenceVerify(args[1:], stdout, stderr)
	case "keygen":
		return runEvidenceKeygen(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "probavi evidence: unknown subcommand %q\n\n", args[0])
		usage(stderr)
		return exitUsage
	}
}

// verifyOutput is the machine-readable verify result printed on stdout.
type verifyOutput struct {
	Status       string `json:"status"`
	Records      int    `json:"records"`
	DamagedLines []int  `json:"damaged_lines"`
	FailedLine   int    `json:"failed_line"`
	Reason       string `json:"reason"`
}

func runEvidenceVerify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("evidence verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	logPath := fs.String("log", "", "path to the evidence log file (required)")
	var keyPaths stringList
	fs.Var(&keyPaths, "key", "public key file; repeat to build a keyring (required)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *logPath == "" || len(keyPaths) == 0 {
		fmt.Fprintln(stderr, "probavi evidence verify: --log and at least one --key are required")
		return exitUsage
	}

	keyring, err := loadKeyring(keyPaths)
	if err != nil {
		fmt.Fprintf(stderr, "probavi evidence verify: %v\n", err)
		return exitUsage
	}
	f, err := os.Open(*logPath)
	if err != nil {
		fmt.Fprintf(stderr, "probavi evidence verify: %v\n", err)
		return exitUsage
	}
	defer closeQuietly(f, stderr)

	res, err := evidence.Verify(f, keyring)
	if err != nil {
		fmt.Fprintf(stderr, "probavi evidence verify: %v\n", err)
		return exitUsage
	}
	out := verifyOutput{
		Status:       res.Status.String(),
		Records:      res.Records,
		DamagedLines: res.DamagedLines,
		FailedLine:   res.FailedLine,
		Reason:       res.Reason,
	}
	if out.DamagedLines == nil {
		out.DamagedLines = []int{}
	}
	if err := json.NewEncoder(stdout).Encode(out); err != nil {
		fmt.Fprintf(stderr, "probavi evidence verify: encode result: %v\n", err)
		return exitUsage
	}
	switch res.Status {
	case evidence.StatusValid:
		return exitValid
	case evidence.StatusValidWithDamage:
		return exitValidWithDamage
	default:
		return exitInvalid
	}
}

// keygenOutput is the machine-readable keygen result printed on stdout.
// It never contains key material.
type keygenOutput struct {
	KeyID         string `json:"key_id"`
	KeyFile       string `json:"key_file"`
	PublicKeyFile string `json:"public_key_file"`
}

func runEvidenceKeygen(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("evidence keygen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "path for the private key; the public key is written next to it as <path>.pub (required)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *out == "" {
		fmt.Fprintln(stderr, "probavi evidence keygen: --out is required")
		return exitUsage
	}
	pubPath := *out + ".pub"
	keyID, err := evidence.GenerateKeyPair(*out, pubPath)
	if err != nil {
		fmt.Fprintf(stderr, "probavi evidence keygen: %v\n", err)
		return exitUsage
	}
	if err := json.NewEncoder(stdout).Encode(keygenOutput{KeyID: keyID, KeyFile: *out, PublicKeyFile: pubPath}); err != nil {
		fmt.Fprintf(stderr, "probavi evidence keygen: encode result: %v\n", err)
		return exitUsage
	}
	return exitValid
}

func loadKeyring(paths []string) (evidence.Keyring, error) {
	keyring := evidence.Keyring{}
	for _, p := range paths {
		pub, err := evidence.LoadPublicKey(p)
		if err != nil {
			return nil, err
		}
		keyring[evidence.PublicKeyID(pub)] = pub
	}
	return keyring, nil
}

func closeQuietly(c io.Closer, stderr io.Writer) {
	if err := c.Close(); err != nil {
		fmt.Fprintf(stderr, "probavi: close: %v\n", err)
	}
}

// stringList collects a repeatable string flag.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// runVersion prints the binary version and the contract versions this
// build speaks. For a trust product the contracts matter as much as the
// binary: an auditor's first question about a log is "written under which
// schema", and an adapter author's is "against which protocol".
func runVersion(stdout io.Writer) int {
	fmt.Fprintf(stdout, "probavi %s %s/%s\n", version, runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(stdout, "adapter protocol: %s\n", adapter.ProtocolVersion)
	fmt.Fprintf(stdout, "evidence schema:  %s (verifies all published versions)\n", evidence.SchemaID)
	return 0
}

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage: probavi <command> [arguments]

Commands:
  run --config <drill.yaml>
      Execute one restore drill: sandbox up, restore, checks, teardown,
      and exactly one signed evidence record. Prints a one-line JSON
      summary on stdout. Run it from cron or a systemd timer — Probavi
      deliberately has no built-in scheduler.
      Exit codes: 0 backup proven restorable, 1 recoverability failure
      (backup/restore/check), 2 infrastructure error or cancelled,
      3 usage or setup error, 5 evidence record could not be written.

  gameday --config <gameday.yaml>
      Execute a DR game-day: member drills in dependency order, each the
      full run pipeline with its own signed evidence record; dependents
      of a failed member are skipped, independent branches continue.
      Prints a one-line JSON summary on stdout (docs/gameday.md).
      Exit codes: 0 every member passed, 1 a member drill failed,
      2 errors/cancellation left members unproven, 3 usage or setup
      error, 5 a member's evidence record could not be written.

  evidence verify --log <file> --key <pubkey> [--key <pubkey> ...]
      Verify an evidence log offline against one or more public keys.
      Prints a one-line JSON result on stdout.
      Exit codes: 0 VALID, 1 VALID_WITH_DAMAGE, 2 INVALID,
      3 usage or I/O error.

  evidence keygen --out <path>
      Generate an ed25519 signing key pair: <path> (mode 0600) and
      <path>.pub. Refuses to overwrite existing files.

  adapter probe <name>
      Resolve probavi-adapter-<name> and print its capabilities as JSON.

  adapter conformance [--source-kind <kind>] [--source-param k=v ...] <name-or-path>
      Drive the adapter through the frozen protocol conformance checks
      (docs/adapter-protocol.md §10) against a simulated sandbox — no
      container runtime involved. A new adapter is done when this passes.
      Prints one line per check on stderr and a JSON report on stdout.
      Exit codes: 0 conformant, 1 one or more checks failed, 3 usage error.

  version
      Print the probavi version and the contract versions this build
      speaks (adapter protocol, evidence schema).
`)
}
