package handlers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/handlers"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/orchestrator/agentbrokered"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── Fixtures ────────────────────────────────────────────────────────────────

// deploymentLifecycleFixture holds a deployment that owner in org made of
// the app's archived version (no audit decision), a published and approved
// version to roll back to, and two users who do not own the deployment.
type deploymentLifecycleFixture struct {
	handle     *db.Handle
	org        models.Organization
	otherOrg   models.Organization
	owner      models.User
	colleague  models.User    // same org, not the deployer
	outsider   models.User    // other org
	cluster    models.Cluster // public
	app        models.App
	deployed   models.AppVersion // archived, currently deployed
	target     models.AppVersion // published
	deployment models.Deployment
}

func newDeploymentLifecycleFixture(t *testing.T, state models.DeploymentState) deploymentLifecycleFixture {
	t.Helper()

	handle := testutil.NewPostgresDB(t)
	f := deploymentLifecycleFixture{handle: handle}
	f.org = seed.SeedOrg(t, handle, models.Organization{})
	f.otherOrg = seed.SeedOrg(t, handle, models.Organization{})
	f.owner = seed.SeedUser(t, handle, models.User{OrganizationID: f.org.ID})
	f.colleague = seed.SeedUser(t, handle, models.User{OrganizationID: f.org.ID})
	f.outsider = seed.SeedUser(t, handle, models.User{OrganizationID: f.otherOrg.ID})

	f.cluster = seed.SeedCluster(t, handle, models.Cluster{})
	f.app = seed.SeedApp(t, handle, models.App{OwningOrganizationID: f.org.ID, OwningUserID: f.owner.ID})
	f.deployed, _ = seed.SeedAppVersion(t, handle, models.AppVersion{AppID: f.app.ID, State: models.AppVersionArchived})
	f.target = seedDecidedVersion(t, f, models.AppVersionPublished, models.DecisionApprove)

	argoName := "app-" + uuid.NewString()[:8]
	f.deployment = seed.SeedDeployment(t, handle, models.Deployment{
		AppID:                f.app.ID,
		AppVersionID:         f.deployed.ID,
		ClusterID:            f.cluster.ID,
		OwningOrganizationID: f.org.ID,
		DeployerUserID:       f.owner.ID,
		State:                state,
		LastArgocdAppName:    &argoName,
	})
	return f
}

// seedDecidedVersion seeds a version of the fixture's app in state, with an
// audit decision on its submission made by the outsider. An empty decision
// leaves the submission undecided.
func seedDecidedVersion(t *testing.T, f deploymentLifecycleFixture, state models.AppVersionState, decision models.Decision) models.AppVersion {
	t.Helper()
	v, sub := seed.SeedAppVersion(t, f.handle, models.AppVersion{AppID: f.app.ID, State: state})
	if decision != "" {
		seed.SeedAuditDecision(t, f.handle, models.AuditDecision{
			AuditSubmissionID: sub.ID, Decision: decision, DecidedByUserID: f.outsider.ID,
		})
	}
	return v
}

// reloadDeployment reads the deployment row, including soft-deleted rows.
func reloadDeployment(t *testing.T, f deploymentLifecycleFixture) models.Deployment {
	t.Helper()
	var d models.Deployment
	require.NoError(t, f.handle.RW.WithContext(t.Context()).Unscoped().First(&d, "id = ?", f.deployment.ID).Error)
	return d
}

// deleteDeploymentCtx builds an Echo context for DELETE /deployments/:id.
func deleteDeploymentCtx(e *echo.Echo, id uuid.UUID, p *auth.Principal) (echo.Context, *httptest.ResponseRecorder) {
	return echoContext(e, asPrincipal(newRequest(http.MethodDelete, "/", ""), p), "id", id.String())
}

// rollbackCtx builds an Echo context for POST /deployments/:id/rollback.
func rollbackCtx(e *echo.Echo, id, toRevision uuid.UUID, p *auth.Principal) (echo.Context, *httptest.ResponseRecorder) {
	body := fmt.Sprintf(`{"to_revision":%q}`, toRevision)
	return echoContext(e, asPrincipal(newRequest(http.MethodPost, "/", body), p), "id", id.String())
}

