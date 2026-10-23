package apierror

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	err := New(http.StatusBadRequest, "bad_request", "something went wrong")

	require.NotNil(t, err)
	assert.Equal(t, http.StatusBadRequest, err.Status)
	assert.Equal(t, "bad_request", err.Code)
	assert.Equal(t, "something went wrong", err.Message)
	assert.Nil(t, err.Errors)
	assert.Empty(t, err.RequestID)
}

func TestNewValidation(t *testing.T) {
	fields := []FieldError{
		{Path: "name", Code: "required", Message: "name is required"},
		{Path: "email", Code: "invalid", Message: "email is malformed"},
	}

	err := NewValidation("payload failed validation", fields)

	require.NotNil(t, err)
	assert.Equal(t, http.StatusUnprocessableEntity, err.Status)
	assert.Equal(t, 422, err.Status)
	assert.Equal(t, "validation_failed", err.Code)
	assert.Equal(t, "payload failed validation", err.Message)
	assert.Equal(t, fields, err.Errors)
	assert.Len(t, err.Errors, 2)
}

func TestErrorMethod(t *testing.T) {
	err := New(http.StatusNotFound, "not_found", "resource missing")

	assert.Equal(t, "resource missing", err.Error())

	// Assert *Error is assignable to the standard error interface.
	var iface error = err
	require.NotNil(t, iface)
	assert.Equal(t, "resource missing", iface.Error())
}

func TestMapHTTPStatusCode(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   string
	}{
		{"bad request", http.StatusBadRequest, "bad_request"},
		{"unauthorized", http.StatusUnauthorized, "unauthorized"},
		{"forbidden", http.StatusForbidden, "forbidden"},
		{"not found", http.StatusNotFound, "not_found"},
		{"conflict", http.StatusConflict, "conflict"},
		{"gone", http.StatusGone, "gone"},
		{"unprocessable entity", http.StatusUnprocessableEntity, "validation_failed"},
		{"too many requests", http.StatusTooManyRequests, "rate_limited"},
		{"not implemented", http.StatusNotImplemented, "not_implemented"},
		{"unmapped default", http.StatusTeapot, "error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, MapHTTPStatusCode(tt.status))
		})
	}
}

func TestErrorJSONMarshal_OmitsAndIncludes(t *testing.T) {
	err := New(http.StatusBadRequest, "bad_request", "boom")

	raw, marshalErr := json.Marshal(err)
	require.NoError(t, marshalErr)

	// Round-trip into a generic map to assert on presence/absence of keys.
	var decoded map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &decoded))

	// Status is tagged json:"-" and must never appear.
	assert.NotContains(t, decoded, "status")
	assert.NotContains(t, decoded, "Status")

	// Errors is empty -> omitempty drops it.
	assert.NotContains(t, decoded, "errors")

	// RequestID is empty -> omitempty drops it.
	assert.NotContains(t, decoded, "request_id")

	// code and message are always present.
	require.Contains(t, decoded, "code")
	require.Contains(t, decoded, "message")

	assert.JSONEq(t, `{"code":"bad_request","message":"boom"}`, string(raw))
}

func TestErrorJSONMarshal_IncludesPopulatedFields(t *testing.T) {
	err := NewValidation("payload failed validation", []FieldError{
		{Path: "name", Code: "required", Message: "name is required"},
	})
	err.RequestID = "req-123"

	raw, marshalErr := json.Marshal(err)
	require.NoError(t, marshalErr)

	var decoded map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &decoded))

	// Status still absent.
	assert.NotContains(t, decoded, "status")

	// Populated fields now present.
	assert.Contains(t, decoded, "errors")
	assert.Contains(t, decoded, "request_id")

	assert.JSONEq(t, `{
		"code": "validation_failed",
		"message": "payload failed validation",
		"errors": [
			{"path": "name", "code": "required", "message": "name is required"}
		],
		"request_id": "req-123"
	}`, string(raw))
}

func TestNewNotImplemented(t *testing.T) {
	err := NewNotImplemented("Vault integration")

	require.NotNil(t, err)
	assert.Equal(t, http.StatusNotImplemented, err.Status)
	assert.Equal(t, "not_implemented", err.Code)
	assert.Equal(t, "Vault integration is not yet implemented (V0 stub)", err.Message)
	assert.Nil(t, err.Errors)
}
