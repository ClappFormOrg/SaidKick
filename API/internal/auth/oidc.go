package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
	"github.com/ClappFormOrg/SaidKick/API/internal/config"
	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"golang.org/x/oauth2"
	"gorm.io/gorm"
)

// OIDCAuthenticator is the Sprint 3 production authenticator. It federates
// identity per-org via the OIDC issuer configured on each Organisation row.
// The authorization code flow with PKCE (S256) is used throughout; the
// resulting session is a short-lived HS256-signed JWT cookie.
//
// Boot condition: requires config.OIDCConfig.Configured() == true AND
// APP_DEV_MODE=false. server.New() enforces mutual exclusion with
// DevModeAuthenticator.
type OIDCAuthenticator struct {
	handle    *db.Handle
	auditLog  audit.Logger
	cfg       config.OIDCConfig
	secrets   *SecretRefStore
	providers *providerCache
	// revocations holds the jti of every session ended by logout.
	revocations revocationStore
}

// InvalidateOIDCCache evicts the cached provider for the given org, forcing
// re-discovery on the next login. Call this after updating an org's OIDC config.
func (a *OIDCAuthenticator) InvalidateOIDCCache(orgID uuid.UUID) {
	a.providers.invalidate(orgID)
}

// NewOIDCAuthenticator constructs a ready-to-use OIDCAuthenticator.
// secrets must not be nil — pass NewSecretRefStore(...) from the caller.
func NewOIDCAuthenticator(handle *db.Handle, auditLog audit.Logger, cfg config.OIDCConfig, secrets *SecretRefStore) *OIDCAuthenticator {
	ttl := cfg.ProviderCacheTTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &OIDCAuthenticator{
		handle:      handle,
		auditLog:    auditLog,
		cfg:         cfg,
		secrets:     secrets,
		providers:   newProviderCache(ttl),
		revocations: dbRevocations{handle: handle},
	}
}

// Middleware satisfies the auth.Authenticator interface. It reads the
// saidkick_session cookie, validates the HS256 JWT, and attaches a
// *Principal to the request context. Anonymous requests (no cookie or
// invalid/expired/revoked JWT) are passed through with a nil Principal so
// optional-auth endpoints still work; a revoked session therefore gets the
// same 401 from RequireAuthenticated as a forged one.
func (a *OIDCAuthenticator) Middleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		cookie, err := c.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			return next(c)
		}

		claims, err := validateSession(c.Request().Context(), cookie.Value, a.cfg.SessionSecret, a.revocations)
		if err != nil {
			// A failed revocation lookup says nothing about the token, so
			// keep the cookie and let the next request retry.
			if !errors.Is(err, errRevocationLookup) {
				clearSessionCookie(c, a.cfg.CookieDomain)
			}
			return next(c)
		}

		userID, err := uuid.Parse(claims.Subject)
		if err != nil {
			clearSessionCookie(c, a.cfg.CookieDomain)
			return next(c)
		}

		ctx := c.Request().Context()
		var user models.User
		if err := a.handle.RW.WithContext(ctx).First(&user, "id = ?", userID).Error; err != nil {
			return next(c)
		}

		var roleRows []models.RoleAssignment
		if err := a.handle.RW.WithContext(ctx).
			Where("user_id = ? AND revoked_at IS NULL", userID).
			Find(&roleRows).Error; err != nil {
			return next(c)
		}
		roles := make(map[models.Role]struct{}, len(roleRows))
		for _, r := range roleRows {
			roles[r.Role] = struct{}{}
		}

		p := &Principal{
			UserID:         user.ID,
			OrganizationID: user.OrganizationID,
			Email:          user.Email,
			Roles:          roles,
			AuthMethod:     AuthMethodSession,
		}
		c.SetRequest(c.Request().WithContext(withPrincipal(ctx, p)))
		return next(c)
	}
}