// upgradeDeploymentCtx builds an Echo context for POST /deployments/:id/upgrade
// that keeps the deployment's current values.
func upgradeDeploymentCtx(e *echo.Echo, id, toVersion uuid.UUID, p *auth.Principal) (echo.Context, *httptest.ResponseRecorder) {
	body := fmt.Sprintf(`{"to_version_id":%q}`, toVersion)
	return echoContext(e, asPrincipal(newRequest(http.MethodPost, "/", body), p), "id", id.String())
}

// versionChangeAllowedStates are settled states from which a deployment may
// be upgraded or rolled back.
var versionChangeAllowedStates = []models.DeploymentState{
	models.DeploymentReady,
	models.DeploymentDegraded,
	models.DeploymentFailedOrchestrator,
}

// versionChangeRejectedStates are teardown and in-flight states from which
// upgrade and rollback must return 409.
var versionChangeRejectedStates = []models.DeploymentState{
	models.DeploymentDeleting,
	models.DeploymentDeleted,
	models.DeploymentApplying,
	models.DeploymentPendingOrchestrator,
}

// deniedWriter is a caller who may not write the fixture's deployment and
// the error the write routes must return to them.
type deniedWriter struct {
	caller func(f deploymentLifecycleFixture) *auth.Principal
	status int
	code   string
}

// deniedDeploymentWriters lists the callers delete, upgrade and rollback
// refuse. Callers who cannot see the deployment through GetDeployment get
// its 404, so the write routes do not reveal that the ID exists; an org
// admin in the owning org can see it and gets 403.
var deniedDeploymentWriters = map[string]deniedWriter{
	"deployer_in_same_org": {
		caller: func(f deploymentLifecycleFixture) *auth.Principal { return principal(f.colleague, models.RoleDeployer) },
		status: http.StatusNotFound, code: "not_found",
	},
	"org_admin_in_same_org": {
		caller: func(f deploymentLifecycleFixture) *auth.Principal {
			return principal(f.colleague, models.RoleDeployer, models.RoleOrgAdmin)
		},
		status: http.StatusForbidden, code: "forbidden",
	},
	"deployer_in_other_org": {
		caller: func(f deploymentLifecycleFixture) *auth.Principal { return principal(f.outsider, models.RoleDeployer) },
		status: http.StatusNotFound, code: "not_found",
	},
	"org_admin_in_other_org": {
		caller: func(f deploymentLifecycleFixture) *auth.Principal {
			return principal(f.outsider, models.RoleDeployer, models.RoleOrgAdmin)
		},
		status: http.StatusNotFound, code: "not_found",
	},
}

// TestDeploymentWrite_MatchesGetVisibility checks that every caller the
// write routes answer with 404 also gets 404 from GetDeployment, and the
// 403 caller can read it.
func TestDeploymentWrite_MatchesGetVisibility(t *testing.T) {
	t.Parallel()
	for name, tc := range deniedDeploymentWriters {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newDeploymentLifecycleFixture(t, models.DeploymentReady)
			h := newTestHandlers(t, f.handle)

			req := asPrincipal(newRequest(http.MethodGet, "/", ""), tc.caller(f))
			c, rec := echoContext(echo.New(), req, "id", f.deployment.ID.String())
			err := h.GetDeployment(c)
			if tc.status == http.StatusNotFound {
				apiErr := requireAPIError(t, err, http.StatusNotFound)
				assert.Equal(t, "not_found", apiErr.Code)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, rec.Code)
		})
	}
}

// ─── DeleteDeployment ────────────────────────────────────────────────────────

// requireTornDown waits for the background teardown to mark the deployment
// deleted and soft-delete the row.
func requireTornDown(t *testing.T, f deploymentLifecycleFixture) {
	t.Helper()
	// The condition runs on another goroutine, so it must not call require.
	require.Eventually(t, func() bool {
		var d models.Deployment
		err := f.handle.RW.WithContext(t.Context()).Unscoped().First(&d, "id = ?", f.deployment.ID).Error
		return err == nil && d.State == models.DeploymentDeleted && d.DeletedAt.Valid
	}, 10*time.Second, 25*time.Millisecond, "deployment was not torn down")
}

