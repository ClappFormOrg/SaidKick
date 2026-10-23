package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
)

// runMiddleware drives mw with a request carrying the given Authorization
// header (omitted when empty) and an optional pre-attached principal. It
// returns the principal the downstream handler observed, whether that
// handler ran, and the middleware's error.
func runMiddleware(t *testing.T, mw echo.MiddlewareFunc, authz string, pre *Principal) (seen *Principal, called bool, err error) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	if authz != "" {
		req.Header.Set(echo.HeaderAuthorization, authz)
	}
	if pre != nil {
		req = req.WithContext(withPrincipal(req.Context(), pre))
	}
	c := e.NewContext(req, httptest.NewRecorder())

	err = mw(func(c echo.Context) error {
		called = true
		seen = PrincipalFromContext(c.Request().Context())
		return nil
	})(c)
	return seen, called, err
}

// TestAPIKeyMiddlewarePassThrough covers every path that must not touch the
// database. The authenticator has a nil Handle, so reaching the lookup
// would panic and fail the test.
func TestAPIKeyMiddlewarePassThrough(t *testing.T) {
	mw := (&APIKeyAuthenticator{}).Middleware

	cases := []struct {
		name  string
		authz string
	}{
		{"no header", ""},
		{"basic scheme", "Basic dXNlcjpwYXNz"},
		{"bearer non-sak token", "Bearer not-a-sak-token"},
		{"lowercase bearer scheme", "bearer sak_abc"},
		{"sak key without scheme", "sak_abc"},
		{"bearer with empty token", "Bearer "},
		{"prefix with wrong case", "Bearer SAK_abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seen, called, err := runMiddleware(t, mw, tc.authz, nil)
			require.NoError(t, err)
			assert.True(t, called, "request must continue to the next handler")
			assert.Nil(t, seen, "request must stay anonymous")
		})
	}
}

func TestAPIKeyMiddlewareShortCircuitsExistingPrincipal(t *testing.T) {
	mw := (&APIKeyAuthenticator{}).Middleware
	pre := &Principal{UserID: uuid.New(), Email: "a@example.com"}

	// A sak_ token is present, but a principal from an earlier authenticator
	// wins and the DB is never consulted (nil Handle).
	seen, called, err := runMiddleware(t, mw, "Bearer sak_whatever", pre)
	require.NoError(t, err)
	assert.True(t, called)
	assert.Same(t, pre, seen)
}

func TestScopedRoles(t *testing.T) {
	live := func(roles ...models.Role) []models.RoleAssignment {
		rows := make([]models.RoleAssignment, 0, len(roles))
		for _, r := range roles {
			rows = append(rows, models.RoleAssignment{Role: r})
		}
		return rows
	}
	set := func(roles ...models.Role) map[models.Role]struct{} {
		m := make(map[models.Role]struct{}, len(roles))
		for _, r := range roles {
			m[r] = struct{}{}
		}
		return m
	}

	cases := []struct {
		name  string
		live  []models.RoleAssignment
		scope []string
		want  map[models.Role]struct{}
	}{
		{
			name:  "owner's extra roles do not leak into the key",
			live:  live(models.RoleDeveloper, models.RolePlatformAdmin),
			scope: []string{"developer"},
			want:  set(models.RoleDeveloper),
		},
		{
			name:  "scope is a ceiling, not a grant",
			live:  live(models.RoleDeveloper),
			scope: []string{"developer", "platform_admin"},
			want:  set(models.RoleDeveloper),
		},
		{
			name:  "role revoked from owner is dropped",
			live:  live(models.RoleAuditor),
			scope: []string{"developer"},
			want:  set(),
		},
		{
			name:  "empty scope grants nothing",
			live:  live(models.RoleDeveloper),
			scope: nil,
			want:  set(),
		},
		{
			name:  "full overlap",
			live:  live(models.RoleDeveloper, models.RoleDeployer),
			scope: []string{"deployer", "developer"},
			want:  set(models.RoleDeveloper, models.RoleDeployer),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, scopedRoles(tc.live, tc.scope))
		})
	}
}

func TestPrincipalHas(t *testing.T) {
	p := &Principal{Roles: map[models.Role]struct{}{
		models.RoleDeveloper: {},
	}}

	assert.True(t, p.Has(models.RoleDeveloper))
	assert.True(t, p.Has(models.RolePlatformAdmin, models.RoleDeveloper))
	assert.False(t, p.Has(models.RolePlatformAdmin))
	assert.False(t, p.Has())

	var nilP *Principal
	assert.False(t, nilP.Has(models.RoleDeveloper), "nil principal holds no roles")
}

func TestRequireAuthenticated(t *testing.T) {
	_, called, err := runMiddleware(t, RequireAuthenticated, "", nil)
	assert.False(t, called)
	assertHTTPStatus(t, err, http.StatusUnauthorized)

	_, called, err = runMiddleware(t, RequireAuthenticated, "", &Principal{UserID: uuid.New()})
	require.NoError(t, err)
	assert.True(t, called)
}

func TestRequireAnyRole(t *testing.T) {
	mw := RequireAnyRole(models.RolePlatformAdmin)

	_, called, err := runMiddleware(t, mw, "", nil)
	assert.False(t, called)
	assertHTTPStatus(t, err, http.StatusUnauthorized)

	dev := &Principal{Roles: map[models.Role]struct{}{models.RoleDeveloper: {}}}
	_, called, err = runMiddleware(t, mw, "", dev)
	assert.False(t, called)
	assertHTTPStatus(t, err, http.StatusForbidden)

	admin := &Principal{Roles: map[models.Role]struct{}{models.RolePlatformAdmin: {}}}
	_, called, err = runMiddleware(t, mw, "", admin)
	require.NoError(t, err)
	assert.True(t, called)
}

func assertHTTPStatus(t *testing.T, err error, want int) {
	t.Helper()
	var he *echo.HTTPError
	require.ErrorAs(t, err, &he)
	assert.Equal(t, want, he.Code)
}
