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
	"github.com/aafeher/probavi/internal/i18n"
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
	tr, err := i18n.New(i18n.Detect(os.Getenv))
	if err != nil {
		// A broken embedded catalog is a build defect; fall back to the
		// canonical English loudly, never crash a cron drill over prose.
		fmt.Fprintf(os.Stderr, "probavi: %v\n", err)
		tr = i18n.English()
	}
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, tr))
}

func run(args []string, stdout, stderr io.Writer, tr *i18n.T) int {
	if len(args) == 0 {
		usage(stderr, tr)
		return exitUsage
	}
	switch args[0] {
	case "run":
		return runDrill(args[1:], stdout, stderr, tr)
	case "gameday":
		return runGameDay(args[1:], stdout, stderr, tr)
	case "evidence":
		return runEvidence(args[1:], stdout, stderr, tr)
	case "adapter":
		return runAdapter(args[1:], stdout, stderr, tr)
	case "version":
		return runVersion(stdout, tr)
	default:
		tr.Fprintf(stderr, msgUnknownCommand, args[0])
		usage(stderr, tr)
		return exitUsage
	}
}

func runAdapter(args []string, stdout, stderr io.Writer, tr *i18n.T) int {
	if len(args) == 0 {
		usage(stderr, tr)
		return exitUsage
	}
	switch args[0] {
	case "probe":
		return runAdapterProbe(args[1:], stdout, stderr, tr)
	case "conformance":
		return runAdapterConformance(args[1:], stdout, stderr, tr)
	default:
		tr.Fprintf(stderr, msgUnknownAdapterSub, args[0])
		usage(stderr, tr)
		return exitUsage
	}
}

func runEvidence(args []string, stdout, stderr io.Writer, tr *i18n.T) int {
	if len(args) == 0 {
		usage(stderr, tr)
		return exitUsage
	}
	switch args[0] {
	case "verify":
		return runEvidenceVerify(args[1:], stdout, stderr, tr)
	case "keygen":
		return runEvidenceKeygen(args[1:], stdout, stderr, tr)
	default:
		tr.Fprintf(stderr, msgUnknownEvidenceSub, args[0])
		usage(stderr, tr)
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

func runEvidenceVerify(args []string, stdout, stderr io.Writer, tr *i18n.T) int {
	fs := flag.NewFlagSet("evidence verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	logPath := fs.String("log", "", "path to the evidence log file (required)")
	var keyPaths stringList
	fs.Var(&keyPaths, "key", "public key file; repeat to build a keyring (required)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *logPath == "" || len(keyPaths) == 0 {
		tr.Fprintf(stderr, msgVerifyFlagsRequired)
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
		tr.Fprintf(stderr, msgVerifyEncodeResult, err)
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

func runEvidenceKeygen(args []string, stdout, stderr io.Writer, tr *i18n.T) int {
	fs := flag.NewFlagSet("evidence keygen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "path for the private key; the public key is written next to it as <path>.pub (required)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *out == "" {
		tr.Fprintf(stderr, msgKeygenOutRequired)
		return exitUsage
	}
	pubPath := *out + ".pub"
	keyID, err := evidence.GenerateKeyPair(*out, pubPath)
	if err != nil {
		fmt.Fprintf(stderr, "probavi evidence keygen: %v\n", err)
		return exitUsage
	}
	if err := json.NewEncoder(stdout).Encode(keygenOutput{KeyID: keyID, KeyFile: *out, PublicKeyFile: pubPath}); err != nil {
		tr.Fprintf(stderr, msgKeygenEncodeResult, err)
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
func runVersion(stdout io.Writer, tr *i18n.T) int {
	fmt.Fprintf(stdout, "probavi %s %s/%s\n", version, runtime.GOOS, runtime.GOARCH)
	tr.Fprintf(stdout, msgVersionProtocol, adapter.ProtocolVersion)
	tr.Fprintf(stdout, msgVersionSchema, evidence.SchemaID)
	return 0
}

func usage(w io.Writer, tr *i18n.T) {
	tr.Fprintf(w, msgUsage)
}
