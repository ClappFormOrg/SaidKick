package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
)

// newSessionTestServer wires the authenticator's middleware and routes onto
// a fresh Echo instance plus GET /me, which requires an authenticated caller.
func newSessionTestServer(t *testing.T) (*OIDCAuthenticator, *echo.Echo) {
	t.Helper()
	a, _ := newTestAuthenticator(t)
	e := echo.New()
	e.Use(a.Middleware)
	a.RegisterRoutes(e)
	e.GET("/me", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	}, RequireAuthenticated)
	return a, e
}

// getMe issues GET /me carrying the given session cookie value.
func getMe(e *echo.Echo, session string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/me", http.NoBody)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// postLogout issues POST /auth/logout carrying the given session cookie value.
func postLogout(e *echo.Echo, session string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/auth/logout", http.NoBody)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func countRevocations(t *testing.T, a *OIDCAuthenticator) int64 {
	t.Helper()
	var n int64
	require.NoError(t, a.handle.RW.WithContext(t.Context()).
		Model(&models.RevokedSession{}).Count(&n).Error)
	return n
}

func TestLogoutRevokesSession(t *testing.T) {
	t.Parallel()

	t.Run("replayed cookie after logout returns 401", func(t *testing.T) {
		t.Parallel()
		const clientID = "revoke-client"
		const nonce = "revoke-nonce"

		idp := newMockIDP(t, clientID, "revoke-sub", "revoke@example.nl")
		a, e := newSessionTestServer(t)
		org := seedOIDCOrg(t, a, idp.URL, clientID, "client-secret")

		// Log in through the callback and capture the session cookie.
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, callbackRequest("auth-code", nonce, mintTestStateCookie(t, org.ID, nonce)))
		require.Equal(t, http.StatusFound, rec.Code, "login failed; body: %s", rec.Body)
		var session string
		for _, c := range rec.Result().Cookies() {
			if c.Name == sessionCookieName {
				session = c.Value
			}
		}
		require.NotEmpty(t, session, "login must set the session cookie")

		require.Equal(t, http.StatusOK, getMe(e, session).Code, "fresh session must authenticate")

		require.Equal(t, http.StatusNoContent, postLogout(e, session).Code)

		replay := getMe(e, session)
		assert.Equal(t, http.StatusUnauthorized, replay.Code, "replayed cookie must be rejected")

		// Same response as a forged token: revocation is not distinguishable.
		forged, err := mintSession(uuid.New(), org.ID, otherSecret, time.Hour)
		require.NoError(t, err)
		forgedRec := getMe(e, forged)
		assert.Equal(t, forgedRec.Code, replay.Code)
		assert.JSONEq(t, forgedRec.Body.String(), replay.Body.String())

		// The row carries the token's own expiry and owner.
		claims, err := parseSession(session, testSecret)
		require.NoError(t, err)
		var row models.RevokedSession
		require.NoError(t, a.handle.RW.WithContext(t.Context()).
			First(&row, "jti = ?", claims.ID).Error)
		assert.Equal(t, claims.Subject, row.UserID.String())
		assert.True(t, claims.ExpiresAt.Equal(row.ExpiresAt), "expires_at must equal the token's exp")
		assert.False(t, row.RevokedAt.IsZero(), "revoked_at must default to now()")
	})

	t.Run("second logout with the same cookie succeeds", func(t *testing.T) {
		t.Parallel()
		a, e := newSessionTestServer(t)
		tok, err := mintSession(uuid.New(), uuid.New(), testSecret, time.Hour)
		require.NoError(t, err)

		assert.Equal(t, http.StatusNoContent, postLogout(e, tok).Code)
		assert.Equal(t, http.StatusNoContent, postLogout(e, tok).Code)
		assert.Equal(t, int64(1), countRevocations(t, a))
	})

	t.Run("malformed cookie logs out without a row", func(t *testing.T) {
		t.Parallel()
		a, e := newSessionTestServer(t)

		rec := postLogout(e, "not-a-jwt")
		assert.Equal(t, http.StatusNoContent, rec.Code)
		assert.True(t, sessionCookieCleared(rec))
		assert.Equal(t, int64(0), countRevocations(t, a))
	})
}

func TestSessionWithoutJTIStillAuthenticates(t *testing.T) {
	t.Parallel()
	a, e := newSessionTestServer(t)
	org := seed.SeedOrg(t, a.handle, models.Organization{})
	user := seed.SeedUser(t, a.handle, models.User{OrganizationID: org.ID})

	// An unrelated revocation must not affect the legacy token.
	seed.SeedRevokedSession(t, a.handle, models.RevokedSession{UserID: user.ID})

	legacy := mintLegacySession(t, user.ID, org.ID, time.Hour)
	assert.Equal(t, http.StatusOK, getMe(e, legacy).Code)
}

func TestDeleteExpiredRevocations(t *testing.T) {
	t.Parallel()
	handle := testutil.NewPostgresDB(t)
	now := time.Now()

	expired := seed.SeedRevokedSession(t, handle, models.RevokedSession{ExpiresAt: now.Add(-time.Minute)})
	live := seed.SeedRevokedSession(t, handle, models.RevokedSession{ExpiresAt: now.Add(time.Hour)})

	n, err := deleteExpiredRevocations(t.Context(), handle.RW, now)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	var remaining []models.RevokedSession
	require.NoError(t, handle.RW.WithContext(t.Context()).Find(&remaining).Error)
	require.Len(t, remaining, 1)
	assert.Equal(t, live.JTI, remaining[0].JTI)
	assert.NotEqual(t, expired.JTI, remaining[0].JTI)

	// A revoked-but-live jti still blocks its token after the sweep.
	revoked, err := dbRevocations{handle: handle}.isRevoked(t.Context(), live.JTI)
	require.NoError(t, err)
	assert.True(t, revoked)
}
