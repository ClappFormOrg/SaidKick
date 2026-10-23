package server_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
	"github.com/ClappFormOrg/SaidKick/API/internal/server"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newErrorCtx builds a bare Echo context + recorder for driving
// server.ErrorHandler directly, mirroring how Echo invokes its
// HTTPErrorHandler.
func newErrorCtx() (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

// decodeEnvelope decodes the {"error": {...}} envelope from the recorder
// body and returns the inner error object as a map.
func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	inner, ok := body["error"].(map[string]any)
	require.True(t, ok, "response must be wrapped in an {\"error\": {...}} envelope")
	return inner
}

func TestErrorHandler(t *testing.T) {
	t.Run("apierror_rendered_with_own_status_and_code", func(t *testing.T) {
		c, rec := newErrorCtx()

		apiErr := apierror.New(http.StatusForbidden, "forbidden", "you shall not pass")
		server.ErrorHandler(apiErr, c)

		assert.Equal(t, http.StatusForbidden, rec.Code)
		inner := decodeEnvelope(t, rec)
		assert.Equal(t, "forbidden", inner["code"])
		assert.Equal(t, "you shall not pass", inner["message"])
	})

	t.Run("apierror_carries_field_errors_and_status_not_in_body", func(t *testing.T) {
		c, rec := newErrorCtx()

		apiErr := apierror.NewValidation("bad input", []apierror.FieldError{
			{Path: "name", Code: "required", Message: "name is required"},
		})
		server.ErrorHandler(apiErr, c)

		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		inner := decodeEnvelope(t, rec)
		assert.Equal(t, "validation_failed", inner["code"])
		// Status is json:"-" and must never be serialised into the body.
		_, hasStatus := inner["status"]
		assert.False(t, hasStatus, "Status must not be serialised into the body")
		errsList, ok := inner["errors"].([]any)
		require.True(t, ok, "field errors must be present")
		require.Len(t, errsList, 1)
	})

	t.Run("echo_httperror_string_message_passed_through", func(t *testing.T) {
		c, rec := newErrorCtx()

		httpErr := echo.NewHTTPError(http.StatusNotFound, "widget not found")
		server.ErrorHandler(httpErr, c)

		assert.Equal(t, http.StatusNotFound, rec.Code)
		inner := decodeEnvelope(t, rec)
		assert.Equal(t, "not_found", inner["code"])
		assert.Equal(t, "widget not found", inner["message"])
	})

	t.Run("echo_httperror_non_string_message_falls_back_to_statustext", func(t *testing.T) {
		c, rec := newErrorCtx()

		// Message is a non-string type -> the type assertion fails and the
		// handler falls back to http.StatusText.
		httpErr := echo.NewHTTPError(http.StatusBadGateway, map[string]any{"detail": "upstream"})
		server.ErrorHandler(httpErr, c)

		assert.Equal(t, http.StatusBadGateway, rec.Code)
		inner := decodeEnvelope(t, rec)
		assert.Equal(t, "error", inner["code"]) // 502 has no explicit mapping
		assert.Equal(t, http.StatusText(http.StatusBadGateway), inner["message"])
	})

	t.Run("echo_httperror_empty_string_message_falls_back_to_statustext", func(t *testing.T) {
		c, rec := newErrorCtx()

		// An empty string message must also fall back to http.StatusText.
		httpErr := echo.NewHTTPError(http.StatusBadRequest, "")
		server.ErrorHandler(httpErr, c)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		inner := decodeEnvelope(t, rec)
		assert.Equal(t, "bad_request", inner["code"])
		assert.Equal(t, http.StatusText(http.StatusBadRequest), inner["message"])
	})

	t.Run("unknown_error_collapsed_to_500_without_leaking_detail", func(t *testing.T) {
		c, rec := newErrorCtx()

		const internalDetail = "gorm: connection refused: internal detail"
		server.ErrorHandler(errors.New(internalDetail), c)

		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		inner := decodeEnvelope(t, rec)
		assert.Equal(t, "internal_error", inner["code"])
		assert.Equal(t, "An unexpected error occurred.", inner["message"])

		// CRUCIAL: the raw internal error string must not appear anywhere
		// in the response body — no info leak.
		assert.NotContains(t, rec.Body.String(), "gorm")
		assert.NotContains(t, rec.Body.String(), "connection refused")
		assert.NotContains(t, rec.Body.String(), internalDetail)
	})

	t.Run("wrapped_apierror_detected_through_errors_as", func(t *testing.T) {
		c, rec := newErrorCtx()

		apiErr := apierror.New(http.StatusConflict, "conflict", "already exists")
		wrapped := fmt.Errorf("service layer: %w", apiErr)
		server.ErrorHandler(wrapped, c)

		assert.Equal(t, http.StatusConflict, rec.Code)
		inner := decodeEnvelope(t, rec)
		assert.Equal(t, "conflict", inner["code"])
		assert.Equal(t, "already exists", inner["message"])
		// The wrapping prefix must not leak into the rendered message.
		assert.NotContains(t, rec.Body.String(), "service layer")
	})

	t.Run("committed_response_writes_nothing", func(t *testing.T) {
		c, rec := newErrorCtx()

		// Commit the response before invoking the handler.
		c.Response().WriteHeader(http.StatusTeapot)
		require.True(t, c.Response().Committed)

		server.ErrorHandler(errors.New("boom"), c)

		// Status stays as first committed, body stays empty — handler is a no-op.
		assert.Equal(t, http.StatusTeapot, rec.Code)
		assert.Empty(t, strings.TrimSpace(rec.Body.String()))
	})
}
