// Package config loads and validates Probavi drill configurations (the
// YAML "drill as code" files). Validation is strict — unknown fields,
// duplicate keys, and misconfigured checks are errors — and reports every
// problem it finds in one pass, not just the first.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/goccy/go-yaml"
)

var (
	adapterNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	envNamePattern     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Config is one drill definition: restore one backup into one sandbox and
// validate it. Hash and Path are filled by Load, never by YAML.
type Config struct {
	Target   Target   `yaml:"target"`
	Sandbox  Sandbox  `yaml:"sandbox"`
	Checks   []Check  `yaml:"checks"`
	Evidence Evidence `yaml:"evidence"`
	Metrics  *Metrics `yaml:"metrics"`
	Notify   *Notify  `yaml:"notify"`

	// Hash is "sha256:<hex>" over the exact file bytes as read — the value
	// evidence records carry as drill.config_hash.
	Hash string `yaml:"-"`
	// Path is the config file path Load read, for error messages.
	Path string `yaml:"-"`
}

// Target names the database under drill and the backup source to restore.
type Target struct {
	Name    string            `yaml:"name"`
	Adapter string            `yaml:"adapter"`
	Source  Source            `yaml:"source"`
	Options map[string]string `yaml:"options"`
	PITR    *PITR             `yaml:"pitr"`
}

// PITR requests point-in-time recovery. Exactly one of TargetTime (an
// absolute RFC 3339 instant) or TargetAge (a relative age the core resolves
// to now−age at drill start, so scheduled drills never go stale) must be
// set. Time is the only engine-neutral recovery target the core schema
// knows (AGENTS.md §6, decided 2026-08-01); engine-specific coordinates
// belong in source.params.
type PITR struct {
	TargetTime string   `yaml:"target_time"`
	TargetAge  Duration `yaml:"target_age"`

	// parsedTime caches the validated TargetTime so Resolve needs no
	// second, error-swallowing parse.
	parsedTime time.Time
}

// Source describes the backup source; Kind is adapter-defined and Params
// pass through the core uninterpreted (adapter protocol §6.2).
type Source struct {
	Kind          string            `yaml:"kind"`
	Path          string            `yaml:"path"`
	Params        map[string]string `yaml:"params"`
	CredentialEnv []string          `yaml:"credential_env"`
}

// Sandbox selects the disposable runtime; Params are provider-specific and
// pass through the core uninterpreted.
type Sandbox struct {
	Provider string            `yaml:"provider"`
	Params   map[string]string `yaml:"params"`
	Timeout  Duration          `yaml:"timeout"`
}

// Check is one validation to run against the restored database: exactly one
// of Builtin or SQL must be set.
type Check struct {
	Builtin string   `yaml:"builtin"`
	Table   string   `yaml:"table"`
	Column  string   `yaml:"column"`
	Min     *int64   `yaml:"min"`
	Max     *int64   `yaml:"max"`
	MaxAge  Duration `yaml:"max_age"`

	Name   string `yaml:"name"`
	SQL    string `yaml:"sql"`
	Expect Scalar `yaml:"expect"`
}

// Evidence configures where records are appended and which key signs them.
type Evidence struct {
	Path    string `yaml:"path"`
	SignKey string `yaml:"sign_key"`
}

// Metrics configures optional metrics exposition.
type Metrics struct {
	PrometheusTextfile string `yaml:"prometheus_textfile"`
}

// Notify configures optional drill-completion notifications
// (docs/notifications.md).
type Notify struct {
	Webhooks []NotifyWebhook `yaml:"webhooks"`
}

// NotifyWebhook is one webhook destination. Exactly one of URL (a
// non-secret literal) or URLEnv (the name of an environment variable
// holding the URL) must be set — token-bearing URLs are credentials and
// belong in the environment, never in config values.
type NotifyWebhook struct {
	URL       string   `yaml:"url"`
	URLEnv    string   `yaml:"url_env"`
	SecretEnv string   `yaml:"secret_env"`
	On        []string `yaml:"on"`
}

// notifyOutcomes are the outcome names a webhook's on filter may list
// (docs/notifications.md §2); they mirror the evidence outcome values.
var notifyOutcomes = map[string]bool{
	"pass": true, "fail": true, "error": true, "cancelled": true,
}

// Load reads, parses, and validates a drill configuration. The returned
// error is human-oriented: syntax errors carry line/column context and an
// annotated source excerpt; validation reports every problem found.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	dec := yaml.NewDecoder(bytes.NewReader(raw), yaml.Strict())
	if err := dec.Decode(cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("config %s is empty", path)
		}
		return nil, fmt.Errorf("parse config %s:\n%s", path, yaml.FormatError(err, false, true))
	}
	sum := sha256.Sum256(raw)
	cfg.Hash = "sha256:" + hex.EncodeToString(sum[:])
	cfg.Path = path
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s:\n%w", path, err)
	}
	return cfg, nil
}

