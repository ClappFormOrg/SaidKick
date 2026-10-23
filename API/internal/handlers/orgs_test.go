package handlers_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/handlers"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memberRolesFixture holds an org admin and a target user who holds
// platform_admin.
type memberRolesFixture struct {
	handle   *db.Handle
	org      models.Organization
	orgAdmin models.User
	target   models.User
}

func newMemberRolesFixture(t *testing.T) (memberRolesFixture, *handlers.Handlers) {
	t.Helper()

	handle := testutil.NewPostgresDB(t)
	ctx := t.Context()
	var f memberRolesFixture
	f.handle = handle

	f.org = seed.SeedOrg(t, handle, models.Organization{Slug: "roles-org", DisplayName: "Roles Org", ContactEmail: "roles@example.com"})
	f.orgAdmin = seed.SeedUser(t, handle, models.User{
		OrganizationID:  f.org.ID,
		ExternalSubject: "org-admin-sub",
		Email:           "orgadmin@roles-org.nl",
		DisplayName:     "Org Admin",
	})
	f.target = seed.SeedUser(t, handle, models.User{
		OrganizationID:  f.org.ID,
		ExternalSubject: "target-sub",
		Email:           "target@roles-org.nl",
		DisplayName:     "Target Platform Admin",
	})

	require.NoError(t, handle.RW.WithContext(ctx).Create(&models.RoleAssignment{
		UserID:          f.target.ID,
		Role:            models.RolePlatformAdmin,
		GrantedByUserID: f.target.ID,
	}).Error)

	return f, newTestHandlers(t, handle)
}

// assignRolesCtx builds an Echo context for POST /orgs/:slug/members/:user_id/roles.
func assignRolesCtx(e *echo.Echo, f memberRolesFixture, body string, p *auth.Principal) (echo.Context, *httptest.ResponseRecorder) {
	req := asPrincipal(newRequest(http.MethodPost, "/", body), p)
	return echoContext(e, req, "slug", f.org.Slug, "user_id", f.target.ID.String())
}

// activeRoles returns the target's unrevoked roles.
func activeRoles(t *testing.T, f memberRolesFixture) []models.Role {
	t.Helper()
	return rolesOf(activeRolesOf(t, f.handle, f.target.ID))
}

func TestAssignMemberRoles_PlatformAdminRevoke(t *testing.T) {
	t.Parallel()
	t.Run("org_admin_cannot_revoke_platform_admin", func(t *testing.T) {
		t.Parallel()
		f, h := newMemberRolesFixture(t)
		e := echo.New()

		p := principal(f.orgAdmin, models.RoleOrgAdmin)
		c, _ := assignRolesCtx(e, f, `{"roles":["developer"]}`, p)
		err := h.AssignMemberRoles(c)

		var apiErr *apierror.Error
		require.True(t, errors.As(err, &apiErr))
		assert.Equal(t, http.StatusForbidden, apiErr.Status)
		assert.ElementsMatch(t, []models.Role{models.RolePlatformAdmin}, activeRoles(t, f))
	})

	t.Run("platform_admin_can_revoke_and_grant_platform_admin", func(t *testing.T) {
		t.Parallel()
		f, h := newMemberRolesFixture(t)
		e := echo.New()

		p := principal(f.orgAdmin, models.RolePlatformAdmin)
		c, rec := assignRolesCtx(e, f, `{"roles":["developer"]}`, p)
		require.NoError(t, h.AssignMemberRoles(c))
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.ElementsMatch(t, []models.Role{models.RoleDeveloper}, activeRoles(t, f))

		c, rec = assignRolesCtx(e, f, `{"roles":["developer","platform_admin"]}`, p)
		require.NoError(t, h.AssignMemberRoles(c))
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.ElementsMatch(t, []models.Role{models.RoleDeveloper, models.RolePlatformAdmin}, activeRoles(t, f))
	})
}

