// Package spec pins the machine-readable JSON Schemas in docs/schemas to
// the implementation. The package contains no production code: its test
// suite compiles every schema file and validates the repository's golden
// files plus positive and negative message samples against them, so a
// schema that drifts from the implementation — or an implementation change
// that violates a frozen spec — fails CI.
//
// The markdown documents in docs/ remain normative; the schemas are derived
// artifacts (docs/schemas/README.md).
package spec
