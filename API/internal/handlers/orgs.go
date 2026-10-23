package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

// orgSlugRegex enforces the rule from the design doc:
//
//	^[a-z][a-z0-9-]{1,38}[a-z0-9]$
var orgSlugRegex = regexp.MustCompile(`^[a-z][a-z0-9-]{1,38}[a-z0-9]$`)

// CreateOrganizationRequest is the body of POST /api/v1/orgs.
type CreateOrganizationRequest struct {
	Slug         string `json:"slug"`
	DisplayName  string `json:"display_name"`
	ContactEmail string `json:"contact_email"`
}

// CreateOrganization provisions a new organisation tenant. PlatformAdmin
// only.
func (h *Handlers) CreateOrganization(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}
	// Mirrors the route's role gate so a routing mistake cannot expose it.
	if !p.Has(models.RolePlatformAdmin) {
		return apierror.New(http.StatusForbidden, "forbidden", "missing required role")
	}

	var req CreateOrganizationRequest
	if err := c.Bind(&req); err != nil {
		return apierror.New(http.StatusBadRequest, "bad_request", "invalid JSON")
	}

	fields := make([]apierror.FieldError, 0)
	slug := strings.ToLower(strings.TrimSpace(req.Slug))
	if !orgSlugRegex.MatchString(slug) {
		fields = append(fields, apierror.FieldError{
			Path: "slug", Code: "invalid_format",
			Message: "slug must match ^[a-z][a-z0-9-]{1,38}[a-z0-9]$",
		})
	}
	if strings.TrimSpace(req.DisplayName) == "" {
		fields = append(fields, apierror.FieldError{
			Path: "display_name", Code: "required", Message: "display_name is required",
		})
	}
	if strings.TrimSpace(req.ContactEmail) == "" {
		fields = append(fields, apierror.FieldError{
			Path: "contact_email", Code: "required", Message: "contact_email is required",
		})
	}
	if len(fields) > 0 {
		return apierror.NewValidation("validation failed", fields)
	}

	org := models.Organization{
		Slug:         slug,
		DisplayName:  strings.TrimSpace(req.DisplayName),
		ContactEmail: strings.TrimSpace(req.ContactEmail),
	}

	ctx := c.Request().Context()
	if err := h.DB.RW.WithContext(ctx).Create(&org).Error; err != nil {
		// Slug-collision returns 409.
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			return apierror.New(http.StatusConflict, "slug_taken", "an organisation with that slug already exists")
		}
		return err
	}

	_ = h.Audit.Log(ctx, audit.Event{
		Type:                "org.created",
		ActorUserID:         &p.UserID,
		ActorOrganizationID: &p.OrganizationID,
		SubjectType:         "organization",
		SubjectID:           org.ID,
		Payload:             map[string]any{"slug": org.Slug, "display_name": org.DisplayName},
	})

	return c.JSON(http.StatusCreated, orgToMap(org))
}

// ConfigureOrganizationOIDCRequest is the body of POST /orgs/:slug/oidc.
type ConfigureOrganizationOIDCRequest struct {
	OIDCIssuer       string `json:"oidc_issuer"`
	OIDCClientID     string `json:"oidc_client_id"`
	OIDCClientSecret string `json:"oidc_client_secret"`
}

// ConfigureOrganizationOIDC sets the federated IdP settings for one org.
// The client secret is stored using the best available scheme (Vault >
// AES-256-GCM in DB > v0-inline dev-only) via h.OIDCSecrets.Write().
func (h *Handlers) ConfigureOrganizationOIDC(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}
	// Mirrors the route's role gate so a routing mistake cannot expose it.
	if !p.Has(models.RolePlatformAdmin) {
		return apierror.New(http.StatusForbidden, "forbidden", "missing required role")
	}

	slug := strings.ToLower(strings.TrimSpace(c.Param("slug")))
	if !orgSlugRegex.MatchString(slug) {
		return apierror.New(http.StatusNotFound, "not_found", "organisation not found")
	}

	var req ConfigureOrganizationOIDCRequest
	if err := c.Bind(&req); err != nil {
		return apierror.New(http.StatusBadRequest, "bad_request", "invalid JSON")
	}

	issuer := strings.TrimSpace(req.OIDCIssuer)
	clientID := strings.TrimSpace(req.OIDCClientID)
	clientSecret := strings.TrimSpace(req.OIDCClientSecret)
	if issuer == "" || clientID == "" || clientSecret == "" {
		return apierror.NewValidation("validation failed", []apierror.FieldError{
			{Path: "oidc_issuer", Code: "required"},
			{Path: "oidc_client_id", Code: "required"},
			{Path: "oidc_client_secret", Code: "required"},
		})
	}

	ctx := c.Request().Context()
	var org models.Organization
	if err := h.DB.RW.WithContext(ctx).First(&org, "slug = ?", slug).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apierror.New(http.StatusNotFound, "not_found", "organisation not found")
		}
		return err
	}

	secretRef, err := h.OIDCSecrets.Write(ctx, org.ID, clientSecret)
	if err != nil {
		slog.Error("oidc secret storage failed", "org_id", org.ID, "err", err)
		return apierror.New(http.StatusServiceUnavailable, "secret_storage_unavailable", "OIDC secret storage is unavailable; contact your platform admin")
	}

	org.OIDCIssuer = &issuer
	org.OIDCClientID = &clientID
	org.OIDCClientSecretRef = &secretRef

	if err := h.DB.RW.WithContext(ctx).Save(&org).Error; err != nil {
		return err
	}

	if h.OIDCAuth != nil {
		h.OIDCAuth.InvalidateOIDCCache(org.ID)
	}

	_ = h.Audit.Log(ctx, audit.Event{
		Type:                "org.oidc_configured",
		ActorUserID:         &p.UserID,
		ActorOrganizationID: &p.OrganizationID,
		SubjectType:         "organization",
		SubjectID:           org.ID,
		Payload: map[string]any{
			"slug":    org.Slug,
			"issuer":  issuer,
			"secret_scheme": func() string {
				if idx := strings.Index(secretRef, ":"); idx >= 0 {
					return secretRef[:idx]
				}
				return secretRef
			}(),
		},
	})

	return c.JSON(http.StatusOK, orgToMap(org))
}

