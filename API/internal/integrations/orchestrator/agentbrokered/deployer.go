// Package agentbrokered implements the orchestrator.Deployer interface via a
// command-queue pattern: the hub writes apply/delete/rollback commands to the
// agent_commands table; the cluster agent claims them on its next heartbeat
// and executes them locally against its own Argo CD installation.
//
// This satisfies ADR-4 (agent-pull model) — the hub never holds a long-lived
// kubeconfig for spoke clusters; all cluster-side actions originate from the
// agent, not the hub.
//
// State transitions flow back from the agent via POST /clusters/:id/deployment-states,
// which is already implemented in handlers/agent.go. Status() reads the current
// deployment state from the DB so the reconciler's pollStatus sweep is a no-op
// for agent-brokered deployments (it sees no state drift because the agent is
// already the authoritative source of truth).
package agentbrokered

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/orchestrator"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

const (
	commandTTL          = 24 * time.Hour
	applyCommandType    = "apply"
	deleteCommandType   = "delete"
	rollbackCommandType = "rollback"
)

// ── Payload structs ────────────────────────────────────────────────────────────
// These mirror the decode structs in cmd/agent/main.go. Both sides must use
// identical JSON field names — snake_case throughout.

type applyPayload struct {
	DeploymentID  string               `json:"deployment_id"`
	AppSlug       string               `json:"app_slug"`
	ClusterID     string               `json:"cluster_id"`
	Namespace     string               `json:"namespace"`
	Project       string               `json:"project"`
	GitHubRepoURL string               `json:"github_repo_url"`
	GitSHA        string               `json:"git_sha"`
	ChartPath     string               `json:"chart_path"`
	Values        json.RawMessage      `json:"values"`
	SecretRefs    []applyPayloadSecRef `json:"secret_refs"`
}

type applyPayloadSecRef struct {
	FieldPath string `json:"field_path"`
	VaultPath string `json:"vault_path"`
}

type deletePayload struct {
	AppName string `json:"app_name"`
}

type rollbackPayload struct {
	AppName    string `json:"app_name"`
	ToRevision string `json:"to_revision"`
}

// Deployer implements orchestrator.Deployer via agent command queueing.
type Deployer struct {
	db *db.Handle
}

// New constructs an agent-brokered Deployer backed by the given DB handle.
func New(h *db.Handle) *Deployer {
	return &Deployer{db: h}
}

// Apply enqueues an apply command for the target cluster's agent and returns a
// DeploymentRef immediately. The application name is deterministic
// ("app-<first12hexchars>") so callers can build it without waiting for an ack.
//
// If the agent is offline, the command waits in the DB until the agent
// reconnects; the reconciler will retry Apply via pending_orchestrator after a
// transient error, so callers should not treat a queued command as a failure.
func (d *Deployer) Apply(ctx context.Context, spec orchestrator.ApplicationSpec) (orchestrator.DeploymentRef, error) {
	refs := make([]applyPayloadSecRef, len(spec.SecretRefs))
	for i, r := range spec.SecretRefs {
		refs[i] = applyPayloadSecRef{FieldPath: r.FieldPath, VaultPath: r.VaultPath}
	}
	p := applyPayload{
		DeploymentID:  spec.DeploymentID.String(),
		AppSlug:       spec.AppSlug,
		ClusterID:     spec.ClusterID.String(),
		Namespace:     spec.Namespace,
		Project:       spec.Project,
		GitHubRepoURL: spec.GitHubRepoURL,
		GitSHA:        spec.GitSHA,
		ChartPath:     spec.ChartPath,
		Values:        spec.Values,
		SecretRefs:    refs,
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return orchestrator.DeploymentRef{}, fmt.Errorf("marshal apply spec: %w", err)
	}

	cmd := models.AgentCommand{
		ClusterID:   spec.ClusterID,
		CommandType: applyCommandType,
		Payload:     datatypes.JSON(payload),
		ExpiresAt:   time.Now().UTC().Add(commandTTL),
	}
	if err := d.db.RW.WithContext(ctx).Create(&cmd).Error; err != nil {
		return orchestrator.DeploymentRef{}, fmt.Errorf("enqueue apply command: %w", err)
	}

	return orchestrator.DeploymentRef{
		ApplicationName: appNameFromID(spec.DeploymentID),
		ClusterID:       spec.ClusterID,
	}, nil
}

