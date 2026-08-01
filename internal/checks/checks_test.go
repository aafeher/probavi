package checks

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aafeher/probavi/internal/config"
	"github.com/aafeher/probavi/internal/sandbox"
)

// testRunner mirrors the postgres adapter's probe-declared template.
var testRunner = Runner{
	Argv: []string{"psql", "-U", "{{user}}", "-d", "{{database}}", "-tA", "-c", "{{sql}}"},
	Env:  map[string]string{"PGPASSWORD": "{{password}}"},
}

// fakeExec scripts sql_runner executions and records every request.
type fakeExec struct {
	t        *testing.T
	requests []sandbox.ExecRequest
	respond  func(sql string) *sandbox.ExecResult
	err      error
}

func (f *fakeExec) Exec(_ context.Context, req sandbox.ExecRequest) (*sandbox.ExecResult, error) {
	f.t.Helper()
	f.requests = append(f.requests, req)
	if f.err != nil {
		return nil, f.err
	}
	return f.respond(req.Argv[len(req.Argv)-1]), nil
}

func (f *fakeExec) lastSQL() string {
	f.t.Helper()
	if len(f.requests) == 0 {
		f.t.Fatal("no sql_runner execution happened")
	}
	argv := f.requests[len(f.requests)-1].Argv
	return argv[len(argv)-1]
}

func value(v string) func(string) *sandbox.ExecResult {
	return func(string) *sandbox.ExecResult {
		return &sandbox.ExecResult{ExitCode: 0, Stdout: []byte(v + "\n")}
	}
}

func queryFailure(stderr string) func(string) *sandbox.ExecResult {
	return func(string) *sandbox.ExecResult {
		return &sandbox.ExecResult{ExitCode: 1, Stderr: []byte(stderr)}
	}
}

func testDeps(exec *fakeExec) Deps {
	return Deps{
		Exec: exec,
		Healthcheck: func(context.Context) (bool, string, error) {
			return true, "accepting queries", nil
		},
		Runner: testRunner,
		Target: Target{User: "u", Database: "d", Password: "s3cret"},
		Now:    func() time.Time { return time.Date(2026, 7, 31, 2, 0, 0, 0, time.UTC) },
	}
}

func runSingle(t *testing.T, c config.Check, exec *fakeExec) Result {
	t.Helper()
	results, err := Run(context.Background(), []config.Check{c}, testDeps(exec))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	return results[0]
}

func i64(n int64) *int64 { return &n }

func TestRenderRunner(t *testing.T) {
	argv, env, err := renderRunner(testRunner, Target{User: "u", Database: "d", Password: "pw"}, "SELECT 1")
	if err != nil {
		t.Fatalf("renderRunner: %v", err)
	}
	if got := strings.Join(argv, " "); got != "psql -U u -d d -tA -c SELECT 1" {
		t.Errorf("argv = %q", got)
	}
	if env["PGPASSWORD"] != "pw" {
		t.Errorf("env = %v — {{password}} must resolve in env values", env)
	}

	if _, _, err := renderRunner(Runner{Argv: []string{"tool", "{{password}}"}}, Target{}, "x"); err == nil {
		t.Error("renderRunner must reject {{password}} in argv — it would leak into process listings")
	}
	if _, _, err := renderRunner(Runner{}, Target{}, "x"); err == nil {
		t.Error("renderRunner must reject an empty template")
	}
}

func TestServiceHealthy(t *testing.T) {
	c := config.Check{Builtin: "service_healthy"}

	res := runSingle(t, c, &fakeExec{t: t})
	if !res.OK || res.Name != "service_healthy" || res.Detail != "accepting queries" {
		t.Errorf("result = %+v", res)
	}

	deps := testDeps(&fakeExec{t: t})
	deps.Healthcheck = func(context.Context) (bool, string, error) { return false, "psql exited 2", nil }
	results, err := Run(context.Background(), []config.Check{c}, deps)
	if err != nil || results[0].OK {
		t.Errorf("unhealthy: results=%+v err=%v", results, err)
	}

	deps.Healthcheck = func(context.Context) (bool, string, error) { return false, "", errors.New("adapter crashed") }
	if _, err := Run(context.Background(), []config.Check{c}, deps); err == nil {
		t.Error("healthcheck infrastructure failure must abort the run")
	}
}

