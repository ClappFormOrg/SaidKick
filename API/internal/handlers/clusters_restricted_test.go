package handlers_test

import (
	"encoding/json"
	"net/http"
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

// A restricted cluster is visible to its owning org plus every org on its
// access list. These tests cover the owner (no grant row), a grantee and a
// stranger across list, get and the deployment writes.

// restrictedRelation is how the caller's org relates to a restricted cluster.
type restrictedRelation string

const (
	relationOwner    restrictedRelation = "owner"
	relationGrantee  restrictedRelation = "grantee"
	relationStranger restrictedRelation = "stranger"
)

var restrictedRelations = []restrictedRelation{relationOwner, relationGrantee, relationStranger}

// restrictCluster moves clusterID to restricted scope. The owner relation
// makes callerOrg the owner with no grant row; grantee makes otherOrg the
// owner and grants callerOrg; stranger makes otherOrg the owner with no grant.
func restrictCluster(t *testing.T, handle *db.Handle, clusterID, callerOrg, otherOrg uuid.UUID, rel restrictedRelation) {
	t.Helper()
	owner := otherOrg
	if rel == relationOwner {
		owner = callerOrg
	}
	require.NoError(t, handle.RW.WithContext(t.Context()).Model(&models.Cluster{}).
		Where("id = ?", clusterID).
		Updates(map[string]any{"scope": models.ClusterScopeRestricted, "owning_organization_id": owner}).Error)
	if rel == relationGrantee {
		grantor := seed.SeedUser(t, handle, models.User{OrganizationID: otherOrg})
		require.NoError(t, handle.RW.WithContext(t.Context()).Create(&models.ClusterAccessGrant{
			ClusterID: clusterID, OrganizationID: callerOrg, GrantedBy: grantor.ID,
		}).Error)
	}
}

// ─── ListClusters / GetCluster ───────────────────────────────────────────────

// restrictedVisibilityFixture holds a caller in callerOrg and one restricted
// cluster per relation, plus a restricted cluster callerOrg owns and also
// holds a self-grant on.
type restrictedVisibilityFixture struct {
	handle    *db.Handle
	caller    models.User
	clusters  map[restrictedRelation]models.Cluster
	selfGrant models.Cluster
}

func newRestrictedVisibilityFixture(t *testing.T) (restrictedVisibilityFixture, *handlers.Handlers) {
	t.Helper()
	handle := testutil.NewPostgresDB(t)
	f := restrictedVisibilityFixture{handle: handle, clusters: map[restrictedRelation]models.Cluster{}}
	callerOrg := seed.SeedOrg(t, handle, models.Organization{})
	otherOrg := seed.SeedOrg(t, handle, models.Organization{})
	f.caller = seed.SeedUser(t, handle, models.User{OrganizationID: callerOrg.ID})
	for _, rel := range restrictedRelations {
		cl := seed.SeedCluster(t, handle, models.Cluster{Scope: models.ClusterScopePublic})
		restrictCluster(t, handle, cl.ID, callerOrg.ID, otherOrg.ID, rel)
		f.clusters[rel] = cl
	}
	f.selfGrant = seed.SeedCluster(t, handle, models.Cluster{
		Scope: models.ClusterScopeRestricted, OwningOrganizationID: &callerOrg.ID,
	})
	require.NoError(t, handle.RW.WithContext(t.Context()).Create(&models.ClusterAccessGrant{
		ClusterID: f.selfGrant.ID, OrganizationID: callerOrg.ID, GrantedBy: f.caller.ID,
	}).Error)
	return f, newTestHandlers(t, handle)
}

// listClusterIDs calls ListClusters as p and returns the ids in the response.
func listClusterIDs(t *testing.T, h *handlers.Handlers, p *auth.Principal) []string {
	t.Helper()
	c, rec := echoContext(echo.New(), asPrincipal(newRequest(http.MethodGet, "/", ""), p))
	require.NoError(t, h.ListClusters(c))
	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	ids := make([]string, 0, len(resp.Items))
	for _, it := range resp.Items {
		ids = append(ids, it.ID)
	}
	return ids
}

func TestRestrictedCluster_Visibility(t *testing.T) {
	t.Parallel()
	f, h := newRestrictedVisibilityFixture(t)
	p := principal(f.caller, models.RoleDeployer)

	t.Run("list_shows_owned_and_granted_only", func(t *testing.T) {
		ids := listClusterIDs(t, h, p)
		assert.ElementsMatch(t, []string{
			f.clusters[relationOwner].ID.String(),
			f.clusters[relationGrantee].ID.String(),
			f.selfGrant.ID.String(),
		}, ids)
	})

	t.Run("list_with_restricted_scope_filter", func(t *testing.T) {
		c, rec := echoContext(echo.New(), asPrincipal(newRequest(http.MethodGet, "/?scope=restricted", ""), p))
		require.NoError(t, h.ListClusters(c))
		var resp struct {
			Items []map[string]any `json:"items"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Len(t, resp.Items, 3)
	})

	get := map[restrictedRelation]int{
		relationOwner:    http.StatusOK,
		relationGrantee:  http.StatusOK,
		relationStranger: http.StatusNotFound,
	}
	for rel, status := range get {
		t.Run("get_as_"+string(rel)+"_returns_"+http.StatusText(status), func(t *testing.T) {
			c, rec := echoContext(echo.New(), asPrincipal(newRequest(http.MethodGet, "/", ""), p), "id", f.clusters[rel].ID.String())
			err := h.GetCluster(c)
			if status == http.StatusOK {
				require.NoError(t, err)
				assert.Equal(t, http.StatusOK, rec.Code)
				return
			}
			apiErr := requireAPIError(t, err, status)
			assert.Equal(t, "not_found", apiErr.Code)
		})
	}

	t.Run("get_self_granted_owner_cluster_returns_200", func(t *testing.T) {
		c, rec := echoContext(echo.New(), asPrincipal(newRequest(http.MethodGet, "/", ""), p), "id", f.selfGrant.ID.String())
		require.NoError(t, h.GetCluster(c))
		assert.Equal(t, http.StatusOK, rec.Code)
	})
}

// ─── CreateDeployment ────────────────────────────────────────────────────────

func TestRestrictedCluster_CreateDeployment(t *testing.T) {
	t.Parallel()
	for _, rel := range restrictedRelations {
		t.Run(string(rel), func(t *testing.T) {
			t.Parallel()
			f := newValuesSchemaFixture(t, schemaChartSchema)
			otherOrg := seed.SeedOrg(t, f.handle, models.Organization{})
			restrictCluster(t, f.handle, f.cluster.ID, f.org.ID, otherOrg.ID, rel)
			h := newTestHandlers(t, f.handle, withDeployer(schemaStubDeployer{}))

			c, rec := createDeploymentCtx(echo.New(), f, `{"logLevel":"info","replicas":3}`, `{"apiKey":"s3cr3t"}`,
				principal(f.user, models.RoleDeployer))
			err := h.CreateDeployment(c)
			if rel == relationStranger {
				apiErr := requireAPIError(t, err, http.StatusForbidden)
				assert.Equal(t, "cluster_not_visible", apiErr.Code)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, http.StatusAccepted, rec.Code)
		})
	}
}

// ─── UpgradeDeployment / RollbackDeployment ──────────────────────────────────

func TestRestrictedCluster_Redeploy(t *testing.T) {
	t.Parallel()
	actions := map[string]func(h *handlers.Handlers, f deploymentLifecycleFixture) (int, error){
		"upgrade": func(h *handlers.Handlers, f deploymentLifecycleFixture) (int, error) {
			c, rec := upgradeDeploymentCtx(echo.New(), f.deployment.ID, f.target.ID, principal(f.owner, models.RoleDeployer))
			err := h.UpgradeDeployment(c)
			return rec.Code, err
		},
		"rollback": func(h *handlers.Handlers, f deploymentLifecycleFixture) (int, error) {
			c, rec := rollbackCtx(echo.New(), f.deployment.ID, f.target.ID, principal(f.owner, models.RoleDeployer))
			err := h.RollbackDeployment(c)
			return rec.Code, err
		},
	}
	for action, run := range actions {
		for _, rel := range restrictedRelations {
			t.Run(action+"_as_"+string(rel), func(t *testing.T) {
				t.Parallel()
				f := newDeploymentLifecycleFixture(t, models.DeploymentReady)
				restrictCluster(t, f.handle, f.cluster.ID, f.org.ID, f.otherOrg.ID, rel)
				h := newTestHandlers(t, f.handle, withDeployer(schemaStubDeployer{}))

				code, err := run(h, f)
				if rel == relationStranger {
					apiErr := requireAPIError(t, err, http.StatusForbidden)
					assert.Equal(t, "cluster_not_visible", apiErr.Code)
					assert.Equal(t, f.deployed.ID, reloadDeployment(t, f).AppVersionID)
					return
				}
				require.NoError(t, err)
				assert.Equal(t, http.StatusAccepted, code)
				assert.Equal(t, f.target.ID, reloadDeployment(t, f).AppVersionID)
			})
		}
	}
}
