package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
	"github.com/ClappFormOrg/SaidKick/API/internal/config"
	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/vault"
	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
)

// The auth counters are process-wide, so the tests in this file do not call
// t.Parallel: they run before any parallel test in the package resumes, and
// the deltas they assert stay exact.

var authMethods = []string{metrics.AuthMethodOIDC, metrics.AuthMethodAPIKey, metrics.AuthMethodDev, metrics.AuthMethodNone}

var authReasons = []string{
	metrics.AuthReasonInvalidSignature, metrics.AuthReasonExpired, metrics.AuthReasonRevoked,
	metrics.AuthReasonUnknownKey, metrics.AuthReasonNoCredential, metrics.AuthReasonMalformed,
	metrics.AuthReasonNoRoles,
}

// authSnapshot holds every attempt and failure counter.
type authSnapshot map[string]uint64

func snapshotAuth() authSnapshot {
	s := authSnapshot{}
	for _, m := range authMethods {
		for _, o := range []string{metrics.OutcomeSuccess, metrics.OutcomeFailure} {
			s["attempt/"+m+"/"+o] = metrics.AuthAttemptsTotal(m, o).Get()
		}
		for _, r := range authReasons {
			s["failure/"+m+"/"+r] = metrics.AuthFailuresTotal(m, r).Get()
		}
	}
	return s
}

// delta returns the counters that moved since s.
func (s authSnapshot) delta() map[string]uint64 {
	out := map[string]uint64{}
	for k, v := range snapshotAuth() {
		if d := v - s[k]; d > 0 {
			out[k] = d
		}
	}
	return out
}

func attemptFailed(method, reason string) map[string]uint64 {
	return map[string]uint64{
		"attempt/" + method + "/failure":   1,
		"failure/" + method + "/" + reason: 1,
	}
}

func attemptSucceeded(method string) map[string]uint64 {
	return map[string]uint64{"attempt/" + method + "/success": 1}
}

// ─── API keys ────────────────────────────────────────────────────────────────

func TestWellFormedAPIKey(t *testing.T) {
	assert.True(t, wellFormedAPIKey(newRawAPIKey(t)))
	assert.False(t, wellFormedAPIKey("sak_short"))
	assert.False(t, wellFormedAPIKey("sak_"+string(make([]byte, 43))))
	assert.False(t, wellFormedAPIKey("sak_"+base64.StdEncoding.EncodeToString(make([]byte, 32))), "padded base64 is not a minted key")
}

func newRawAPIKey(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 32)
	_, err := rand.Read(raw)
	require.NoError(t, err)
	return apiKeyPrefix + base64.RawURLEncoding.EncodeToString(raw)
}

func TestAPIKeyMalformedIsCountedWithoutLookup(t *testing.T) {
	mw := (&APIKeyAuthenticator{}).Middleware // nil Handle: a lookup would panic
	before := snapshotAuth()
	_, called, err := runMiddleware(t, mw, "Bearer sak_short", nil)
	assert.False(t, called)
	assertHTTPStatus(t, err, http.StatusUnauthorized)
	assert.Equal(t, attemptFailed(metrics.AuthMethodAPIKey, metrics.AuthReasonMalformed), before.delta())
}

func TestAnonymousRequestIsNotAnAttempt(t *testing.T) {
	before := snapshotAuth()
	for _, authz := range []string{"", "Basic dXNlcjpwYXNz", "Bearer not-a-sak-token"} {
		_, called, err := runMiddleware(t, (&APIKeyAuthenticator{}).Middleware, authz, nil)
		require.NoError(t, err)
		assert.True(t, called)
	}
	assert.Empty(t, before.delta(), "presenting no API key is not an attempt")

	// Where a principal is required, the missing credential is counted, but
	// still not as an attempt.
	_, _, err := runMiddleware(t, RequireAuthenticated, "", nil)
	assertHTTPStatus(t, err, http.StatusUnauthorized)
	_, _, err = runMiddleware(t, RequireAnyRole(models.RoleDeveloper), "", nil)
	assertHTTPStatus(t, err, http.StatusUnauthorized)
	assert.Equal(t, map[string]uint64{"failure/none/no_credential": 2}, before.delta())
}

// apiKeyFixture seeds a developer and returns a function that inserts a key
// for them and returns its token.
type apiKeyFixture struct {
	handle *db.Handle
	owner  models.User
}