// problems collects validation errors so a config author sees everything
// wrong at once instead of fixing one field per run.
type problems []error

func (p *problems) add(format string, a ...any) {
	*p = append(*p, fmt.Errorf(format, a...))
}

func (c *Config) validate() error {
	var p problems
	c.Target.validate(&p)
	c.Sandbox.validate(&p)
	c.validateChecks(&p)
	c.Evidence.validate(&p)
	if c.Metrics != nil && c.Metrics.PrometheusTextfile == "" {
		p.add("metrics.prometheus_textfile is required when the metrics section is present")
	}
	if c.Notify != nil {
		c.Notify.validate(&p)
	}
	return errors.Join(p...)
}

func (n *Notify) validate(p *problems) {
	if len(n.Webhooks) == 0 {
		p.add("notify.webhooks must list at least one webhook when the notify section is present")
		return
	}
	for i := range n.Webhooks {
		n.Webhooks[i].validate(p, i)
	}
}

func (w *NotifyWebhook) validate(p *problems, i int) {
	at := fmt.Sprintf("notify.webhooks[%d]", i)
	switch {
	case w.URL != "" && w.URLEnv != "":
		p.add("%s: exactly one of url or url_env must be set, not both", at)
	case w.URLEnv != "":
		if !envNamePattern.MatchString(w.URLEnv) {
			p.add("%s: url_env %q is not a valid environment variable name", at, w.URLEnv)
		}
	case w.URL != "":
		if u, err := url.Parse(w.URL); err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			p.add("%s: url must be an absolute http(s) URL", at)
		}
	default:
		p.add("%s: exactly one of url or url_env must be set (token-bearing URLs belong in url_env)", at)
	}
	if w.SecretEnv != "" && !envNamePattern.MatchString(w.SecretEnv) {
		p.add("%s: secret_env %q is not a valid environment variable name", at, w.SecretEnv)
	}
	seen := make(map[string]bool, len(w.On))
	for _, o := range w.On {
		switch {
		case !notifyOutcomes[o]:
			p.add("%s: unknown outcome %q in on (supported: pass, fail, error, cancelled)", at, o)
		case seen[o]:
			p.add("%s: duplicate outcome %q in on", at, o)
		}
		seen[o] = true
	}
}

func (t *Target) validate(p *problems) {
	if t.Name == "" {
		p.add("target.name is required — it identifies the drill in evidence records")
	}
	switch {
	case t.Adapter == "":
		p.add(`target.adapter is required (e.g. "postgres")`)
	case !adapterNamePattern.MatchString(t.Adapter):
		p.add("target.adapter %q must be lowercase letters, digits, and hyphens (it resolves to the executable probavi-adapter-%s)", t.Adapter, t.Adapter)
	}
	if t.Source.Kind == "" {
		p.add(`target.source.kind is required (adapter-defined, e.g. "pgdump" — see the adapter's probe output)`)
	}
	for _, name := range t.Source.CredentialEnv {
		if !envNamePattern.MatchString(name) {
			p.add("target.source.credential_env entry %q is not a valid environment variable name", name)
		}
	}
	if t.PITR != nil {
		t.PITR.validate(p)
	}
}

func (pt *PITR) validate(p *problems) {
	hasTime := pt.TargetTime != ""
	hasAge := pt.TargetAge != 0
	if hasTime == hasAge {
		p.add(`target.pitr requires exactly one of target_time (RFC 3339, e.g. "2026-07-30T14:32:00Z") or target_age (e.g. "24h")`)
		return
	}
	if hasTime {
		ts, err := time.Parse(time.RFC3339, pt.TargetTime)
		if err != nil {
			p.add("target.pitr.target_time %q is not an RFC 3339 timestamp", pt.TargetTime)
			return
		}
		pt.parsedTime = ts
	}
}

// Resolve returns the absolute recovery target: the validated target_time,
// or now minus target_age. Only meaningful on a Config returned by Load.
func (pt *PITR) Resolve(now time.Time) time.Time {
	if !pt.parsedTime.IsZero() {
		return pt.parsedTime
	}
	return now.Add(-pt.TargetAge.Std())
}

