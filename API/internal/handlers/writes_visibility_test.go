package handlers_test

import (
	"net/http"
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/handlers"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── PATCH / DELETE /clusters/:id ────────────────────────────────────────────

// clusterWriteFixture holds a user in callerOrg, clusters owned by another
// org in each scope (one of them granted to callerOrg), a public cluster
// with no owner, and a restricted cluster callerOrg owns.
type clusterWriteFixture struct {
	caller        models.User
	orgScoped     models.Cluster
	restricted    models.Cluster
	grantedRestr  models.Cluster
	public        models.Cluster
	ownRestricted models.Cluster
}

func newClusterWriteFixture(t *testing.T) (clusterWriteFixture, *handlers.Handlers) {
	t.Helper()
	handle := testutil.NewPostgresDB(t)
	var f clusterWriteFixture
	callerOrg := seed.SeedOrg(t, handle, models.Organization{})
	ownerOrg := seed.SeedOrg(t, handle, models.Organization{})
	f.caller = seed.SeedUser(t, handle, models.User{OrganizationID: callerOrg.ID})
	owner := seed.SeedUser(t, handle, models.User{OrganizationID: ownerOrg.ID})
	f.orgScoped = seed.SeedCluster(t, handle, models.Cluster{Scope: models.ClusterScopeOrg, OwningOrganizationID: &ownerOrg.ID})
	f.restricted = seed.SeedCluster(t, handle, models.Cluster{Scope: models.ClusterScopeRestricted, OwningOrganizationID: &ownerOrg.ID})
	f.grantedRestr = seed.SeedCluster(t, handle, models.Cluster{Scope: models.ClusterScopeRestricted, OwningOrganizationID: &ownerOrg.ID})
	f.public = seed.SeedCluster(t, handle, models.Cluster{Scope: models.ClusterScopePublic})
	f.ownRestricted = seed.SeedCluster(t, handle, models.Cluster{Scope: models.ClusterScopeRestricted, OwningOrganizationID: &callerOrg.ID})
	require.NoError(t, handle.RW.WithContext(t.Context()).Create(&models.ClusterAccessGrant{
		ClusterID: f.grantedRestr.ID, OrganizationID: callerOrg.ID, GrantedBy: owner.ID,
	}).Error)
	return f, newTestHandlers(t, handle)
}

// TestClusterWrites_Visibility checks that PatchCluster and DeleteCluster
// answer a cluster admin from another org with GetCluster's 404 when the
// cluster is hidden from them and 403 when it is visible but not theirs.
func TestClusterWrites_Visibility(t *testing.T) {
	t.Parallel()
	f, h := newClusterWriteFixture(t)
	admin := principal(f.caller, models.RoleClusterAdmin)

	writes := map[string]func(echo.Context) error{"patch": h.PatchCluster, "delete": h.DeleteCluster}
	cases := []struct {
		name    string
		cluster models.Cluster
		status  int
	}{
		{"other_org_cluster_returns_404", f.orgScoped, http.StatusNotFound},
		{"ungranted_restricted_cluster_returns_404", f.restricted, http.StatusNotFound},
		{"granted_restricted_cluster_returns_403", f.grantedRestr, http.StatusForbidden},
		{"public_cluster_returns_403", f.public, http.StatusForbidden},
	}
	for name, call := range writes {
		for _, tc := range cases {
			t.Run(name+"_"+tc.name, func(t *testing.T) {
				req := asPrincipal(newRequest(http.MethodPatch, "/", `{"base_domain":"x.example"}`), admin)
				c, _ := echoContext(echo.New(), req, "id", tc.cluster.ID.String())
				requireAPIError(t, call(c), tc.status)
			})
		}
		t.Run(name+"_without_cluster_admin_returns_403", func(t *testing.T) {
			req := asPrincipal(newRequest(http.MethodPatch, "/", `{"base_domain":"x.example"}`), principal(f.caller, models.RoleDeveloper))
			c, _ := echoContext(echo.New(), req, "id", f.ownRestricted.ID.String())
			apiErr := requireAPIError(t, call(c), http.StatusForbidden)
			assert.Equal(t, "forbidden", apiErr.Code)
		})
	}

	// An org keeps control of a restricted cluster it owns even without a
	// grant to itself.
	t.Run("patch_own_restricted_cluster_succeeds", func(t *testing.T) {
		req := asPrincipal(newRequest(http.MethodPatch, "/", `{"base_domain":"own.example"}`), admin)
		c, rec := echoContext(echo.New(), req, "id", f.ownRestricted.ID.String())
		require.NoError(t, h.PatchCluster(c))
		assert.Equal(t, http.StatusOK, rec.Code)
	})
	t.Run("delete_own_restricted_cluster_succeeds", func(t *testing.T) {
		c, rec := echoContext(echo.New(), asPrincipal(newRequest(http.MethodDelete, "/", ""), admin), "id", f.ownRestricted.ID.String())
		require.NoError(t, h.DeleteCluster(c))
		assert.Equal(t, http.StatusNoContent, rec.Code)
	})
	t.Run("platform_admin_patches_any_cluster", func(t *testing.T) {
		req := asPrincipal(newRequest(http.MethodPatch, "/", `{"base_domain":"pa.example"}`), principal(f.caller, models.RolePlatformAdmin))
		c, rec := echoContext(echo.New(), req, "id", f.restricted.ID.String())
		require.NoError(t, h.PatchCluster(c))
		assert.Equal(t, http.StatusOK, rec.Code)
	})
}

// ─── App developer routes ────────────────────────────────────────────────────

// TestAppWrites_Visibility checks that the developer routes answer a caller
// from another org with GetApp's 404 for an unpublished app, so its slug is
// not confirmed, and with 403 for a published one.
func TestAppWrites_Visibility(t *testing.T) {
	t.Parallel()
	handle := testutil.NewPostgresDB(t)
	h := newTestHandlers(t, handle)

	ownerOrg := seed.SeedOrg(t, handle, models.Organization{})
	owner := seed.SeedUser(t, handle, models.User{OrganizationID: ownerOrg.ID})
	outsider := seed.SeedUser(t, handle, models.User{OrganizationID: seed.SeedOrg(t, handle, models.Organization{}).ID})
	draft := seed.SeedApp(t, handle, models.App{OwningOrganizationID: ownerOrg.ID, OwningUserID: owner.ID, State: models.AppDraft})
	published := seed.SeedApp(t, handle, models.App{OwningOrganizationID: ownerOrg.ID, OwningUserID: owner.ID})

	routes := map[string]func(echo.Context) error{
		"create_version": h.CreateAppVersion,
		"submit":         h.SubmitApp,
		"list_versions":  h.ListAppVersions,
	}
	for name, call := range routes {
		for _, tc := range []struct {
			label  string
			app    models.App
			status int
		}{
			{"draft_returns_404", draft, http.StatusNotFound},
			{"published_returns_403", published, http.StatusForbidden},
		} {
			t.Run(name+"_"+tc.label, func(t *testing.T) {
				req := asPrincipal(newRequest(http.MethodPost, "/", `{"git_ref":"main"}`), principal(outsider, models.RoleDeveloper))
				c, _ := echoContext(echo.New(), req, "slug", tc.app.Slug)
				requireAPIError(t, call(c), tc.status)
			})
		}
	}

	t.Run("auditor_listing_draft_versions_succeeds", func(t *testing.T) {
		req := asPrincipal(newRequest(http.MethodGet, "/", ""), principal(outsider, models.RoleAuditor))
		c, rec := echoContext(echo.New(), req, "slug", draft.Slug)
		require.NoError(t, h.ListAppVersions(c))
		assert.Equal(t, http.StatusOK, rec.Code)
	})
}
