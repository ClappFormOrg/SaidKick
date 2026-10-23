package deploystate_test

import (
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/deploystate"
	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
)

func TestMain(m *testing.M) { os.Exit(testutil.RunMain(m)) }

// The transition counters are process-wide, so these tests do not run in
// parallel.

func transitions(from, to string) uint64 {
	return metrics.DeploymentTransitionsTotal(from, to).Get()
}

// seedDeployment seeds one deployment in state, with everything it needs.
func seedDeployment(t *testing.T, handle *db.Handle, state models.DeploymentState) models.Deployment {
	t.Helper()
	org := seed.SeedOrg(t, handle, models.Organization{})
	user := seed.SeedUser(t, handle, models.User{OrganizationID: org.ID})
	cluster := seed.SeedCluster(t, handle, models.Cluster{})
	app := seed.SeedApp(t, handle, models.App{OwningOrganizationID: org.ID, OwningUserID: user.ID})
	version, _ := seed.SeedPublishedVersion(t, handle, models.AppVersion{AppID: app.ID})
	return seed.SeedDeployment(t, handle, models.Deployment{
		AppID: app.ID, AppVersionID: version.ID, ClusterID: cluster.ID,
		OwningOrganizationID: org.ID, DeployerUserID: user.ID, State: state,
	})
}

func TestCreatedCountsFromUnknown(t *testing.T) {
	before := transitions("unknown", "pending")
	deploystate.Created(models.Deployment{State: models.DeploymentPending})
	assert.Equal(t, before+1, transitions("unknown", "pending"))
}

func TestSetCountsTheStateTheRowLeft(t *testing.T) {
	handle := testutil.NewPostgresDB(t)
	dep := seedDeployment(t, handle, models.DeploymentApplying)

	// A stale copy still names the row's real from state.
	stale := dep
	stale.State = models.DeploymentPending
	before := transitions("applying", "ready")
	beforeStale := transitions("pending", "ready")

	app := "app-x"
	require.NoError(t, deploystate.Set(t.Context(), handle.RW, &stale, models.DeploymentReady,
		map[string]any{"last_argocd_app_name": app}))
	assert.Equal(t, models.DeploymentReady, stale.State)
	assert.Equal(t, before+1, transitions("applying", "ready"))
	assert.Equal(t, beforeStale, transitions("pending", "ready"))

	var got models.Deployment
	require.NoError(t, handle.RW.First(&got, "id = ?", dep.ID).Error)
	assert.Equal(t, models.DeploymentReady, got.State)
	require.NotNil(t, got.LastArgocdAppName)
	assert.Equal(t, app, *got.LastArgocdAppName)
	assert.True(t, got.LastStateChangeAt.After(dep.LastStateChangeAt), "the state change must refresh last_state_change_at")

	// Writing the state the row already holds is not a transition.
	before = transitions("ready", "ready")
	require.NoError(t, deploystate.Set(t.Context(), handle.RW, &got, models.DeploymentReady, nil))
	assert.Equal(t, before, transitions("ready", "ready"))
}

func TestSetIgnoresMissingRow(t *testing.T) {
	handle := testutil.NewPostgresDB(t)
	before := transitions("unknown", "ready")
	missing := models.Deployment{IDModel: models.IDModel{ID: uuid.New()}}
	require.NoError(t, deploystate.Set(t.Context(), handle.RW, &missing, models.DeploymentReady, nil))
	assert.Equal(t, before, transitions("unknown", "ready"))
}

func TestSetWhereCountsEachRowMoved(t *testing.T) {
	handle := testutil.NewPostgresDB(t)
	a := seedDeployment(t, handle, models.DeploymentWaitingForCluster)
	b := seedDeployment(t, handle, models.DeploymentWaitingForCluster)
	other := seedDeployment(t, handle, models.DeploymentReady)

	before := transitions("waiting_for_cluster", "pending_orchestrator")
	n, err := deploystate.SetWhere(handle.RW.Model(&models.Deployment{}).Where("id IN ?", []uuid.UUID{a.ID, b.ID, other.ID}),
		models.DeploymentWaitingForCluster, models.DeploymentPendingOrchestrator)
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)
	assert.Equal(t, before+2, transitions("waiting_for_cluster", "pending_orchestrator"))

	var got models.Deployment
	require.NoError(t, handle.RW.First(&got, "id = ?", other.ID).Error)
	assert.Equal(t, models.DeploymentReady, got.State, "a row in another state must not move")
}