func TestTableExists(t *testing.T) {
	t.Run("exists", func(t *testing.T) {
		exec := &fakeExec{t: t, respond: value("0")}
		res := runSingle(t, config.Check{Builtin: "table_exists", Table: "orders"}, exec)
		if !res.OK || res.Name != "table_exists:orders" || res.Detail != "table exists" {
			t.Errorf("result = %+v", res)
		}
		if exec.lastSQL() != `SELECT count(*) FROM "orders" WHERE 1=0` {
			t.Errorf("sql = %q", exec.lastSQL())
		}
	})
	t.Run("schema qualified", func(t *testing.T) {
		exec := &fakeExec{t: t, respond: value("0")}
		runSingle(t, config.Check{Builtin: "table_exists", Table: "sales.orders"}, exec)
		if exec.lastSQL() != `SELECT count(*) FROM "sales"."orders" WHERE 1=0` {
			t.Errorf("sql = %q", exec.lastSQL())
		}
	})
	t.Run("missing table is a verdict", func(t *testing.T) {
		exec := &fakeExec{t: t, respond: queryFailure(`ERROR: relation "orders" does not exist` + "\nLINE 1: ...")}
		res := runSingle(t, config.Check{Builtin: "table_exists", Table: "orders"}, exec)
		if res.OK || !strings.Contains(res.Detail, "relation 'orders' does not exist") {
			t.Errorf("result = %+v — stderr must be single-line and quote-sanitized", res)
		}
	})
	t.Run("injection attempt aborts the run", func(t *testing.T) {
		poisoned := []config.Check{
			{Builtin: "table_exists", Table: `orders"; DROP TABLE x; --`},
			{Builtin: "row_count", Table: "orders; --", Min: i64(1)},
			{Builtin: "freshness", Table: "bad name", Column: "created_at", MaxAge: config.Duration(time.Hour)},
			{Builtin: "freshness", Table: "orders", Column: `c"ol`, MaxAge: config.Duration(time.Hour)},
		}
		for _, c := range poisoned {
			exec := &fakeExec{t: t, respond: value("0")}
			_, err := Run(context.Background(), []config.Check{c}, testDeps(exec))
			if err == nil || len(exec.requests) != 0 {
				t.Errorf("check %+v: err=%v requests=%d — poisoned identifiers must never reach the engine", c, err, len(exec.requests))
			}
		}
	})
}

func TestRunDefaults(t *testing.T) {
	// Nil Now falls back to time.Now; an impossible check shape (guarded
	// by config validation) is an infrastructure error, not a panic.
	deps := testDeps(&fakeExec{t: t, respond: value("0")})
	deps.Now = nil
	if _, err := Run(context.Background(), []config.Check{{Builtin: "table_exists", Table: "t"}}, deps); err != nil {
		t.Errorf("Run with nil Now: %v", err)
	}
	if _, err := Run(context.Background(), []config.Check{{}}, deps); err == nil {
		t.Error("empty check shape must be an error")
	}
}

func TestBrokenRunnerTemplateAbortsRun(t *testing.T) {
	exec := &fakeExec{t: t, respond: value("1")}
	deps := testDeps(exec)
	deps.Runner = Runner{Argv: []string{"tool", "{{password}}"}}
	_, err := Run(context.Background(), []config.Check{{SQL: "SELECT 1", Expect: config.ScalarFromString("1")}}, deps)
	if err == nil || len(exec.requests) != 0 {
		t.Errorf("err=%v requests=%d — a template leaking secrets into argv must never execute", err, len(exec.requests))
	}
}

