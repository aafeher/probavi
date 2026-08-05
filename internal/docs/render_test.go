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
