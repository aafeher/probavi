package sandbox

import (
	"fmt"
	"sort"
	"strings"
)

// EnvPreludeScript returns a POSIX shell program that reads exactly n
// "NAME=value" lines from stdin, exports each, and then execs the command
// it is given as arguments. Whatever follows those n lines on stdin is the
// command's own stdin, untouched.
//
// It exists because two providers have no out-of-band way to set the
// environment of a command they run: kubectl exec has no environment flag,
// and ssh's SendEnv depends on the target server's AcceptEnv. Putting the
// values on the command line instead — `env NAME=value cmd` — publishes
// every secret a check needs to the process list, on the drill host and
// again on the target. internal/checks refuses {{password}} in an
// sql_runner argv for exactly that reason; a provider must not undo it.
//
// A shell reading a pipe consumes one byte at a time for `read`, so the
// prelude cannot swallow bytes meant for the command. `sh` is already a
// requirement of these providers (put_file needs it too).
func EnvPreludeScript(n int) string {
	return fmt.Sprintf(
		`n=%d; while [ "$n" -gt 0 ]; do IFS= read -r probavi_env || exit 125; `+
			`export "$probavi_env"; n=$((n-1)); done; unset probavi_env; exec "$@"`, n)
}

// EnvPreludeLines renders env as the newline-terminated block
// EnvPreludeScript expects, sorted by name so a command line is
// reproducible.
//
// A value containing a newline cannot be expressed in a line protocol.
// Rather than silently truncating a credential — or worse, exporting its
// tail as another variable — such a value is rejected: a caller that needs
// one has hit a real limitation and deserves to be told.
func EnvPreludeLines(env map[string]string) (string, error) {
	names := make([]string, 0, len(env))
	for k := range env {
		names = append(names, k)
	}
	sort.Strings(names)

	var b strings.Builder
	for _, k := range names {
		if strings.ContainsAny(env[k], "\n\r") {
			return "", fmt.Errorf("%w: environment value for %s contains a newline and cannot be passed to the sandbox", ErrInvalidParams, k)
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(env[k])
		b.WriteByte('\n')
	}
	return b.String(), nil
}
