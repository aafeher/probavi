package docs_test

import (
	"regexp"
	"testing"
)

// changelogRelease matches the newest released heading of CHANGELOG.md.
// [Unreleased] carries no date and is skipped by the date requirement, so
// the first match is the version this repository currently claims to be.
var changelogRelease = regexp.MustCompile(`(?m)^## \[(\d+\.\d+\.\d+)\] - \d{4}-\d{2}-\d{2}$`)

// currentVersion is the newest version CHANGELOG.md declares released.
func currentVersion(t *testing.T) string {
	t.Helper()
	m := changelogRelease.FindStringSubmatch(read(t, "CHANGELOG.md"))
	if m == nil {
		t.Fatal("CHANGELOG.md declares no released version — this gate would pass vacuously")
	}
	return m[1]
}

// versionClaim is one place that must name the current release, and the
// pattern that finds it. Every capture group must equal that version.
//
// The list is deliberately narrow. A blanket search for version-shaped
// tokens would flag the things that must NOT move: the adapters' own
// adapterVersion numbers (postgres is at 0.3.0 quite independently), the
// contract versions probavi-adapter/0 and probavi-evidence/2, and the
// historical notes about v0.1.0's module path. Those are not claims about
// this release, and rewriting them would turn accurate history into a
// lie. What is listed here is only what goes stale.
type versionClaim struct {
	file    string
	what    string
	pattern *regexp.Regexp
}

var versionClaims = []versionClaim{
	{sourceDoc, "the Status line's release claim",
		regexp.MustCompile(`Released as \*\*v(\d+\.\d+\.\d+)\*\*`)},
	{sourceDoc, "the download example's tag",
		regexp.MustCompile(`\$ tag=v(\d+\.\d+\.\d+) `)},
	{sourceDoc, "the packaged install example",
		regexp.MustCompile(`probavi_(\d+\.\d+\.\d+)_amd64\.deb`)},
	{sourceDoc, "the independent verifier's pinned tag",
		regexp.MustCompile(`probavi-evidence-verify@v(\d+\.\d+\.\d+)`)},
	{"spec/evidence/README.md", "the independent verifier's pinned tag",
		regexp.MustCompile(`probavi-evidence-verify@v(\d+\.\d+\.\d+)`)},
	{"docs/docker.md", "the image tag",
		regexp.MustCompile(`ghcr\.io/probavi/probavi:(\d+\.\d+\.\d+)`)},
	{"docs/docker.md", "the reproduce-the-image build argument",
		regexp.MustCompile(`--build-arg VERSION=(\d+\.\d+\.\d+)`)},
	{"docs/packaging.md", "the release-download URLs",
		regexp.MustCompile(`releases/download/v(\d+\.\d+\.\d+)/`)},
	{"docs/packaging.md", "the install examples' version variable",
		regexp.MustCompile(`\$ ver=(\d+\.\d+\.\d+) `)},
	{"docs/packaging.md", "the package filenames",
		regexp.MustCompile(`probavi[-_](?:adapter-\w+[-_])?(\d+\.\d+\.\d+)[-_.]`)},
}

// TestDocumentedVersionsMatchTheChangelog keeps every install instruction
// on the version this repository says it is.
//
// A stale version in an install command is a special kind of wrong: it
// looks tested, it is copied verbatim, and it fails on the reader's
// machine with a 404 rather than in anyone's CI. Bumping a release means
// touching a dozen files across three documents, and the one that gets
// missed is always the one somebody runs.
func TestDocumentedVersionsMatchTheChangelog(t *testing.T) {
	want := currentVersion(t)
	for _, claim := range versionClaims {
		t.Run(claim.file+": "+claim.what, func(t *testing.T) {
			matches := claim.pattern.FindAllStringSubmatch(read(t, claim.file), -1)
			if len(matches) == 0 {
				t.Fatalf("%s no longer contains %s — this gate is watching a line that moved",
					claim.file, claim.what)
			}
			for _, m := range matches {
				if m[1] != want {
					t.Errorf("%s names version %s in %s, but CHANGELOG.md released %s",
						claim.file, m[1], claim.what, want)
				}
			}
		})
	}
}

// devVersion matches the version the binary stamps into itself.
var devVersion = regexp.MustCompile(`(?m)^var version = "(\d+\.\d+\.\d+)-dev"$`)

// TestBinaryVersionTracksTheChangelog holds `probavi version` to the
// release it belongs to.
//
// The stamp is what a drill records as env.probavi_version and what an
// auditor reads back out of a signed record, so a build claiming to be
// the previous release is not a cosmetic slip — it is a signed record
// naming the wrong software. It has drifted before: 0.2.0 shipped and the
// stamp stayed at 0.2.0-dev through everything that followed.
func TestBinaryVersionTracksTheChangelog(t *testing.T) {
	const source = "cmd/probavi/run.go"
	m := devVersion.FindStringSubmatch(read(t, source))
	if m == nil {
		t.Fatalf("%s no longer declares a `var version = \"X.Y.Z-dev\"` stamp", source)
	}
	if want := currentVersion(t); m[1] != want {
		t.Errorf("%s stamps %s-dev, but CHANGELOG.md released %s — a signed record would "+
			"name the wrong build", source, m[1], want)
	}
}
