package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

const apiKeyPrefix = "sak_"

// APIKeyAuthenticator reads Authorization: Bearer sak_… headers and
// authenticates the caller against the api_keys table.
//
// It runs AFTER the OIDC middleware. If a principal is already attached
// (session cookie path), it is a no-op. If no Bearer token is present,
// the request continues as anonymous. If a Bearer token is present and
// invalid/expired/revoked, the request is rejected with 401.
//
// The principal's roles are the owner's live role assignments intersected
// with the key's stored Roles. An empty intersection is also a 401.
type APIKeyAuthenticator struct {
	Handle *db.Handle
}

// Middleware satisfies auth.Authenticator.
func (a *APIKeyAuthenticator) Middleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Short-circuit: another authenticator already identified this caller.
		if PrincipalFromContext(c.Request().Context()) != nil {
			return next(c)
		}

		raw := strings.TrimSpace(c.Request().Header.Get("Authorization"))
		if !strings.HasPrefix(raw, "Bearer ") {
			return next(c) // no Bearer token — anonymous
		}
		token := strings.TrimPrefix(raw, "Bearer ")
		if !strings.HasPrefix(token, apiKeyPrefix) {
			return next(c) // Bearer token is not a sak_ key; could be some other scheme
		}

		// A key of the wrong shape cannot match a stored hash. It gets the
		// same answer as an unknown or revoked key.
		if !wellFormedAPIKey(token) {
			authFailed(metrics.AuthMethodAPIKey, metrics.AuthReasonMalformed)
			return echo.NewHTTPError(http.StatusUnauthorized, "invalid or revoked API key")
		}

		// Hash the raw token and look it up. Revoked keys are loaded too, so
		// the failure is counted with its reason; the response stays the
		// same for unknown and revoked keys.
		sum := sha256.Sum256([]byte(token))
		keyHash := hex.EncodeToString(sum[:])

		var key models.APIKey
		err := a.Handle.RW.WithContext(c.Request().Context()).
			Where("key_hash = ?", keyHash).
			First(&key).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				authFailed(metrics.AuthMethodAPIKey, metrics.AuthReasonUnknownKey)
				return echo.NewHTTPError(http.StatusUnauthorized, "invalid or revoked API key")
			}
			return err
		}
		if key.RevokedAt != nil {
			authFailed(metrics.AuthMethodAPIKey, metrics.AuthReasonRevoked)
			return echo.NewHTTPError(http.StatusUnauthorized, "invalid or revoked API key")
		}

		// Expiry check.
		if key.ExpiresAt != nil && key.ExpiresAt.Before(time.Now().UTC()) {
			authFailed(metrics.AuthMethodAPIKey, metrics.AuthReasonExpired)
			return echo.NewHTTPError(http.StatusUnauthorized, "API key has expired")
		}

		// Load owner + their active roles.
		var user models.User
		if err := a.Handle.RW.WithContext(c.Request().Context()).
			First(&user, "id = ?", key.OwnerUserID).Error; err != nil {
			return err
		}
		var roleRows []models.RoleAssignment
		if err := a.Handle.RW.WithContext(c.Request().Context()).
			Where("user_id = ? AND revoked_at IS NULL", key.OwnerUserID).
			Find(&roleRows).Error; err != nil {
			return err
		}
		roles := scopedRoles(roleRows, key.Roles)
		if len(roles) == 0 {
			// The key's scope no longer overlaps the owner's live roles. Refuse
			// outright rather than attaching a principal that holds nothing.
			authFailed(metrics.AuthMethodAPIKey, metrics.AuthReasonNoRoles)
			return echo.NewHTTPError(http.StatusUnauthorized, "API key has no active roles")
		}
		authSucceeded(metrics.AuthMethodAPIKey)

		// Update last_used_at (best-effort; not a blocker for the request).
		now := time.Now().UTC()
		_ = a.Handle.RW.WithContext(c.Request().Context()).
			Model(&key).Update("last_used_at", now).Error

		p := &Principal{
			UserID:         user.ID,
			OrganizationID: user.OrganizationID,
			Email:          user.Email,
			Roles:          roles,
			AuthMethod:     AuthMethodAPIKey,
		}
		c.SetRequest(c.Request().WithContext(withPrincipal(c.Request().Context(), p)))
		return next(c)
	}
}

// apiKeyLength is the length of a key handlers.CreateAPIKey mints: "sak_"
// and 32 random bytes in unpadded base64url.
const apiKeyLength = len(apiKeyPrefix) + 43

// wellFormedAPIKey reports whether token has the shape of a minted key.
func wellFormedAPIKey(token string) bool {
	if len(token) != apiKeyLength {
		return false
	}
	return !strings.ContainsFunc(token[len(apiKeyPrefix):], func(r rune) bool {
		return !isBase64URL(r)
	})
}

// isBase64URL reports whether r is in the unpadded base64url alphabet.
func isBase64URL(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		return true
	default:
		return false
	}
}

// scopedRoles returns the roles in live (the owner's active assignments)
// that the key's scope also names. The scope is a ceiling, not a snapshot:
// a role the owner has lost is dropped even if the key still lists it.
func scopedRoles(live []models.RoleAssignment, scope []string) map[models.Role]struct{} {
	roles := make(map[models.Role]struct{}, len(live))
	for _, r := range live {
		if slices.Contains(scope, string(r.Role)) {
			roles[r.Role] = struct{}{}
		}
	}
	return roles
}