func TestDeleteDeployment(t *testing.T) {
	t.Parallel()
	allowed := map[string]func(f deploymentLifecycleFixture) *auth.Principal{
		"owner": func(f deploymentLifecycleFixture) *auth.Principal {
			return principal(f.owner, models.RoleDeployer)
		},
		"platform_admin_from_other_org": func(f deploymentLifecycleFixture) *auth.Principal {
			return principal(f.outsider, models.RolePlatformAdmin)
		},
	}
	for name, caller := range allowed {
		t.Run(name+"_returns_202_and_tears_down", func(t *testing.T) {
			t.Parallel()
			f := newDeploymentLifecycleFixture(t, models.DeploymentReady)
			h := newTestHandlers(t, f.handle, withDeployer(schemaStubDeployer{}))

			c, rec := deleteDeploymentCtx(echo.New(), f.deployment.ID, caller(f))
			require.NoError(t, h.DeleteDeployment(c))
			assert.Equal(t, http.StatusAccepted, rec.Code)

			var resp map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			assert.Equal(t, string(models.DeploymentDeleting), resp["state"])
			requireTornDown(t, f)
		})
	}

	for name, tc := range deniedDeploymentWriters {
		t.Run(name+"_returns_"+strconv.Itoa(tc.status), func(t *testing.T) {
			t.Parallel()
			f := newDeploymentLifecycleFixture(t, models.DeploymentReady)
			h := newTestHandlers(t, f.handle, withDeployer(schemaStubDeployer{}))

			c, _ := deleteDeploymentCtx(echo.New(), f.deployment.ID, tc.caller(f))
			apiErr := requireAPIError(t, h.DeleteDeployment(c), tc.status)
			assert.Equal(t, tc.code, apiErr.Code)

			d := reloadDeployment(t, f)
			assert.Equal(t, models.DeploymentReady, d.State)
			assert.False(t, d.DeletedAt.Valid)
		})
	}

	t.Run("already_deleted_returns_204", func(t *testing.T) {
		t.Parallel()
		f := newDeploymentLifecycleFixture(t, models.DeploymentDeleted)
		h := newTestHandlers(t, f.handle, withDeployer(schemaStubDeployer{}))

		c, rec := deleteDeploymentCtx(echo.New(), f.deployment.ID, principal(f.owner, models.RoleDeployer))
		require.NoError(t, h.DeleteDeployment(c))
		assert.Equal(t, http.StatusNoContent, rec.Code)
	})

	t.Run("unknown_deployment_returns_404", func(t *testing.T) {
		t.Parallel()
		f := newDeploymentLifecycleFixture(t, models.DeploymentReady)
		h := newTestHandlers(t, f.handle)

		c, _ := deleteDeploymentCtx(echo.New(), uuid.New(), principal(f.owner, models.RoleDeployer))
		requireAPIError(t, h.DeleteDeployment(c), http.StatusNotFound)
	})
}

// ─── RollbackDeployment ──────────────────────────────────────────────────────