// assignRolesFor builds an Echo context for
// POST /orgs/:slug/members/:user_id/roles with an explicit slug and user.
func assignRolesFor(e *echo.Echo, slug string, userID uuid.UUID, body string, p *auth.Principal) (echo.Context, *httptest.ResponseRecorder) {
	req := asPrincipal(newRequest(http.MethodPost, "/", body), p)
	return echoContext(e, req, "slug", slug, "user_id", userID.String())
}

// activeRolesOf returns userID's unrevoked role assignments.
func activeRolesOf(t *testing.T, handle *db.Handle, userID uuid.UUID) []models.RoleAssignment {
	t.Helper()
	var rows []models.RoleAssignment
	require.NoError(t, handle.RW.WithContext(t.Context()).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Find(&rows).Error)
	return rows
}

func TestAssignMemberRoles_Authorization(t *testing.T) {
	t.Parallel()
	t.Run("org_admin_assigns_roles_in_own_org", func(t *testing.T) {
		t.Parallel()
		f, h := newMemberRolesFixture(t)
		e := echo.New()
		member := seed.SeedUser(t, f.handle, models.User{OrganizationID: f.org.ID})

		c, rec := assignRolesFor(e, f.org.Slug, member.ID, `{"roles":["developer","deployer"]}`, principal(f.orgAdmin, models.RoleOrgAdmin))
		require.NoError(t, h.AssignMemberRoles(c))
		assert.Equal(t, http.StatusOK, rec.Code)

		rows := activeRolesOf(t, f.handle, member.ID)
		got := make([]models.Role, 0, len(rows))
		for _, r := range rows {
			got = append(got, r.Role)
			assert.Equal(t, f.orgAdmin.ID, r.GrantedByUserID)
		}
		assert.ElementsMatch(t, []models.Role{models.RoleDeveloper, models.RoleDeployer}, got)
	})

	t.Run("target_in_other_org_returns_404", func(t *testing.T) {
		t.Parallel()
		f, h := newMemberRolesFixture(t)
		e := echo.New()
		otherOrg := seed.SeedOrg(t, f.handle, models.Organization{})
		outsider := seed.SeedUser(t, f.handle, models.User{OrganizationID: otherOrg.ID})

		c, _ := assignRolesFor(e, f.org.Slug, outsider.ID, `{"roles":["developer"]}`, principal(f.orgAdmin, models.RoleOrgAdmin))
		requireAPIError(t, h.AssignMemberRoles(c), http.StatusNotFound)
		assert.Empty(t, activeRolesOf(t, f.handle, outsider.ID),
			"no role may be granted to a user outside the slug's organisation")
	})

	t.Run("org_admin_of_other_org_returns_403", func(t *testing.T) {
		t.Parallel()
		f, h := newMemberRolesFixture(t)
		e := echo.New()
		otherOrg := seed.SeedOrg(t, f.handle, models.Organization{})
		otherAdmin := seed.SeedUser(t, f.handle, models.User{OrganizationID: otherOrg.ID})

		c, _ := assignRolesCtx(e, f, `{"roles":["developer","platform_admin"]}`, principal(otherAdmin, models.RoleOrgAdmin))
		requireAPIError(t, h.AssignMemberRoles(c), http.StatusForbidden)
		assert.ElementsMatch(t, []models.Role{models.RolePlatformAdmin}, activeRoles(t, f))
	})

	t.Run("org_admin_cannot_grant_platform_admin", func(t *testing.T) {
		t.Parallel()
		f, h := newMemberRolesFixture(t)
		e := echo.New()
		member := seed.SeedUser(t, f.handle, models.User{OrganizationID: f.org.ID})

		c, _ := assignRolesFor(e, f.org.Slug, member.ID, `{"roles":["platform_admin"]}`, principal(f.orgAdmin, models.RoleOrgAdmin))
		requireAPIError(t, h.AssignMemberRoles(c), http.StatusForbidden)
		assert.Empty(t, activeRolesOf(t, f.handle, member.ID))
	})

	t.Run("unknown_role_returns_422", func(t *testing.T) {
		t.Parallel()
		f, h := newMemberRolesFixture(t)
		e := echo.New()

		c, _ := assignRolesCtx(e, f, `{"roles":["platform_admin","superuser"]}`, principal(f.orgAdmin, models.RolePlatformAdmin))
		apiErr := requireAPIError(t, h.AssignMemberRoles(c), http.StatusUnprocessableEntity)
		assert.True(t, hasFieldCode(apiErr.Errors, "invalid_role"), "got %+v", apiErr.Errors)
		assert.ElementsMatch(t, []models.Role{models.RolePlatformAdmin}, activeRoles(t, f))
	})

	t.Run("unknown_org_returns_404", func(t *testing.T) {
		t.Parallel()
		f, h := newMemberRolesFixture(t)
		e := echo.New()

		c, _ := assignRolesFor(e, "no-such-org", f.target.ID, `{"roles":["developer"]}`, principal(f.orgAdmin, models.RolePlatformAdmin))
		requireAPIError(t, h.AssignMemberRoles(c), http.StatusNotFound)
	})
}

