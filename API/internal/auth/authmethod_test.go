package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
)

// capturePrincipal returns a handler that records the request's principal.
func capturePrincipal(seen **Principal) echo.HandlerFunc {
	return func(c echo.Context) error {
		*seen = PrincipalFromContext(c.Request().Context())
		return nil
	}
}

func TestAPIKeyMiddlewareSetsAuthMethod(t *testing.T) {
	t.Parallel()
	f := newAPIKeyMetricsFixture(t)
	future := time.Now().Add(24 * time.Hour)
	token := f.key(t, []string{string(models.RoleDeveloper)}, &future, nil)

	seen, called, err := runMiddleware(t, (&APIKeyAuthenticator{Handle: f.handle}).Middleware, "Bearer "+token, nil)
	require.NoError(t, err)
	require.True(t, called)
	require.NotNil(t, seen)
	assert.Equal(t, AuthMethodAPIKey, seen.AuthMethod)
}

func TestDevModeMiddlewareSetsAuthMethod(t *testing.T) {
	t.Parallel()
	f := newAPIKeyMetricsFixture(t)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	req.Header.Set("X-Dev-User-ID", f.owner.ID.String())
	c := echo.New().NewContext(req, httptest.NewRecorder())
	var seen *Principal
	require.NoError(t, (&DevModeAuthenticator{Handle: f.handle}).Middleware(capturePrincipal(&seen))(c))

	require.NotNil(t, seen)
	assert.Equal(t, AuthMethodDev, seen.AuthMethod)
}

func TestSessionMiddlewareSetsAuthMethod(t *testing.T) {
	t.Parallel()
	a, _ := newTestAuthenticator(t)
	org := seed.SeedOrg(t, a.handle, models.Organization{})
	user := seed.SeedUser(t, a.handle, models.User{OrganizationID: org.ID})
	session, err := mintSession(user.ID, org.ID, testSecret, time.Hour)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	c := echo.New().NewContext(req, httptest.NewRecorder())
	var seen *Principal
	require.NoError(t, a.Middleware(capturePrincipal(&seen))(c))

	require.NotNil(t, seen)
	assert.Equal(t, AuthMethodSession, seen.AuthMethod)
}