func TestRollbackDeployment(t *testing.T) {
	t.Parallel()
	allowed := map[string]func(f deploymentLifecycleFixture) *auth.Principal{
		"owner": func(f deploymentLifecycleFixture) *auth.Principal {
			return principal(f.owner, models.RoleDeployer)
		},
		"platform_admin_from_other_org": func(f deploymentLifecycleFixture) *auth.Principal {
			return principal(f.outsider, models.RolePlatformAdmin)
		},
	}
	for name, caller := range allowed {
		t.Run(name+"_returns_202_and_switches_version", func(t *testing.T) {
			t.Parallel()
			f := newDeploymentLifecycleFixture(t, models.DeploymentReady)
			h := newTestHandlers(t, f.handle, withDeployer(schemaStubDeployer{}))

			c, rec := rollbackCtx(echo.New(), f.deployment.ID, f.target.ID, caller(f))
			require.NoError(t, h.RollbackDeployment(c))
			assert.Equal(t, http.StatusAccepted, rec.Code)

			d := reloadDeployment(t, f)
			assert.Equal(t, f.target.ID, d.AppVersionID)
			assert.Equal(t, models.DeploymentApplying, d.State)
		})
	}

	for name, tc := range deniedDeploymentWriters {
		t.Run(name+"_returns_"+strconv.Itoa(tc.status), func(t *testing.T) {
			t.Parallel()
			f := newDeploymentLifecycleFixture(t, models.DeploymentReady)
			h := newTestHandlers(t, f.handle, withDeployer(schemaStubDeployer{}))

			c, _ := rollbackCtx(echo.New(), f.deployment.ID, f.target.ID, tc.caller(f))
			apiErr := requireAPIError(t, h.RollbackDeployment(c), tc.status)
			assert.Equal(t, tc.code, apiErr.Code)
			assert.Equal(t, f.deployed.ID, reloadDeployment(t, f).AppVersionID)
		})
	}

	for _, state := range versionChangeAllowedStates {
		t.Run("from_"+string(state)+"_returns_202", func(t *testing.T) {
			t.Parallel()
			f := newDeploymentLifecycleFixture(t, state)
			h := newTestHandlers(t, f.handle, withDeployer(schemaStubDeployer{}))

			c, rec := rollbackCtx(echo.New(), f.deployment.ID, f.target.ID, principal(f.owner, models.RoleDeployer))
			require.NoError(t, h.RollbackDeployment(c))
			assert.Equal(t, http.StatusAccepted, rec.Code)

			d := reloadDeployment(t, f)
			assert.Equal(t, f.target.ID, d.AppVersionID)
			assert.Equal(t, models.DeploymentApplying, d.State)
		})
	}

	for _, state := range versionChangeRejectedStates {
		t.Run("from_"+string(state)+"_returns_409", func(t *testing.T) {
			t.Parallel()
			f := newDeploymentLifecycleFixture(t, state)
			h := newTestHandlers(t, f.handle, withDeployer(schemaStubDeployer{}))

			c, _ := rollbackCtx(echo.New(), f.deployment.ID, f.target.ID, principal(f.owner, models.RoleDeployer))
			apiErr := requireAPIError(t, h.RollbackDeployment(c), http.StatusConflict)
			assert.Equal(t, "deployment_not_rollbackable", apiErr.Code)

			d := reloadDeployment(t, f)
			assert.Equal(t, f.deployed.ID, d.AppVersionID)
			assert.Equal(t, state, d.State)
		})
	}

	t.Run("archived_approved_target_returns_202", func(t *testing.T) {
		t.Parallel()
		f := newDeploymentLifecycleFixture(t, models.DeploymentReady)
		h := newTestHandlers(t, f.handle, withDeployer(schemaStubDeployer{}))
		archived := seedDecidedVersion(t, f, models.AppVersionArchived, models.DecisionApprove)

		c, rec := rollbackCtx(echo.New(), f.deployment.ID, archived.ID, principal(f.owner, models.RoleDeployer))
		require.NoError(t, h.RollbackDeployment(c))
		assert.Equal(t, http.StatusAccepted, rec.Code)

		d := reloadDeployment(t, f)
		assert.Equal(t, archived.ID, d.AppVersionID)
		assert.Equal(t, models.DeploymentApplying, d.State)
	})

	// Targets in a state outside published/archived are refused whatever
	// their decision, and so are published or archived versions without an
	// approve decision.
	ineligible := map[string]struct {
		state    models.AppVersionState
		decision models.Decision
	}{
		"rejected":                   {models.AppVersionRejected, models.DecisionReject},
		"draft":                      {models.AppVersionDraft, ""},
		"submitted":                  {models.AppVersionSubmitted, ""},
		"needs_changes":              {models.AppVersionNeedsChanges, models.DecisionRequestChanges},
		"archived_without_decision":  {models.AppVersionArchived, ""},
		"archived_request_changes":   {models.AppVersionArchived, models.DecisionRequestChanges},
		"archived_rejected_decision": {models.AppVersionArchived, models.DecisionReject},
		"published_without_decision": {models.AppVersionPublished, ""},
	}
	for name, tc := range ineligible {
		t.Run(name+"_target_returns_422", func(t *testing.T) {
			t.Parallel()
			f := newDeploymentLifecycleFixture(t, models.DeploymentReady)
			h := newTestHandlers(t, f.handle, withDeployer(schemaStubDeployer{}))
			if tc.state == models.AppVersionPublished {
				// One published version per app: retire the fixture's.
				require.NoError(t, f.handle.RW.WithContext(t.Context()).Model(&models.AppVersion{}).
					Where("id = ?", f.target.ID).Update("state", models.AppVersionArchived).Error)
			}
			target := seedDecidedVersion(t, f, tc.state, tc.decision)

			c, _ := rollbackCtx(echo.New(), f.deployment.ID, target.ID, principal(f.owner, models.RoleDeployer))
			apiErr := requireAPIError(t, h.RollbackDeployment(c), http.StatusUnprocessableEntity)
			assert.True(t, hasFieldCode(apiErr.Errors, "not_rollback_eligible"), "got %+v", apiErr.Errors)

			d := reloadDeployment(t, f)
			assert.Equal(t, f.deployed.ID, d.AppVersionID)
			assert.Equal(t, models.DeploymentReady, d.State)
		})
	}

	t.Run("version_of_other_app_returns_422", func(t *testing.T) {
		t.Parallel()
		f := newDeploymentLifecycleFixture(t, models.DeploymentReady)
		h := newTestHandlers(t, f.handle, withDeployer(schemaStubDeployer{}))
		otherApp := seed.SeedApp(t, f.handle, models.App{OwningOrganizationID: f.org.ID, OwningUserID: f.owner.ID})
		foreign, _ := seed.SeedPublishedVersion(t, f.handle, models.AppVersion{AppID: otherApp.ID})

		c, _ := rollbackCtx(echo.New(), f.deployment.ID, foreign.ID, principal(f.owner, models.RoleDeployer))
		apiErr := requireAPIError(t, h.RollbackDeployment(c), http.StatusUnprocessableEntity)
		assert.True(t, hasFieldCode(apiErr.Errors, "wrong_app"), "got %+v", apiErr.Errors)
		assert.Equal(t, f.deployed.ID, reloadDeployment(t, f).AppVersionID)
	})

	t.Run("unknown_target_returns_404", func(t *testing.T) {
		t.Parallel()
		f := newDeploymentLifecycleFixture(t, models.DeploymentReady)
		h := newTestHandlers(t, f.handle, withDeployer(schemaStubDeployer{}))

		c, _ := rollbackCtx(echo.New(), f.deployment.ID, uuid.New(), principal(f.owner, models.RoleDeployer))
		requireAPIError(t, h.RollbackDeployment(c), http.StatusNotFound)
	})

	t.Run("orchestrator_not_configured_returns_503_and_marks_failed", func(t *testing.T) {
		t.Parallel()
		f := newDeploymentLifecycleFixture(t, models.DeploymentReady)
		h := newTestHandlers(t, f.handle) // default NullDeployer

		c, _ := rollbackCtx(echo.New(), f.deployment.ID, f.target.ID, principal(f.owner, models.RoleDeployer))
		requireAPIError(t, h.RollbackDeployment(c), http.StatusServiceUnavailable)

		d := reloadDeployment(t, f)
		assert.Equal(t, models.DeploymentFailedOrchestrator, d.State)
		assert.Equal(t, f.deployed.ID, d.AppVersionID)
	})
}