// seedMemberWithRoles creates a member of f.org holding roles, granted by
// the org admin.
func seedMemberWithRoles(t *testing.T, f memberRolesFixture, roles ...models.Role) models.User {
	t.Helper()
	member := seed.SeedUser(t, f.handle, models.User{OrganizationID: f.org.ID})
	for _, r := range roles {
		require.NoError(t, f.handle.RW.WithContext(t.Context()).Create(&models.RoleAssignment{
			UserID:          member.ID,
			Role:            r,
			GrantedByUserID: f.orgAdmin.ID,
		}).Error)
	}
	return member
}

// rolesOf returns the roles in rows.
func rolesOf(rows []models.RoleAssignment) []models.Role {
	roles := make([]models.Role, 0, len(rows))
	for _, r := range rows {
		roles = append(roles, r.Role)
	}
	return roles
}

func TestAssignMemberRoles_AuditorPlatformOnly(t *testing.T) {
	t.Parallel()
	t.Run("org_admin_cannot_grant_auditor", func(t *testing.T) {
		t.Parallel()
		f, h := newMemberRolesFixture(t)
		e := echo.New()
		member := seedMemberWithRoles(t, f, models.RoleDeveloper)

		c, _ := assignRolesFor(e, f.org.Slug, member.ID, `{"roles":["developer","auditor"]}`, principal(f.orgAdmin, models.RoleOrgAdmin))
		apiErr := requireAPIError(t, h.AssignMemberRoles(c), http.StatusForbidden)
		assert.Equal(t, "only platform_admin may grant auditor", apiErr.Message)
		assert.ElementsMatch(t, []models.Role{models.RoleDeveloper}, rolesOf(activeRolesOf(t, f.handle, member.ID)))
	})

	t.Run("org_admin_cannot_revoke_auditor", func(t *testing.T) {
		t.Parallel()
		f, h := newMemberRolesFixture(t)
		e := echo.New()
		member := seedMemberWithRoles(t, f, models.RoleDeveloper, models.RoleAuditor)

		c, _ := assignRolesFor(e, f.org.Slug, member.ID, `{"roles":["developer","deployer"]}`, principal(f.orgAdmin, models.RoleOrgAdmin))
		apiErr := requireAPIError(t, h.AssignMemberRoles(c), http.StatusForbidden)
		assert.Equal(t, "only platform_admin may revoke auditor", apiErr.Message)
		assert.ElementsMatch(t, []models.Role{models.RoleDeveloper, models.RoleAuditor}, rolesOf(activeRolesOf(t, f.handle, member.ID)))
	})

	t.Run("org_admin_changes_other_roles_keeping_auditor", func(t *testing.T) {
		t.Parallel()
		f, h := newMemberRolesFixture(t)
		e := echo.New()
		member := seedMemberWithRoles(t, f, models.RoleDeveloper, models.RoleAuditor)

		c, rec := assignRolesFor(e, f.org.Slug, member.ID, `{"roles":["auditor","deployer"]}`, principal(f.orgAdmin, models.RoleOrgAdmin))
		require.NoError(t, h.AssignMemberRoles(c))
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.ElementsMatch(t, []models.Role{models.RoleAuditor, models.RoleDeployer}, rolesOf(activeRolesOf(t, f.handle, member.ID)))
	})

	t.Run("platform_admin_grants_and_revokes_auditor_in_any_org", func(t *testing.T) {
		t.Parallel()
		f, h := newMemberRolesFixture(t)
		e := echo.New()
		otherOrg := seed.SeedOrg(t, f.handle, models.Organization{})
		platformAdmin := seed.SeedUser(t, f.handle, models.User{OrganizationID: otherOrg.ID})
		member := seedMemberWithRoles(t, f, models.RoleDeveloper)
		p := principal(platformAdmin, models.RolePlatformAdmin)

		c, rec := assignRolesFor(e, f.org.Slug, member.ID, `{"roles":["developer","auditor"]}`, p)
		require.NoError(t, h.AssignMemberRoles(c))
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.ElementsMatch(t, []models.Role{models.RoleDeveloper, models.RoleAuditor}, rolesOf(activeRolesOf(t, f.handle, member.ID)))

		c, rec = assignRolesFor(e, f.org.Slug, member.ID, `{"roles":["developer"]}`, p)
		require.NoError(t, h.AssignMemberRoles(c))
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.ElementsMatch(t, []models.Role{models.RoleDeveloper}, rolesOf(activeRolesOf(t, f.handle, member.ID)))
	})
}

