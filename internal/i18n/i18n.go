// Package i18n localizes user-facing CLI output (docs/i18n.md). The
// catalog key is the English text itself, so a missing translation
// falls back to the canonical English structurally; machine contracts
// (evidence, JSON outputs, protocol, logs) are never translated.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
)

//go:embed locales/*.json
var locales embed.FS

// SourceLocale is the canonical source language of the project. English
// text is written inline as the catalog key, so English has no catalog
// file and Locales() cannot report it (docs/i18n.md §6).
const SourceLocale = "en"

// TranslationScope states the translation boundary of docs/i18n.md §1 in
// one sentence, for the generated capabilities manifest: a consumer that
// advertises supported languages must not imply that machine contracts are
// localized too.
const TranslationScope = "CLI usage text and diagnostics only. Evidence records, machine-readable JSON outputs, structured logs, the adapter protocol, notification payloads, and configuration keys are never translated."

// envChain is the docs/i18n.md §2 selection order: a Probavi-specific
// override first, then the POSIX locale variables.
var envChain = []string{"PROBAVI_LANG", "LC_ALL", "LC_MESSAGES", "LANG"}

// T translates user-facing text for one locale. The zero-value-like
// English translator has an empty catalog: every lookup falls through
// to the canonical English input.
type T struct {
	catalog map[string]string
}

// English returns the identity translator for the canonical source
// language.
func English() *T {
	return &T{}
}

// New loads the embedded catalog for a normalized locale tag. An empty
// or unknown tag yields the English translator — a wrong LANG must
// never break a cron drill.
func New(tag string) (*T, error) {
	if tag == "" {
		return English(), nil
	}
	raw, err := locales.ReadFile(path.Join("locales", tag+".json"))
	if err != nil {
		return English(), nil
	}
	catalog, err := parseCatalog(tag, raw)
	if err != nil {
		return nil, err
	}
	return &T{catalog: catalog}, nil
}

func parseCatalog(tag string, raw []byte) (map[string]string, error) {
	catalog := map[string]string{}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return nil, fmt.Errorf("parse embedded catalog %s.json: %w", tag, err)
	}
	return catalog, nil
}

// Detect resolves the locale tag from the environment (docs/i18n.md §2).
// The lookup function is injected so tests stay deterministic regardless
// of the machine's locale.
func Detect(getenv func(string) string) string {
	for _, key := range envChain {
		if v := getenv(key); v != "" {
			return Normalize(v)
		}
	}
	return ""
}

// Normalize reduces a POSIX locale string to a catalog tag: lowercase
// language subtag with codeset, territory, and modifier stripped
// ("hu_HU.UTF-8" → "hu"). "C", "POSIX", and anything malformed mean
// English ("").
func Normalize(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, sep := range []string{".", "@", "_", "-"} {
		if i := strings.Index(v, sep); i >= 0 {
			v = v[:i]
		}
	}
	if v == "c" || v == "posix" || len(v) < 2 || len(v) > 3 {
		return ""
	}
	for _, r := range v {
		if r < 'a' || r > 'z' {
			return ""
		}
	}
	return v
}

// Sprintf formats like fmt.Sprintf after translating the format string.
// The English format is the catalog key; a miss prints the original.
func (t *T) Sprintf(format string, a ...any) string {
	if translated, ok := t.catalog[format]; ok {
		format = translated
	}
	if len(a) == 0 {
		// Messages without arguments (usage text) must never be mangled
		// by stray formatting-verb interpretation.
		return format
	}
	return fmt.Sprintf(format, a...)
}

// Fprintf writes a translated diagnostic. Like the fmt.Fprint* family in
// this codebase (see .golangci.yml), it is used solely for user-facing
// diagnostics where a failed write has nowhere left to be reported, so
// it deliberately returns nothing.
func (t *T) Fprintf(w io.Writer, format string, a ...any) {
	fmt.Fprint(w, t.Sprintf(format, a...))
}

// Catalog returns the translation table of an embedded locale, for the
// completeness and parity gates (docs/i18n.md §4).
func Catalog(tag string) (map[string]string, error) {
	raw, err := locales.ReadFile(path.Join("locales", tag+".json"))
	if err != nil {
		return nil, fmt.Errorf("read embedded catalog %s.json: %w", tag, err)
	}
	return parseCatalog(tag, raw)
}

// Available lists every locale this binary speaks, sorted: the embedded
// catalogs plus the canonical source language, which needs no catalog.
// It is what the generated capabilities manifest reports.
func Available() ([]string, error) {
	tags, err := Locales()
	if err != nil {
		return nil, err
	}
	tags = append(tags, SourceLocale)
	sort.Strings(tags)
	return tags, nil
}

// Locales lists the embedded catalog tags, sorted, so tests gate every
// shipped language without maintaining a second registry.
func Locales() ([]string, error) {
	entries, err := fs.ReadDir(locales, "locales")
	if err != nil {
		return nil, fmt.Errorf("list embedded catalogs: %w", err)
	}
	tags := make([]string, 0, len(entries))
	for _, e := range entries {
		tags = append(tags, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(tags)
	return tags, nil
}
