package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

const (
	maxAPIKeyNameLength = 120
	// maxAPIKeyExpiresDays matches the agent certificate lifetime, so the
	// platform has one rotation cadence.
	maxAPIKeyExpiresDays = 90
)

type CreateAPIKeyRequest struct {
	Name        string   `json:"name"`
	Roles       []string `json:"roles"`
	ExpiresDays int      `json:"expires_days"`
}

// validateCreateAPIKey checks req against the creator p and returns the
// key's role scope, sorted and deduplicated. Every role must be one p holds
// right now and may not be app_user, and the key must expire within 1 to
// 90 days.
func validateCreateAPIKey(req CreateAPIKeyRequest, p *auth.Principal) ([]string, error) {
	var fields []apierror.FieldError
	if req.Name == "" || len(req.Name) > maxAPIKeyNameLength {
		fields = append(fields, apierror.FieldError{
			Path: "name", Code: "required", Message: "name is required (max 120 chars)",
		})
	}

	if len(req.Roles) == 0 {
		fields = append(fields, apierror.FieldError{
			Path: "roles", Code: "required", Message: "roles must name at least one role",
		})
	}
	for i, r := range req.Roles {
		// app_user is a person opening apps in a browser; a machine token
		// has no use for it.
		if models.Role(r) == models.RoleAppUser {
			fields = append(fields, apierror.FieldError{
				Path:    fmt.Sprintf("roles[%d]", i),
				Code:    "not_allowed",
				Message: fmt.Sprintf("the role %q cannot be given to an API key", r),
			})
			continue
		}
		if !p.Has(models.Role(r)) {
			fields = append(fields, apierror.FieldError{
				Path:    fmt.Sprintf("roles[%d]", i),
				Code:    "not_held",
				Message: fmt.Sprintf("you do not hold the role %q", r),
			})
		}
	}

	if req.ExpiresDays < 1 || req.ExpiresDays > maxAPIKeyExpiresDays {
		fields = append(fields, apierror.FieldError{
			Path:    "expires_days",
			Code:    "out_of_range",
			Message: fmt.Sprintf("expires_days is required and must be between 1 and %d", maxAPIKeyExpiresDays),
		})
	}

	if len(fields) > 0 {
		return nil, apierror.NewValidation("invalid api key request", fields)
	}

	roles := slices.Clone(req.Roles)
	slices.Sort(roles)
	return slices.Compact(roles), nil
}

// CreateAPIKey generates a new API key for the authenticated user. The full
// token is returned once in the response and never stored — only a SHA-256
// digest is persisted for subsequent verification.
func (h *Handlers) CreateAPIKey(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}
	// Allow-list: a leaked API key must not mint a successor that outlives
	// it, and an unknown or empty method is refused too.
	if p.AuthMethod != auth.AuthMethodSession && p.AuthMethod != auth.AuthMethodDev {
		return apierror.New(http.StatusForbidden, "session_required",
			"API keys can only be created from a signed-in session")
	}

	var req CreateAPIKeyRequest
	if err := c.Bind(&req); err != nil {
		return apierror.New(http.StatusBadRequest, "bad_request", "invalid JSON")
	}
	roles, err := validateCreateAPIKey(req, p)
	if err != nil {
		return err
	}

	// Generate 32 random bytes → base64url → "sak_<rawToken>".
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("generate api key: %w", err)
	}
	rawToken := base64.RawURLEncoding.EncodeToString(raw)
	token := "sak_" + rawToken
	prefix := token[:12] // "sak_" + first 8 chars of random part

	sum := sha256.Sum256([]byte(token))
	keyHash := hex.EncodeToString(sum[:])

	expiresAt := time.Now().UTC().Add(time.Duration(req.ExpiresDays) * 24 * time.Hour)

	key := models.APIKey{
		Name:        req.Name,
		Prefix:      prefix,
		KeyHash:     keyHash,
		OwnerUserID: p.UserID,
		Roles:       roles,
		ExpiresAt:   &expiresAt,
	}
	if err := h.DB.RW.WithContext(c.Request().Context()).Create(&key).Error; err != nil {
		return err
	}

	_ = h.Audit.Log(c.Request().Context(), audit.Event{
		Type:                "api_key.created",
		ActorUserID:         &p.UserID,
		ActorOrganizationID: &p.OrganizationID,
		SubjectType:         "api_key",
		SubjectID:           key.ID,
		Payload: map[string]any{
			"name": key.Name, "prefix": key.Prefix,
			"roles": []string(key.Roles), "expires_at": key.ExpiresAt,
			"auth_method": string(p.AuthMethod),
		},
	})

	return c.JSON(http.StatusCreated, map[string]any{
		"id":         key.ID.String(),
		"name":       key.Name,
		"prefix":     key.Prefix,
		"roles":      key.Roles,
		"created_at": key.CreatedAt,
		"expires_at": key.ExpiresAt,
		"token":      token,
	})
}

// ListAPIKeys returns all active (non-revoked) API keys owned by the caller.
func (h *Handlers) ListAPIKeys(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}

	page, pageSize := paginationFromQuery(c)

	tx := h.DB.RW.WithContext(c.Request().Context()).
		Model(&models.APIKey{}).
		Where("owner_user_id = ? AND revoked_at IS NULL", p.UserID)

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return err
	}

	var keys []models.APIKey
	if err := tx.Order("created_at DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&keys).Error; err != nil {
		return err
	}

	items := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		items = append(items, map[string]any{
			"id":           k.ID.String(),
			"name":         k.Name,
			"prefix":       k.Prefix,
			"roles":        k.Roles,
			"created_at":   k.CreatedAt,
			"last_used_at": k.LastUsedAt,
			"expires_at":   k.ExpiresAt,
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"items": items, "total": total})
}

// RevokeAPIKey marks a key as revoked. Only the owning user or a
// PlatformAdmin may revoke a key.
func (h *Handlers) RevokeAPIKey(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}

	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return apierror.New(http.StatusNotFound, "not_found", "api key not found")
	}

	var key models.APIKey
	if err := h.DB.RW.WithContext(c.Request().Context()).First(&key, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apierror.New(http.StatusNotFound, "not_found", "api key not found")
		}
		return err
	}

	if key.OwnerUserID != p.UserID && !p.Has(models.RolePlatformAdmin) {
		return apierror.New(http.StatusForbidden, "forbidden", "you may only revoke your own API keys")
	}

	now := time.Now().UTC()
	if err := h.DB.RW.WithContext(c.Request().Context()).
		Model(&key).Update("revoked_at", now).Error; err != nil {
		return err
	}

	_ = h.Audit.Log(c.Request().Context(), audit.Event{
		Type:                "api_key.revoked",
		ActorUserID:         &p.UserID,
		ActorOrganizationID: &p.OrganizationID,
		SubjectType:         "api_key",
		SubjectID:           key.ID,
		Payload:             map[string]any{"name": key.Name, "prefix": key.Prefix},
	})

	return c.NoContent(http.StatusNoContent)
}
