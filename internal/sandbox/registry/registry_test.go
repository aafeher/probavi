package registry_test

import (
	"strings"
	"testing"

	"github.com/probavi/probavi/internal/sandbox/registry"
)

// TestDescriptorsAreWellFormed holds every shipped provider to the fields
// docs/capabilities.json publishes. An empty isolation statement or a
// parameter without documentation would become an empty promise on a
// public page.
func TestDescriptorsAreWellFormed(t *testing.T) {
	descriptors := registry.Descriptors()
	if len(descriptors) == 0 {
		t.Fatal("the registry ships no providers")
	}
	seen := map[string]bool{}
	for _, d := range descriptors {
		switch {
		case d.ID == "":
			t.Error("a descriptor has no id")
			continue
		case seen[d.ID]:
			t.Errorf("duplicate provider id %q", d.ID)
		case d.Name == "":
			t.Errorf("provider %q has no name", d.ID)
		case d.Status == "":
			t.Errorf("provider %q has no status", d.ID)
		case len(d.Params) == 0:
			t.Errorf("provider %q declares no parameters", d.ID)
		case d.Isolation.Storage == "":
			t.Errorf("provider %q does not state where restored data lives", d.ID)
		case d.Isolation.OrphanSweep == "":
			t.Errorf("provider %q does not state how orphans are reclaimed", d.ID)
		case !d.Isolation.ForcedTeardown:
			t.Errorf("provider %q does not force teardown — cleanup is not optional", d.ID)
		case d.Isolation.PublishedPorts:
			t.Errorf("provider %q publishes ports, which no provider may do", d.ID)
		case len(d.VerifiedAgainst) == 0:
			t.Errorf("provider %q names no environment it is verified against", d.ID)
		}
		seen[d.ID] = true
	}
}

// TestDescriptorParamsAreDocumented keeps every published parameter
// explicable: an undocumented one becomes an empty cell on a public page.
func TestDescriptorParamsAreDocumented(t *testing.T) {
	for _, d := range registry.Descriptors() {
		for _, p := range d.Params {
			if p.Name == "" || p.Doc == "" {
				t.Errorf("provider %q has an undocumented parameter %q", d.ID, p.Name)
			}
			if p.Family && !strings.HasSuffix(p.Name, ".") {
				t.Errorf("provider %q: family parameter %q does not end with a dot", d.ID, p.Name)
			}
		}
	}
}

// TestIDsMatchDescriptors pins the convenience accessor to the list.
func TestIDsMatchDescriptors(t *testing.T) {
	descriptors := registry.Descriptors()
	ids := registry.IDs()
	if len(ids) != len(descriptors) {
		t.Fatalf("IDs() has %d entries, Descriptors() has %d", len(ids), len(descriptors))
	}
	for i, d := range descriptors {
		if ids[i] != d.ID {
			t.Errorf("IDs()[%d] = %q, want %q", i, ids[i], d.ID)
		}
	}
}
