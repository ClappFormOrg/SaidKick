// Package apierror is the shared error envelope used across the API.
//
// Lives in its own tiny package so both internal/handlers and
// internal/server (and any future package) can import it without creating
// a cycle. The HTTP error renderer that consumes these types lives in
// internal/server.
package apierror

import "net/http"

// Error is the canonical shape returned for any non-2xx response.
// Status is carried out-of-band; only Code, Message, Errors, RequestID and
// Details are serialised.
type Error struct {
	Status    int          `json:"-"`
	Code      string       `json:"code"`
	Message   string       `json:"message"`
	Errors    []FieldError `json:"errors,omitempty"`
	RequestID string       `json:"request_id,omitempty"`

	// Details carries endpoint-specific data a client needs to act on the
	// error, such as the failing pairs of a contrast check.
	Details any `json:"details,omitempty"`
}

// FieldError describes one validation failure inside an Error.
type FieldError struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error satisfies the standard error interface.
func (e *Error) Error() string { return e.Message }

// New constructs an Error with the given status and code.
func New(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// NewValidation constructs a 422 Unprocessable Entity Error carrying
// per-field details.
func NewValidation(message string, fields []FieldError) *Error {
	return &Error{
		Status:  http.StatusUnprocessableEntity,
		Code:    "validation_failed",
		Message: message,
		Errors:  fields,
	}
}

// NewNotImplemented is the canonical placeholder for endpoints whose backing
// integration is not yet wired (GitHub, Vault, Argo CD, agent WebSocket).
func NewNotImplemented(feature string) *Error {
	return &Error{
		Status:  http.StatusNotImplemented,
		Code:    "not_implemented",
		Message: feature + " is not yet implemented (V0 stub)",
	}
}

// MapHTTPStatusCode returns the canonical string code for a given HTTP
// status, used when wrapping echo.HTTPError into the envelope.
func MapHTTPStatusCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusGone:
		return "gone"
	case http.StatusUnprocessableEntity:
		return "validation_failed"
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusNotImplemented:
		return "not_implemented"
	default:
		return "error"
	}
}
