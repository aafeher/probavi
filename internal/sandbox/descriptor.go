package sandbox

import (
	"fmt"
	"strings"
)

// descriptor.go declares how a sandbox provider describes itself. Every
// provider resolves each configured parameter through Descriptor.Lookup
// and rejects what the descriptor does not list, so a parameter cannot be
// honored without being declared. That is what lets the generated
// capabilities manifest (docs/capabilities.md) state the parameter surface
// and the isolation properties without a second, hand-maintained list.

// Param describes one drill-config sandbox parameter a provider accepts.
type Param struct {
	// Name is the config key, or the prefix of a family when Family is
	// set (then it ends with a dot, as in "env.").
	Name string
	// Family marks a prefix family: every key starting with Name matches.
	Family bool
	// Required reports whether omitting the key is an error.
	Required bool
	// Default is the value used when the key is absent, "" when there is
	// no default.
	Default string
	// Doc is a one-line English description.
	Doc string
}

// Key renders the parameter the way an operator writes it in drill config.
func (p Param) Key() string {
	if p.Family {
		return p.Name + "<NAME>"
	}
	return p.Name
}

// Isolation states the containment properties an operator must know
// before pointing a provider at restored production data.
type Isolation struct {
	// NetworkDefault is the network the sandbox joins unless configured
	// otherwise, "" when the provider does not control networking.
	NetworkDefault string
	// PublishedPorts reports whether the provider can expose ports —
	// false everywhere by design (AGENTS.md §3.3).
	PublishedPorts bool
	// Storage describes where restored data lives and how it goes away.
	Storage string
	// ForcedTeardown reports whether the provider destroys the sandbox on
	// every path, failures included.
	ForcedTeardown bool
	// OrphanSweep describes how sandboxes of crashed runs are reclaimed.
	OrphanSweep string
	// ExternalBackstop describes cleanup that survives the drill host
	// dying outright, "" when the provider has none.
	ExternalBackstop string
}

// Descriptor is a sandbox provider's self-description.
type Descriptor struct {
	// ID is the drill-config provider name.
	ID string
	// Name is a one-line English label.
	Name string
	// Status is the maturity level (docs/capabilities.md).
	Status string
	// Params are the accepted parameters, in documentation order.
	Params []Param
	// Isolation states the containment properties.
	Isolation Isolation
	// Constraints are the operational preconditions an operator must meet.
	Constraints []string
	// VerifiedAgainst names the environments this repository's test suite
	// exercises the provider against.
	VerifiedAgainst []string
	// Docs is a repository-relative path to the normative document for
	// this provider, "" when none exists.
	Docs string
}

// Lookup resolves a configured key to the parameter that declares it.
func (d Descriptor) Lookup(key string) (Param, bool) {
	for _, p := range d.Params {
		if p.Family {
			if strings.HasPrefix(key, p.Name) {
				return p, true
			}
			continue
		}
		if key == p.Name {
			return p, true
		}
	}
	return Param{}, false
}

// ParamKeys lists the declared keys as an operator writes them.
func (d Descriptor) ParamKeys() []string {
	keys := make([]string, 0, len(d.Params))
	for _, p := range d.Params {
		keys = append(keys, p.Key())
	}
	return keys
}

// UnknownParamError reports a key the provider does not declare. It names
// every accepted key: a typo must fail loudly rather than silently weaken
// a sandbox.
func (d Descriptor) UnknownParamError(key string) error {
	return fmt.Errorf("%w: unknown %s sandbox param %q (supported: %s)",
		ErrInvalidParams, d.ID, key, strings.Join(d.ParamKeys(), ", "))
}

// UnhandledParamError reports a key the descriptor declares but the
// provider does not implement. That is a defect in the provider — surfaced
// rather than silently ignored, because a parameter an operator writes and
// the provider drops is a sandbox that is not what the drill asked for.
func (d Descriptor) UnhandledParamError(key string) error {
	return fmt.Errorf("%w: %s sandbox param %q is declared but not implemented",
		ErrInvalidParams, d.ID, key)
}
