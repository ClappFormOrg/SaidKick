package handlers_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nonPlatformRoles is every role except platform_admin.
var nonPlatformRoles = []models.Role{
	models.RoleDeveloper, models.RoleAuditor, models.RoleDeployer,
	models.RoleClusterAdmin, models.RoleOrgAdmin, models.RoleAppUser,
}

// countRows returns the number of rows of model in the database.
func countRows(t *testing.T, handle *db.Handle, model any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, handle.RW.WithContext(t.Context()).Model(model).Count(&n).Error)
	return n
}

// ─── POST /orgs ──────────────────────────────────────────────────────────────

// TestCreateOrganization_WriteAuthz sends the create-org request as every
// caller that must be refused and checks that none of them writes a row.
func TestCreateOrganization_WriteAuthz(t *testing.T) {
	t.Parallel()
	handle := testutil.NewPostgresDB(t)
	h := newTestHandlers(t, handle)
	const body = `{"slug":"rogue-org","display_name":"Rogue","contact_email":"rogue@example.com"}`
	before := countRows(t, handle, &models.Organization{})

	t.Run("anonymous_returns_401", func(t *testing.T) {
		rec := serveRoute(h, newRequest(http.MethodPost, "/api/v1/orgs", body))
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
	for _, role := range nonPlatformRoles {
		t.Run(string(role)+"_returns_403", func(t *testing.T) {
			req := asPrincipal(newRequest(http.MethodPost, "/api/v1/orgs", body), principal(testUser(), role))
			assert.Equal(t, http.StatusForbidden, serveRoute(h, req).Code)
		})
	}
	assert.Equal(t, before, countRows(t, handle, &models.Organization{}), "a refused caller wrote an organisation")

	t.Run("platform_admin_creates", func(t *testing.T) {
		admin := seed.SeedUser(t, handle, models.User{OrganizationID: seed.SeedOrg(t, handle, models.Organization{}).ID})
		c, rec := echoContext(echo.New(), asPrincipal(newRequest(http.MethodPost, "/", body), principal(admin, models.RolePlatformAdmin)))
		require.NoError(t, h.CreateOrganization(c))
		assert.Equal(t, http.StatusCreated, rec.Code)
	})
}

// ─── POST /clusters ──────────────────────────────────────────────────────────

// TestCreateCluster_WriteAuthz checks that cluster registration refuses
// callers without cluster_admin or platform_admin, never hands them a join
// token, and keeps a cluster_admin to clusters owned by their own org.
func TestCreateCluster_WriteAuthz(t *testing.T) {
	t.Parallel()
	handle := testutil.NewPostgresDB(t)
	h := newTestHandlers(t, handle)
	const body = `{"name":"rogue-cluster"}`
	before := countRows(t, handle, &models.Cluster{})

	t.Run("anonymous_returns_401", func(t *testing.T) {
		rec := serveRoute(h, newRequest(http.MethodPost, "/api/v1/clusters", body))
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.NotContains(t, rec.Body.String(), "join_token")
	})
	for _, role := range []models.Role{models.RoleDeveloper, models.RoleAuditor, models.RoleDeployer, models.RoleOrgAdmin} {
		t.Run(string(role)+"_returns_403", func(t *testing.T) {
			req := asPrincipal(newRequest(http.MethodPost, "/api/v1/clusters", body), principal(testUser(), role))
			rec := serveRoute(h, req)
			assert.Equal(t, http.StatusForbidden, rec.Code)
			assert.NotContains(t, rec.Body.String(), "join_token")
		})
	}
	t.Run("handler_refuses_without_role", func(t *testing.T) {
		c, _ := echoContext(echo.New(), asPrincipal(newRequest(http.MethodPost, "/", body), principal(testUser(), models.RoleDeveloper)))
		requireAPIError(t, h.CreateCluster(c), http.StatusForbidden)
	})

	org := seed.SeedOrg(t, handle, models.Organization{})
	clusterAdmin := principal(seed.SeedUser(t, handle, models.User{OrganizationID: org.ID}), models.RoleClusterAdmin)

	t.Run("cluster_admin_public_scope_returns_403", func(t *testing.T) {
		c, _ := echoContext(echo.New(), asPrincipal(newRequest(http.MethodPost, "/", `{"name":"pub","scope":"public"}`), clusterAdmin))
		requireAPIError(t, h.CreateCluster(c), http.StatusForbidden)
	})
	assert.Equal(t, before, countRows(t, handle, &models.Cluster{}), "a refused caller wrote a cluster")

	t.Run("cluster_admin_registers_own_org_cluster", func(t *testing.T) {
		reqBody := `{"name":"own","owning_org_id":"` + uuid.NewString() + `"}`
		c, rec := echoContext(echo.New(), asPrincipal(newRequest(http.MethodPost, "/", reqBody), clusterAdmin))
		require.NoError(t, h.CreateCluster(c))
		require.Equal(t, http.StatusCreated, rec.Code)

		var resp struct {
			Cluster map[string]any `json:"cluster"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, string(models.ClusterScopeOrg), resp.Cluster["scope"])
		assert.Equal(t, org.ID.String(), resp.Cluster["owning_organization_id"], "owning_org_id from the body must be ignored")
	})
}

// ─── platform_admin-only writes ──────────────────────────────────────────────

// TestPlatformAdminWrites_HandlerRoles calls the platform_admin-only write
// handlers directly, bypassing the route gate, and checks that each refuses
// every other role before touching the database.
func TestPlatformAdminWrites_HandlerRoles(t *testing.T) {
	t.Parallel()
	h := newTestHandlers(t, nil)

	writes := map[string]struct {
		call func(echo.Context) error
		body string
	}{
		"create_org":      {h.CreateOrganization, `{"slug":"x-org","display_name":"X","contact_email":"x@example.com"}`},
		"create_advisory": {h.CreateAdvisory, `{"title":"t","body":"b","severity":"info"}`},
		"change_scope":    {h.ChangeClusterScope, `{"new_scope":"public","reason":"r"}`},
	}
	for name, w := range writes {
		for _, role := range nonPlatformRoles {
			t.Run(name+"_"+string(role)+"_returns_403", func(t *testing.T) {
				t.Parallel()
				req := asPrincipal(newRequest(http.MethodPost, "/", w.body), principal(testUser(), role))
				c, _ := echoContext(echo.New(), req, "id", uuid.NewString())
				apiErr := requireAPIError(t, w.call(c), http.StatusForbidden)
				assert.Equal(t, "forbidden", apiErr.Code)
			})
		}
	}
}

// ─── app_user ────────────────────────────────────────────────────────────────

// appUserUngatedWrites are the mutating routes with no role gate. Each
// authenticates or authorises the caller another way, so an app_user is
// refused there by something other than the route table's 403.
var appUserUngatedWrites = map[string]string{
	"POST /api/v1/clusters/:id/handshake": "one-time join-token JWT",
	"POST /api/v1/webhooks/github":        "HMAC signature",
	"DELETE /api/v1/api-keys/:id":         "owner or platform_admin, checked in the handler",
}

// TestAppUser_RefusedOnEveryWrite walks every mutating route in the real
// route table as a principal holding only app_user and checks that each is
// refused, so a new write route cannot forget its gate.
func TestAppUser_RefusedOnEveryWrite(t *testing.T) {
	t.Parallel()
	handle := testutil.NewPostgresDB(t)
	h := newTestHandlers(t, handle)
	org := seed.SeedOrg(t, handle, models.Organization{})
	appUser := principal(seed.SeedUser(t, handle, models.User{OrganizationID: org.ID}), models.RoleAppUser)

	e := echo.New()
	h.Register(e)
	params := strings.NewReplacer(
		":id", uuid.NewString(), ":slug", org.Slug, ":user_id", uuid.NewString(),
		":org_id", uuid.NewString(), ":cmd_id", uuid.NewString(), ":name", "x",
	)
	writes := 0
	for _, r := range e.Routes() {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == echo.RouteNotFound {
			continue
		}
		writes++
		key := r.Method + " " + r.Path
		target := params.Replace(r.Path)
		t.Run(key, func(t *testing.T) {
			rec := serveRoute(h, asPrincipal(newRequest(r.Method, target, `{}`), appUser))
			if reason, ok := appUserUngatedWrites[key]; ok {
				assert.GreaterOrEqual(t, rec.Code, 400, "refused by %s", reason)
				return
			}
			assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		})
	}
	assert.Greater(t, writes, 20, "the route walk found too few writes")
}

// TestAppUser_RefusedOnPrivilegedReads checks the role-gated read routes.
func TestAppUser_RefusedOnPrivilegedReads(t *testing.T) {
	t.Parallel()
	h := newTestHandlers(t, nil)
	appUser := principal(testUser(), models.RoleAppUser)
	for _, target := range []string{
		"/api/v1/audit-log",
		"/api/v1/audit-submissions",
		"/api/v1/apps/x/versions",
		"/api/v1/orgs/some-org/members",
		"/api/v1/clusters/" + uuid.NewString() + "/access",
	} {
		t.Run(target, func(t *testing.T) {
			rec := serveRoute(h, asPrincipal(newRequest(http.MethodGet, target, ""), appUser))
			assert.Equal(t, http.StatusForbidden, rec.Code)
		})
	}
}