// Status reads the current deployment state from the DB and maps it to an
// orchestrator Phase. This keeps the reconciler's pollStatus sweep a no-op:
// it sees phaseToState(Status.Phase) == dep.State and writes nothing, which
// is correct — the agent is the authoritative driver of state transitions.
func (d *Deployer) Status(ctx context.Context, ref orchestrator.DeploymentRef) (orchestrator.DeploymentStatus, error) {
	var dep models.Deployment
	err := d.db.RW.WithContext(ctx).
		Where("last_argocd_app_name = ? AND deleted_at IS NULL", ref.ApplicationName).
		First(&dep).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return orchestrator.DeploymentStatus{}, orchestrator.ErrApplicationNotFound
		}
		return orchestrator.DeploymentStatus{}, fmt.Errorf("status lookup: %w", err)
	}

	return orchestrator.DeploymentStatus{
		Phase:   stateToPhase(dep.State),
		Healthy: dep.State == models.DeploymentReady,
	}, nil
}

// Delete enqueues a delete command for the cluster agent.
func (d *Deployer) Delete(ctx context.Context, ref orchestrator.DeploymentRef) error {
	payload, err := json.Marshal(deletePayload{AppName: ref.ApplicationName})
	if err != nil {
		return fmt.Errorf("marshal delete payload: %w", err)
	}
	cmd := models.AgentCommand{
		ClusterID:   ref.ClusterID,
		CommandType: deleteCommandType,
		Payload:     datatypes.JSON(payload),
		ExpiresAt:   time.Now().UTC().Add(commandTTL),
	}
	if err := d.db.RW.WithContext(ctx).Create(&cmd).Error; err != nil {
		return fmt.Errorf("enqueue delete command: %w", err)
	}
	return nil
}

// Rollback enqueues a rollback command for the cluster agent.
func (d *Deployer) Rollback(ctx context.Context, ref orchestrator.DeploymentRef, toRevision string) error {
	payload, err := json.Marshal(rollbackPayload{AppName: ref.ApplicationName, ToRevision: toRevision})
	if err != nil {
		return fmt.Errorf("marshal rollback payload: %w", err)
	}
	cmd := models.AgentCommand{
		ClusterID:   ref.ClusterID,
		CommandType: rollbackCommandType,
		Payload:     datatypes.JSON(payload),
		ExpiresAt:   time.Now().UTC().Add(commandTTL),
	}
	if err := d.db.RW.WithContext(ctx).Create(&cmd).Error; err != nil {
		return fmt.Errorf("enqueue rollback command: %w", err)
	}
	return nil
}

// appNameFromID returns the deterministic Argo CD application name for a
// deployment UUID: "app-" + the first 12 hex chars (without dashes).
func appNameFromID(id uuid.UUID) string {
	hex := strings.ReplaceAll(id.String(), "-", "")
	return "app-" + hex[:12]
}

// stateToPhase maps a DB DeploymentState to an orchestrator Phase.
// The reconciler compares phaseToState(phase) == dep.State to decide whether
// to write a state update; returning the current state's phase makes that
// comparison a guaranteed no-op.
func stateToPhase(s models.DeploymentState) orchestrator.Phase {
	switch s {
	case models.DeploymentApplying:
		return orchestrator.PhaseApplying
	case models.DeploymentSecretSync:
		return orchestrator.PhaseSecretSync
	case models.DeploymentReconciling:
		return orchestrator.PhaseReconciling
	case models.DeploymentReady:
		return orchestrator.PhaseReady
	case models.DeploymentDegraded:
		return orchestrator.PhaseDegraded
	case models.DeploymentFailedOrchestrator, models.DeploymentFailedRender, models.DeploymentFailedSecretSync, models.DeploymentFailedVault:
		return orchestrator.PhaseFailed
	default:
		return orchestrator.PhasePending
	}
}
