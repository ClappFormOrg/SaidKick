package handlers_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/orchestrator"
	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
)

// failingApplyDeployer fails every Apply permanently.
type failingApplyDeployer struct{ schemaStubDeployer }

func (failingApplyDeployer) Apply(context.Context, orchestrator.ApplicationSpec) (orchestrator.DeploymentRef, error) {
	return orchestrator.DeploymentRef{}, errors.New("argo rejected the application")
}

// The counters are process-wide, so these tests do not call t.Parallel.

func TestCreateDeploymentCountsTransitionsAndApply(t *testing.T) {
	f := newValuesSchemaFixture(t, schemaChartSchema)
	h := newTestHandlers(t, f.handle, withDeployer(orchestrator.Instrument(schemaStubDeployer{})))

	created := metrics.DeploymentTransitionsTotal("unknown", "pending").Get()
	applying := metrics.DeploymentTransitionsTotal("pending", "applying").Get()
	applies := metrics.OrchestratorCallsTotal(metrics.OperationApply, metrics.OutcomeSuccess).Get()
	failures := metrics.OrchestratorCallsTotal(metrics.OperationApply, metrics.OutcomeFailure).Get()
	timed := testutil.HistogramCount(metrics.OrchestratorCallDuration(metrics.OperationApply))

	c, rec := createDeploymentCtx(echo.New(), f, `{"logLevel":"info","replicas":3}`, `{"apiKey":"s3cr3t"}`,
		principal(f.user, models.RoleDeployer))
	require.NoError(t, h.CreateDeployment(c))
	require.Equal(t, http.StatusAccepted, rec.Code)

	assert.Equal(t, created+1, metrics.DeploymentTransitionsTotal("unknown", "pending").Get())
	assert.Equal(t, applying+1, metrics.DeploymentTransitionsTotal("pending", "applying").Get())
	assert.Equal(t, applies+1, metrics.OrchestratorCallsTotal(metrics.OperationApply, metrics.OutcomeSuccess).Get())
	assert.Equal(t, failures, metrics.OrchestratorCallsTotal(metrics.OperationApply, metrics.OutcomeFailure).Get())
	assert.Equal(t, timed+1, testutil.HistogramCount(metrics.OrchestratorCallDuration(metrics.OperationApply)))

	testutil.RequireNoIdentifyingLabels(t)
}

func TestCreateDeploymentCountsOrchestratorFailure(t *testing.T) {
	f := newValuesSchemaFixture(t, schemaChartSchema)
	h := newTestHandlers(t, f.handle, withDeployer(orchestrator.Instrument(failingApplyDeployer{})))

	failed := metrics.DeploymentTransitionsTotal("pending", "failed_orchestrator").Get()
	applies := metrics.OrchestratorCallsTotal(metrics.OperationApply, metrics.OutcomeSuccess).Get()
	failures := metrics.OrchestratorCallsTotal(metrics.OperationApply, metrics.OutcomeFailure).Get()

	c, _ := createDeploymentCtx(echo.New(), f, `{"logLevel":"info","replicas":3}`, `{"apiKey":"s3cr3t"}`,
		principal(f.user, models.RoleDeployer))
	requireAPIError(t, h.CreateDeployment(c), http.StatusBadGateway)

	assert.Equal(t, failures+1, metrics.OrchestratorCallsTotal(metrics.OperationApply, metrics.OutcomeFailure).Get())
	assert.Equal(t, applies, metrics.OrchestratorCallsTotal(metrics.OperationApply, metrics.OutcomeSuccess).Get())
	assert.Equal(t, failed+1, metrics.DeploymentTransitionsTotal("pending", "failed_orchestrator").Get())
}
