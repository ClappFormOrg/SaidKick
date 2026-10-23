package handlers

import (
	"net/http"
	"strings"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/labstack/echo/v4"
)

// ListAdvisories returns active platform advisories — those whose ExpiresAt is
// nil or in the future. Requires an authenticated session (not fully public
// since some advisories may be org-specific in future).
//
// Query params:
//   - app_slug: filter to advisories for a specific app (optional)
func (h *Handlers) ListAdvisories(c echo.Context) error {
	appSlug := strings.TrimSpace(c.QueryParam("app_slug"))

	tx := h.DB.RW.WithContext(c.Request().Context()).
		Model(&models.Advisory{}).
		Where("expires_at IS NULL OR expires_at > ?", time.Now()).
		Order("published_at DESC")

	if appSlug != "" {
		tx = tx.Where("app_slug = ? OR app_slug IS NULL", appSlug)
	}

	var advisories []models.Advisory
	if err := tx.Find(&advisories).Error; err != nil {
		return err
	}

	out := make([]map[string]any, 0, len(advisories))
	for _, a := range advisories {
		item := map[string]any{
			"id":           a.ID.String(),
			"title":        a.Title,
			"body":         a.Body,
			"severity":     a.Severity,
			"published_at": a.PublishedAt,
		}
		if a.AppSlug != nil {
			item["app_slug"] = *a.AppSlug
		}
		if a.ExpiresAt != nil {
			item["expires_at"] = a.ExpiresAt
		}
		out = append(out, item)
	}

	return c.JSON(http.StatusOK, map[string]any{
		"items": out,
		"total": len(out),
	})
}

// CreateAdvisoryRequest is the body of POST /api/v1/advisories.
type CreateAdvisoryRequest struct {
	Title     string  `json:"title"`
	Body      string  `json:"body"`
	Severity  string  `json:"severity"`   // "info", "warning", "critical"
	AppSlug   string  `json:"app_slug"`   // optional
	ExpiresAt *string `json:"expires_at"` // RFC3339, optional
}

// CreateAdvisory creates a new platform advisory. PlatformAdmin only.
func (h *Handlers) CreateAdvisory(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}
	// Mirrors the route's role gate so a routing mistake cannot expose it.
	if !p.Has(models.RolePlatformAdmin) {
		return apierror.New(http.StatusForbidden, "forbidden", "missing required role")
	}

	var req CreateAdvisoryRequest
	if err := c.Bind(&req); err != nil {
		return apierror.New(http.StatusBadRequest, "bad_request", "invalid JSON")
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		return apierror.NewValidation("validation failed", []apierror.FieldError{
			{Path: "title", Code: "required", Message: "title is required"},
		})
	}

	severity := strings.TrimSpace(req.Severity)
	if severity == "" {
		severity = "info"
	}
	validSeverities := map[string]bool{"info": true, "warning": true, "critical": true}
	if !validSeverities[severity] {
		return apierror.NewValidation("validation failed", []apierror.FieldError{
			{Path: "severity", Code: "invalid", Message: "severity must be info, warning, or critical"},
		})
	}

	a := models.Advisory{
		Title:       title,
		Body:        strings.TrimSpace(req.Body),
		Severity:    severity,
		PublishedAt: time.Now().UTC(),
	}
	if s := strings.TrimSpace(req.AppSlug); s != "" {
		a.AppSlug = &s
	}
	if req.ExpiresAt != nil {
		t, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err != nil {
			return apierror.NewValidation("validation failed", []apierror.FieldError{
				{Path: "expires_at", Code: "invalid_format", Message: "expires_at must be RFC3339"},
			})
		}
		a.ExpiresAt = &t
	}

	if err := h.DB.RW.WithContext(c.Request().Context()).Create(&a).Error; err != nil {
		return err
	}

	return c.JSON(http.StatusCreated, map[string]any{
		"id":           a.ID.String(),
		"title":        a.Title,
		"severity":     a.Severity,
		"published_at": a.PublishedAt,
	})
}
