package handlers

import (
	"net/http"

	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/labstack/echo/v4"
)

// MeResponse is the response body for GET /api/v1/me.
type MeResponse struct {
	ID    string   `json:"id"`
	Email string   `json:"email"`
	Name  string   `json:"name"`
	Roles []string `json:"roles"`
	// OrgID is the user's organisation, so the webapp can tell which
	// clusters the user's org owns (cluster write actions are owner-only).
	OrgID   string `json:"org_id,omitempty"`
	OrgSlug string `json:"org_slug,omitempty"`
	OrgName string `json:"org_name,omitempty"`
}

// GetMe returns the authenticated user's profile, roles, and owning organisation.
func (h *Handlers) GetMe(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return echo.ErrUnauthorized
	}

	var user models.User
	if err := h.DB.RW.WithContext(c.Request().Context()).
		First(&user, "id = ?", p.UserID).Error; err != nil {
		return echo.ErrInternalServerError
	}

	roles := make([]string, 0, len(p.Roles))
	for r := range p.Roles {
		roles = append(roles, string(r))
	}

	resp := MeResponse{
		ID:    user.ID.String(),
		Email: user.Email,
		Name:  user.DisplayName,
		Roles: roles,
		OrgID: user.OrganizationID.String(),
	}

	// Enrich with organisation context from the user's OrganizationID.
	var org models.Organization
	if err := h.DB.RW.WithContext(c.Request().Context()).
		First(&org, "id = ?", user.OrganizationID).Error; err == nil {
		resp.OrgSlug = org.Slug
		resp.OrgName = org.DisplayName
	}

	return c.JSON(http.StatusOK, resp)
}
