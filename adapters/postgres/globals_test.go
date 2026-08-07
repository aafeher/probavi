package main

import (
	"strings"
	"testing"
)

// TestGlobalsFailureClassification is the heart of the with-globals kind.
//
// psql runs the script with ON_ERROR_STOP off, so its exit code says
// nothing: every statement is attempted and the verdict comes from these
// diagnostics alone. Exactly one failure is tolerated — the bootstrap
// superuser pg_dumpall always re-creates — and widening that tolerance by
// one line would turn a half-loaded set of roles into a "pass".
func TestGlobalsFailureClassification(t *testing.T) {
	const (
		collision = `psql:/scratch/probavi-globals.sql:20: ERROR:  role "postgres" already exists`
		notice    = `psql:/scratch/probavi-globals.sql:8: NOTICE:  role "app_ro" is a member of role "app_rw"`
		warning   = `psql:/scratch/probavi-globals.sql:9: WARNING:  no privileges were granted`
	)

	tests := []struct {
		name   string
		user   string
		stderr string
		want   string // substring the verdict must name; "" means acceptable
	}{
		{"clean load", "postgres", "", ""},
		{"only the bootstrap-role collision", "postgres", collision, ""},
		{"collision under a non-default superuser", "pgadmin",
			`psql:g.sql:20: ERROR:  role "pgadmin" already exists`, ""},
		{"notices and warnings are not failures", "postgres",
			notice + "\n" + warning + "\n" + collision, ""},
		{"a second role collision is a real failure", "postgres",
			collision + "\n" + `psql:g.sql:24: ERROR:  role "app_rw" already exists`,
			`role 'app_rw' already exists`},
		{"a collision for another role alone is a failure", "postgres",
			`psql:g.sql:24: ERROR:  role "app_rw" already exists`,
			`role 'app_rw' already exists`},
		{"permission denied", "postgres",
			collision + "\n" + `psql:g.sql:31: ERROR:  permission denied to create role`,
			"permission denied"},
		{"syntax error", "postgres",
			`psql:g.sql:3: ERROR:  syntax error at or near CREATE`, "syntax error"},
		{"server-level failure", "postgres",
			`psql:g.sql:3: FATAL:  terminating connection due to administrator command`,
			"terminating connection"},
		{"client-level failure", "postgres",
			`psql: error: could not open file "/scratch/probavi-globals.sql": No such file or directory`,
			"could not open file"},
		{"the tolerated line must be a role collision, not any message naming it", "postgres",
			`psql:g.sql:20: ERROR:  cannot drop role "postgres" already exists elsewhere`,
			"cannot drop role"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := globalsFailure([]byte(tt.stderr), tt.user)
			switch {
			case tt.want == "" && got != "":
				t.Errorf("globalsFailure = %q, want the load accepted", got)
			case tt.want != "" && !strings.Contains(got, tt.want):
				t.Errorf("globalsFailure = %q, want a verdict naming %q", got, tt.want)
			}
		})
	}
}

// TestGlobalsFailureIsProtocolSafe keeps a classified failure embeddable.
func TestGlobalsFailureIsProtocolSafe(t *testing.T) {
	got := globalsFailure([]byte(`psql:g.sql:3: ERROR:  syntax error at or near "CREATE"`), "postgres")
	if strings.Contains(got, `"`) {
		t.Errorf("verdict %q carries double quotes — it crosses the protocol as a JSON string", got)
	}
}

// TestScrubSecrets closes the path from a backup's credentials into a
// signed evidence record.
//
// A globals script carries every role's password verifier, and PostgreSQL
// quotes offending source text back in syntax errors — so without this,
// one malformed line in a customer's globals file would publish their
// password hashes into an append-only, signed, auditor-facing log.
func TestScrubSecrets(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		leak  string
		clean bool
	}{
		{"scram verifier in a quoted statement",
			`ERROR:  syntax error at or near ALTER ROLE app_rw WITH LOGIN PASSWORD 'SCRAM-SHA-256$4096:JgQ9NIdpfLS0JhSO==$abc:def'`,
			"SCRAM-SHA-256", false},
		{"lowercase keyword",
			`ERROR:  syntax error at CREATE ROLE x password 'plaintext'`, "plaintext", false},
		{"multiple literals on one line",
			`ERROR:  a PASSWORD 'one' and PASSWORD 'two'`, "one", false},
		{"unrelated text is untouched",
			`ERROR:  role "postgres" already exists`, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scrubSecrets(tt.in)
			if tt.clean {
				if got != tt.in {
					t.Errorf("scrubSecrets rewrote %q to %q", tt.in, got)
				}
				return
			}
			if strings.Contains(got, tt.leak) {
				t.Errorf("scrubSecrets left %q in %q", tt.leak, got)
			}
			if !strings.Contains(got, "[redacted]") {
				t.Errorf("scrubSecrets = %q, want the literal replaced by a marker", got)
			}
		})
	}

	t.Run("firstLine scrubs, so every protocol message is covered", func(t *testing.T) {
		got := firstLine([]byte(`pg_restore: error: ALTER ROLE x PASSWORD 'secret-hash' failed`))
		if strings.Contains(got, "secret-hash") {
			t.Errorf("firstLine = %q — the scrub must sit on the shared path, not one caller", got)
		}
	})
}
