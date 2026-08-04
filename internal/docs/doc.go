// Package docs pins the translated README files to the English original
// (docs/i18n.md §7). The package contains no production code: its test
// suite hashes the marked spans of README.md and holds every
// README.<tag>.md to the hash it records, so an edit to the English source
// cannot ship without the translations being refreshed.
//
// English is the canonical source language (AGENTS.md §5.7); translations
// are derived artifacts. The gate here is the prose analog of the catalog
// gates in docs/i18n.md §4: a translation may only exist in this
// repository while a machine can prove it is current.
package docs
