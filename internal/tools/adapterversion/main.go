// Command adapterversion fails a change that edits an adapter's source
// without moving that adapter's adapterVersion.
//
// The constant is not bookkeeping. Each adapter reports it through the
// protocol, and the core copies it into every signed evidence record as
// adapter.version (internal/core/core.go). Two different adapter builds
// reporting the same version leave an auditor holding two records that
// claim the same provenance and cannot be told apart.
//
// It is a repository tool, not a shipped binary, and it is a pull-request
// gate: "changed" only means anything relative to a base ref.
//
// What counts as a change is a deny-list, not an allow-list, so a file
// type nobody has thought of yet is caught rather than ignored. Only four
// things under an adapter directory are known not to reach its binary and
// are therefore excluded: *_test.go, anything under testdata/, README.md,
// and adapter.json (read from disk by the capabilities generator, never
// compiled in). Should an adapter ever grow a //go:embed, the embedded
// file is already covered.
//
// Two limits, stated rather than implied. The check cannot see a change
// in the Go toolchain, which is pinned by go.mod and would alter every
// binary at once — a repository-wide event that should not push every
// adapter's semantic version up, or the number stops meaning anything.
// And it says nothing about which bytes actually produced a record: that
// is build identity, and the evidence schema carries no digest today.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
)

// versionRe matches the constant declaration in an adapter's source.
var versionRe = regexp.MustCompile(`adapterVersion\s*=\s*"([^"]*)"`)

// gitRunner runs one git command and returns its stdout. Injected so the
// gate's logic is testable without a repository.
type gitRunner func(args ...string) ([]byte, error)

func main() {
	if err := run(os.Args[1:], execGit, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "adapterversion: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, git gitRunner, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("adapterversion", flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.String("base", "", "git ref the change is measured against (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *base == "" {
		return errors.New("-base is required")
	}

	changed, err := changedFiles(git, *base)
	if err != nil {
		return err
	}
	baseVersions, err := versionsAt(git, *base)
	if err != nil {
		return err
	}
	headVersions, err := versionsAt(git, "HEAD")
	if err != nil {
		return err
	}

	bad := violations(changed, baseVersions, headVersions)
	if len(bad) == 0 {
		fmt.Fprintln(stdout, "no adapter source changed without a version bump")
		return nil
	}
	for _, v := range bad {
		fmt.Fprintf(stdout, "adapters/%s: source changed, but adapterVersion is still %q\n", v.adapter, v.version)
		for _, f := range v.files {
			fmt.Fprintf(stdout, "    %s\n", f)
		}
	}
	return fmt.Errorf("%d adapter(s) changed without a version bump — raise adapterVersion, "+
		"regenerate the probe golden, and run `go generate ./...`; if the change genuinely cannot "+
		"alter behaviour, label the pull request adapter-version-exempt so the exemption is on record",
		len(bad))
}

// violation is one adapter whose source moved while its version did not.
type violation struct {
	adapter string
	version string
	files   []string
}

// adapterOf returns the adapter a repository path belongs to, and whether
// that path can reach the adapter's binary at all.
func adapterOf(path string) (string, bool) {
	parts := strings.Split(path, "/")
	if len(parts) < 3 || parts[0] != "adapters" {
		return "", false
	}
	for _, p := range parts[2:] {
		if p == "testdata" {
			return "", false
		}
	}
	switch last := parts[len(parts)-1]; {
	case strings.HasSuffix(last, "_test.go"), last == "README.md", last == "adapter.json":
		return "", false
	}
	return parts[1], true
}

// violations reports every adapter whose source changed while the version
// it publishes stayed put. An adapter that did not exist at the base has
// nothing to bump from, and one deleted since is no longer ours.
func violations(changed []string, base, head map[string]string) []violation {
	byAdapter := make(map[string][]string)
	for _, path := range changed {
		if id, ok := adapterOf(path); ok {
			byAdapter[id] = append(byAdapter[id], path)
		}
	}
	out := make([]violation, 0, len(byAdapter))
	for _, id := range slices.Sorted(maps.Keys(byAdapter)) {
		before, existed := base[id]
		if !existed || before != head[id] {
			continue
		}
		files := byAdapter[id]
		slices.Sort(files)
		out = append(out, violation{adapter: id, version: before, files: files})
	}
	return out
}

// changedFiles lists the paths this branch changed relative to where it
// left the base. The three-dot form measures from the merge base, so an
// unrelated commit landing on the base meanwhile is not attributed here.
func changedFiles(git gitRunner, base string) ([]string, error) {
	out, err := git("diff", "--name-only", base+"...HEAD")
	if err != nil {
		return nil, err
	}
	return nonEmptyLines(out), nil
}

// versionsAt reads every adapter's declared version at one ref.
func versionsAt(git gitRunner, ref string) (map[string]string, error) {
	out, err := git("ls-tree", "-r", "--name-only", ref, "--", "adapters")
	if err != nil {
		return nil, err
	}
	versions := make(map[string]string)
	for _, path := range nonEmptyLines(out) {
		id, ok := adapterOf(path)
		if !ok || !strings.HasSuffix(path, ".go") {
			continue
		}
		blob, err := git("show", ref+":"+path)
		if err != nil {
			return nil, err
		}
		m := versionRe.FindSubmatch(blob)
		if m == nil {
			continue
		}
		found := string(m[1])
		if prev, seen := versions[id]; seen && prev != found {
			return nil, fmt.Errorf("adapters/%s declares adapterVersion as both %q and %q at %s",
				id, prev, found, ref)
		}
		versions[id] = found
	}
	return versions, nil
}

func nonEmptyLines(out []byte) []string {
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func execGit(args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
