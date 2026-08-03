package main

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/aafeher/probavi/internal/capabilities"
)

const repoRoot = "../../.."

// TestRunWritesTheManifest covers the exact invocation the CI drift gate
// and `go generate ./...` use.
func TestRunWritesTheManifest(t *testing.T) {
	out := filepath.Join(t.TempDir(), "capabilities.json")
	if err := run([]string{"-root", repoRoot, "-out", out}, io.Discard); err != nil {
		t.Fatalf("run: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	committed, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(capabilities.Path)))
	if err != nil {
		t.Fatalf("read committed manifest: %v", err)
	}
	if !bytes.Equal(got, committed) {
		t.Error("the tool did not reproduce the committed manifest")
	}
}

// TestRunDefaultsToTheContractPath proves the output path is not something
// an invocation has to remember: downstream consumers read a fixed path.
func TestRunDefaultsToTheContractPath(t *testing.T) {
	root := t.TempDir()
	real, err := filepath.Abs(repoRoot)
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	for _, shared := range []string{"docs", "spec", "adapters"} {
		if lerr := os.Symlink(filepath.Join(real, shared), filepath.Join(root, shared)); lerr != nil {
			t.Fatalf("link %s: %v", shared, lerr)
		}
	}
	// docs/ is a symlink into the real tree, so replace it with a copy
	// rather than writing the manifest over the committed one.
	if err := os.Remove(filepath.Join(root, "docs")); err != nil {
		t.Fatalf("unlink docs: %v", err)
	}
	if err := copyTree(filepath.Join(real, "docs"), filepath.Join(root, "docs")); err != nil {
		t.Fatalf("copy docs: %v", err)
	}
	if err := run([]string{"-root", root}, io.Discard); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(capabilities.Path))); err != nil {
		t.Errorf("default output path was not written: %v", err)
	}
}

func TestRunReportsBadInvocations(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"unknown flag", []string{"-no-such-flag"}},
		{"stray argument", []string{"capabilities.json"}},
		{"root that does not exist", []string{"-root", filepath.Join(t.TempDir(), "absent")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := run(tc.args, io.Discard); err == nil {
				t.Error("run accepted an invocation it must reject")
			}
		})
	}
}

// copyTree copies a directory tree shallowly enough for the test above:
// regular files and directories, no modes beyond readability.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		return os.WriteFile(target, raw, 0o600)
	})
}
