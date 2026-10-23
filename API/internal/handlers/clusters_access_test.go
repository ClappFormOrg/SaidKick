package handlers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

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

// ─── Fixtures ────────────────────────────────────────────────────────────────

// clusterAccessFixture holds a restricted cluster owned by ownerOrg, a
// public cluster, two orgs that may be granted access, and a platform admin
// from a third org.
type clusterAccessFixture struct {
	handle     *db.Handle
	ownerOrg   models.Organization
	granteeOrg models.Organization
	otherOrg   models.Organization
	admin      models.User
	restricted models.Cluster
	public     models.Cluster
}

func newClusterAccessFixture(t *testing.T) (clusterAccessFixture, *handlers.Handlers) {
	t.Helper()

	handle := testutil.NewPostgresDB(t)
	f := clusterAccessFixture{handle: handle}
	f.ownerOrg = seed.SeedOrg(t, handle, models.Organization{})
	f.granteeOrg = seed.SeedOrg(t, handle, models.Organization{})
	f.otherOrg = seed.SeedOrg(t, handle, models.Organization{})
	adminOrg := seed.SeedOrg(t, handle, models.Organization{})
	f.admin = seed.SeedUser(t, handle, models.User{OrganizationID: adminOrg.ID})
	f.restricted = seed.SeedCluster(t, handle, models.Cluster{
		Scope:                models.ClusterScopeRestricted,
		OwningOrganizationID: &f.ownerOrg.ID,
	})
	f.public = seed.SeedCluster(t, handle, models.Cluster{Scope: models.ClusterScopePublic})

	return f, newTestHandlers(t, handle)
}

// grantAccessCtx builds an Echo context for POST /clusters/:id/access.
func grantAccessCtx(e *echo.Echo, clusterID, orgID uuid.UUID, p *auth.Principal) (echo.Context, *httptest.ResponseRecorder) {
	body := fmt.Sprintf(`{"organization_id":%q}`, orgID)
	return echoContext(e, asPrincipal(newRequest(http.MethodPost, "/", body), p), "id", clusterID.String())
}

// revokeAccessCtx builds an Echo context for DELETE /clusters/:id/access/:org_id.
func revokeAccessCtx(e *echo.Echo, clusterID uuid.UUID, orgID string, p *auth.Principal) (echo.Context, *httptest.ResponseRecorder) {
	req := asPrincipal(newRequest(http.MethodDelete, "/", ""), p)
	return echoContext(e, req, "id", clusterID.String(), "org_id", orgID)
}

// grantsFor returns the organisations holding a grant on clusterID.
func grantsFor(t *testing.T, handle *db.Handle, clusterID uuid.UUID) []uuid.UUID {
	t.Helper()
	var rows []models.ClusterAccessGrant
	require.NoError(t, handle.RW.WithContext(t.Context()).
		Where("cluster_id = ?", clusterID).Find(&rows).Error)
	orgs := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		orgs = append(orgs, r.OrganizationID)
	}
	return orgs
}

// ─── GrantClusterAccess ──────────────────────────────────────────────────────

func TestGrantClusterAccess(t *testing.T) {
	t.Parallel()
	t.Run("platform_admin_grants_other_org", func(t *testing.T) {
		t.Parallel()
		f, h := newClusterAccessFixture(t)
		e := echo.New()

		c, rec := grantAccessCtx(e, f.restricted.ID, f.granteeOrg.ID, principal(f.admin, models.RolePlatformAdmin))
		require.NoError(t, h.GrantClusterAccess(c))
		assert.Equal(t, http.StatusOK, rec.Code)

		var resp map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, f.restricted.ID.String(), resp["cluster_id"])
		assert.Equal(t, f.granteeOrg.ID.String(), resp["organization_id"])
		assert.Equal(t, f.admin.ID.String(), resp["granted_by"])
		assert.Equal(t, []uuid.UUID{f.granteeOrg.ID}, grantsFor(t, f.handle, f.restricted.ID))
	})

	t.Run("repeat_grant_is_idempotent", func(t *testing.T) {
		t.Parallel()
		f, h := newClusterAccessFixture(t)
		e := echo.New()
		p := principal(f.admin, models.RolePlatformAdmin)

		for range 2 {
			c, rec := grantAccessCtx(e, f.restricted.ID, f.granteeOrg.ID, p)
			require.NoError(t, h.GrantClusterAccess(c))
			assert.Equal(t, http.StatusOK, rec.Code)
		}
		assert.Equal(t, []uuid.UUID{f.granteeOrg.ID}, grantsFor(t, f.handle, f.restricted.ID))
	})

	t.Run("non_restricted_cluster_returns_409", func(t *testing.T) {
		t.Parallel()
		f, h := newClusterAccessFixture(t)
		e := echo.New()

		c, _ := grantAccessCtx(e, f.public.ID, f.granteeOrg.ID, principal(f.admin, models.RolePlatformAdmin))
		apiErr := requireAPIError(t, h.GrantClusterAccess(c), http.StatusConflict)
		assert.Equal(t, "wrong_scope", apiErr.Code)
		assert.Empty(t, grantsFor(t, f.handle, f.public.ID))
	})

	t.Run("unknown_cluster_returns_404", func(t *testing.T) {
		t.Parallel()
		f, h := newClusterAccessFixture(t)
		e := echo.New()

		c, _ := grantAccessCtx(e, uuid.New(), f.granteeOrg.ID, principal(f.admin, models.RolePlatformAdmin))
		requireAPIError(t, h.GrantClusterAccess(c), http.StatusNotFound)
	})

	t.Run("unknown_org_returns_422", func(t *testing.T) {
		t.Parallel()
		f, h := newClusterAccessFixture(t)
		e := echo.New()

		c, _ := grantAccessCtx(e, f.restricted.ID, uuid.New(), principal(f.admin, models.RolePlatformAdmin))
		apiErr := requireAPIError(t, h.GrantClusterAccess(c), http.StatusUnprocessableEntity)
		assert.True(t, hasFieldCode(apiErr.Errors, "not_found"), "got %+v", apiErr.Errors)
		assert.Empty(t, grantsFor(t, f.handle, f.restricted.ID))
	})

	t.Run("database_rejects_grant_for_unknown_org", func(t *testing.T) {
		t.Parallel()
		f, _ := newClusterAccessFixture(t)

		err := f.handle.RW.WithContext(t.Context()).Create(&models.ClusterAccessGrant{
			ClusterID:      f.restricted.ID,
			OrganizationID: uuid.New(),
			GrantedBy:      f.admin.ID,
		}).Error
		require.Error(t, err, "the organisation foreign key must reject the row")
		assert.Empty(t, grantsFor(t, f.handle, f.restricted.ID))
	})
}

