package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestAdapterOf pins the deny-list: everything under an adapter directory
// counts as source unless it is one of the four things that provably
// cannot reach the binary. A new file type must fail closed.
func TestAdapterOf(t *testing.T) {
	tests := []struct {
		path string
		id   string
		want bool
	}{
		{"adapters/postgres/ops.go", "postgres", true},
		{"adapters/postgres/main.go", "postgres", true},
		{"adapters/mysql/internal/pkg/helper.go", "mysql", true},
		{"adapters/postgres/schema.sql", "postgres", true},   // an embed would land here
		{"adapters/postgres/ops_test.go", "", false},         // tests are not shipped
		{"adapters/postgres/testdata/probe.json", "", false}, // goldens are not shipped
		{"adapters/postgres/testdata/nested/x.go", "", false},
		{"adapters/postgres/README.md", "", false},
		{"adapters/postgres/adapter.json", "", false}, // read from disk, never compiled in
		{"internal/core/core.go", "", false},
		{"adapters/postgres", "", false}, // the directory itself
		{"README.md", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			id, ok := adapterOf(tt.path)
			if ok != tt.want || id != tt.id {
				t.Errorf("adapterOf(%q) = (%q, %v), want (%q, %v)", tt.path, id, ok, tt.id, tt.want)
			}
		})
	}
}

func TestViolations(t *testing.T) {
	tests := []struct {
		name    string
		changed []string
		base    map[string]string
		head    map[string]string
		want    []violation
	}{
		{
			name:    "source changed, version stood still",
			changed: []string{"adapters/postgres/ops.go"},
			base:    map[string]string{"postgres": "0.3.0"},
			head:    map[string]string{"postgres": "0.3.0"},
			want:    []violation{{adapter: "postgres", version: "0.3.0", files: []string{"adapters/postgres/ops.go"}}},
		},
		{
			name:    "source changed and version moved",
			changed: []string{"adapters/postgres/ops.go"},
			base:    map[string]string{"postgres": "0.3.0"},
			head:    map[string]string{"postgres": "0.4.0"},
		},
		{
			name:    "only tests and docs changed",
			changed: []string{"adapters/postgres/ops_test.go", "adapters/postgres/README.md", "adapters/postgres/testdata/probe.json"},
			base:    map[string]string{"postgres": "0.3.0"},
			head:    map[string]string{"postgres": "0.3.0"},
		},
		{
			name:    "a brand new adapter has nothing to bump from",
			changed: []string{"adapters/oracle/main.go"},
			base:    map[string]string{},
			head:    map[string]string{"oracle": "0.1.0"},
		},
		{
			name:    "a deleted adapter is not our problem",
			changed: []string{"adapters/oracle/main.go"},
			base:    map[string]string{"oracle": "0.1.0"},
			head:    map[string]string{},
		},
		{
			name:    "core changes are out of scope",
			changed: []string{"internal/core/core.go", "cmd/probavi/run.go"},
			base:    map[string]string{"postgres": "0.3.0"},
			head:    map[string]string{"postgres": "0.3.0"},
		},
		{
			name: "each offending adapter is reported once, in order",
			changed: []string{
				"adapters/mysql/ops.go",
				"adapters/postgres/source.go",
				"adapters/postgres/ops.go",
			},
			base: map[string]string{"mysql": "0.2.0", "postgres": "0.3.0"},
			head: map[string]string{"mysql": "0.2.0", "postgres": "0.3.0"},
			want: []violation{
				{adapter: "mysql", version: "0.2.0", files: []string{"adapters/mysql/ops.go"}},
				{adapter: "postgres", version: "0.3.0", files: []string{"adapters/postgres/ops.go", "adapters/postgres/source.go"}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := violations(tt.changed, tt.base, tt.head)
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("violations() = %v, want %v", got, tt.want)
			}
		})
	}
}

// fakeGit scripts the three commands the gate issues, so run() is tested
// end to end without a repository.
type fakeGit struct {
	changed  []string          // paths reported by git diff
	trees    map[string]string // ref → ls-tree output
	blobs    map[string]string // "ref:path" → file contents
	failOn   string            // first argument that should fail
	commands []string
}

func (f *fakeGit) run(args ...string) ([]byte, error) {
	f.commands = append(f.commands, strings.Join(args, " "))
	if f.failOn != "" && args[0] == f.failOn {
		return nil, errors.New("scripted failure")
	}
	switch args[0] {
	case "diff":
		return []byte(strings.Join(f.changed, "\n")), nil
	case "ls-tree":
		return []byte(f.trees[args[3]]), nil
	case "show":
		return []byte(f.blobs[strings.TrimPrefix(args[1], "")]), nil
	}
	return nil, fmt.Errorf("unscripted git command %q", args[0])
}