func TestRowCount(t *testing.T) {
	base := config.Check{Builtin: "row_count", Table: "orders"}
	tests := []struct {
		name       string
		min, max   *int64
		output     func(string) *sandbox.ExecResult
		wantOK     bool
		wantDetail string
	}{
		{"within min", i64(100), nil, value("1000"), true, "1000 rows (min 100)"},
		{"within max", nil, i64(2000), value("1000"), true, "1000 rows (max 2000)"},
		{"within both", i64(100), i64(2000), value("1000"), true, "1000 rows (min 100, max 2000)"},
		{"below min", i64(1001), nil, value("1000"), false, "1000 rows (min 1001)"},
		{"above max", nil, i64(999), value("1000"), false, "1000 rows (max 999)"},
		{"garbage output", i64(1), nil, value("banana"), false, "unexpected output"},
		{"query failure", i64(1), nil, queryFailure("ERROR: permission denied"), false, "count query failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := base
			c.Min, c.Max = tt.min, tt.max
			exec := &fakeExec{t: t, respond: tt.output}
			res := runSingle(t, c, exec)
			if res.OK != tt.wantOK || !strings.Contains(res.Detail, tt.wantDetail) {
				t.Errorf("result = %+v, want ok=%v detail~%q", res, tt.wantOK, tt.wantDetail)
			}
			if exec.lastSQL() != `SELECT count(*) FROM "orders"` {
				t.Errorf("sql = %q", exec.lastSQL())
			}
		})
	}
}

func TestFreshness(t *testing.T) {
	base := config.Check{Builtin: "freshness", Table: "orders", Column: "created_at"}
	maxAge := config.Check{}
	_ = maxAge
	withAge := func(d time.Duration) config.Check {
		c := base
		c.MaxAge = config.Duration(d)
		return c
	}
	tests := []struct {
		name       string
		check      config.Check
		output     func(string) *sandbox.ExecResult
		wantOK     bool
		wantDetail string
	}{
		{"fresh with offset tz", withAge(2 * time.Hour), value("2026-07-31 01:00:00+00"), true, "newest row is 1h0m0s old (max_age 2h0m0s)"},
		{"fresh with colon tz and fraction", withAge(2 * time.Hour), value("2026-07-31 03:00:00.123+02:00"), true, "59m59s old"},
		{"stale", withAge(30 * time.Minute), value("2026-07-31 01:00:00+00"), false, "1h0m0s old (max_age 30m0s)"},
		{"naive timestamp treated as UTC", withAge(2 * time.Hour), value("2026-07-31 01:30:00"), true, "30m0s old"},
		{"future timestamp counts as fresh", withAge(time.Hour), value("2026-07-31 02:30:00+00"), true, "0s old"},
		{"empty table", withAge(time.Hour), value(""), false, "no rows or only NULL"},
		{"unparseable", withAge(time.Hour), value("yesterday-ish"), false, "unparseable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := &fakeExec{t: t, respond: tt.output}
			res := runSingle(t, tt.check, exec)
			if res.OK != tt.wantOK || !strings.Contains(res.Detail, tt.wantDetail) {
				t.Errorf("result = %+v, want ok=%v detail~%q", res, tt.wantOK, tt.wantDetail)
			}
			if res.Name != "freshness:orders.created_at" {
				t.Errorf("name = %q", res.Name)
			}
			if exec.lastSQL() != `SELECT max("created_at") FROM "orders"` {
				t.Errorf("sql = %q", exec.lastSQL())
			}
		})
	}
}

func TestSQLCheck(t *testing.T) {
	c := config.Check{Name: "no-negatives", SQL: "SELECT count(*) = 0 FROM orders WHERE total < 0",
		Expect: config.ScalarFromString("true")}

	t.Run("match", func(t *testing.T) {
		exec := &fakeExec{t: t, respond: value("true")}
		res := runSingle(t, c, exec)
		if !res.OK || res.Name != "sql:no-negatives" || res.Detail != "matched expectation" {
			t.Errorf("result = %+v", res)
		}
		if exec.lastSQL() != c.SQL {
			t.Errorf("sql = %q — custom SQL must pass through verbatim", exec.lastSQL())
		}
	})
	t.Run("mismatch never leaks the value", func(t *testing.T) {
		exec := &fakeExec{t: t, respond: value("secret-user-data")}
		res := runSingle(t, c, exec)
		if res.OK || strings.Contains(res.Detail, "secret-user-data") {
			t.Errorf("result = %+v — returned values must never enter evidence details", res)
		}
	})
	t.Run("query failure", func(t *testing.T) {
		exec := &fakeExec{t: t, respond: queryFailure("ERROR: syntax error")}
		res := runSingle(t, c, exec)
		if res.OK || !strings.Contains(res.Detail, "query failed") {
			t.Errorf("result = %+v", res)
		}
	})
	t.Run("unnamed uses index", func(t *testing.T) {
		unnamed := config.Check{SQL: "SELECT 1", Expect: config.ScalarFromString("1")}
		exec := &fakeExec{t: t, respond: value("1")}
		res := runSingle(t, unnamed, exec)
		if res.Name != "sql:0" {
			t.Errorf("name = %q", res.Name)
		}
	})
}