// ─── UpgradeDeployment: allowed source states ────────────────────────────────

func TestUpgradeDeployment_States(t *testing.T) {
	t.Parallel()
	for _, state := range versionChangeAllowedStates {
		t.Run("from_"+string(state)+"_returns_202", func(t *testing.T) {
			t.Parallel()
			f := newDeploymentLifecycleFixture(t, state)
			h := newTestHandlers(t, f.handle, withDeployer(schemaStubDeployer{}))

			c, rec := upgradeDeploymentCtx(echo.New(), f.deployment.ID, f.target.ID, principal(f.owner, models.RoleDeployer))
			require.NoError(t, h.UpgradeDeployment(c))
			assert.Equal(t, http.StatusAccepted, rec.Code)

			d := reloadDeployment(t, f)
			assert.Equal(t, f.target.ID, d.AppVersionID)
			assert.Equal(t, models.DeploymentApplying, d.State)
		})
	}

	for _, state := range versionChangeRejectedStates {
		t.Run("from_"+string(state)+"_returns_409", func(t *testing.T) {
			t.Parallel()
			f := newDeploymentLifecycleFixture(t, state)
			h := newTestHandlers(t, f.handle, withDeployer(schemaStubDeployer{}))

			c, _ := upgradeDeploymentCtx(echo.New(), f.deployment.ID, f.target.ID, principal(f.owner, models.RoleDeployer))
			apiErr := requireAPIError(t, h.UpgradeDeployment(c), http.StatusConflict)
			assert.Equal(t, "deployment_not_upgradeable", apiErr.Code)

			d := reloadDeployment(t, f)
			assert.Equal(t, f.deployed.ID, d.AppVersionID)
			assert.Equal(t, state, d.State)
		})
	}
}

// ─── UpgradeDeployment: callers who may not write ────────────────────────────

