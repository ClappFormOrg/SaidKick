package orchestrator

import (
	"context"
	"errors"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
)

// Instrument wraps d so every call is counted in
// orchestrator_calls_total{operation,outcome} and timed in
// orchestrator_call_duration_seconds{operation}. cmd/api wraps whichever
// Deployer it builds, so every implementation is measured in one place.
//
// The outcome is "failure" for any non-nil error, with one exception:
// Status returning ErrApplicationNotFound counts as "success". The
// orchestrator answered correctly that the application is gone; that is a
// normal condition (a finished delete, an out-of-band removal) and the
// caller decides what it means, so it must not read as an orchestrator
// fault.
func Instrument(d Deployer) Deployer {
	return instrumented{next: d}
}

type instrumented struct {
	next Deployer
}

// observe records one call of operation that started at start.
func observe(operation string, start time.Time, err error) {
	metrics.OrchestratorCallDuration(operation).UpdateDuration(start)
	outcome := metrics.OutcomeSuccess
	if err != nil {
		outcome = metrics.OutcomeFailure
	}
	metrics.OrchestratorCallsTotal(operation, outcome).Inc()
}

func (i instrumented) Apply(ctx context.Context, spec ApplicationSpec) (DeploymentRef, error) {
	start := time.Now()
	ref, err := i.next.Apply(ctx, spec)
	observe(metrics.OperationApply, start, err)
	return ref, err
}

func (i instrumented) Status(ctx context.Context, ref DeploymentRef) (DeploymentStatus, error) {
	start := time.Now()
	status, err := i.next.Status(ctx, ref)
	counted := err
	if errors.Is(err, ErrApplicationNotFound) {
		counted = nil
	}
	observe(metrics.OperationStatus, start, counted)
	return status, err
}

func (i instrumented) Delete(ctx context.Context, ref DeploymentRef) error {
	start := time.Now()
	err := i.next.Delete(ctx, ref)
	observe(metrics.OperationDelete, start, err)
	return err
}

func (i instrumented) Rollback(ctx context.Context, ref DeploymentRef, toRevision string) error {
	start := time.Now()
	err := i.next.Rollback(ctx, ref, toRevision)
	observe(metrics.OperationRollback, start, err)
	return err
}