// TestAssignMemberRoles_RouteRoles checks the role gate on the route. The
// middleware rejects the request before the handler runs, so no database
// is needed.
func TestAssignMemberRoles_RouteRoles(t *testing.T) {
	t.Parallel()
	h := newTestHandlers(t, nil)
	target := "/api/v1/orgs/roles-org/members/" + uuid.NewString() + "/roles"

	for _, role := range []models.Role{models.RoleDeveloper, models.RoleDeployer, models.RoleAuditor, models.RoleClusterAdmin} {
		t.Run(string(role)+"_returns_403", func(t *testing.T) {
			t.Parallel()
			req := asPrincipal(newRequest(http.MethodPost, target, `{"roles":["org_admin"]}`), principal(testUser(), role))
			assert.Equal(t, http.StatusForbidden, serveRoute(h, req).Code)
		})
	}

	t.Run("anonymous_returns_401", func(t *testing.T) {
		t.Parallel()
		rec := serveRoute(h, newRequest(http.MethodPost, target, `{"roles":["org_admin"]}`))
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}

// TestListMembers_TenantBoundary checks that an org admin can read only their
// own organisation's members, while a platform admin can read any.
func TestListMembers_TenantBoundary(t *testing.T) {
	t.Parallel()
	f, h := newMemberRolesFixture(t)
	other := seed.SeedOrg(t, f.handle, models.Organization{})
	outsider := seed.SeedUser(t, f.handle, models.User{OrganizationID: other.ID})
	e := echo.New()

	list := func(p *auth.Principal) (echo.Context, *httptest.ResponseRecorder) {
		return echoContext(e, asPrincipal(newRequest(http.MethodGet, "/", ""), p), "slug", f.org.Slug)
	}

	t.Run("org_admin_of_another_org_gets_403", func(t *testing.T) {
		c, rec := list(principal(outsider, models.RoleOrgAdmin))
		requireAPIError(t, h.ListMembers(c), http.StatusForbidden)
		assert.NotContains(t, rec.Body.String(), f.orgAdmin.Email, "no member data may leak")
	})

	t.Run("own_org_admin_gets_members", func(t *testing.T) {
		c, rec := list(principal(f.orgAdmin, models.RoleOrgAdmin))
		require.NoError(t, h.ListMembers(c))
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), f.orgAdmin.Email)
	})

	t.Run("platform_admin_of_another_org_gets_members", func(t *testing.T) {
		c, rec := list(principal(outsider, models.RolePlatformAdmin))
		require.NoError(t, h.ListMembers(c))
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), f.orgAdmin.Email)
	})
}