func TestUpgradeDeployment_DeniedCallers(t *testing.T) {
	t.Parallel()
	for name, tc := range deniedDeploymentWriters {
		t.Run(name+"_returns_"+strconv.Itoa(tc.status), func(t *testing.T) {
			t.Parallel()
			f := newDeploymentLifecycleFixture(t, models.DeploymentReady)
			h := newTestHandlers(t, f.handle, withDeployer(schemaStubDeployer{}))

			c, _ := upgradeDeploymentCtx(echo.New(), f.deployment.ID, f.target.ID, tc.caller(f))
			apiErr := requireAPIError(t, h.UpgradeDeployment(c), tc.status)
			assert.Equal(t, tc.code, apiErr.Code)
			assert.Equal(t, f.deployed.ID, reloadDeployment(t, f).AppVersionID)
		})
	}
}

// ─── Upgrade and rollback: cluster access ────────────────────────────────────

// restrictClusterWithGrant moves the fixture's cluster to restricted scope,
// owned by the other org, and grants the deployment's org access through
// the grant handler.
func restrictClusterWithGrant(t *testing.T, f deploymentLifecycleFixture) {
	t.Helper()
	require.NoError(t, f.handle.RW.WithContext(t.Context()).Model(&models.Cluster{}).
		Where("id = ?", f.cluster.ID).
		Updates(map[string]any{"scope": models.ClusterScopeRestricted, "owning_organization_id": f.otherOrg.ID}).Error)

	h := newTestHandlers(t, f.handle)
	c, _ := grantAccessCtx(echo.New(), f.cluster.ID, f.org.ID, principal(f.outsider, models.RolePlatformAdmin))
	require.NoError(t, h.GrantClusterAccess(c))
}

// revokeOrgAccess revokes the deployment org's grant on the fixture's
// cluster through DELETE /clusters/:id/access/:org_id.
func revokeOrgAccess(t *testing.T, f deploymentLifecycleFixture) {
	t.Helper()
	h := newTestHandlers(t, f.handle)
	c, _ := revokeAccessCtx(echo.New(), f.cluster.ID, f.org.ID.String(), principal(f.outsider, models.RolePlatformAdmin))
	require.NoError(t, h.RevokeClusterAccess(c))
}

// newRevokedAccessFixture returns a ready deployment on a restricted cluster
// whose grant for the deployment's org has been revoked.
func newRevokedAccessFixture(t *testing.T) deploymentLifecycleFixture {
	t.Helper()
	f := newDeploymentLifecycleFixture(t, models.DeploymentReady)
	restrictClusterWithGrant(t, f)
	revokeOrgAccess(t, f)
	return f
}