func newAPIKeyMetricsFixture(t *testing.T) apiKeyFixture {
	t.Helper()
	handle := testutil.NewPostgresDB(t)
	org := seed.SeedOrg(t, handle, models.Organization{})
	owner := seed.SeedUser(t, handle, models.User{OrganizationID: org.ID})
	seed.SeedRoleAssignment(t, handle, models.RoleAssignment{UserID: owner.ID, Role: models.RoleDeveloper})
	return apiKeyFixture{handle: handle, owner: owner}
}

func (f apiKeyFixture) key(t *testing.T, roles []string, expiresAt, revokedAt *time.Time) string {
	t.Helper()
	token := newRawAPIKey(t)
	sum := sha256.Sum256([]byte(token))
	require.NoError(t, f.handle.RW.Create(&models.APIKey{
		Name: "metrics", Prefix: token[:12], KeyHash: hex.EncodeToString(sum[:]),
		OwnerUserID: f.owner.ID, Roles: roles, ExpiresAt: expiresAt, RevokedAt: revokedAt,
	}).Error)
	return token
}

func TestAPIKeyOutcomesAreCountedByReason(t *testing.T) {
	f := newAPIKeyMetricsFixture(t)
	mw := (&APIKeyAuthenticator{Handle: f.handle}).Middleware
	future := time.Now().Add(24 * time.Hour)
	past := time.Now().Add(-time.Hour)
	dev := []string{string(models.RoleDeveloper)}

	cases := []struct {
		name   string
		token  string
		status int
		want   map[string]uint64
	}{
		{"valid", f.key(t, dev, &future, nil), 0, attemptSucceeded(metrics.AuthMethodAPIKey)},
		{"expired", f.key(t, dev, &past, nil), http.StatusUnauthorized, attemptFailed(metrics.AuthMethodAPIKey, metrics.AuthReasonExpired)},
		{"revoked", f.key(t, dev, &future, &past), http.StatusUnauthorized, attemptFailed(metrics.AuthMethodAPIKey, metrics.AuthReasonRevoked)},
		{"unknown", newRawAPIKey(t), http.StatusUnauthorized, attemptFailed(metrics.AuthMethodAPIKey, metrics.AuthReasonUnknownKey)},
		{"scope outside owner roles", f.key(t, []string{string(models.RolePlatformAdmin)}, &future, nil), http.StatusUnauthorized,
			attemptFailed(metrics.AuthMethodAPIKey, metrics.AuthReasonNoRoles)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := snapshotAuth()
			seen, called, err := runMiddleware(t, mw, "Bearer "+tc.token, nil)
			if tc.status == 0 {
				require.NoError(t, err)
				assert.True(t, called)
				assert.NotNil(t, seen)
			} else {
				assertHTTPStatus(t, err, tc.status)
			}
			assert.Equal(t, tc.want, before.delta())
		})
	}

	testutil.RequireNoIdentifyingLabels(t)
}

// ─── Dev mode ────────────────────────────────────────────────────────────────

func TestDevModeAttemptsAreCounted(t *testing.T) {
	before := snapshotAuth()
	runDevMiddleware(t, &DevModeAuthenticator{}, "not-a-uuid")
	assert.Equal(t, attemptFailed(metrics.AuthMethodDev, metrics.AuthReasonMalformed), before.delta())

	before = snapshotAuth()
	runDevMiddleware(t, &DevModeAuthenticator{}, "")
	assert.Empty(t, before.delta(), "no header is not an attempt")
}

func TestDevModeLookupsAreCounted(t *testing.T) {
	handle := testutil.NewPostgresDB(t)
	org := seed.SeedOrg(t, handle, models.Organization{})
	user := seed.SeedUser(t, handle, models.User{OrganizationID: org.ID})
	a := &DevModeAuthenticator{Handle: handle}

	before := snapshotAuth()
	runDevMiddleware(t, a, uuid.NewString())
	assert.Equal(t, attemptFailed(metrics.AuthMethodDev, metrics.AuthReasonUnknownKey), before.delta())

	before = snapshotAuth()
	runDevMiddleware(t, a, user.ID.String())
	assert.Equal(t, attemptSucceeded(metrics.AuthMethodDev), before.delta())
}

func runDevMiddleware(t *testing.T, a *DevModeAuthenticator, userID string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	if userID != "" {
		req.Header.Set("X-Dev-User-ID", userID)
	}
	c := echo.New().NewContext(req, httptest.NewRecorder())
	require.NoError(t, a.Middleware(func(echo.Context) error { return nil })(c))
}