func TestQueryVerdictsAndInfraAborts(t *testing.T) {
	t.Run("row_count query failure is a verdict", func(t *testing.T) {
		res := runSingle(t, config.Check{Builtin: "row_count", Table: "orders", Min: i64(1)},
			&fakeExec{t: t, respond: queryFailure("ERROR: disk on fire")})
		if res.OK || !strings.Contains(res.Detail, "count query failed") {
			t.Errorf("result = %+v", res)
		}
	})
	t.Run("freshness query failure is a verdict", func(t *testing.T) {
		res := runSingle(t, config.Check{Builtin: "freshness", Table: "orders", Column: "created_at",
			MaxAge: config.Duration(time.Hour)}, &fakeExec{t: t, respond: queryFailure("ERROR: nope")})
		if res.OK || !strings.Contains(res.Detail, "freshness query failed") {
			t.Errorf("result = %+v", res)
		}
	})
	t.Run("infrastructure failure aborts every table builtin", func(t *testing.T) {
		for _, c := range []config.Check{
			{Builtin: "table_exists", Table: "t"},
			{Builtin: "row_count", Table: "t", Min: i64(1)},
			{Builtin: "freshness", Table: "t", Column: "c", MaxAge: config.Duration(time.Hour)},
		} {
			exec := &fakeExec{t: t, err: errors.New("sandbox died")}
			if _, err := Run(context.Background(), []config.Check{c}, testDeps(exec)); err == nil {
				t.Errorf("check %+v must abort on infrastructure failure", c)
			}
		}
	})
}

func TestRunAbortsOnInfrastructureFailure(t *testing.T) {
	list := []config.Check{
		{Builtin: "row_count", Table: "orders", Min: i64(1)},
		{SQL: "SELECT 1", Expect: config.ScalarFromString("1")},
	}
	exec := &fakeExec{t: t}
	exec.respond = func(string) *sandbox.ExecResult {
		// Fail at transport level from the second call on.
		exec.err = errors.New("sandbox died")
		return &sandbox.ExecResult{ExitCode: 0, Stdout: []byte("1\n")}
	}
	deps := testDeps(exec)
	results, err := Run(context.Background(), list, deps)
	if err == nil || !strings.Contains(err.Error(), "sandbox died") {
		t.Fatalf("err = %v, want transport failure", err)
	}
	if len(results) != 1 || !results[0].OK {
		t.Errorf("partial results = %+v, want the first verdict preserved", results)
	}
	if !strings.Contains(err.Error(), "sql:1") {
		t.Errorf("err = %v, want the failing check named", err)
	}
}

func TestQuoteIdent(t *testing.T) {
	valid := map[string]string{
		"orders":       `"orders"`,
		"sales.orders": `"sales"."orders"`,
		"_x1":          `"_x1"`,
	}
	for in, want := range valid {
		if got, err := quoteIdent(in); err != nil || got != want {
			t.Errorf("quoteIdent(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{`a"b`, "a;b", "a b", "a.b.c", "1abc", "", "a.", `x); DROP`} {
		if _, err := quoteIdent(in); err == nil {
			t.Errorf("quoteIdent(%q) succeeded, want rejection", in)
		}
	}
}

func TestDetailTruncation(t *testing.T) {
	long := strings.Repeat("e", 500)
	exec := &fakeExec{t: t, respond: queryFailure(long)}
	res := runSingle(t, config.Check{SQL: "SELECT 1", Expect: config.ScalarFromString("1")}, exec)
	if len(res.Detail) > maxDetailLen || !strings.HasSuffix(res.Detail, "...") {
		t.Errorf("detail length = %d (%q...), want truncated to %d", len(res.Detail), res.Detail[:40], maxDetailLen)
	}
}
