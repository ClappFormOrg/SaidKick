// Package orchestrator is the platform's abstraction over the actual
// deployment tool. V1 ships a single concrete implementation
// (internal/integrations/orchestrator/argocd) backed by an Argo CD REST
// API. The abstraction is in place so a future Flux / Portainer / or
// agent-brokered implementation can be swapped in without touching the
// handlers.
//
// # ADR-4 reality check
//
// The design doc's ADR-4 commits to an agent-pull model — the hub never
// holds long-lived kubeconfigs for spoke clusters. The V1 ArgoCDDeployer
// in this package talks DIRECTLY to a configured Argo CD endpoint over
// REST, using a stored service-account token. That is the pragmatic
// spike path before the sAId-Kick agent itself is built. The
// AgentBrokeredDeployer impl that fully satisfies ADR-4 lands once we
// have an agent process — and it implements the same Deployer
// interface, so handler code does not change.
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrNotConfigured is returned by NullDeployer methods when no
// orchestrator endpoint is wired into this environment. Handlers
// translate this into a 503 with code "orchestrator_not_configured".
var ErrNotConfigured = errors.New("orchestrator: not configured")

// ErrApplicationNotFound is returned when Status / Delete / Rollback is
// asked about a Deployment the orchestrator no longer knows about.
// Often follows a manual deletion from the orchestrator UI; the handler
// maps this to 404.
var ErrApplicationNotFound = errors.New("orchestrator: application not found")

// ErrTransient is wrapped around upstream errors that are worth
// retrying (Argo CD unreachable, network blip, etc.). Handlers can
// surface this as 502/503 vs the 5xx we'd return for an unknown error.
var ErrTransient = errors.New("orchestrator: transient upstream error")

// SecretRef ties one helm-values field to its location in the central
// secret store. The orchestrator does NOT receive the secret value
// itself — it references the path so the in-cluster ExternalSecrets
// Operator (or equivalent) can fetch the value at deploy time.
type SecretRef struct {
	// FieldPath is the dot-separated path inside helm `values` that
	// will receive the secret at deploy time, e.g. "openai.apiKey".
	FieldPath string `json:"field_path"`

	// VaultPath is the secret store address, e.g.
	// "kv/data/deployments/<deployment_id>/openai_api_key". The
	// orchestrator's secret-sync mechanism reads from this path.
	VaultPath string `json:"vault_path"`
}

// ApplicationSpec is the everything-the-orchestrator-needs payload.
// Implementations translate this into their own representation
// (Argo CD Application CR, Flux HelmRelease, etc.).
type ApplicationSpec struct {
	// DeploymentID is the platform's UUID for this deployment. It is
	// derived into the orchestrator-side application name as
	// "app-<short_id>" (first 12 hex chars).
	DeploymentID uuid.UUID

	// ClusterID is the platform UUID of the target cluster. Used by
	// orchestrator implementations that look up per-cluster credentials
	// (e.g. per-project Argo CD tokens from Vault).
	ClusterID uuid.UUID

	// AppSlug is the human-friendly identifier — used only for log
	// lines and operator visibility (Argo CD UI labels, etc.).
	AppSlug string

	// ClusterName is the destination cluster as known to the
	// orchestrator. For Argo CD this is the cluster name registered
	// via `argocd cluster add` (matching one of the entries Argo CD
	// reports under /api/v1/clusters).
	ClusterName string

	// Project is the Argo CD AppProject (or equivalent) that this
	// deployment belongs to. Defaults to "default" when blank.
	Project string

	// Namespace is the target Kubernetes namespace inside the cluster.
	Namespace string

	// GitHubRepoURL is the HTTPS URL of the developer's repository,
	// e.g. "https://github.com/example-org/govchat".
	GitHubRepoURL string

	// GitSHA is the immutable 40-char commit hash being deployed.
	// Pinning to a SHA (not a branch) guarantees deployments are
	// reproducible.
	GitSHA string

	// ChartPath is the chart directory relative to the repo root.
	// Matches appstore.yaml's spec.chart.path.
	ChartPath string

	// Values is the non-secret half of helm values, already validated
	// against the AppVersion's values.schema.json. Will be inlined into
	// the orchestrator's application spec.
	Values json.RawMessage

	// SecretRefs declares each user-supplied secret as a vault-path
	// reference. The orchestrator does NOT see the values.
	SecretRefs []SecretRef
}

