package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ClappFormOrg/SaidKick/API/internal/config"
)

// fakeRevocations is an in-memory revocationStore for tests that must not
// touch Postgres.
type fakeRevocations struct {
	revoked   map[uuid.UUID]time.Time // jti -> expires_at
	users     map[uuid.UUID]uuid.UUID // jti -> user_id
	lookupErr error
	revokeErr error
	lookups   int
}

func newFakeRevocations() *fakeRevocations {
	return &fakeRevocations{
		revoked: map[uuid.UUID]time.Time{},
		users:   map[uuid.UUID]uuid.UUID{},
	}
}

func (f *fakeRevocations) isRevoked(_ context.Context, jti uuid.UUID) (bool, error) {
	f.lookups++
	if f.lookupErr != nil {
		return false, f.lookupErr
	}
	_, ok := f.revoked[jti]
	return ok, nil
}

func (f *fakeRevocations) revoke(_ context.Context, jti, userID uuid.UUID, expiresAt time.Time) error {
	if f.revokeErr != nil {
		return f.revokeErr
	}
	f.revoked[jti] = expiresAt
	f.users[jti] = userID
	return nil
}

// mintLegacySession signs a session token the way it was minted before the
// jti claim existed.
func mintLegacySession(t *testing.T, userID, orgID uuid.UUID, ttl time.Duration) string {
	t.Helper()
	now := time.Now()
	return signHS(t, jwt.SigningMethodHS256, sessionClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
		OrgID: orgID,
	}, testSecret)
}

// jtiOf returns the parsed jti of a session token minted by mintSession.
func jtiOf(t *testing.T, token string) uuid.UUID {
	t.Helper()
	claims, err := parseSession(token, testSecret)
	require.NoError(t, err)
	jti, err := uuid.Parse(claims.ID)
	require.NoError(t, err)
	return jti
}

func TestValidateSessionRevocation(t *testing.T) {
	userID, orgID := uuid.New(), uuid.New()

	t.Run("live session is accepted", func(t *testing.T) {
		store := newFakeRevocations()
		tok, err := mintSession(userID, orgID, testSecret, time.Hour)
		require.NoError(t, err)

		claims, err := validateSession(t.Context(), tok, testSecret, store)
		require.NoError(t, err)
		assert.Equal(t, userID.String(), claims.Subject)
		assert.Equal(t, 1, store.lookups)
	})

	t.Run("revoked session is rejected", func(t *testing.T) {
		store := newFakeRevocations()
		tok, err := mintSession(userID, orgID, testSecret, time.Hour)
		require.NoError(t, err)
		store.revoked[jtiOf(t, tok)] = time.Now().Add(time.Hour)

		claims, err := validateSession(t.Context(), tok, testSecret, store)
		require.ErrorIs(t, err, errSessionRevoked)
		assert.Nil(t, claims)
	})

	t.Run("token without jti is accepted without a lookup", func(t *testing.T) {
		store := newFakeRevocations()
		tok := mintLegacySession(t, userID, orgID, time.Hour)

		claims, err := validateSession(t.Context(), tok, testSecret, store)
		require.NoError(t, err)
		assert.Empty(t, claims.ID)
		assert.Equal(t, 0, store.lookups, "a token with no jti is not revokable")
	})

	t.Run("lookup failure rejects the token", func(t *testing.T) {
		store := newFakeRevocations()
		store.lookupErr = errors.New("connection refused")
		tok, err := mintSession(userID, orgID, testSecret, time.Hour)
		require.NoError(t, err)

		claims, err := validateSession(t.Context(), tok, testSecret, store)
		require.ErrorIs(t, err, errRevocationLookup)
		assert.Nil(t, claims)
	})

	t.Run("signed token with non-UUID jti is rejected", func(t *testing.T) {
		store := newFakeRevocations()
		tok := signHS(t, jwt.SigningMethodHS256, sessionClaims{
			RegisteredClaims: jwt.RegisteredClaims{
				ID:        "not-a-uuid",
				Subject:   userID.String(),
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			},
			OrgID: orgID,
		}, testSecret)

		claims, err := validateSession(t.Context(), tok, testSecret, store)
		require.Error(t, err)
		assert.Nil(t, claims)
		assert.Equal(t, 0, store.lookups)
	})

	t.Run("invalid signature is rejected before any lookup", func(t *testing.T) {
		store := newFakeRevocations()
		tok, err := mintSession(userID, orgID, otherSecret, time.Hour)
		require.NoError(t, err)

		claims, err := validateSession(t.Context(), tok, testSecret, store)
		require.Error(t, err)
		assert.Nil(t, claims)
		assert.Equal(t, 0, store.lookups)
	})
}

// newUnitAuthenticator builds an OIDCAuthenticator with no database. Only
// paths that stop before the user lookup may run against it.
func newUnitAuthenticator(store revocationStore) *OIDCAuthenticator {
	return &OIDCAuthenticator{
		cfg:         config.OIDCConfig{SessionSecret: testSecret, SessionTTL: time.Hour},
		revocations: store,
	}
}

