package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/handlers"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAssignMemberRoles_AppUser checks that an org_admin may grant and
// revoke app_user in their own organisation, that each change writes a
// role.assigned event, and that the auditor rule still holds.
func TestAssignMemberRoles_AppUser(t *testing.T) {
	t.Parallel()
	handle := testutil.NewPostgresDB(t)
	h := handlers.New(handle, audit.NewLogger(handle), nil, nil, nil, nil, nil, nil,
		[]byte(testJoinSecret), "http://localhost:8080", nil, "", "")
	org := seed.SeedOrg(t, handle, models.Organization{})
	other := seed.SeedOrg(t, handle, models.Organization{})
	admin := principal(seed.SeedUser(t, handle, models.User{OrganizationID: org.ID}), models.RoleOrgAdmin)
	member := seed.SeedUser(t, handle, models.User{OrganizationID: org.ID})
	outsider := seed.SeedUser(t, handle, models.User{OrganizationID: other.ID})

	assign := func(slug, userID, body string) int {
		req := newRequest(http.MethodPost, "/api/v1/orgs/"+slug+"/members/"+userID+"/roles", body)
		return serveRoute(h, asPrincipal(req, admin)).Code
	}
	active := func() []models.Role {
		var rows []models.RoleAssignment
		require.NoError(t, handle.RW.WithContext(t.Context()).
			Where("user_id = ? AND revoked_at IS NULL", member.ID).Find(&rows).Error)
		roles := make([]models.Role, 0, len(rows))
		for _, r := range rows {
			roles = append(roles, r.Role)
		}
		return roles
	}
	events := func() []map[string]any {
		var rows []models.AuditLog
		require.NoError(t, handle.RW.WithContext(t.Context()).
			Where("event_type = ? AND subject_id = ?", "role.assigned", member.ID).
			Order("occurred_at").Find(&rows).Error)
		out := make([]map[string]any, 0, len(rows))
		for _, r := range rows {
			var payload map[string]any
			require.NoError(t, json.Unmarshal(r.Payload, &payload))
			assert.Equal(t, admin.UserID, *r.ActorUserID)
			out = append(out, payload)
		}
		return out
	}

	require.Equal(t, http.StatusOK, assign(org.Slug, member.ID.String(), `{"roles":["app_user"]}`))
	assert.Equal(t, []models.Role{models.RoleAppUser}, active())

	require.Equal(t, http.StatusOK, assign(org.Slug, member.ID.String(), `{"roles":["developer"]}`))
	assert.Equal(t, []models.Role{models.RoleDeveloper}, active())

	assert.Equal(t, []map[string]any{
		{"roles": []any{"app_user"}},
		{"roles": []any{"developer"}},
	}, events(), "a grant and a revoke each write role.assigned")

	t.Run("auditor_stays_platform_only", func(t *testing.T) {
		req := newRequest(http.MethodPost, "/api/v1/orgs/"+org.Slug+"/members/"+member.ID.String()+"/roles",
			`{"roles":["app_user","auditor"]}`)
		c, _ := echoContext(echo.New(), asPrincipal(req, admin), "slug", org.Slug, "user_id", member.ID.String())
		requireAPIError(t, h.AssignMemberRoles(c), http.StatusForbidden)
		assert.Equal(t, []models.Role{models.RoleDeveloper}, active())
	})

	t.Run("not_in_another_org", func(t *testing.T) {
		req := newRequest(http.MethodPost, "/", `{"roles":["app_user"]}`)
		c, _ := echoContext(echo.New(), asPrincipal(req, admin), "slug", other.Slug, "user_id", outsider.ID.String())
		requireAPIError(t, h.AssignMemberRoles(c), http.StatusForbidden)
	})
}

// TestCreateAPIKey_RefusesAppUser checks that app_user can never be part of
// an API key's role scope, and that an app_user-only user cannot reach key
// creation at all.
func TestCreateAPIKey_RefusesAppUser(t *testing.T) {
	t.Parallel()
	h := newTestHandlers(t, nil)

	t.Run("not_allowed_in_the_scope", func(t *testing.T) {
		p := principal(testUser(), models.RoleDeveloper, models.RoleAppUser)
		req := asPrincipal(newRequest(http.MethodPost, "/", `{"name":"k","roles":["app_user"],"expires_days":1}`), p)
		c, _ := echoContext(echo.New(), req)
		apiErr := requireAPIError(t, h.CreateAPIKey(c), http.StatusUnprocessableEntity)
		require.Len(t, apiErr.Errors, 1)
		assert.Equal(t, "roles[0]", apiErr.Errors[0].Path)
		assert.Equal(t, "not_allowed", apiErr.Errors[0].Code)
	})

	t.Run("app_user_only_cannot_create_keys", func(t *testing.T) {
		req := newRequest(http.MethodPost, "/api/v1/api-keys", `{"name":"k","roles":["app_user"],"expires_days":1}`)
		rec := serveRoute(h, asPrincipal(req, principal(testUser(), models.RoleAppUser)))
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})
}
