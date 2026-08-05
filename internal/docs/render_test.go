package docs_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// shellVarsThatMustSurvive lists, per recipe template, variables that
// belong to the *rendered* file and must still be there afterwards.
//
// `envsubst` with no argument substitutes every ${...} it sees and
// replaces the unset ones with nothing. Run that way over a PKGBUILD it
// silently eats ${pkgdir}, ${srcdir} and ${pkgbase}, and the result is a
// recipe that builds nothing from a URL with no version in it — attached
// to a release, where the first person to notice is an Arch user running
// makepkg. The fix is a SHELL-FORMAT argument naming only the variables
// the render is meant to fill; this test is what keeps it.
var shellVarsThatMustSurvive = map[string][]string{
	"packaging/aur/PKGBUILD.tmpl": {
		"${pkgdir}", "${srcdir}", "${pkgbase}", "${pkgver}",
	},
	"packaging/gentoo/app-backup/probavi/probavi-9999.ebuild.tmpl": {
		"${WORKDIR}", "${PN}", "${P}", "${MY_PV}",
	},
}

// renderVars are the substitutions each template legitimately expects.
var renderVars = map[string][]string{
	"packaging/aur/PKGBUILD.tmpl": {
		"PKGVER=0.0.0", "SOURCE_SHA256=" + strings.Repeat("a", 64),
		"TAG=v0.0.0", "SRCVER=0.0.0",
	},
	"packaging/gentoo/app-backup/probavi/probavi-9999.ebuild.tmpl": {
		"TAG=v0.0.0", "SRCVER=0.0.0",
	},
}

// envsubstFormat is the SHELL-FORMAT argument built from renderVars.
func envsubstFormat(vars []string) string {
	names := make([]string, 0, len(vars))
	for _, v := range vars {
		names = append(names, "${"+strings.SplitN(v, "=", 2)[0]+"}")
	}
	return strings.Join(names, " ")
}

// TestRecipeRenderingKeepsShellVariables renders each recipe the way the
// release does and proves nothing that belongs to the recipe was eaten.
// render runs envsubst the way the release does, restricted to the
// variables the template legitimately expects.
func render(t *testing.T, tmpl string, vars []string) string {
	t.Helper()
	in, err := os.Open(filepath.Join(repoRoot, tmpl))
	if err != nil {
		t.Fatalf("open template: %v", err)
	}
	defer func() {
		if err := in.Close(); err != nil {
			t.Fatalf("close template: %v", err)
		}
	}()
	cmd := exec.Command("envsubst", envsubstFormat(vars))
	cmd.Env = append(os.Environ(), vars...)
	cmd.Stdin = in
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("envsubst %s: %v", tmpl, err)
	}
	return string(out)
}

func TestRecipeRenderingKeepsShellVariables(t *testing.T) {
	if _, err := exec.LookPath("envsubst"); err != nil {
		// Never a silent skip: the release depends on this tool, and CI
		// runs where it exists.
		t.Fatalf("envsubst is required to check the recipe rendering: %v", err)
	}
	for tmpl, mustSurvive := range shellVarsThatMustSurvive {
		t.Run(tmpl, func(t *testing.T) {
			vars := renderVars[tmpl]
			rendered := render(t, tmpl, vars)
			for _, v := range mustSurvive {
				if !strings.Contains(rendered, v) {
					t.Errorf("rendering %s consumed %s, which belongs to the recipe, not to the render",
						tmpl, v)
				}
			}
			// The other half: what the render *was* meant to fill must be
			// gone, or the recipe ships a literal placeholder.
			for _, v := range vars {
				if name := "${" + strings.SplitN(v, "=", 2)[0] + "}"; strings.Contains(rendered, name) {
					t.Errorf("rendering %s left %s unsubstituted", tmpl, name)
				}
			}
		})
	}
}

// downloadURL matches the release tarball each source recipe fetches.
var downloadURL = regexp.MustCompile(`archive/refs/tags/([^."\s]+)\.tar\.gz`)

// TestSourceRecipesDownloadTheRealTag keeps the sortable version and the
// tag apart.
//
// Arch forbids a hyphen in pkgver and portage wants _rc1, so a
// pre-release is renamed for both — and the naive way to do that renames
// the download URL with it, pointing at a tag that was never pushed. The
// URL must come from the tag, never from the version.
func TestSourceRecipesDownloadTheRealTag(t *testing.T) {
	for _, tmpl := range []string{
		"packaging/aur/PKGBUILD.tmpl",
		"packaging/gentoo/app-backup/probavi/probavi-9999.ebuild.tmpl",
	} {
		t.Run(tmpl, func(t *testing.T) {
			m := downloadURL.FindStringSubmatch(read(t, tmpl))
			if m == nil {
				t.Fatalf("%s no longer downloads a release tarball", tmpl)
			}
			if !strings.Contains(m[1], "TAG") && !strings.Contains(m[1], "_tag") {
				t.Errorf("%s builds its download URL from %q rather than the tag — a "+
					"pre-release renames the version and would point at a tag nobody pushed",
					tmpl, m[1])
			}
		})
	}
}

