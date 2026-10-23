package server

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
	"github.com/labstack/echo/v4"
)

// ErrorHandler is wired as Echo's HTTPErrorHandler. It converts every
// surfaced error into the canonical envelope shape and writes the right
// status code. Domain errors should be *apierror.Error; everything
// else is mapped through best-effort.
func ErrorHandler(err error, c echo.Context) {
	if c.Response().Committed {
		return
	}

	requestID := requestIDFromContext(c)

	var apiErr *apierror.Error
	if errors.As(err, &apiErr) {
		apiErr.RequestID = requestID
		_ = c.JSON(apiErr.Status, envelope(apiErr))
		return
	}

	var httpErr *echo.HTTPError
	if errors.As(err, &httpErr) {
		code := apierror.MapHTTPStatusCode(httpErr.Code)
		msg := httpErr.Message
		if m, ok := msg.(string); ok && m != "" {
			_ = c.JSON(httpErr.Code, envelope(&apierror.Error{
				Status:    httpErr.Code,
				Code:      code,
				Message:   m,
				RequestID: requestID,
			}))
			return
		}
		_ = c.JSON(httpErr.Code, envelope(&apierror.Error{
			Status:    httpErr.Code,
			Code:      code,
			Message:   http.StatusText(httpErr.Code),
			RequestID: requestID,
		}))
		return
	}

	// Unknown error -> 500 with a generic message.
	slog.Error("unhandled error", "error", err, "request_id", requestID)
	_ = c.JSON(http.StatusInternalServerError, envelope(&apierror.Error{
		Status:    http.StatusInternalServerError,
		Code:      "internal_error",
		Message:   "An unexpected error occurred.",
		RequestID: requestID,
	}))
}

func envelope(e *apierror.Error) map[string]any {
	return map[string]any{"error": e}
}
