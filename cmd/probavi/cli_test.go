package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aafeher/probavi/internal/cli"
	"github.com/aafeher/probavi/internal/i18n"
	"github.com/aafeher/probavi/internal/sandbox/registry"
)

// cli_test.go pins the wiring between the CLI contract (internal/cli) and
// this package. The contract is what docs/capabilities.json publishes, so
// a command that exists in one place and not the other would mean either
// an unreachable command or a published claim nothing implements.

// TestEveryCommandHasAHandler proves the published command list is
// exactly what the binary can run.
func TestEveryCommandHasAHandler(t *testing.T) {
	table := handlers()
	for _, c := range cli.Commands() {
		if _, ok := table[c.ID]; !ok {
			t.Errorf("command %q is declared but has no handler", c.ID)
		}
	}
	declared := map[string]bool{}
	for _, c := range cli.Commands() {
		declared[c.ID] = true
	}
	for id := range table {
		if !declared[id] {
			t.Errorf("handler %q implements a command the contract does not declare", id)
		}
	}
}

// TestEveryGroupHasAMessage proves each command group can explain an
// unknown subcommand in the operator's language.
func TestEveryGroupHasAMessage(t *testing.T) {
	messages := groupMessages()
	for _, g := range cli.Groups() {
		format, ok := messages[g]
		if !ok {
			t.Errorf("group %q has no unknown-subcommand message", g)
			continue
		}
		if !strings.Contains(format, "%q") {
			t.Errorf("group %q message does not name the offending word", g)
		}
	}
	for g := range messages {
		if !containsString(cli.Groups(), g) {
			t.Errorf("message for %q, which is not a command group", g)
		}
	}
}

// TestUsageListsEveryCommand keeps the translated help text and the
// contract in step. The usage text is not generated from the table — it is
// a catalog key, and regenerating it would invalidate every translation —
// so this is what stops a new command from being invisible in --help.
func TestUsageListsEveryCommand(t *testing.T) {
	for _, c := range cli.Commands() {
		if !strings.Contains(msgUsage, "\n  "+c.ID) {
			t.Errorf("usage text does not document the %q command", c.ID)
		}
	}
}

// TestDispatchRejectsAnUnimplementedCommand covers the build-defect path:
// a declared command with no handler must fail loudly rather than exit
// zero having done nothing.
func TestDispatchRejectsAnUnimplementedCommand(t *testing.T) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	m := cli.Resolve([]string{"run"})
	code := dispatch(map[string]handler{}, m, stdout, stderr, i18n.English())
	if code != exitUsage {
		t.Errorf("exit %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "run") {
		t.Errorf("stderr %q does not name the command", stderr)
	}
}

// TestUnknownSubcommandUsesTheGroupMessage pins the diagnostics the
// dispatch refactor had to preserve.
func TestUnknownSubcommandUsesTheGroupMessage(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"evidence", "sign"}, "probavi evidence: unknown subcommand"},
		{[]string{"adapter", "fuzz"}, "probavi adapter: unknown subcommand"},
		{[]string{"restore"}, "probavi: unknown command"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
			if code := run(tc.args, stdout, stderr, i18n.English()); code != exitUsage {
				t.Errorf("exit %d, want %d", code, exitUsage)
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("stderr %q does not contain %q", stderr, tc.want)
			}
		})
	}
}

// TestExitCodeNamesMatchTheContract pins this package's readable aliases
// to the numbers internal/cli publishes.
func TestExitCodeNamesMatchTheContract(t *testing.T) {
	cases := []struct {
		name  string
		got   int
		want  int
		label string
	}{
		{"valid", exitValid, cli.ExitPass, "ExitPass"},
		{"pass", exitPass, cli.ExitPass, "ExitPass"},
		{"valid with damage", exitValidWithDamage, cli.ExitFail, "ExitFail"},
		{"fail", exitFail, cli.ExitFail, "ExitFail"},
		{"invalid", exitInvalid, cli.ExitError, "ExitError"},
		{"error", exitError, cli.ExitError, "ExitError"},
		{"usage", exitUsage, cli.ExitUsage, "ExitUsage"},
		{"evidence lost", exitEvidenceLost, cli.ExitEvidenceLost, "ExitEvidenceLost"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want cli.%s = %d", tc.name, tc.got, tc.label, tc.want)
		}
	}
}

// TestSandboxProviderResolvesEveryRegisteredID proves the published
// provider list is exactly what a drill config can select.
func TestSandboxProviderResolvesEveryRegisteredID(t *testing.T) {
	t.Setenv("PROBAVI_SSH_TARGET", "drills@example.invalid")
	for _, id := range providerIDs(t) {
		if _, err := sandboxProvider(id, nil, nil); err != nil {
			t.Errorf("registered provider %q does not resolve: %v", id, err)
		}
	}
	_, err := sandboxProvider("no-such-provider", nil, nil)
	if err == nil {
		t.Fatal("an unregistered provider resolved")
	}
	for _, id := range providerIDs(t) {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("diagnostic %q does not offer %q", err, id)
		}
	}
}

func providerIDs(t *testing.T) []string {
	t.Helper()
	ids := registry.IDs()
	if len(ids) == 0 {
		t.Fatal("the sandbox registry is empty")
	}
	return ids
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