// ListMembers returns every user provisioned in an org. OrgAdmin (own
// org) or PlatformAdmin.
func (h *Handlers) ListMembers(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}

	slug := strings.ToLower(strings.TrimSpace(c.Param("slug")))
	if !orgSlugRegex.MatchString(slug) {
		return apierror.New(http.StatusNotFound, "not_found", "organisation not found")
	}
	ctx := c.Request().Context()

	var org models.Organization
	if err := h.DB.RW.WithContext(ctx).First(&org, "slug = ?", slug).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apierror.New(http.StatusNotFound, "not_found", "organisation not found")
		}
		return err
	}

	if !p.Has(models.RolePlatformAdmin) && p.OrganizationID != org.ID {
		return apierror.New(http.StatusForbidden, "forbidden", "not in this organisation")
	}

	var users []models.User
	if err := h.DB.RW.WithContext(ctx).
		Where("organization_id = ?", org.ID).
		Order("display_name ASC").
		Find(&users).Error; err != nil {
		return err
	}

	out := make([]map[string]any, 0, len(users))
	for _, u := range users {
		// Pull active roles per user. Could be batched; for V0 small N this
		// is fine.
		var roles []models.RoleAssignment
		if err := h.DB.RW.WithContext(ctx).
			Where("user_id = ? AND revoked_at IS NULL", u.ID).
			Find(&roles).Error; err != nil {
			return err
		}
		roleNames := make([]string, 0, len(roles))
		for _, r := range roles {
			roleNames = append(roleNames, string(r.Role))
		}
		out = append(out, map[string]any{
			"id":            u.ID.String(),
			"email":         u.Email,
			"display_name":  u.DisplayName,
			"roles":         roleNames,
			"last_login_at": u.LastLoginAt,
			"created_at":    u.CreatedAt,
		})
	}

	return c.JSON(http.StatusOK, map[string]any{"items": out})
}

// AssignMemberRolesRequest is the body of POST /orgs/:slug/members/:user_id/roles.
type AssignMemberRolesRequest struct {
	Roles []string `json:"roles"`
}

// platformOnlyRoles are the roles only a PlatformAdmin may grant or revoke.
// Auditors approve apps across organisations, so they are vetted centrally
// like PlatformAdmins (ADR-40-3).
var platformOnlyRoles = []models.Role{models.RolePlatformAdmin, models.RoleAuditor}

// checkPlatformOnlyRoles refuses a request that adds or removes a
// platform-only role relative to the member's active roles. Keeping such a
// role unchanged is allowed. The caller must hold PlatformAdmin to change one.
func checkPlatformOnlyRoles(p *auth.Principal, existing map[models.Role]*models.RoleAssignment, want map[models.Role]struct{}) error {
	if p.Has(models.RolePlatformAdmin) {
		return nil
	}
	for _, role := range platformOnlyRoles {
		_, held := existing[role]
		_, wanted := want[role]
		switch {
		case wanted && !held:
			return apierror.New(http.StatusForbidden, "forbidden",
				"only platform_admin may grant "+string(role))
		case held && !wanted:
			return apierror.New(http.StatusForbidden, "forbidden",
				"only platform_admin may revoke "+string(role))
		}
	}
	return nil
}