// DeploymentRef is the orchestrator-issued handle to one deployment.
// It is what subsequent Status / Delete / Rollback calls need. The
// platform persists this on the Deployment row.
type DeploymentRef struct {
	// ApplicationName is the orchestrator-side name (Argo CD
	// Application.metadata.name).
	ApplicationName string

	// OrchestratorNamespace is the namespace where the Application
	// resource lives (typically "argocd" for Argo CD).
	OrchestratorNamespace string

	// ClusterID is the platform UUID of the cluster this deployment
	// targets. Carried so Status/Delete/Rollback can look up
	// per-cluster credentials without an extra DB query.
	ClusterID uuid.UUID
}

// DefaultNamespace is the namespace Argo CD Application resources live in
// unless an implementation is configured otherwise.
const DefaultNamespace = "argocd"

// NewDeploymentRef rebuilds the handle Apply returned for a persisted
// deployment from its stored application name and cluster. Every caller
// that acts on an existing deployment (Status, Delete, Rollback) must build
// its ref here, so no field Apply sets can go missing. The agent-brokered
// deployer queues commands by ClusterID; a ref without it targets the nil
// cluster, which no agent ever claims.
func NewDeploymentRef(applicationName string, clusterID uuid.UUID) DeploymentRef {
	return DeploymentRef{
		ApplicationName:       applicationName,
		OrchestratorNamespace: DefaultNamespace,
		ClusterID:             clusterID,
	}
}

// Phase is the canonical state the platform tracks. Argo CD reports
// richer states; we collapse them into this fixed set so the handler /
// state machine is orchestrator-agnostic.
type Phase string

const (
	PhaseUnknown     Phase = ""             // zero value — uninitialized
	PhasePending     Phase = "pending"      // application created, not yet synced
	PhaseApplying    Phase = "applying"     // sync in progress
	PhaseSecretSync  Phase = "secret_sync"  // application created; ExternalSecrets pulling
	PhaseReconciling Phase = "reconciling"  // synced but not yet healthy
	PhaseReady       Phase = "ready"        // synced + healthy
	PhaseDegraded    Phase = "degraded"     // healthy at some point, now degraded
	PhaseFailed      Phase = "failed"       // unrecoverable without operator action
)

// DeploymentStatus is the result of Deployer.Status. Implementations
// normalise their native status into this shape.
type DeploymentStatus struct {
	Phase        Phase
	Message      string
	LastSyncedAt time.Time
	Revision     string // current git SHA the orchestrator believes is deployed
	Healthy      bool
}

// Deployer is the interface every orchestrator implementation
// satisfies. Pass this (not a concrete type) into handlers so we can
// stub it in tests.
type Deployer interface {
	// Apply creates or updates the application on the orchestrator.
	// Idempotent: calling Apply twice with the same spec is a no-op
	// (the orchestrator de-dupes on application name).
	Apply(ctx context.Context, spec ApplicationSpec) (DeploymentRef, error)

	// Status returns the current state of an application. Returns
	// ErrApplicationNotFound if the orchestrator no longer knows about
	// it (e.g. someone deleted it out-of-band).
	Status(ctx context.Context, ref DeploymentRef) (DeploymentStatus, error)

	// Delete tears down the application and all child resources.
	// Returns ErrApplicationNotFound if the application is already
	// gone (treated as success at the handler level).
	Delete(ctx context.Context, ref DeploymentRef) error

	// Rollback reverts the application to a previous revision (git
	// SHA). The orchestrator must have history for the target
	// revision; otherwise returns ErrApplicationNotFound.
	Rollback(ctx context.Context, ref DeploymentRef, toRevision string) error
}
