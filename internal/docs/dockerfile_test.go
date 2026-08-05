package docs_test

import (
	"regexp"
	"strings"
	"testing"
)

const dockerfile = "Dockerfile"

// runtimePackages are the packages the published image cannot do without,
// each with what breaks when it is missing. None is a convenience.
var runtimePackages = map[string]string{
	"docker-cli": "the docker sandbox provider shells out to it; without it the image supports no sandbox at all",
	"openssh-client": "the remotehost provider needs it, and so does DOCKER_HOST=ssh://…, " +
		"which docs/docker.md recommends over mounting the host socket",
	"ca-certificates": "HTTPS webhook notifications must not depend on the base image happening to " +
		"carry ca-certificates-bundle, and update-ca-certificates is how an operator trusts a private CA",
}

// TestImageKeepsWhatItNeeds holds the runtime stage to the packages the
// shipped features need.
//
// Each entry breaks something a build would not notice. Dropping
// docker-cli leaves an image that cannot run a drill at all; dropping
// openssh-client silently removes both the remotehost provider and the
// DOCKER_HOST=ssh:// deployment this project recommends over mounting a
// socket; dropping ca-certificates leaves the roots to whatever the base
// image happens to ship and takes away the operator's way to trust a
// private CA. None of them fails at build time, which is why they are
// pinned here.
func TestImageKeepsWhatItNeeds(t *testing.T) {
	df := read(t, dockerfile)
	for pkg, why := range runtimePackages {
		if !regexp.MustCompile(`(?m)^\s+` + regexp.QuoteMeta(pkg) + `\s*\\?$`).MatchString(df) {
			t.Errorf("%s no longer installs %s — %s", dockerfile, pkg, why)
		}
	}
}

// TestImageRunsUnprivileged pins the two properties that keep a restored
// production dataset from being handled with more authority than it needs
// (AGENTS.md §3.3).
func TestImageRunsUnprivileged(t *testing.T) {
	df := read(t, dockerfile)
	if !strings.Contains(df, "USER probavi") {
		t.Errorf("%s does not drop to an unprivileged user before ENTRYPOINT", dockerfile)
	}
	if !strings.Contains(df, "adduser") {
		t.Errorf("%s no longer creates the unprivileged user it runs as", dockerfile)
	}
}

// TestImageBasesArePinnedByDigest keeps the supply-chain rule of
// AGENTS.md §3.3 mechanical: a tag can be moved under us, a digest cannot.
func TestImageBasesArePinnedByDigest(t *testing.T) {
	froms := regexp.MustCompile(`(?m)^FROM\s+(\S+)`).FindAllStringSubmatch(read(t, dockerfile), -1)
	if len(froms) < 2 {
		t.Fatalf("%s has %d FROM lines, want the build and runtime stages", dockerfile, len(froms))
	}
	for _, m := range froms {
		if !strings.Contains(m[1], "@sha256:") {
			t.Errorf("%s: base image %q is not pinned by digest", dockerfile, m[1])
		}
	}
}

// TestImageShipsEveryDeclaredAdapter is the image's half of the release
// gate in release_test.go: the archives ship one artifact per adapter and
// the image bundles them, but both must cover exactly what
// docs/capabilities.json declares. The Dockerfile loops over adapters/*,
// so this holds that loop to the manifest.
func TestImageShipsEveryDeclaredAdapter(t *testing.T) {
	if df := read(t, dockerfile); !strings.Contains(df, "for dir in adapters/*/;") {
		t.Errorf("%s no longer builds the adapters by globbing adapters/*, so "+
			"TestReleaseShipsExactlyTheDeclaredAdapters no longer says anything about the image", dockerfile)
	}
	// The manifest is read for the same reason release_test.go reads it:
	// an empty adapter list would make every assertion here vacuous.
	if len(readManifest(t).Adapters) == 0 {
		t.Fatal("capabilities manifest declares no adapters")
	}
}
