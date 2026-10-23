package orchestrator

import "context"

// NullDeployer is wired when no orchestrator endpoint is configured at
// startup. Every method returns ErrNotConfigured so handlers can map
// that to a 503 with a clear "ask your platform admin" message.
//
// The API still boots in this state — the catalogue, audit, cluster
// and org endpoints remain fully functional.
type NullDeployer struct{}

var _ Deployer = (*NullDeployer)(nil)

func (NullDeployer) Apply(_ context.Context, _ ApplicationSpec) (DeploymentRef, error) {
	return DeploymentRef{}, ErrNotConfigured
}

func (NullDeployer) Status(_ context.Context, _ DeploymentRef) (DeploymentStatus, error) {
	return DeploymentStatus{}, ErrNotConfigured
}

func (NullDeployer) Delete(_ context.Context, _ DeploymentRef) error {
	return ErrNotConfigured
}

func (NullDeployer) Rollback(_ context.Context, _ DeploymentRef, _ string) error {
	return ErrNotConfigured
}