// TestGrantClusterAccess_Validation covers requests rejected before the
// handler reads the database.
func TestGrantClusterAccess_Validation(t *testing.T) {
	t.Parallel()
	h := newTestHandlers(t, nil)
	admin := principal(testUser(), models.RolePlatformAdmin)

	t.Run("missing_organization_id_returns_422", func(t *testing.T) {
		t.Parallel()
		c, _ := echoContext(echo.New(), asPrincipal(newRequest(http.MethodPost, "/", `{}`), admin), "id", uuid.NewString())
		requireAPIError(t, h.GrantClusterAccess(c), http.StatusUnprocessableEntity)
	})

	t.Run("malformed_cluster_id_returns_404", func(t *testing.T) {
		t.Parallel()
		body := fmt.Sprintf(`{"organization_id":%q}`, uuid.New())
		c, _ := echoContext(echo.New(), asPrincipal(newRequest(http.MethodPost, "/", body), admin), "id", "not-a-uuid")
		requireAPIError(t, h.GrantClusterAccess(c), http.StatusNotFound)
	})

	t.Run("anonymous_returns_401", func(t *testing.T) {
		t.Parallel()
		body := fmt.Sprintf(`{"organization_id":%q}`, uuid.New())
		c, _ := echoContext(echo.New(), newRequest(http.MethodPost, "/", body), "id", uuid.NewString())
		requireAPIError(t, h.GrantClusterAccess(c), http.StatusUnauthorized)
	})
}

// ─── RevokeClusterAccess ─────────────────────────────────────────────────────

func TestRevokeClusterAccess(t *testing.T) {
	t.Parallel()
	t.Run("revokes_only_the_named_org", func(t *testing.T) {
		t.Parallel()
		f, h := newClusterAccessFixture(t)
		e := echo.New()
		p := principal(f.admin, models.RolePlatformAdmin)
		for _, org := range []uuid.UUID{f.granteeOrg.ID, f.otherOrg.ID} {
			c, _ := grantAccessCtx(e, f.restricted.ID, org, p)
			require.NoError(t, h.GrantClusterAccess(c))
		}

		c, rec := revokeAccessCtx(e, f.restricted.ID, f.granteeOrg.ID.String(), p)
		require.NoError(t, h.RevokeClusterAccess(c))
		assert.Equal(t, http.StatusNoContent, rec.Code)
		assert.Equal(t, []uuid.UUID{f.otherOrg.ID}, grantsFor(t, f.handle, f.restricted.ID))
	})

	t.Run("revoking_absent_grant_returns_204", func(t *testing.T) {
		t.Parallel()
		f, h := newClusterAccessFixture(t)
		e := echo.New()
		p := principal(f.admin, models.RolePlatformAdmin)
		c, _ := grantAccessCtx(e, f.restricted.ID, f.granteeOrg.ID, p)
		require.NoError(t, h.GrantClusterAccess(c))

		for range 2 {
			c, rec := revokeAccessCtx(e, f.restricted.ID, f.granteeOrg.ID.String(), p)
			require.NoError(t, h.RevokeClusterAccess(c))
			assert.Equal(t, http.StatusNoContent, rec.Code)
		}
		assert.Empty(t, grantsFor(t, f.handle, f.restricted.ID))
	})
}