// envsubstCall matches an envsubst invocation and captures what follows.
var envsubstCall = regexp.MustCompile(`envsubst(\s+\S+)?`)

// callersOfEnvsubst are every place that renders a template.
var callersOfEnvsubst = []string{
	"packaging/build-packages.sh",
	"packaging/homebrew/render.sh",
	".github/workflows/release.yml",
}

// TestEveryEnvsubstCallIsRestricted closes the other half of the bug the
// templates alone cannot prevent.
//
// TestRecipeRenderingKeepsShellVariables proves a *correctly invoked*
// envsubst leaves a recipe intact. It says nothing about how the release
// actually invokes it — and that is where the mistake was: a bare
// `envsubst` substitutes every ${...} in the file, so a PKGBUILD came out
// with ${pkgdir}, ${srcdir} and ${pkgbase} replaced by nothing. It built
// cleanly into a release asset; the first person to see it would have
// been an Arch user running makepkg.
//
// A SHELL-FORMAT argument naming the variables to fill is the whole fix,
// and it is invisible when missing, which is why it is pinned here.
func TestEveryEnvsubstCallIsRestricted(t *testing.T) {
	for _, caller := range callersOfEnvsubst {
		t.Run(caller, func(t *testing.T) {
			// Strip prose first: these files explain envsubst at length, and
			// a comment naming it is not an invocation.
			calls := envsubstCall.FindAllStringSubmatch(directives(t, caller), -1)
			if len(calls) == 0 {
				t.Fatalf("%s no longer calls envsubst — this gate is watching a line that moved", caller)
			}
			for _, c := range calls {
				arg := strings.TrimSpace(c[1])
				// The argument is a quoted list of ${NAME} placeholders.
				if !strings.HasPrefix(arg, "'$") {
					t.Errorf("%s calls %q without a SHELL-FORMAT argument: an unrestricted "+
						"envsubst erases every other ${...} in the rendered file", caller, strings.TrimSpace(c[0]))
				}
			}
		})
	}
}

// imageTag matches the tag the release workflow pushes to GHCR.
var imageTag = regexp.MustCompile(`(?m)^\s*tags: ghcr\.io/\$\{\{ github\.repository \}\}:(.+)$`)

// TestPublishedImageTagMatchesTheDocumentedPull keeps one pull command
// true.
//
// github.ref_name is the git tag, so using it raw published
// probavi:v0.3.0 while every document said `docker pull probavi:0.3.0` —
// a 404 for every reader, and one nothing in CI would notice, because
// the push succeeds and the documentation is prose.
func TestPublishedImageTagMatchesTheDocumentedPull(t *testing.T) {
	m := imageTag.FindStringSubmatch(read(t, ".github/workflows/release.yml"))
	if m == nil {
		t.Fatal(".github/workflows/release.yml no longer pushes a tagged image")
	}
	if strings.Contains(m[1], "github.ref_name") {
		t.Errorf("the image is tagged %s, which carries the leading \"v\" of the git tag; "+
			"docs/docker.md tells readers to pull the version without it", m[1])
	}
	// The documented pull must name a bare version, which is what the
	// version gate already holds to the changelog.
	if doc := read(t, "docs/docker.md"); !strings.Contains(doc, "docker pull ghcr.io/probavi/probavi:") {
		t.Error("docs/docker.md no longer shows a docker pull command to keep in step")
	}
}

// TestPackageNamesSurviveARelease pins the rename that keeps the
// documented checksum command honest.
//
// nfpm spells a pre-release 0.3.0~rc.1, which is the correct version
// ordering — it sorts before the final release. GitHub will not keep a
// "~" in an asset filename, so the file uploaded as
// probavi_0.3.0.rc.1_amd64.deb was checksummed under a name nobody could
// download. `sha256sum -c SHA256SUMS --ignore-missing` then skipped every
// package and exited 0: a green tick for something it never looked at,
// in a product whose entire proposition is verifiable artifacts.
func TestPackageNamesSurviveARelease(t *testing.T) {
	script := directives(t, "packaging/build-packages.sh")
	if !regexp.MustCompile(`(?m)^safe_names\s*$`).MatchString(script) {
		t.Error("packaging/build-packages.sh no longer normalises package filenames, so a " +
			"pre-release would be checksummed under names GitHub rewrites")
	}
	if !strings.Contains(script, "[!A-Za-z0-9._-]") {
		t.Error("packaging/build-packages.sh no longer rejects filenames a release asset " +
			"cannot keep — the rename alone only covers the character we already know about")
	}
}
