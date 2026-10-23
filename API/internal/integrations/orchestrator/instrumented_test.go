package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/orchestrator"
	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
)

// stubDeployer returns err from every call and records what it was called
// with, standing in for any concrete Deployer.
type stubDeployer struct {
	err   error
	calls []string
}

func (s *stubDeployer) Apply(_ context.Context, spec orchestrator.ApplicationSpec) (orchestrator.DeploymentRef, error) {
	s.calls = append(s.calls, "apply "+spec.AppSlug)
	return orchestrator.DeploymentRef{ApplicationName: "app-" + spec.AppSlug}, s.err
}

func (s *stubDeployer) Status(_ context.Context, ref orchestrator.DeploymentRef) (orchestrator.DeploymentStatus, error) {
	s.calls = append(s.calls, "status "+ref.ApplicationName)
	return orchestrator.DeploymentStatus{Phase: orchestrator.PhaseReady}, s.err
}

func (s *stubDeployer) Delete(_ context.Context, ref orchestrator.DeploymentRef) error {
	s.calls = append(s.calls, "delete "+ref.ApplicationName)
	return s.err
}

func (s *stubDeployer) Rollback(_ context.Context, ref orchestrator.DeploymentRef, rev string) error {
	s.calls = append(s.calls, "rollback "+ref.ApplicationName+" "+rev)
	return s.err
}

// callCounts snapshots one operation's counters and histogram.
type callCounts struct{ success, failure, observed uint64 }

func snapshot(op string) callCounts {
	return callCounts{
		success:  metrics.OrchestratorCallsTotal(op, metrics.OutcomeSuccess).Get(),
		failure:  metrics.OrchestratorCallsTotal(op, metrics.OutcomeFailure).Get(),
		observed: testutil.HistogramCount(metrics.OrchestratorCallDuration(op)),
	}
}

// invoke calls operation op on d.
func invoke(d orchestrator.Deployer, op string) error {
	ctx := context.Background()
	ref := orchestrator.NewDeploymentRef("app-x", uuid.New())
	switch op {
	case metrics.OperationApply:
		_, err := d.Apply(ctx, orchestrator.ApplicationSpec{AppSlug: "x"})
		return err
	case metrics.OperationStatus:
		_, err := d.Status(ctx, ref)
		return err
	case metrics.OperationDelete:
		return d.Delete(ctx, ref)
	case metrics.OperationRollback:
		return d.Rollback(ctx, ref, "abc123")
	}
	panic("unknown operation " + op)
}

// The counters are process-wide, so these tests do not run in parallel.

func TestInstrumentCountsEveryOperation(t *testing.T) {
	boom := errors.New("orchestrator down")
	for _, op := range metrics.OrchestratorOperations {
		for _, tc := range []struct {
			name             string
			err              error
			success, failure uint64
		}{
			{"success", nil, 1, 0},
			{"failure", boom, 0, 1},
		} {
			t.Run(op+"/"+tc.name, func(t *testing.T) {
				stub := &stubDeployer{err: tc.err}
				before := snapshot(op)

				err := invoke(orchestrator.Instrument(stub), op)
				assert.ErrorIs(t, err, tc.err)
				assert.Len(t, stub.calls, 1, "the wrapped deployer must be called once")

				after := snapshot(op)
				assert.Equal(t, before.success+tc.success, after.success)
				assert.Equal(t, before.failure+tc.failure, after.failure)
				assert.Equal(t, before.observed+1, after.observed, "every call is timed")
			})
		}
	}
}

func TestInstrumentPassesResultsThrough(t *testing.T) {
	stub := &stubDeployer{}
	d := orchestrator.Instrument(stub)
	ref, err := d.Apply(context.Background(), orchestrator.ApplicationSpec{AppSlug: "govchat"})
	require.NoError(t, err)
	assert.Equal(t, "app-govchat", ref.ApplicationName)
	status, err := d.Status(context.Background(), ref)
	require.NoError(t, err)
	assert.Equal(t, orchestrator.PhaseReady, status.Phase)
	require.NoError(t, d.Rollback(context.Background(), ref, "abc123"))
	assert.Equal(t, []string{"apply govchat", "status app-govchat", "rollback app-govchat abc123"}, stub.calls)
}

// Status answering that the application is gone is a normal condition, not
// an orchestrator fault: it counts as success, and the error still reaches
// the caller. Delete and the other operations count it as a failure.
func TestInstrumentStatusNotFoundIsSuccess(t *testing.T) {
	notFound := fmt.Errorf("argo: %w", orchestrator.ErrApplicationNotFound)
	before := snapshot(metrics.OperationStatus)

	err := invoke(orchestrator.Instrument(&stubDeployer{err: notFound}), metrics.OperationStatus)
	require.ErrorIs(t, err, orchestrator.ErrApplicationNotFound)

	after := snapshot(metrics.OperationStatus)
	assert.Equal(t, before.success+1, after.success)
	assert.Equal(t, before.failure, after.failure)

	before = snapshot(metrics.OperationDelete)
	require.ErrorIs(t, invoke(orchestrator.Instrument(&stubDeployer{err: notFound}), metrics.OperationDelete), orchestrator.ErrApplicationNotFound)
	assert.Equal(t, before.failure+1, snapshot(metrics.OperationDelete).failure)
}

// The wrapper works for any Deployer, including the null one main wires when
// no orchestrator is configured.
func TestInstrumentWrapsNullDeployer(t *testing.T) {
	before := snapshot(metrics.OperationApply)
	err := invoke(orchestrator.Instrument(orchestrator.NullDeployer{}), metrics.OperationApply)
	require.ErrorIs(t, err, orchestrator.ErrNotConfigured)
	assert.Equal(t, before.failure+1, snapshot(metrics.OperationApply).failure)
}