// ─── OIDC callback ───────────────────────────────────────────────────────────

// newBareOIDCAuthenticator has no database: enough for the callback checks
// that run before the organisation lookup.
func newBareOIDCAuthenticator() *echo.Echo {
	a := NewOIDCAuthenticator(nil, audit.NoopLogger{}, config.OIDCConfig{SessionSecret: testSecret, SessionTTL: time.Hour},
		NewSecretRefStore(vault.NullClient{}, "", true))
	e := echo.New()
	a.RegisterRoutes(e)
	return e
}

func TestOIDCCallbackFailuresAreCountedByReason(t *testing.T) {
	e := newBareOIDCAuthenticator()
	orgID := uuid.New()
	forged, err := mintStateToken(orgID, "nonce", "cv", "/", "a-different-secret-of-32-bytes!!")
	require.NoError(t, err)

	cases := []struct {
		name string
		req  *http.Request
		want map[string]uint64
	}{
		{"missing code", httptest.NewRequest(http.MethodGet, "/auth/callback?state=x", http.NoBody),
			attemptFailed(metrics.AuthMethodOIDC, metrics.AuthReasonMalformed)},
		{"missing state cookie", callbackRequest("code", "nonce", ""),
			attemptFailed(metrics.AuthMethodOIDC, metrics.AuthReasonNoCredential)},
		{"forged state", callbackRequest("code", "nonce", forged),
			attemptFailed(metrics.AuthMethodOIDC, metrics.AuthReasonInvalidSignature)},
		{"expired state", callbackRequest("code", "nonce", mintExpiredStateCookie(t, orgID)),
			attemptFailed(metrics.AuthMethodOIDC, metrics.AuthReasonExpired)},
		{"state mismatch", callbackRequest("code", "TAMPERED", mintTestStateCookie(t, orgID, "nonce")),
			attemptFailed(metrics.AuthMethodOIDC, metrics.AuthReasonInvalidSignature)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := snapshotAuth()
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, tc.req)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Equal(t, tc.want, before.delta())
		})
	}

	// The redirect to the IdP is not an attempt.
	before := snapshotAuth()
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/auth/login", http.NoBody))
	assert.Empty(t, before.delta())
}

func TestOIDCCallbackLoginIsCounted(t *testing.T) {
	const clientID = "metrics-client"
	idp := newMockIDP(t, clientID, "metrics-sub", "metrics@gemeente.nl")
	a, e := newTestAuthenticator(t)
	org := seedOIDCOrg(t, a, idp.URL, clientID, "secret")

	before := snapshotAuth()
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, callbackRequest("code", "nonce", mintTestStateCookie(t, org.ID, "nonce")))
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	assert.Equal(t, attemptSucceeded(metrics.AuthMethodOIDC), before.delta())

	testutil.RequireNoIdentifyingLabels(t)
}

func TestOIDCCallbackRejectedIDTokenIsCounted(t *testing.T) {
	// The IdP issues tokens for another client, so verification fails on
	// the audience.
	idp := newMockIDP(t, "another-client", "metrics-sub-2", "metrics2@gemeente.nl")
	a, e := newTestAuthenticator(t)
	org := seedOIDCOrg(t, a, idp.URL, "metrics-client-2", "secret")

	before := snapshotAuth()
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, callbackRequest("code", "nonce", mintTestStateCookie(t, org.ID, "nonce")))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, attemptFailed(metrics.AuthMethodOIDC, metrics.AuthReasonInvalidSignature), before.delta())
}

// ─── Authorisation ───────────────────────────────────────────────────────────

func TestRequireAnyRoleCountsDenialByRoutePattern(t *testing.T) {
	const route = "/api/v1/apps/:slug/submit"
	e := echo.New()
	asDeveloper := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			p := &Principal{UserID: uuid.New(), Roles: map[models.Role]struct{}{models.RoleDeveloper: {}}}
			c.SetRequest(c.Request().WithContext(withPrincipal(c.Request().Context(), p)))
			return next(c)
		}
	}
	e.POST(route, func(c echo.Context) error { return c.NoContent(http.StatusOK) }, asDeveloper, RequireAnyRole(models.RolePlatformAdmin))

	before := metrics.AuthzDenialsTotal(route).Get()
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/apps/govchat-"+uuid.NewString()+"/submit", http.NoBody))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, before+1, metrics.AuthzDenialsTotal(route).Get())

	// The concrete path never becomes a label value.
	testutil.RequireNoIdentifyingLabels(t)
}