// AssignMemberRoles replaces the active role set for a user. Only a
// PlatformAdmin may grant or revoke a platform-only role (PlatformAdmin,
// Auditor); a request from anyone else that adds or removes one, relative
// to the member's active roles, is rejected with 403 before any write.
func (h *Handlers) AssignMemberRoles(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}

	slug := strings.ToLower(strings.TrimSpace(c.Param("slug")))
	if !orgSlugRegex.MatchString(slug) {
		return apierror.New(http.StatusNotFound, "not_found", "organisation not found")
	}
	userID, err := uuid.Parse(c.Param("user_id"))
	if err != nil {
		return apierror.New(http.StatusNotFound, "not_found", "user not found")
	}

	var req AssignMemberRolesRequest
	if err := c.Bind(&req); err != nil {
		return apierror.New(http.StatusBadRequest, "bad_request", "invalid JSON")
	}

	ctx := c.Request().Context()

	var org models.Organization
	if err := h.DB.RW.WithContext(ctx).First(&org, "slug = ?", slug).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apierror.New(http.StatusNotFound, "not_found", "organisation not found")
		}
		return err
	}
	if !p.Has(models.RolePlatformAdmin) && p.OrganizationID != org.ID {
		return apierror.New(http.StatusForbidden, "forbidden", "not in this organisation")
	}

	// Validate roles.
	wantRoles := make(map[models.Role]struct{}, len(req.Roles))
	for _, raw := range req.Roles {
		r := models.Role(strings.ToLower(strings.TrimSpace(raw)))
		if !isValidRole(r) {
			return apierror.NewValidation("validation failed", []apierror.FieldError{
				{Path: "roles", Code: "invalid_role",
					Message: "unknown role: " + string(r)},
			})
		}
		wantRoles[r] = struct{}{}
	}

	var user models.User
	if err := h.DB.RW.WithContext(ctx).First(&user, "id = ? AND organization_id = ?", userID, org.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apierror.New(http.StatusNotFound, "not_found", "user not found in this organisation")
		}
		return err
	}

	err = h.DB.RW.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Read existing active roles.
		var existing []models.RoleAssignment
		if err := tx.Where("user_id = ? AND revoked_at IS NULL", user.ID).
			Find(&existing).Error; err != nil {
			return err
		}
		existingByRole := make(map[models.Role]*models.RoleAssignment, len(existing))
		for i := range existing {
			existingByRole[existing[i].Role] = &existing[i]
		}

		if err := checkPlatformOnlyRoles(p, existingByRole, wantRoles); err != nil {
			return err
		}

		// Revoke roles not in wantRoles.
		for role, row := range existingByRole {
			if _, keep := wantRoles[role]; !keep {
				if err := tx.Model(row).
					Update("revoked_at", gorm.Expr("now()")).Error; err != nil {
					return err
				}
			}
		}
		// Grant roles in wantRoles not already active.
		for role := range wantRoles {
			if _, already := existingByRole[role]; already {
				continue
			}
			row := &models.RoleAssignment{
				UserID:          user.ID,
				Role:            role,
				GrantedByUserID: p.UserID,
			}
			if err := tx.Create(row).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	_ = h.Audit.Log(ctx, audit.Event{
		Type:                "role.assigned",
		ActorUserID:         &p.UserID,
		ActorOrganizationID: &p.OrganizationID,
		SubjectType:         "user",
		SubjectID:           user.ID,
		Payload:             map[string]any{"roles": req.Roles},
	})

	return c.JSON(http.StatusOK, map[string]any{
		"id":           user.ID.String(),
		"email":        user.Email,
		"display_name": user.DisplayName,
		"roles":        req.Roles,
	})
}

// ListFederatedOrgs returns organisations that have a fully configured OIDC
// provider. The endpoint is public (no auth required) so the login page can
// populate clickable org buttons without a prior session.
//
// Only slug and display_name are returned — no internal OIDC fields.
func (h *Handlers) ListFederatedOrgs(c echo.Context) error {
	var orgs []models.Organization
	if err := h.DB.RW.WithContext(c.Request().Context()).
		Where("oidc_issuer IS NOT NULL AND oidc_client_id IS NOT NULL AND oidc_client_secret_ref IS NOT NULL").
		Order("display_name ASC").
		Find(&orgs).Error; err != nil {
		return err
	}
	out := make([]map[string]any, 0, len(orgs))
	for _, o := range orgs {
		out = append(out, map[string]any{
			"slug":         o.Slug,
			"display_name": o.DisplayName,
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"items": out})
}

func orgToMap(o models.Organization) map[string]any {
	out := map[string]any{
		"id":            o.ID.String(),
		"slug":          o.Slug,
		"display_name":  o.DisplayName,
		"contact_email": o.ContactEmail,
		"created_at":    o.CreatedAt,
	}
	if o.OIDCIssuer != nil {
		out["oidc_issuer"] = *o.OIDCIssuer
	}
	if o.OIDCClientID != nil {
		out["oidc_client_id"] = *o.OIDCClientID
	}
	return out
}

func isValidRole(r models.Role) bool {
	return slices.Contains(models.AllRoles, r)
}

// requireOwningOrg returns a 422 field error on owning_org_id when id does
// not match an organisation. Soft-deleted organisations count as unknown.
func (h *Handlers) requireOwningOrg(ctx context.Context, id uuid.UUID) error {
	var n int64
	if err := h.DB.RW.WithContext(ctx).Model(&models.Organization{}).
		Where("id = ?", id).Count(&n).Error; err != nil {
		return err
	}
	if n == 0 {
		return apierror.NewValidation("validation failed", []apierror.FieldError{
			{Path: "owning_org_id", Code: "not_found", Message: "organisation not found"},
		})
	}
	return nil
}
