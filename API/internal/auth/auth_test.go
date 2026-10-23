package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDevModeAuthenticatorAnonymousPaths covers the header checks that run
// before any DB lookup. The authenticator has a nil Handle, so reaching the
// lookup would panic and fail the test.
func TestDevModeAuthenticatorAnonymousPaths(t *testing.T) {
	cases := []struct {
		name       string
		authHeader string // configured header name; "" means the default
		reqHeader  string
		value      string
	}{
		{"default header absent", "", "", ""},
		{"default header malformed", "", "X-Dev-User-ID", "not-a-uuid"},
		{"custom header absent", "X-Custom-User", "", ""},
		{"custom header malformed", "X-Custom-User", "X-Custom-User", "1234"},
		{"default header ignored when custom configured", "X-Custom-User", "X-Dev-User-ID", uuid.NewString()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mw := (&DevModeAuthenticator{Header: tc.authHeader}).Middleware

			req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
			if tc.reqHeader != "" {
				req.Header.Set(tc.reqHeader, tc.value)
			}
			c := echo.New().NewContext(req, httptest.NewRecorder())

			called := false
			err := mw(func(c echo.Context) error {
				called = true
				assert.Nil(t, PrincipalFromContext(c.Request().Context()))
				return nil
			})(c)
			require.NoError(t, err)
			assert.True(t, called)
		})
	}
}