// RegisterRoutes mounts the three OIDC flow endpoints on the Echo instance.
// These routes do not pass through the auth middleware — they are the
// unauthenticated entry point for the login flow.
func (a *OIDCAuthenticator) RegisterRoutes(e *echo.Echo) {
	g := e.Group("/auth")
	g.GET("/login", a.handleLogin)
	g.GET("/callback", a.handleCallback)
	g.POST("/logout", a.handleLogout)
}

// handleLogin starts the authorization code flow for the named org.
//
// Query params:
//   - org      (required) — organisation slug
//   - redirect (optional) — URL to return to after login; defaults to /
//
// Flow: look up org → resolve OIDC config → generate PKCE verifier + state
// nonce → store both in a signed state cookie → redirect to IdP.
func (a *OIDCAuthenticator) handleLogin(c echo.Context) error {
	orgSlug := strings.TrimSpace(c.QueryParam("org"))
	if orgSlug == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "org query parameter is required")
	}
	redirect := sanitiseRedirect(c.QueryParam("redirect"), a.cfg.AllowedRedirectOrigins)

	ctx := c.Request().Context()
	org, err := a.orgBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "organisation not found")
		}
		return err
	}

	clientSecret, err := a.resolveOrgSecret(c.Request().Context(), org)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	redirectURL := a.cfg.BaseURL + "/auth/callback"
	cp, err := a.providers.get(org.ID, *org.OIDCIssuer, *org.OIDCClientID, clientSecret, redirectURL)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, "could not reach OIDC provider")
	}

	codeVerifier := oauth2.GenerateVerifier()
	stateNonce, err := randomBase64URL(24)
	if err != nil {
		return err
	}

	stateToken, err := mintStateToken(org.ID, stateNonce, codeVerifier, redirect, a.cfg.SessionSecret)
	if err != nil {
		return err
	}
	setStateCookie(c, stateToken, a.cfg.CookieDomain)

	authURL := cp.oauth2Config.AuthCodeURL(stateNonce, oauth2.S256ChallengeOption(codeVerifier))
	return c.Redirect(http.StatusFound, authURL)
}