func TestRedeploy_ClusterAccess(t *testing.T) {
	t.Parallel()
	t.Run("granted_access_upgrade_returns_202", func(t *testing.T) {
		t.Parallel()
		f := newDeploymentLifecycleFixture(t, models.DeploymentReady)
		restrictClusterWithGrant(t, f)
		h := newTestHandlers(t, f.handle, withDeployer(schemaStubDeployer{}))

		c, rec := upgradeDeploymentCtx(echo.New(), f.deployment.ID, f.target.ID, principal(f.owner, models.RoleDeployer))
		require.NoError(t, h.UpgradeDeployment(c))
		assert.Equal(t, http.StatusAccepted, rec.Code)
	})

	t.Run("revoked_access_upgrade_returns_403", func(t *testing.T) {
		t.Parallel()
		f := newRevokedAccessFixture(t)
		h := newTestHandlers(t, f.handle, withDeployer(schemaStubDeployer{}))

		c, _ := upgradeDeploymentCtx(echo.New(), f.deployment.ID, f.target.ID, principal(f.owner, models.RoleDeployer))
		apiErr := requireAPIError(t, h.UpgradeDeployment(c), http.StatusForbidden)
		assert.Equal(t, "cluster_not_visible", apiErr.Code)
		assert.Equal(t, f.deployed.ID, reloadDeployment(t, f).AppVersionID)
	})

	t.Run("revoked_access_rollback_returns_403", func(t *testing.T) {
		t.Parallel()
		f := newRevokedAccessFixture(t)
		h := newTestHandlers(t, f.handle, withDeployer(schemaStubDeployer{}))

		c, _ := rollbackCtx(echo.New(), f.deployment.ID, f.target.ID, principal(f.owner, models.RoleDeployer))
		apiErr := requireAPIError(t, h.RollbackDeployment(c), http.StatusForbidden)
		assert.Equal(t, "cluster_not_visible", apiErr.Code)
		assert.Equal(t, f.deployed.ID, reloadDeployment(t, f).AppVersionID)
	})

	t.Run("revoked_access_delete_returns_202", func(t *testing.T) {
		t.Parallel()
		f := newRevokedAccessFixture(t)
		h := newTestHandlers(t, f.handle, withDeployer(schemaStubDeployer{}))

		c, rec := deleteDeploymentCtx(echo.New(), f.deployment.ID, principal(f.owner, models.RoleDeployer))
		require.NoError(t, h.DeleteDeployment(c))
		assert.Equal(t, http.StatusAccepted, rec.Code)
		requireTornDown(t, f)
	})

	t.Run("scope_change_to_other_org_upgrade_returns_403", func(t *testing.T) {
		t.Parallel()
		f := newDeploymentLifecycleFixture(t, models.DeploymentReady)
		h := newTestHandlers(t, f.handle, withDeployer(schemaStubDeployer{}))

		body := fmt.Sprintf(`{"new_scope":"org","owning_org_id":%q,"reason":"moved to the other org"}`, f.otherOrg.ID)
		req := asPrincipal(newRequest(http.MethodPost, "/", body), principal(f.outsider, models.RolePlatformAdmin))
		sc, _ := echoContext(echo.New(), req, "id", f.cluster.ID.String())
		require.NoError(t, h.ChangeClusterScope(sc))

		c, _ := upgradeDeploymentCtx(echo.New(), f.deployment.ID, f.target.ID, principal(f.owner, models.RoleDeployer))
		apiErr := requireAPIError(t, h.UpgradeDeployment(c), http.StatusForbidden)
		assert.Equal(t, "cluster_not_visible", apiErr.Code)
		assert.Equal(t, f.deployed.ID, reloadDeployment(t, f).AppVersionID)
	})

	t.Run("platform_admin_without_grant_upgrade_returns_202", func(t *testing.T) {
		t.Parallel()
		f := newRevokedAccessFixture(t)
		h := newTestHandlers(t, f.handle, withDeployer(schemaStubDeployer{}))

		// The admin's org (the deployment's org) no longer holds a grant.
		c, rec := upgradeDeploymentCtx(echo.New(), f.deployment.ID, f.target.ID, principal(f.colleague, models.RolePlatformAdmin))
		require.NoError(t, h.UpgradeDeployment(c))
		assert.Equal(t, http.StatusAccepted, rec.Code)
		assert.Equal(t, f.target.ID, reloadDeployment(t, f).AppVersionID)
	})
}

// ─── Upgrade and rollback: decommissioned cluster ────────────────────────────

func TestRedeploy_DecommissionedCluster(t *testing.T) {
	t.Parallel()
	callers := map[string]func(f deploymentLifecycleFixture) *auth.Principal{
		"owner": func(f deploymentLifecycleFixture) *auth.Principal {
			return principal(f.owner, models.RoleDeployer)
		},
		"platform_admin": func(f deploymentLifecycleFixture) *auth.Principal {
			return principal(f.outsider, models.RolePlatformAdmin)
		},
	}
	actions := map[string]func(h *handlers.Handlers, f deploymentLifecycleFixture, p *auth.Principal) error{
		"upgrade": func(h *handlers.Handlers, f deploymentLifecycleFixture, p *auth.Principal) error {
			c, _ := upgradeDeploymentCtx(echo.New(), f.deployment.ID, f.target.ID, p)
			return h.UpgradeDeployment(c)
		},
		"rollback": func(h *handlers.Handlers, f deploymentLifecycleFixture, p *auth.Principal) error {
			c, _ := rollbackCtx(echo.New(), f.deployment.ID, f.target.ID, p)
			return h.RollbackDeployment(c)
		},
	}
	for action, run := range actions {
		for name, caller := range callers {
			t.Run(action+"_by_"+name+"_returns_409", func(t *testing.T) {
				t.Parallel()
				f := newDeploymentLifecycleFixture(t, models.DeploymentReady)
				require.NoError(t, f.handle.RW.WithContext(t.Context()).Model(&models.Cluster{}).
					Where("id = ?", f.cluster.ID).Update("state", models.ClusterDecommissioned).Error)
				h := newTestHandlers(t, f.handle, withDeployer(schemaStubDeployer{}))

				apiErr := requireAPIError(t, run(h, f, caller(f)), http.StatusConflict)
				assert.Equal(t, "cluster_decommissioned", apiErr.Code)
				assert.Equal(t, f.deployed.ID, reloadDeployment(t, f).AppVersionID)
			})
		}
	}
}

