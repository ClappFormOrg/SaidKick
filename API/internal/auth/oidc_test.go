package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
	"github.com/ClappFormOrg/SaidKick/API/internal/config"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/vault"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
)

const testSecret = "test-session-secret-32bytes!!!!!"

// ─── sanitiseRedirect unit tests ─────────────────────────────────────────────

func TestSanitiseRedirect(t *testing.T) {
	t.Parallel()
	allowed := []string{"http://localhost:3000", "https://app.example.nl"}

	cases := []struct{ raw, want string }{
		{"", "/"},
		{"/dashboard", "/dashboard"},
		{"/dashboard/apps?q=1", "/dashboard/apps?q=1"},
		{"//evil.com", "/"},
		{"http://localhost:3000/auth/complete", "http://localhost:3000/auth/complete"},
		{"http://localhost:3000", "http://localhost:3000"},
		{"https://app.example.nl/dashboard", "https://app.example.nl/dashboard"},
		{"https://evil.example.com/steal", "/"},
		{"http://localhost:3000.evil.com/hijack", "/"},
		{"javascript:alert(1)", "/"},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, sanitiseRedirect(tc.raw, allowed))
		})
	}
}

// ─── OIDC callback test helpers ───────────────────────────────────────────────

// newTestAuthenticator creates an OIDCAuthenticator backed by a real Postgres
// database (testcontainers) and returns the authenticator plus a wired Echo
// instance. The secret store uses v0-inline (dev mode) so tests can store
// secrets without Vault or an encryption key.
func newTestAuthenticator(t *testing.T) (*OIDCAuthenticator, *echo.Echo) {
	t.Helper()
	handle := testutil.NewPostgresDB(t)
	store := NewSecretRefStore(vault.NullClient{}, "", true)
	cfg := config.OIDCConfig{
		SessionSecret:    testSecret,
		SessionTTL:       1 * time.Hour,
		BaseURL:          "http://localhost:8080",
		ProviderCacheTTL: 1 * time.Second,
	}
	a := NewOIDCAuthenticator(handle, audit.NoopLogger{}, cfg, store)
	e := echo.New()
	a.RegisterRoutes(e)
	return a, e
}

// seedOIDCOrg inserts an organisation pre-configured with the given OIDC
// issuer, clientID, and a v0-inline: secret ref (valid in dev mode).
func seedOIDCOrg(t *testing.T, a *OIDCAuthenticator, issuer, clientID, secret string) models.Organization {
	t.Helper()
	ref := "v0-inline:" + secret
	return seed.SeedOrg(t, a.handle, models.Organization{
		Slug:                "oidc-test-org",
		DisplayName:         "OIDC Test Org",
		ContactEmail:        "oidc@example.nl",
		OIDCIssuer:          &issuer,
		OIDCClientID:        &clientID,
		OIDCClientSecretRef: &ref,
	})
}

func ptrBool(b bool) *bool { return &b }

// seedPreProvisionedUser inserts a user whose identity has not yet been claimed
// via OIDC — its external_subject is the "seed:<email>" sentinel that the
// email-fallback link path looks for.
func seedPreProvisionedUser(t *testing.T, a *OIDCAuthenticator, orgID uuid.UUID, email string) models.User {
	t.Helper()
	return seed.SeedUser(t, a.handle, models.User{
		OrganizationID:  orgID,
		ExternalSubject: "seed:" + email,
		Email:           email,
		DisplayName:     "Seeded Admin",
	})
}

// mintTestStateCookie returns a signed state JWT cookie value for the given
// org, with nonce "test-nonce" and post-login redirect "/".
func mintTestStateCookie(t *testing.T, orgID uuid.UUID, nonce string) string {
	t.Helper()
	tok, err := mintStateToken(orgID, nonce, "pkce-verifier", "/", testSecret)
	require.NoError(t, err)
	return tok
}

// mintExpiredStateCookie returns a state JWT that expired 30 seconds ago.
func mintExpiredStateCookie(t *testing.T, orgID uuid.UUID) string {
	t.Helper()
	claims := stateClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-30 * time.Second)),
		},
		State:        "nonce",
		CodeVerifier: "cv",
		OrgID:        orgID,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte(testSecret))
	require.NoError(t, err)
	return signed
}

// callbackRequest builds a GET /auth/callback request with the given query
// params and optional state cookie.
func callbackRequest(code, state, cookieVal string) *http.Request {
	req := httptest.NewRequest(http.MethodGet,
		"/auth/callback?code="+code+"&state="+state, http.NoBody)
	if cookieVal != "" {
		req.AddCookie(&http.Cookie{Name: stateCookieName, Value: cookieVal})
	}
	return req
}

// ─── Mock OIDC provider ───────────────────────────────────────────────────────

// mockIDP is a minimal OIDC provider stub that serves the three endpoints the
// OIDCAuthenticator needs: discovery, token exchange, and JWKS.
type mockIDP struct {
	*httptest.Server
	privKey *rsa.PrivateKey
	keyID   string
	// emailVerified controls the id_token `email_verified` claim: nil omits it
	// entirely (as many IdPs do), otherwise the pointed-to value is emitted.
	emailVerified *bool
}