// handleCallback completes the authorization code exchange.
//
// Flow: validate state cookie → exchange code + PKCE verifier for tokens →
// verify ID token → JIT-provision user → mint session cookie → redirect.
//
// Each callback is one auth_attempts_total{method="oidc"}; the redirect to
// the IdP in handleLogin is not an attempt. Failures caused by this side
// (database, secret store, IdP unreachable) are not counted.
func (a *OIDCAuthenticator) handleCallback(c echo.Context) error {
	code := c.QueryParam("code")
	stateParam := c.QueryParam("state")
	if code == "" || stateParam == "" {
		authFailed(metrics.AuthMethodOIDC, metrics.AuthReasonMalformed)
		return echo.NewHTTPError(http.StatusBadRequest, "code and state are required")
	}

	stateCookie, err := c.Cookie(stateCookieName)
	if err != nil || stateCookie.Value == "" {
		authFailed(metrics.AuthMethodOIDC, metrics.AuthReasonNoCredential)
		return echo.NewHTTPError(http.StatusBadRequest, "state cookie missing; restart the login flow")
	}

	sc, err := validateStateToken(stateCookie.Value, a.cfg.SessionSecret)
	if err != nil {
		reason := metrics.AuthReasonInvalidSignature
		if errors.Is(err, jwt.ErrTokenExpired) {
			reason = metrics.AuthReasonExpired
		}
		authFailed(metrics.AuthMethodOIDC, reason)
		return echo.NewHTTPError(http.StatusBadRequest, "invalid or expired state; restart the login flow")
	}
	if sc.State != stateParam {
		authFailed(metrics.AuthMethodOIDC, metrics.AuthReasonInvalidSignature)
		return echo.NewHTTPError(http.StatusBadRequest, "state mismatch; possible CSRF")
	}

	ctx := c.Request().Context()
	org, err := a.orgByID(ctx, sc.OrgID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "organisation not found")
		}
		return err
	}

	clientSecret, err := a.resolveOrgSecret(c.Request().Context(), org)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	redirectURL := a.cfg.BaseURL + "/auth/callback"
	cp, err := a.providers.get(org.ID, *org.OIDCIssuer, *org.OIDCClientID, clientSecret, redirectURL)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, "could not reach OIDC provider")
	}

	oauthToken, err := cp.oauth2Config.Exchange(ctx, code, oauth2.VerifierOption(sc.CodeVerifier))
	if err != nil {
		// The IdP answered and refused the code (bad, replayed or expired
		// code, or a PKCE mismatch). A transport error is not counted.
		var refused *oauth2.RetrieveError
		if errors.As(err, &refused) {
			authFailed(metrics.AuthMethodOIDC, metrics.AuthReasonInvalidSignature)
		}
		return echo.NewHTTPError(http.StatusBadGateway, "token exchange failed")
	}

	rawIDToken, ok := oauthToken.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		authFailed(metrics.AuthMethodOIDC, metrics.AuthReasonMalformed)
		return echo.NewHTTPError(http.StatusBadGateway, "id_token missing from token response")
	}

	verifier := cp.provider.Verifier(&oidc.Config{ClientID: *org.OIDCClientID})
	idToken, err := verifier.Verify(ctx, rawIDToken)
	if err != nil {
		reason := metrics.AuthReasonInvalidSignature
		var expired *oidc.TokenExpiredError
		if errors.As(err, &expired) {
			reason = metrics.AuthReasonExpired
		}
		authFailed(metrics.AuthMethodOIDC, reason)
		return echo.NewHTTPError(http.StatusUnauthorized, "id_token verification failed")
	}

	var claims struct {
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified *bool  `json:"email_verified"`
		Name          string `json:"name"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return fmt.Errorf("parse id_token claims: %w", err)
	}
	if claims.Sub == "" {
		authFailed(metrics.AuthMethodOIDC, metrics.AuthReasonMalformed)
		return echo.NewHTTPError(http.StatusBadGateway, "id_token missing sub claim")
	}
	emailVerified := claims.EmailVerified != nil && *claims.EmailVerified

	user, err := a.provisionUser(ctx, org.ID, claims.Sub, claims.Email, claims.Name, emailVerified)
	if err != nil {
		return fmt.Errorf("provision user: %w", err)
	}

	_ = a.auditLog.Log(ctx, audit.Event{
		Type:                "user.login",
		ActorUserID:         &user.ID,
		ActorOrganizationID: &org.ID,
		SubjectType:         "user",
		SubjectID:           user.ID,
		Payload:             map[string]any{"method": "oidc", "org_slug": org.Slug},
	})

	sessionTok, err := mintSession(user.ID, org.ID, a.cfg.SessionSecret, a.cfg.SessionTTL)
	if err != nil {
		return err
	}
	setSessionCookie(c, sessionTok, a.cfg.CookieDomain, a.cfg.SessionTTL)
	clearStateCookie(c, a.cfg.CookieDomain)
	authSucceeded(metrics.AuthMethodOIDC)

	// sanitiseRedirect is applied at login (mint time); re-validate here as
	// defence-in-depth against state cookies issued before this guard existed.
	redirectTo := sanitiseRedirect(sc.Redirect, a.cfg.AllowedRedirectOrigins)
	if redirectTo == "" {
		redirectTo = "/"
	}
	return c.Redirect(http.StatusFound, redirectTo)
}

// sanitiseRedirect ensures a post-login redirect target is safe, preventing
// open-redirect phishing.
//
// Allowed:
//   - Relative paths starting with "/" (but not "//", which is
//     protocol-relative and can point off-site).
//   - Absolute URLs whose origin (scheme+host+port) is in allowedOrigins,
//     which is derived from AUTH_CORS_ALLOWED_ORIGINS at startup.
//
// Everything else falls back to "/".
func sanitiseRedirect(raw string, allowedOrigins []string) string {
	if raw == "" {
		return "/"
	}
	// Relative path — always safe.
	if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") {
		return raw
	}
	// Absolute URL — only allow if the origin is in the trusted list.
	for _, origin := range allowedOrigins {
		if origin == "" {
			continue
		}
		if raw == origin || strings.HasPrefix(raw, origin+"/") {
			return raw
		}
	}
	return "/"
}

// handleLogout revokes the presented session, clears the session cookie and
// returns 204. It always succeeds: a missing, malformed, expired or
// pre-jti cookie needs no revocation row, and a failed write is logged.
func (a *OIDCAuthenticator) handleLogout(c echo.Context) error {
	if cookie, err := c.Cookie(sessionCookieName); err == nil && cookie.Value != "" {
		a.revokeSession(c.Request().Context(), cookie.Value)
	}
	clearSessionCookie(c, a.cfg.CookieDomain)
	return c.NoContent(http.StatusNoContent)
}

// revokeSession records the jti of a valid session token in the revocation
// store until the token's own expiry. Tokens that do not parse, have no jti
// or carry a malformed subject are ignored.
func (a *OIDCAuthenticator) revokeSession(ctx context.Context, token string) {
	claims, err := parseSession(token, a.cfg.SessionSecret)
	if err != nil || claims.ID == "" || claims.ExpiresAt == nil {
		return
	}
	jti, err := uuid.Parse(claims.ID)
	if err != nil {
		return
	}
	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return
	}
	if err := a.revocations.revoke(ctx, jti, userID, claims.ExpiresAt.Time); err != nil {
		slog.Error("logout: revoke session", "error", err, "user_id", userID)
	}
}

// provisionUser upserts the User record for (orgID, externalSubject), updating
// display profile fields and last_login_at on every login.
func (a *OIDCAuthenticator) provisionUser(
	ctx context.Context,
	orgID uuid.UUID,
	externalSubject, email, displayName string,
	emailVerified bool,
) (models.User, error) {
	now := time.Now()
	var user models.User

	err := a.handle.RW.WithContext(ctx).
		Where("organization_id = ? AND external_subject = ?", orgID, externalSubject).
		First(&user).Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Email fallback: if a seeded user exists (external_subject starts with
		// "seed:") with a matching email in this org, link their OIDC identity
		// on first login instead of creating a duplicate row.
		//
		// This links a self-asserted IdP identity to a pre-provisioned account
		// that may carry elevated roles, so it is only safe when the IdP has
		// verified the email claim. Without email_verified=true an attacker who
		// controls a spoofable/editable email at the org's IdP could claim a
		// seeded admin account; in that case we skip the link and fall through
		// to standard JIT provisioning, which creates a zero-role user.
		var seeded models.User
		seedErr := a.handle.RW.WithContext(ctx).
			Where("organization_id = ? AND email = ? AND external_subject LIKE 'seed:%'", orgID, email).
			First(&seeded).Error
		if seedErr == nil && emailVerified {
			updates := map[string]any{
				"external_subject": externalSubject,
				"display_name":     displayName,
				"last_login_at":    now,
			}
			if err := a.handle.RW.WithContext(ctx).Model(&seeded).Updates(updates).Error; err != nil {
				return models.User{}, fmt.Errorf("link seeded user: %w", err)
			}
			return seeded, nil
		}

		user = models.User{
			OrganizationID:  orgID,
			ExternalSubject: externalSubject,
			Email:           email,
			DisplayName:     displayName,
			LastLoginAt:     &now,
		}
		if err := a.handle.RW.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&user).Error; err != nil {
				return fmt.Errorf("create: %w", err)
			}
			return grantDefaultRole(tx, user)
		}); err != nil {
			return models.User{}, err
		}
		a.logDefaultRoleGrant(ctx, user)
		return user, nil
	}
	if err != nil {
		return models.User{}, fmt.Errorf("lookup: %w", err)
	}

	updates := map[string]any{
		"email":         email,
		"display_name":  displayName,
		"last_login_at": now,
	}
	if err := a.handle.RW.WithContext(ctx).Model(&user).Updates(updates).Error; err != nil {
		return models.User{}, fmt.Errorf("update: %w", err)
	}
	return user, nil
}

// grantDefaultRole gives a user created at first sign-in the app_user role,
// so they can open the apps their organisation runs without waiting for an
// org_admin. No user granted it, so GrantedByUserID records the user's own
// ID; the column is NOT NULL and references users.
func grantDefaultRole(tx *gorm.DB, user models.User) error {
	row := models.RoleAssignment{
		UserID:          user.ID,
		Role:            models.RoleAppUser,
		GrantedByUserID: user.ID,
	}
	if err := tx.Create(&row).Error; err != nil {
		return fmt.Errorf("grant %s: %w", models.RoleAppUser, err)
	}
	return nil
}

// logDefaultRoleGrant records the grant made by grantDefaultRole as a
// role.assigned event with no actor user, the convention for events the
// system raises on its own.
func (a *OIDCAuthenticator) logDefaultRoleGrant(ctx context.Context, user models.User) {
	if a.auditLog == nil {
		return
	}
	if err := a.auditLog.Log(ctx, audit.Event{
		Type:                "role.assigned",
		ActorOrganizationID: &user.OrganizationID,
		SubjectType:         "user",
		SubjectID:           user.ID,
		Payload: map[string]any{
			"roles":  []string{string(models.RoleAppUser)},
			"reason": "first_sign_in",
		},
	}); err != nil {
		slog.Error("oidc: audit default role grant", "error", err, "user_id", user.ID)
	}
}

// orgBySlug fetches an Organisation by slug. Returns gorm.ErrRecordNotFound
// when absent.
func (a *OIDCAuthenticator) orgBySlug(ctx context.Context, slug string) (models.Organization, error) {
	var org models.Organization
	return org, a.handle.RW.WithContext(ctx).First(&org, "slug = ?", slug).Error
}

// orgByID fetches an Organisation by ID. Returns gorm.ErrRecordNotFound
// when absent.
func (a *OIDCAuthenticator) orgByID(ctx context.Context, id uuid.UUID) (models.Organization, error) {
	var org models.Organization
	return org, a.handle.RW.WithContext(ctx).First(&org, "id = ?", id).Error
}

// resolveOrgSecret checks that the org has OIDC fields set and delegates
// secret resolution to the SecretRefStore, which handles all three schemes
// (vault:, encrypted:, v0-inline:).
func (a *OIDCAuthenticator) resolveOrgSecret(ctx context.Context, org models.Organization) (string, error) {
	if org.OIDCIssuer == nil || org.OIDCClientID == nil || org.OIDCClientSecretRef == nil {
		return "", fmt.Errorf("organisation %q OIDC is not configured", org.Slug)
	}
	return a.secrets.Resolve(ctx, *org.OIDCClientSecretRef)
}

// Cookie helpers — keep all Set-Cookie logic in one place.

func setSessionCookie(c echo.Context, token, domain string, ttl time.Duration) {
	c.SetCookie(&http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Domain:   domain,
		MaxAge:   int(ttl / time.Second),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

func clearSessionCookie(c echo.Context, domain string) {
	c.SetCookie(&http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		Domain:   domain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

func setStateCookie(c echo.Context, token, domain string) {
	c.SetCookie(&http.Cookie{
		Name:     stateCookieName,
		Value:    token,
		Path:     "/auth/callback",
		Domain:   domain,
		MaxAge:   int(5 * time.Minute / time.Second),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearStateCookie(c echo.Context, domain string) {
	c.SetCookie(&http.Cookie{
		Name:     stateCookieName,
		Value:    "",
		Path:     "/auth/callback",
		Domain:   domain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}