func (s *Sandbox) validate(p *problems) {
	if s.Provider == "" {
		p.add(`sandbox.provider is required (e.g. "docker")`)
	}
	if s.Timeout == 0 {
		p.add(`sandbox.timeout is required — every drill needs a hard wall-clock limit (e.g. "30m")`)
	}
}

func (c *Config) validateChecks(p *problems) {
	if len(c.Checks) == 0 {
		p.add(`at least one check is required (start with "- builtin: service_healthy")`)
		return
	}
	for i := range c.Checks {
		c.Checks[i].validate(p, i)
	}
}

func (e *Evidence) validate(p *problems) {
	if e.Path == "" {
		p.add("evidence.path is required — a drill that leaves no evidence record proves nothing")
	}
	if e.SignKey == "" {
		p.add(`evidence.sign_key is required (generate one with "probavi evidence keygen")`)
	}
}

func (ch *Check) validate(p *problems, i int) {
	at := fmt.Sprintf("checks[%d]", i)
	switch {
	case ch.Builtin != "" && ch.SQL != "":
		p.add("%s: exactly one of builtin or sql must be set, not both", at)
	case ch.Builtin != "":
		ch.validateBuiltin(p, at)
	case ch.SQL != "":
		ch.validateSQL(p, at)
	default:
		p.add("%s: exactly one of builtin or sql must be set", at)
	}
}

func (ch *Check) validateBuiltin(p *problems, at string) {
	if ch.Expect.IsSet() {
		p.add("%s: expect is only valid for sql checks", at)
	}
	if ch.Name != "" {
		p.add("%s: name is only valid for sql checks (builtin checks are named automatically)", at)
	}
	switch ch.Builtin {
	case "service_healthy":
		ch.forbid(p, at, fields{table: true, column: true, minmax: true, maxAge: true})
	case "table_exists":
		ch.requireTable(p, at)
		ch.forbid(p, at, fields{column: true, minmax: true, maxAge: true})
	case "row_count":
		ch.validateRowCount(p, at)
	case "freshness":
		ch.validateFreshness(p, at)
	default:
		p.add("%s: unknown builtin %q (supported: service_healthy, table_exists, row_count, freshness)", at, ch.Builtin)
	}
}

func (ch *Check) validateRowCount(p *problems, at string) {
	ch.requireTable(p, at)
	ch.forbid(p, at, fields{column: true, maxAge: true})
	switch {
	case ch.Min == nil && ch.Max == nil:
		p.add("%s: row_count requires min, max, or both", at)
	case ch.Min != nil && *ch.Min < 0, ch.Max != nil && *ch.Max < 0:
		p.add("%s: row_count bounds must not be negative", at)
	case ch.Min != nil && ch.Max != nil && *ch.Min > *ch.Max:
		p.add("%s: row_count min (%d) exceeds max (%d)", at, *ch.Min, *ch.Max)
	}
}

func (ch *Check) validateFreshness(p *problems, at string) {
	ch.requireTable(p, at)
	ch.forbid(p, at, fields{minmax: true})
	if ch.Column == "" {
		p.add("%s: freshness requires column (the timestamp column to inspect)", at)
	}
	if ch.MaxAge == 0 {
		p.add(`%s: freshness requires max_age (e.g. "24h")`, at)
	}
}

func (ch *Check) validateSQL(p *problems, at string) {
	if !ch.Expect.IsSet() {
		p.add("%s: sql checks require expect — the exact value the query must return", at)
	}
	ch.forbid(p, at, fields{table: true, column: true, minmax: true, maxAge: true})
}

// fields marks which check parameters are not applicable in a context.
type fields struct {
	table, column, minmax, maxAge bool
}

func (ch *Check) requireTable(p *problems, at string) {
	if ch.Table == "" {
		p.add("%s: %s requires table", at, ch.Builtin)
	}
}

func (ch *Check) forbid(p *problems, at string, f fields) {
	kind := ch.Builtin
	if kind == "" {
		kind = "sql checks"
	}
	if f.table && ch.Table != "" {
		p.add("%s: table is not valid for %s", at, kind)
	}
	if f.column && ch.Column != "" {
		p.add("%s: column is not valid for %s", at, kind)
	}
	if f.minmax && (ch.Min != nil || ch.Max != nil) {
		p.add("%s: min/max are not valid for %s", at, kind)
	}
	if f.maxAge && ch.MaxAge != 0 {
		p.add("%s: max_age is not valid for %s", at, kind)
	}
}
