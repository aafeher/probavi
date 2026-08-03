package notify

import "net/http"

// transport.go describes the delivery mechanisms this package ships, so
// the generated capabilities manifest states them from the constants the
// deliveries actually use rather than from a second list. There is exactly
// one transport: Slack, email, and dead-man's-switch services are recipes
// on top of it (docs/notifications.md), documentation rather than shipped
// integrations, and must never be published as capabilities.

// Transport is one notification delivery mechanism.
type Transport struct {
	// ID is the stable identifier.
	ID string
	// Name is a one-line English label.
	Name string
	// Status is the maturity level, validated against the capabilities
	// vocabulary (docs/capabilities.md).
	Status string
	// Method is the HTTP method used.
	Method string
	// ContentType is the request body's media type.
	ContentType string
	// EventHeader names the header carrying the event name.
	EventHeader string
	// SignatureHeader names the header carrying the body MAC.
	SignatureHeader string
	// SignatureAlgorithm names the MAC.
	SignatureAlgorithm string
	// SigningOptional reports whether signing is opt-in.
	SigningOptional bool
	// Docs is the repository-relative normative document.
	Docs string
}

// Transports returns every shipped delivery mechanism.
func Transports() []Transport {
	return []Transport{{
		ID:                 "webhook",
		Name:               "HTTP webhook",
		Status:             "experimental",
		Method:             http.MethodPost,
		ContentType:        ContentType,
		EventHeader:        HeaderEvent,
		SignatureHeader:    HeaderSignature,
		SignatureAlgorithm: SignatureAlgorithm,
		SigningOptional:    true,
		Docs:               "docs/notifications.md",
	}}
}
