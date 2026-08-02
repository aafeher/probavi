package i18n

import (
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	tests := []struct{ in, want string }{
		{"hu", "hu"},
		{"hu_HU", "hu"},
		{"hu_HU.UTF-8", "hu"},
		{"HU_hu.utf8@euro", "hu"},
		{"de-DE", "de"},
		{"gsw", "gsw"},
		{"C", ""},
		{"C.UTF-8", ""},
		{"POSIX", ""},
		{"", ""},
		{"x", ""},
		{"toolong", ""},
		{"h1", ""},
		{" hu ", "hu"},
	}
	for _, tt := range tests {
		if got := Normalize(tt.in); got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDetectOrder(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"empty environment", nil, ""},
		{"LANG alone", map[string]string{"LANG": "hu_HU.UTF-8"}, "hu"},
		{"LC_MESSAGES beats LANG", map[string]string{"LANG": "de_DE", "LC_MESSAGES": "hu_HU"}, "hu"},
		{"LC_ALL beats LC_MESSAGES", map[string]string{"LC_MESSAGES": "de_DE", "LC_ALL": "hu_HU"}, "hu"},
		{"PROBAVI_LANG beats everything", map[string]string{"LC_ALL": "de_DE", "PROBAVI_LANG": "hu"}, "hu"},
		{"C means English", map[string]string{"LANG": "C.UTF-8"}, ""},
		{"garbage means English", map[string]string{"LANG": "!!"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(k string) string { return tt.env[k] }
			if got := Detect(getenv); got != tt.want {
				t.Errorf("Detect = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewFallsBackToEnglish(t *testing.T) {
	for _, tag := range []string{"", "xx", "zz"} {
		tr, err := New(tag)
		if err != nil {
			t.Fatalf("New(%q): %v", tag, err)
		}
		if got := tr.Sprintf("no such key %q", "x"); got != `no such key "x"` {
			t.Errorf("New(%q).Sprintf = %q, want English passthrough", tag, got)
		}
	}
}

func TestSprintfTranslates(t *testing.T) {
	hu, err := New("hu")
	if err != nil {
		t.Fatalf("New(hu): %v", err)
	}
	got := hu.Sprintf("probavi: unknown command %q\n\n", "restore")
	if !strings.Contains(got, "ismeretlen parancs") || !strings.Contains(got, `"restore"`) {
		t.Errorf("Sprintf = %q, want the Hungarian translation with the argument applied", got)
	}
	if got := hu.Sprintf("untranslated %d", 7); got != "untranslated 7" {
		t.Errorf("Sprintf fallback = %q, want English formatting", got)
	}
}

func TestSprintfWithoutArgsIsVerbatim(t *testing.T) {
	// A literal % in an argumentless message must never be interpreted.
	tr := English()
	if got := tr.Sprintf("100% coverage"); got != "100% coverage" {
		t.Errorf("Sprintf = %q, want verbatim passthrough", got)
	}
}

func TestFprintf(t *testing.T) {
	hu, err := New("hu")
	if err != nil {
		t.Fatalf("New(hu): %v", err)
	}
	var sb strings.Builder
	hu.Fprintf(&sb, "probavi run: --config is required\n")
	if got := sb.String(); !strings.Contains(got, "kötelező") {
		t.Errorf("Fprintf wrote %q, want the Hungarian translation", got)
	}
}

func TestParseCatalogRejectsBrokenJSON(t *testing.T) {
	if _, err := parseCatalog("xx", []byte(`{"key": `)); err == nil ||
		!strings.Contains(err.Error(), "parse embedded catalog xx.json") {
		t.Errorf("parseCatalog = %v, want a parse error naming the catalog", err)
	}
}

func TestLocalesAndCatalog(t *testing.T) {
	tags, err := Locales()
	if err != nil {
		t.Fatalf("Locales: %v", err)
	}
	found := false
	for _, tag := range tags {
		if tag == "hu" {
			found = true
		}
		catalog, err := Catalog(tag)
		if err != nil {
			t.Errorf("Catalog(%s): %v", tag, err)
		}
		if len(catalog) == 0 {
			t.Errorf("Catalog(%s) is empty", tag)
		}
	}
	if !found {
		t.Errorf("Locales() = %v, want it to include hu", tags)
	}
	if _, err := Catalog("xx"); err == nil {
		t.Error("Catalog(xx) succeeded for a locale that does not exist")
	}
}
