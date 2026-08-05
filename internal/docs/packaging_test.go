package docs_test

import (
	"regexp"
	"strings"
	"testing"
)

// packagingRecipes are every file that decides where a packaged binary
// lands. They disagree about syntax and agree about one thing.
var packagingRecipes = []string{
	"packaging/nfpm/probavi.yaml.tmpl",
	"packaging/nfpm/adapter.yaml.tmpl",
	"packaging/aur/PKGBUILD.tmpl",
	"packaging/gentoo/app-backup/probavi/probavi-9999.ebuild.tmpl",
}

// libexecish matches the directories a packager's FHS instinct reaches
// for when a binary looks like a helper.
var libexecish = regexp.MustCompile(`/usr/libexec|/usr/lib/probavi|libexecdir`)

// commentLine matches a whole-line comment. Every recipe format here uses
// "#", and each one *explains* in prose why /usr/libexec is wrong — so
// scanning the raw text would match the warning rather than a real path.
var commentLine = regexp.MustCompile(`(?m)^\s*#.*$`)

// directives is a recipe with its prose removed.
func directives(t *testing.T, path string) string {
	t.Helper()
	return commentLine.ReplaceAllString(read(t, path), "")
}

// TestPackagesInstallOntoPATH is the invariant every recipe shares: the
// core resolves an adapter with exec.LookPath, so a binary outside PATH
// fails every drill with "resolve adapter: executable file not found".
//
// An adapter looks exactly like a helper program, and /usr/libexec is
// where a careful Debian packager would put one. Nothing about the
// package would look wrong — it installs, dpkg is satisfied, and only a
// drill discovers the problem.
func TestPackagesInstallOntoPATH(t *testing.T) {
	for _, recipe := range packagingRecipes {
		t.Run(recipe, func(t *testing.T) {
			body := directives(t, recipe)
			if m := libexecish.FindString(body); m != "" {
				t.Errorf("%s installs into %s — the core finds adapters on PATH only", recipe, m)
			}
			if !strings.Contains(body, "/usr/bin") && !strings.Contains(body, "dobin") {
				t.Errorf("%s names no PATH install location", recipe)
			}
		})
	}
}

// TestAdapterPackagesDependOnTheCoreOnly pins the two dependency
// decisions that carry meaning.
//
// An engine client (postgresql-client and friends) is the reflex, and it
// would be wrong: the engine's tools run inside the sandbox image, never
// on the drill host — every adapter contains zero exec calls in
// production code. A *versioned* dependency on the core would be the
// other mistake: the compatibility contract is the adapter protocol
// version negotiated at handshake, and pinning package versions instead
// would close off the per-adapter repositories AGENTS.md §6 keeps open.
func TestAdapterPackagesDependOnTheCoreOnly(t *testing.T) {
	body := directives(t, "packaging/nfpm/adapter.yaml.tmpl")
	for _, client := range []string{
		"postgresql-client", "postgresql", "mysql-client", "mariadb-client",
		"mongodb", "mssql-tools",
	} {
		if regexp.MustCompile(`(?m)^\s*-\s*` + regexp.QuoteMeta(client) + `\s*$`).MatchString(body) {
			t.Errorf("the adapter package depends on %s — engine tools run in the sandbox image, "+
				"not on the drill host", client)
		}
	}
	if regexp.MustCompile(`-\s*probavi\s*[<>=]`).MatchString(body) {
		t.Error("the adapter package pins a core version — the contract is the adapter protocol " +
			"version negotiated at handshake, not either package version")
	}
	if !regexp.MustCompile(`(?m)^\s*-\s*probavi\s*$`).MatchString(body) {
		t.Error("the adapter package does not depend on probavi at all")
	}
}

// TestCorePackageHasNoHardDependency keeps the verification-only install
// possible: `probavi evidence verify` reads a log and a public key and
// needs no runtime, so an auditor must not have a container engine pulled
// onto their machine by installing the core.
func TestCorePackageHasNoHardDependency(t *testing.T) {
	body := directives(t, "packaging/nfpm/probavi.yaml.tmpl")
	// A top-level `depends:` block would apply to every format. The apk
	// override is deliberate and lives under overrides.apk.
	if regexp.MustCompile(`(?m)^depends:`).MatchString(body) {
		t.Error("packaging/nfpm/probavi.yaml.tmpl declares a top-level dependency — " +
			"verifying an evidence log must not require anything")
	}
}

// TestPackagingRecipesCoverEveryAdapter holds the hand-written recipes to
// the generated manifest. The nfpm path loops over adapters/* and is
// covered by TestReleaseShipsExactlyTheDeclaredAdapters; PKGBUILD and the
// ebuild list engines by name, so they can silently fall behind.
func TestPackagingRecipesCoverEveryAdapter(t *testing.T) {
	adapters := readManifest(t).Adapters
	for _, recipe := range []string{
		"packaging/aur/PKGBUILD.tmpl",
		"packaging/gentoo/app-backup/probavi/probavi-9999.ebuild.tmpl",
	} {
		t.Run(recipe, func(t *testing.T) {
			body := directives(t, recipe)
			for _, a := range adapters {
				if !strings.Contains(body, a.ID) {
					t.Errorf("%s never mentions the %s adapter, which ships — a user of this "+
						"distribution would silently not get it", recipe, a.ID)
				}
			}
		})
	}
}

// TestPackagingDocIsReachable keeps the install path discoverable: a
// package nobody can find instructions for is a package nobody installs.
func TestPackagingDocIsReachable(t *testing.T) {
	for _, want := range []string{"docs/packaging.md", "docs/docker.md"} {
		if readme := read(t, sourceDoc); !strings.Contains(readme, want) {
			t.Errorf("%s does not link %s", sourceDoc, want)
		}
		// Reading it also proves it exists; read() fails the test if not.
		if body := read(t, want); len(body) == 0 {
			t.Errorf("%s is empty", want)
		}
	}
}