// TestRevokeClusterAccess_Validation covers requests rejected before the
// handler reads the database.
func TestRevokeClusterAccess_Validation(t *testing.T) {
	t.Parallel()
	h := newTestHandlers(t, nil)
	admin := principal(testUser(), models.RolePlatformAdmin)

	t.Run("malformed_org_id_returns_400", func(t *testing.T) {
		t.Parallel()
		c, _ := revokeAccessCtx(echo.New(), uuid.New(), "not-a-uuid", admin)
		requireAPIError(t, h.RevokeClusterAccess(c), http.StatusBadRequest)
	})

	t.Run("malformed_cluster_id_returns_404", func(t *testing.T) {
		t.Parallel()
		req := asPrincipal(newRequest(http.MethodDelete, "/", ""), admin)
		c, _ := echoContext(echo.New(), req, "id", "not-a-uuid", "org_id", uuid.NewString())
		requireAPIError(t, h.RevokeClusterAccess(c), http.StatusNotFound)
	})

	t.Run("anonymous_returns_401", func(t *testing.T) {
		t.Parallel()
		c, _ := echoContext(echo.New(), newRequest(http.MethodDelete, "/", ""), "id", uuid.NewString(), "org_id", uuid.NewString())
		requireAPIError(t, h.RevokeClusterAccess(c), http.StatusUnauthorized)
	})
}

// ─── Route role gate ─────────────────────────────────────────────────────────

// TestClusterAccess_RouteRoles checks that only platform_admin passes the
// role gate on the grant and revoke routes, so an org's own cluster_admin
// or org_admin is stopped here.
func TestClusterAccess_RouteRoles(t *testing.T) {
	t.Parallel()
	h := newTestHandlers(t, nil)
	clusterID := uuid.NewString()
	orgID := uuid.NewString()

	for _, role := range []models.Role{models.RoleClusterAdmin, models.RoleOrgAdmin, models.RoleDeployer, models.RoleDeveloper, models.RoleAuditor} {
		t.Run("grant_"+string(role)+"_returns_403", func(t *testing.T) {
			t.Parallel()
			body := fmt.Sprintf(`{"organization_id":%q}`, orgID)
			req := asPrincipal(newRequest(http.MethodPost, "/api/v1/clusters/"+clusterID+"/access", body), principal(testUser(), role))
			assert.Equal(t, http.StatusForbidden, serveRoute(h, req).Code)
		})
		t.Run("revoke_"+string(role)+"_returns_403", func(t *testing.T) {
			t.Parallel()
			req := asPrincipal(newRequest(http.MethodDelete, "/api/v1/clusters/"+clusterID+"/access/"+orgID, ""), principal(testUser(), role))
			assert.Equal(t, http.StatusForbidden, serveRoute(h, req).Code)
		})
	}

	t.Run("anonymous_grant_returns_401", func(t *testing.T) {
		t.Parallel()
		body := fmt.Sprintf(`{"organization_id":%q}`, orgID)
		rec := serveRoute(h, newRequest(http.MethodPost, "/api/v1/clusters/"+clusterID+"/access", body))
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}

// TestClusterAccess_HandlerRoles calls the grant, revoke and list handlers
// directly, bypassing the route gate, and checks that each refuses a caller
// without platform_admin before touching the database.
func TestClusterAccess_HandlerRoles(t *testing.T) {
	t.Parallel()
	h := newTestHandlers(t, nil)

	for _, role := range []models.Role{models.RoleClusterAdmin, models.RoleOrgAdmin, models.RoleDeployer, models.RoleDeveloper, models.RoleAuditor} {
		t.Run("grant_"+string(role)+"_returns_403", func(t *testing.T) {
			t.Parallel()
			c, _ := grantAccessCtx(echo.New(), uuid.New(), uuid.New(), principal(testUser(), role))
			apiErr := requireAPIError(t, h.GrantClusterAccess(c), http.StatusForbidden)
			assert.Equal(t, "forbidden", apiErr.Code)
		})
		t.Run("revoke_"+string(role)+"_returns_403", func(t *testing.T) {
			t.Parallel()
			c, _ := revokeAccessCtx(echo.New(), uuid.New(), uuid.NewString(), principal(testUser(), role))
			apiErr := requireAPIError(t, h.RevokeClusterAccess(c), http.StatusForbidden)
			assert.Equal(t, "forbidden", apiErr.Code)
		})
		t.Run("list_"+string(role)+"_returns_403", func(t *testing.T) {
			t.Parallel()
			req := asPrincipal(newRequest(http.MethodGet, "/", ""), principal(testUser(), role))
			c, _ := echoContext(echo.New(), req, "id", uuid.NewString())
			apiErr := requireAPIError(t, h.ListClusterAccessGrants(c), http.StatusForbidden)
			assert.Equal(t, "forbidden", apiErr.Code)
			assert.Equal(t, "missing required role", apiErr.Message)
		})
	}
}
