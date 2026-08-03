// Command capabilities regenerates docs/capabilities.json from the code
// registries that implement each capability.
//
// It is a repository tool, not a shipped binary: the manifest describes
// this repository — the adapters' probe goldens and manifests, the paths
// of the normative documents — not the contents of a compiled probavi, so
// there is deliberately no `probavi capabilities` subcommand.
//
// Run it through `go generate ./...`. CI regenerates and fails on any
// difference (AGENTS.md §5.8).
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/probavi/probavi/internal/capabilities"
)

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "capabilities: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("capabilities", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", ".", "repository root to read the capabilities from")
	out := fs.String("out", "", "output path (default: <root>/"+capabilities.Path+")")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	target := *out
	if target == "" {
		target = filepath.Join(*root, filepath.FromSlash(capabilities.Path))
	}
	return capabilities.Generate(*root, target)
}
