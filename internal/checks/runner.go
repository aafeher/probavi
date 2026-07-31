package checks

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aafeher/probavi/internal/sandbox"
)

// queryResult is one sql_runner execution: succeeded reflects the runner's
// exit code, value the trimmed stdout, errLine a sanitized first stderr
// line for failed queries.
type queryResult struct {
	succeeded bool
	value     string
	errLine   string
}

// query renders the sql_runner template and executes it in the sandbox.
func query(ctx context.Context, deps *Deps, sql string) (*queryResult, error) {
	argv, env, err := renderRunner(deps.Runner, deps.Target, sql)
	if err != nil {
		return nil, err
	}
	res, err := deps.Exec.Exec(ctx, sandbox.ExecRequest{Argv: argv, Env: env})
	if err != nil {
		return nil, fmt.Errorf("sql_runner exec: %w", err)
	}
	if res.ExitCode != 0 {
		return &queryResult{succeeded: false, errLine: sanitizeLine(res.Stderr)}, nil
	}
	return &queryResult{succeeded: true, value: strings.TrimSpace(string(res.Stdout))}, nil
}

// renderRunner substitutes the §6.1 placeholders: {{user}}, {{database}},
// {{sql}} in argv and env values; {{password}} in env values only — a
// password in argv would leak into process listings.
func renderRunner(r Runner, t Target, sql string) ([]string, map[string]string, error) {
	if len(r.Argv) == 0 {
		return nil, nil, fmt.Errorf("sql_runner template is empty — the adapter's probe did not declare one")
	}
	replace := func(s string) string {
		s = strings.ReplaceAll(s, "{{user}}", t.User)
		s = strings.ReplaceAll(s, "{{database}}", t.Database)
		return strings.ReplaceAll(s, "{{sql}}", sql)
	}
	argv := make([]string, len(r.Argv))
	for i, a := range r.Argv {
		if strings.Contains(a, "{{password}}") {
			return nil, nil, fmt.Errorf("sql_runner argv contains {{password}} — secrets belong in env values only")
		}
		argv[i] = replace(a)
	}
	var env map[string]string
	if len(r.Env) > 0 {
		env = make(map[string]string, len(r.Env))
		for k, v := range r.Env {
			env[k] = strings.ReplaceAll(replace(v), "{{password}}", t.Password)
		}
	}
	return argv, env, nil
}

// sanitizeLine reduces runner stderr to a single quote-free line that is
// safe for evidence details: engine error messages, never row data.
func sanitizeLine(stderr []byte) string {
	s := strings.TrimSpace(string(stderr))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.ReplaceAll(s, `"`, "'")
}

// timestampFormats covers the textual forms engines commonly print for
// max(timestamp) through their CLI runners. Naive timestamps (no zone) are
// interpreted as UTC — documented behavior for freshness checks.
var timestampFormats = []string{
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999-07",
	"2006-01-02 15:04:05.999999999",
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999",
}

func parseTimestamp(s string) (time.Time, error) {
	for _, layout := range timestampFormats {
		if ts, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return ts, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized timestamp format")
}