func newFakeGit(changed []string, baseVersion, headVersion string) *fakeGit {
	tree := "adapters/postgres/ops.go\nadapters/postgres/README.md\nadapters/postgres/ops_test.go\n"
	return &fakeGit{
		changed: changed,
		trees:   map[string]string{"base": tree, "HEAD": tree},
		blobs: map[string]string{
			"base:adapters/postgres/ops.go": fmt.Sprintf("const (\n\tadapterVersion = %q\n)", baseVersion),
			"HEAD:adapters/postgres/ops.go": fmt.Sprintf("const (\n\tadapterVersion = %q\n)", headVersion),
		},
	}
}

func TestRun(t *testing.T) {
	tests := []struct {
		name    string
		changed []string
		base    string
		head    string
		wantErr string
		wantOut string
	}{
		{
			name:    "clean when the version moved",
			changed: []string{"adapters/postgres/ops.go"},
			base:    "0.3.0", head: "0.4.0",
			wantOut: "no adapter source changed without a version bump",
		},
		{
			name:    "clean when nothing shippable changed",
			changed: []string{"adapters/postgres/README.md"},
			base:    "0.3.0", head: "0.3.0",
			wantOut: "no adapter source changed without a version bump",
		},
		{
			name:    "fails and names the file",
			changed: []string{"adapters/postgres/ops.go"},
			base:    "0.3.0", head: "0.3.0",
			wantErr: "1 adapter(s) changed without a version bump",
			wantOut: "adapters/postgres: source changed, but adapterVersion is still \"0.3.0\"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run([]string{"-base", "base"}, newFakeGit(tt.changed, tt.base, tt.head).run, &stdout, &stderr)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("run: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatal("run accepted a change that skipped the version bump")
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Errorf("error = %v, want it to mention %q", err, tt.wantErr)
			}
			if !strings.Contains(stdout.String(), tt.wantOut) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tt.wantOut)
			}
		})
	}
}

// TestRunReportsTheExemption keeps the escape hatch discoverable: an
// operator who hits the gate must learn how to record the exemption from
// the message itself.
func TestRunReportsTheExemption(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run([]string{"-base", "base"},
		newFakeGit([]string{"adapters/postgres/ops.go"}, "0.3.0", "0.3.0").run, &stdout, &stderr)
	if err == nil {
		t.Fatal("run accepted a change that skipped the version bump")
	}
	if !strings.Contains(err.Error(), "adapter-version-exempt") {
		t.Errorf("error = %v, want it to name the exemption label", err)
	}
}

func TestRunUsage(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"base is required", nil, "-base is required"},
		{"stray argument", []string{"-base", "base", "extra"}, `unexpected argument "extra"`},
		{"bad flag", []string{"-nope"}, "flag provided but not defined"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run(tt.args, newFakeGit(nil, "0.3.0", "0.3.0").run, &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

// TestRunPropagatesGitFailures proves the gate fails loudly rather than
// passing when it cannot read the repository — a silent pass here would
// be worse than no gate.
func TestRunPropagatesGitFailures(t *testing.T) {
	for _, failOn := range []string{"diff", "ls-tree", "show"} {
		t.Run(failOn, func(t *testing.T) {
			g := newFakeGit([]string{"adapters/postgres/ops.go"}, "0.3.0", "0.4.0")
			g.failOn = failOn
			var stdout, stderr bytes.Buffer
			if err := run([]string{"-base", "base"}, g.run, &stdout, &stderr); err == nil {
				t.Fatalf("run passed although git %s failed", failOn)
			}
		})
	}
}

// TestExecGit covers the one impure hop, against the repository the test
// itself runs in. git is not an optional dependency here: the gate only
// ever runs on a checkout.
func TestExecGit(t *testing.T) {
	out, err := execGit("rev-parse", "--is-inside-work-tree")
	if err != nil {
		t.Fatalf("execGit: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "true" {
		t.Errorf("rev-parse --is-inside-work-tree = %q, want %q", got, "true")
	}

	// A failure must carry git's own diagnosis, not just an exit status:
	// a gate that fails opaquely wastes the reviewer's time.
	_, err = execGit("rev-parse", "--verify", "refs/heads/no-such-branch-here")
	if err == nil {
		t.Fatal("execGit succeeded on a ref that does not exist")
	}
	if !strings.Contains(err.Error(), "rev-parse") {
		t.Errorf("error = %v, want it to name the failing command", err)
	}
}

// TestVersionsAtRejectsTwoDeclarations guards the assumption the gate
// rests on: one adapter declares its version exactly once.
func TestVersionsAtRejectsTwoDeclarations(t *testing.T) {
	g := &fakeGit{
		trees: map[string]string{"HEAD": "adapters/postgres/ops.go\nadapters/postgres/extra.go\n"},
		blobs: map[string]string{
			"HEAD:adapters/postgres/ops.go":   `adapterVersion = "0.3.0"`,
			"HEAD:adapters/postgres/extra.go": `adapterVersion = "9.9.9"`,
		},
	}
	_, err := versionsAt(g.run, "HEAD")
	if err == nil || !strings.Contains(err.Error(), "both") {
		t.Errorf("error = %v, want it to report the conflicting declarations", err)
	}
}