// ─── Agent-brokered commands target the deployment's cluster ─────────────────

// queuedCommand returns the only agent command of cmdType.
func queuedCommand(t *testing.T, f deploymentLifecycleFixture, cmdType string) models.AgentCommand {
	t.Helper()
	var cmds []models.AgentCommand
	require.NoError(t, f.handle.RW.WithContext(t.Context()).
		Where("command_type = ?", cmdType).Find(&cmds).Error)
	require.Len(t, cmds, 1, "expected one %s command", cmdType)
	return cmds[0]
}

func TestAgentBrokered_CommandsTargetDeploymentCluster(t *testing.T) {
	t.Parallel()
	t.Run("delete", func(t *testing.T) {
		t.Parallel()
		f := newDeploymentLifecycleFixture(t, models.DeploymentReady)
		h := newTestHandlers(t, f.handle, withDeployer(agentbrokered.New(f.handle)))

		c, rec := deleteDeploymentCtx(echo.New(), f.deployment.ID, principal(f.owner, models.RoleDeployer))
		require.NoError(t, h.DeleteDeployment(c))
		assert.Equal(t, http.StatusAccepted, rec.Code)
		requireTornDown(t, f)

		cmd := queuedCommand(t, f, "delete")
		assert.Equal(t, f.cluster.ID, cmd.ClusterID)
		assert.JSONEq(t, fmt.Sprintf(`{"app_name":%q}`, *f.deployment.LastArgocdAppName), string(cmd.Payload))
	})

	t.Run("rollback", func(t *testing.T) {
		t.Parallel()
		f := newDeploymentLifecycleFixture(t, models.DeploymentReady)
		h := newTestHandlers(t, f.handle, withDeployer(agentbrokered.New(f.handle)))

		c, rec := rollbackCtx(echo.New(), f.deployment.ID, f.target.ID, principal(f.owner, models.RoleDeployer))
		require.NoError(t, h.RollbackDeployment(c))
		assert.Equal(t, http.StatusAccepted, rec.Code)

		cmd := queuedCommand(t, f, "rollback")
		assert.Equal(t, f.cluster.ID, cmd.ClusterID)
		assert.JSONEq(t,
			fmt.Sprintf(`{"app_name":%q,"to_revision":%q}`, *f.deployment.LastArgocdAppName, f.target.GitSHA),
			string(cmd.Payload))
	})
}

func TestRollbackDeployment_InvalidRevision(t *testing.T) {
	t.Parallel()
	h := newTestHandlers(t, nil)
	req := asPrincipal(newRequest(http.MethodPost, "/", `{"to_revision":"v1"}`), principal(testUser(), models.RoleDeployer))
	c, _ := echoContext(echo.New(), req, "id", uuid.NewString())
	apiErr := requireAPIError(t, h.RollbackDeployment(c), http.StatusUnprocessableEntity)
	assert.True(t, hasFieldCode(apiErr.Errors, "invalid_uuid"), "got %+v", apiErr.Errors)
}

// TestDeploymentWrite_RouteRoles checks that delete and rollback require the
// deployer role at the route.
func TestDeploymentWrite_RouteRoles(t *testing.T) {
	t.Parallel()
	h := newTestHandlers(t, nil)
	id := uuid.NewString()
	body := fmt.Sprintf(`{"to_revision":%q}`, uuid.New())

	for _, role := range []models.Role{models.RoleDeveloper, models.RoleAuditor, models.RoleOrgAdmin, models.RoleClusterAdmin} {
		t.Run("delete_"+string(role)+"_returns_403", func(t *testing.T) {
			t.Parallel()
			req := asPrincipal(newRequest(http.MethodDelete, "/api/v1/deployments/"+id, ""), principal(testUser(), role))
			assert.Equal(t, http.StatusForbidden, serveRoute(h, req).Code)
		})
		t.Run("rollback_"+string(role)+"_returns_403", func(t *testing.T) {
			t.Parallel()
			req := asPrincipal(newRequest(http.MethodPost, "/api/v1/deployments/"+id+"/rollback", body), principal(testUser(), role))
			assert.Equal(t, http.StatusForbidden, serveRoute(h, req).Code)
		})
	}
}
