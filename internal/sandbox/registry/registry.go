// Package registry lists the sandbox providers this build ships. It
// exists so the two consumers that must agree on that list — the CLI,
// which resolves a drill config's provider name, and the capabilities
// generator, which states the provider surface — read one list instead of
// keeping two in step. It holds descriptions only: constructing a provider
// stays in cmd/probavi, where the concrete constructors and their differing
// arguments belong.
package registry

import (
	"github.com/aafeher/probavi/internal/sandbox"
	"github.com/aafeher/probavi/internal/sandbox/docker"
	"github.com/aafeher/probavi/internal/sandbox/k8s"
	"github.com/aafeher/probavi/internal/sandbox/remotehost"
)

// Descriptors returns every shipped provider's self-description, in
// documentation order. It returns a fresh slice on every call: the
// registry is a contract, not shared state.
func Descriptors() []sandbox.Descriptor {
	return []sandbox.Descriptor{
		docker.Descriptor,
		k8s.Descriptor,
		remotehost.Descriptor,
	}
}

// IDs returns the drill-config provider names, in the same order.
func IDs() []string {
	descriptors := Descriptors()
	ids := make([]string, 0, len(descriptors))
	for _, d := range descriptors {
		ids = append(ids, d.ID)
	}
	return ids
}
