// Package auth owns user-identity middleware and role checks for the API.
//
// Three Authenticator implementations are available:
//   - DevModeAuthenticator  — reads an X-Dev-User-ID header; dev only (APP_DEV_MODE=true)
//   - OIDCAuthenticator     — per-org federated session JWT cookies (production)
//   - APIKeyAuthenticator   — Authorization: Bearer sak_… API keys
//
// All authenticators put a *Principal on the request context. Handlers
// read it via auth.PrincipalFromContext(c.Request().Context()).
package auth

import (
	"context"
	"errors"
	"net/http"

	"github.com/ClappFormOrg/SaidKick/API/internal/auth/principalctx"
	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

// AuthMethod records how the caller authenticated for this request.
type AuthMethod string

const (
	AuthMethodSession AuthMethod = "session" // OIDC-issued session cookie
	AuthMethodAPIKey  AuthMethod = "api_key" // Authorization: Bearer sak_...
	AuthMethodDev     AuthMethod = "dev"     // dev-mode header authentication
)

// Principal is the authenticated identity attached to every request that
// passed an authenticator. Anonymous requests have a nil Principal.
type Principal struct {
	UserID         uuid.UUID
	OrganizationID uuid.UUID
	Email          string
	Roles          map[models.Role]struct{}
	// AuthMethod is never empty for a principal set by an authenticator.
	AuthMethod AuthMethod
}

// Has reports whether the principal holds any of the given roles.
func (p *Principal) Has(roles ...models.Role) bool {
	if p == nil {
		return false
	}
	for _, r := range roles {
		if _, ok := p.Roles[r]; ok {
			return true
		}
	}
	return false
}

// ctxKey types the auth package's own context keys. The principal key lives
// in principalctx so authtest can share it.
type ctxKey int

// PrincipalFromContext returns the principal attached to ctx, or nil if
// the request was anonymous (or the authenticator chain did not run).
func PrincipalFromContext(ctx context.Context) *Principal {
	if v, ok := principalctx.From(ctx).(*Principal); ok {
		return v
	}
	return nil
}

// withPrincipal returns a context carrying the given principal.
func withPrincipal(ctx context.Context, p *Principal) context.Context {
	return principalctx.With(ctx, p)
}

// Authenticator resolves the identity of the caller from the HTTP request.
// Implementations should put a *Principal on the request context (or leave
// it nil for anonymous) and return nil for any "could not determine" case
// — errors are reserved for hard failures (e.g. DB unavailable).
type Authenticator interface {
	Middleware(next echo.HandlerFunc) echo.HandlerFunc
}

// DevModeAuthenticator is the V0 implementation. It trusts the dev header
// unconditionally and looks up the user (with active role assignments) in
// the DB. NEVER enable in production — gate via APP_DEV_MODE=true.
type DevModeAuthenticator struct {
	Handle *db.Handle
	Header string
}

// Middleware satisfies Authenticator.
func (a *DevModeAuthenticator) Middleware(next echo.HandlerFunc) echo.HandlerFunc {
	header := a.Header
	if header == "" {
		header = "X-Dev-User-ID"
	}
	return func(c echo.Context) error {
		raw := c.Request().Header.Get(header)
		if raw == "" {
			return next(c)
		}
		uid, err := uuid.Parse(raw)
		if err != nil {
			authFailed(metrics.AuthMethodDev, metrics.AuthReasonMalformed)
			return next(c) // ignore malformed header; treat as anonymous
		}
		var user models.User
		if err := a.Handle.RW.WithContext(c.Request().Context()).
			First(&user, "id = ?", uid).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				authFailed(metrics.AuthMethodDev, metrics.AuthReasonUnknownKey)
			}
			return next(c) // unknown user -> anonymous
		}
		var roleRows []models.RoleAssignment
		if err := a.Handle.RW.WithContext(c.Request().Context()).
			Where("user_id = ? AND revoked_at IS NULL", uid).
			Find(&roleRows).Error; err != nil {
			return next(c)
		}
		authSucceeded(metrics.AuthMethodDev)
		roles := make(map[models.Role]struct{}, len(roleRows))
		for _, r := range roleRows {
			roles[r.Role] = struct{}{}
		}
		p := &Principal{
			UserID:         user.ID,
			OrganizationID: user.OrganizationID,
			Email:          user.Email,
			Roles:          roles,
			AuthMethod:     AuthMethodDev,
		}
		c.SetRequest(c.Request().WithContext(withPrincipal(c.Request().Context(), p)))
		return next(c)
	}
}

// ErrUnauthenticated is returned by RequireAuthenticated when no principal
// is present.
var ErrUnauthenticated = errors.New("auth: authentication required")

// ErrForbidden is returned by RequireRole when the principal lacks the
// required role.
var ErrForbidden = errors.New("auth: forbidden")

// RequireAuthenticated rejects anonymous requests with HTTP 401 and counts
// them as auth_failures_total{reason="no_credential"}.
func RequireAuthenticated(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if PrincipalFromContext(c.Request().Context()) == nil {
			noCredential()
			return echo.NewHTTPError(http.StatusUnauthorized, ErrUnauthenticated.Error())
		}
		return next(c)
	}
}

// RequireAnyRole returns a middleware that allows the request if the
// principal holds at least one of the listed roles. Anonymous requests are
// rejected with 401 first (counted as no_credential), then role check yields
// 403 on mismatch, counted in authz_denials_total by route pattern.
func RequireAnyRole(roles ...models.Role) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			p := PrincipalFromContext(c.Request().Context())
			if p == nil {
				noCredential()
				return echo.NewHTTPError(http.StatusUnauthorized, ErrUnauthenticated.Error())
			}
			if !p.Has(roles...) {
				metrics.AuthzDenialsTotal(c.Path()).Inc()
				return echo.NewHTTPError(http.StatusForbidden, ErrForbidden.Error())
			}
			return next(c)
		}
	}
}