// sessionCookieCleared reports whether rec carries a Set-Cookie that deletes
// the session cookie.
func sessionCookieCleared(rec *httptest.ResponseRecorder) bool {
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName && c.Value == "" && c.MaxAge < 0 {
			return true
		}
	}
	return false
}

func TestMiddlewareRevokedSession(t *testing.T) {
	userID, orgID := uuid.New(), uuid.New()

	run := func(t *testing.T, store revocationStore, token string) (*httptest.ResponseRecorder, bool) {
		t.Helper()
		a := newUnitAuthenticator(store)
		req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(req, rec)

		called := false
		err := a.Middleware(func(c echo.Context) error {
			called = true
			assert.Nil(t, PrincipalFromContext(c.Request().Context()))
			return nil
		})(c)
		require.NoError(t, err)
		return rec, called
	}

	t.Run("revoked session is anonymous and cookie is cleared", func(t *testing.T) {
		store := newFakeRevocations()
		tok, err := mintSession(userID, orgID, testSecret, time.Hour)
		require.NoError(t, err)
		store.revoked[jtiOf(t, tok)] = time.Now().Add(time.Hour)

		rec, called := run(t, store, tok)
		assert.True(t, called)
		assert.True(t, sessionCookieCleared(rec))
	})

	t.Run("lookup failure is anonymous but keeps the cookie", func(t *testing.T) {
		store := newFakeRevocations()
		store.lookupErr = errors.New("connection refused")
		tok, err := mintSession(userID, orgID, testSecret, time.Hour)
		require.NoError(t, err)

		rec, called := run(t, store, tok)
		assert.True(t, called)
		assert.False(t, sessionCookieCleared(rec))
	})
}

func TestHandleLogoutRevocation(t *testing.T) {
	userID, orgID := uuid.New(), uuid.New()

	logout := func(t *testing.T, store revocationStore, cookie string) *httptest.ResponseRecorder {
		t.Helper()
		e := echo.New()
		newUnitAuthenticator(store).RegisterRoutes(e)
		req := httptest.NewRequest(http.MethodPost, "/auth/logout", http.NoBody)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: cookie})
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}

	t.Run("valid session is revoked until its own expiry", func(t *testing.T) {
		store := newFakeRevocations()
		tok, err := mintSession(userID, orgID, testSecret, time.Hour)
		require.NoError(t, err)
		claims, err := parseSession(tok, testSecret)
		require.NoError(t, err)

		rec := logout(t, store, tok)
		assert.Equal(t, http.StatusNoContent, rec.Code)
		assert.True(t, sessionCookieCleared(rec))

		jti := jtiOf(t, tok)
		require.Contains(t, store.revoked, jti)
		assert.True(t, claims.ExpiresAt.Equal(store.revoked[jti]), "expires_at must be the token's exp")
		assert.Equal(t, userID, store.users[jti])
	})

	cases := []struct {
		name   string
		cookie func(t *testing.T) string
	}{
		{"no cookie", func(*testing.T) string { return "" }},
		{"malformed cookie", func(*testing.T) string { return "not-a-jwt" }},
		{"cookie signed with another secret", func(t *testing.T) string {
			tok, err := mintSession(userID, orgID, otherSecret, time.Hour)
			require.NoError(t, err)
			return tok
		}},
		{"expired cookie", func(t *testing.T) string {
			tok, err := mintSession(userID, orgID, testSecret, -time.Minute)
			require.NoError(t, err)
			return tok
		}},
		{"cookie without jti", func(t *testing.T) string {
			return mintLegacySession(t, userID, orgID, time.Hour)
		}},
		{"cookie with non-UUID subject", func(t *testing.T) string {
			return signHS(t, jwt.SigningMethodHS256, sessionClaims{
				RegisteredClaims: jwt.RegisteredClaims{
					ID:        uuid.NewString(),
					Subject:   "not-a-uuid",
					ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
				},
				OrgID: orgID,
			}, testSecret)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name+" succeeds without a revocation row", func(t *testing.T) {
			store := newFakeRevocations()
			rec := logout(t, store, tc.cookie(t))
			assert.Equal(t, http.StatusNoContent, rec.Code)
			assert.True(t, sessionCookieCleared(rec))
			assert.Empty(t, store.revoked)
		})
	}

	t.Run("store failure still logs out", func(t *testing.T) {
		store := newFakeRevocations()
		store.revokeErr = errors.New("connection refused")
		tok, err := mintSession(userID, orgID, testSecret, time.Hour)
		require.NoError(t, err)

		rec := logout(t, store, tok)
		assert.Equal(t, http.StatusNoContent, rec.Code)
		assert.True(t, sessionCookieCleared(rec))
	})
}

func TestRunSessionCleanupStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	done := make(chan struct{})
	go func() {
		// The handle is never touched: the context is already cancelled
		// and the first tick is an hour away.
		RunSessionCleanup(ctx, nil, time.Hour)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunSessionCleanup did not return after cancel")
	}
}