// newMockIDP starts a test HTTP server acting as an OIDC IdP. It will issue
// ID tokens for the given subject and email when the token endpoint is called.
func newMockIDP(t *testing.T, clientID, sub, email string) *mockIDP {
	t.Helper()

	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	m := &mockIDP{privKey: privKey, keyID: "k1"}
	mux := http.NewServeMux()
	m.Server = httptest.NewServer(mux)
	issuer := m.URL

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                issuer,
			"authorization_endpoint":                issuer + "/auth",
			"token_endpoint":                        issuer + "/token",
			"jwks_uri":                              issuer + "/keys",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})

	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		idToken := m.mintIDToken(t, issuer, clientID, sub, email)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "test-access",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     idToken,
		})
	})

	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		pub := &privKey.PublicKey
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{{
				"kty": "RSA",
				"kid": m.keyID,
				"use": "sig",
				"alg": "RS256",
				"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			}},
		})
	})

	t.Cleanup(m.Close)
	return m
}

func (m *mockIDP) mintIDToken(t *testing.T, issuer, clientID, sub, email string) string {
	t.Helper()
	now := time.Now()
	claims := jwt.MapClaims{
		"iss":   issuer,
		"sub":   sub,
		"aud":   []string{clientID},
		"email": email,
		"name":  "Test User",
		"iat":   now.Unix(),
		"exp":   now.Add(1 * time.Hour).Unix(),
	}
	if m.emailVerified != nil {
		claims["email_verified"] = *m.emailVerified
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = m.keyID
	signed, err := tok.SignedString(m.privKey)
	require.NoError(t, err)
	return signed
}

// ─── TestOIDCCallback ─────────────────────────────────────────────────────────

func TestOIDCCallback(t *testing.T) {
	t.Parallel()

	t.Run("missing_code_or_state_param_returns_400", func(t *testing.T) {
		t.Parallel()
		_, e := newTestAuthenticator(t)

		req := httptest.NewRequest(http.MethodGet, "/auth/callback", http.NoBody)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("missing_state_cookie_returns_400", func(t *testing.T) {
		t.Parallel()
		_, e := newTestAuthenticator(t)

		req := callbackRequest("some-code", "some-nonce", "" /* no cookie */)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("expired_state_cookie_returns_400", func(t *testing.T) {
		t.Parallel()
		a, e := newTestAuthenticator(t)
		org := seedOIDCOrg(t, a, "https://dummy.idp", "cid", "csecret")

		req := callbackRequest("some-code", "nonce", mintExpiredStateCookie(t, org.ID))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("state_mismatch_returns_400", func(t *testing.T) {
		t.Parallel()
		a, e := newTestAuthenticator(t)
		org := seedOIDCOrg(t, a, "https://dummy.idp", "cid", "csecret")

		// Cookie was minted with "correct-nonce" but the request carries a different value.
		cookie := mintTestStateCookie(t, org.ID, "correct-nonce")
		req := callbackRequest("some-code", "TAMPERED-NONCE", cookie)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("happy_path_provisions_user_and_sets_session_cookie", func(t *testing.T) {
		t.Parallel()
		const clientID = "test-client-id"
		const sub = "idp-subject-001"
		const email = "floor@gemeente.nl"
		const nonce = "abc123"

		idp := newMockIDP(t, clientID, sub, email)
		a, e := newTestAuthenticator(t)
		org := seedOIDCOrg(t, a, idp.URL, clientID, "client-secret")

		req := callbackRequest("auth-code", nonce, mintTestStateCookie(t, org.ID, nonce))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		// Successful login must redirect (302).
		require.Equal(t, http.StatusFound, rec.Code, "expected redirect after login; body: %s", rec.Body)

		// Session cookie must be present and non-empty.
		var session *http.Cookie
		for _, c := range rec.Result().Cookies() {
			if c.Name == sessionCookieName {
				session = c
				break
			}
		}
		require.NotNil(t, session, "saidkick_session cookie must be set")
		assert.NotEmpty(t, session.Value)
		assert.True(t, session.HttpOnly, "session cookie must be HttpOnly")

		// State cookie must be cleared (MaxAge = -1 or 0 with empty value).
		for _, c := range rec.Result().Cookies() {
			if c.Name == stateCookieName {
				assert.LessOrEqual(t, c.MaxAge, 0, "state cookie must be cleared after login")
			}
		}

		// User must have been JIT-provisioned in the database.
		ctx := t.Context()
		var user models.User
		err := a.handle.RW.WithContext(ctx).
			Where("organization_id = ? AND external_subject = ?", org.ID, sub).
			First(&user).Error
		require.NoError(t, err, "user must exist in DB after JIT provisioning")
		assert.Equal(t, email, user.Email)
		assert.NotNil(t, user.LastLoginAt, "last_login_at must be set on first login")
	})

	// AUTHNZ-001 regression: the email-fallback link to a pre-seeded account
	// must only fire when the IdP asserts email_verified=true, otherwise a
	// spoofable/editable email could hijack a pre-provisioned (possibly
	// privileged) account.
	t.Run("seed_link_fires_only_when_email_verified", func(t *testing.T) {
		t.Parallel()
		const clientID = "seed-client"
		const sub = "real-idp-sub"
		const email = "seeded.admin@gemeente.nl"
		const nonce = "seed-nonce"

		idp := newMockIDP(t, clientID, sub, email)
		idp.emailVerified = ptrBool(true)
		a, e := newTestAuthenticator(t)
		org := seedOIDCOrg(t, a, idp.URL, clientID, "secret")

		ctx := t.Context()
		seeded := seedPreProvisionedUser(t, a, org.ID, email)

		req := callbackRequest("code", nonce, mintTestStateCookie(t, org.ID, nonce))
		e.ServeHTTP(httptest.NewRecorder(), req)

		// Exactly one row for this email: the seeded row, relinked in place.
		var count int64
		require.NoError(t, a.handle.RW.WithContext(ctx).
			Model(&models.User{}).
			Where("organization_id = ? AND email = ?", org.ID, email).
			Count(&count).Error)
		assert.Equal(t, int64(1), count, "verified seed link must reuse the seeded row, not duplicate")

		var linked models.User
		require.NoError(t, a.handle.RW.WithContext(ctx).
			First(&linked, "id = ?", seeded.ID).Error)
		assert.Equal(t, sub, linked.ExternalSubject,
			"seeded row must be rebound to the caller's IdP sub")
	})

	t.Run("seed_link_refused_when_email_unverified", func(t *testing.T) {
		t.Parallel()
		const clientID = "seed-client-2"
		const sub = "attacker-idp-sub"
		const email = "seeded.admin2@gemeente.nl"
		const nonce = "seed-nonce-2"

		idp := newMockIDP(t, clientID, sub, email)
		idp.emailVerified = ptrBool(false) // attacker's IdP does not verify email
		a, e := newTestAuthenticator(t)
		org := seedOIDCOrg(t, a, idp.URL, clientID, "secret")

		ctx := t.Context()
		seeded := seedPreProvisionedUser(t, a, org.ID, email)

		req := callbackRequest("code", nonce, mintTestStateCookie(t, org.ID, nonce))
		e.ServeHTTP(httptest.NewRecorder(), req)

		// The seeded (privileged) row must be untouched — no hijack.
		var seededAfter models.User
		require.NoError(t, a.handle.RW.WithContext(ctx).
			First(&seededAfter, "id = ?", seeded.ID).Error)
		assert.Equal(t, "seed:"+email, seededAfter.ExternalSubject,
			"unverified email must NOT relink the seeded account")

		// The caller is instead JIT-provisioned as a distinct, separate row.
		var attacker models.User
		require.NoError(t, a.handle.RW.WithContext(ctx).
			Where("organization_id = ? AND external_subject = ?", org.ID, sub).
			First(&attacker).Error)
		assert.NotEqual(t, seeded.ID, attacker.ID,
			"unverified caller must get a fresh row, not the seeded one")
	})

	t.Run("second_login_updates_profile_not_duplicates", func(t *testing.T) {
		t.Parallel()
		const clientID = "client-x"
		const sub = "returning-user-sub"
		const nonce = "nonce-x"

		idp := newMockIDP(t, clientID, sub, "original@example.nl")
		a, e := newTestAuthenticator(t)
		org := seedOIDCOrg(t, a, idp.URL, clientID, "secret-x")

		ctx := t.Context()

		// First login.
		req1 := callbackRequest("code-1", nonce, mintTestStateCookie(t, org.ID, nonce))
		e.ServeHTTP(httptest.NewRecorder(), req1)

		// Count users after first login.
		var count1 int64
		require.NoError(t, a.handle.RW.WithContext(ctx).
			Model(&models.User{}).
			Where("organization_id = ? AND external_subject = ?", org.ID, sub).
			Count(&count1).Error)
		assert.Equal(t, int64(1), count1)

		// Second login with updated email from IdP.
		idp2 := newMockIDP(t, clientID, sub, "updated@example.nl")
		// Point the org to the new IdP so the updated email is returned.
		newIssuer := idp2.URL
		require.NoError(t, a.handle.RW.WithContext(ctx).
			Model(&org).Update("oidc_issuer", newIssuer).Error)
		a.InvalidateOIDCCache(org.ID)

		req2 := callbackRequest("code-2", nonce, mintTestStateCookie(t, org.ID, nonce))
		e.ServeHTTP(httptest.NewRecorder(), req2)

		// Still exactly one user record.
		var count2 int64
		require.NoError(t, a.handle.RW.WithContext(ctx).
			Model(&models.User{}).
			Where("organization_id = ? AND external_subject = ?", org.ID, sub).
			Count(&count2).Error)
		assert.Equal(t, int64(1), count2, "second login must not duplicate the user row")

		// Email must have been updated.
		var user models.User
		require.NoError(t, a.handle.RW.WithContext(ctx).
			Where("organization_id = ? AND external_subject = ?", org.ID, sub).
			First(&user).Error)
		assert.Equal(t, "updated@example.nl", user.Email, "user email must be updated on re-login")
	})
}
