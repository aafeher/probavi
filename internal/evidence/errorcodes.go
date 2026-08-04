package evidence

import "slices"

// ErrorCode values a record's error.code may carry (evidence-schema.md §7,
// mirrored by the enum in docs/schemas/evidence/record.json). Thirteen come
// from the adapter protocol's §5 registry; check_failed is defined by this
// schema, because a failed validation check is a verdict the core reaches,
// not something an adapter reports.
//
// Summary-level codes that describe failures *outside* a record —
// gameday's setup_error and evidence_lost — deliberately do not appear
// here: they exist because there is no record to put them in.
const (
	CodeInvalidRequest    = "invalid_request"
	CodeUnsupportedProto  = "unsupported_protocol"
	CodeUnsupportedSource = "unsupported_source"
	CodeSourceNotFound    = "source_not_found"
	CodeSourceUnreadable  = "source_unreadable"
	CodeSourceCorrupt     = "source_corrupt"
	CodeRestoreFailed     = "restore_failed"
	CodeEngineNotReady    = "engine_not_ready"
	CodeSandboxError      = "sandbox_error"
	CodeTimeout           = "timeout"
	CodeCancelled         = "cancelled"
	CodeAdapterCrash      = "adapter_crash"
	CodeInternal          = "internal"
	CodeCheckFailed       = "check_failed"
)

// errorCodes is the vocabulary in the order the published schema lists it.
var errorCodes = []string{
	CodeInvalidRequest, CodeUnsupportedProto, CodeUnsupportedSource,
	CodeSourceNotFound, CodeSourceUnreadable, CodeSourceCorrupt,
	CodeRestoreFailed, CodeEngineNotReady, CodeSandboxError,
	CodeTimeout, CodeCancelled, CodeAdapterCrash, CodeInternal,
	CodeCheckFailed,
}

// ErrorCodes returns the complete error.code vocabulary. Producers
// normalize against it before signing, and the schema test pins it to the
// published enum, so the Go list and docs/schemas/evidence/record.json
// cannot drift apart.
func ErrorCodes() []string {
	return slices.Clone(errorCodes)
}

// IsErrorCode reports whether code is in the published vocabulary.
//
// Deliberately not enforced by Record.Validate: a record rejected at
// Append is a drill that ran and left no proof, which is a worse outcome
// than an unexpected code. The rule belongs where the record is composed,
// where an off-vocabulary value can be mapped to internal and the original
// preserved in the message.
func IsErrorCode(code string) bool {
	return slices.Contains(errorCodes, code)
}
