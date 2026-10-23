// Package reconciler runs a background goroutine that drives Deployment state
// transitions independently of HTTP requests. Two sweeps run on every tick:
//
//  1. Status poll — non-terminal deployments with a known Argo CD application
//     name are queried via Orchestrator.Status; the DB row is updated whenever
//     the reported phase changes.
//
//  2. Apply retry — deployments in pending_orchestrator (set when a transient
//     upstream error interrupted the initial Apply call) are retried. If
//     Apply succeeds the row transitions to applying; if the row has been in
//     pending_orchestrator for more than retryTimeout it is permanently failed.
package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/deploystate"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/orchestrator"
	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
)

const (
	defaultInterval  = 30 * time.Second
	retryTimeout     = 10 * time.Minute
	heartbeatTimeout = 5 * time.Minute
)

// Reconciler drives asynchronous deployment state transitions.
type Reconciler struct {
	db           *db.Handle
	orchestrator orchestrator.Deployer
	audit        audit.Logger
	interval     time.Duration
}

// New creates a Reconciler. interval controls the poll cadence; pass 0 to
// use the default (30 s).
func New(h *db.Handle, d orchestrator.Deployer, a audit.Logger, interval time.Duration) *Reconciler {
	if interval <= 0 {
		interval = defaultInterval
	}
	return &Reconciler{db: h, orchestrator: d, audit: a, interval: interval}
}

// Run blocks until ctx is cancelled. Call it in a goroutine from main.
func (r *Reconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	slog.Info("reconciler: started", "interval", r.interval)
	for {
		select {
		case <-ctx.Done():
			slog.Info("reconciler: stopped")
			return
		case <-ticker.C:
			r.tick(ctx)
		}
	}
}

func (r *Reconciler) tick(ctx context.Context) {
	r.pollStatus(ctx)
	r.retryPending(ctx)
	r.clusterHealthCheck(ctx)
	r.warnExpiringCerts(ctx)
	r.recordStateCounts(ctx)
	r.recordAuditSubmissionStates(ctx)
	r.recordAgentGauges(ctx)
	r.recordActiveAPIKeys(ctx)
}

// recordStateCounts publishes deployment_state_total for every known state
// from a single GROUP BY query. Soft-deleted rows are excluded.
func (r *Reconciler) recordStateCounts(ctx context.Context) {
	var rows []struct {
		State models.DeploymentState
		Count int64
	}
	if err := r.db.RW.WithContext(ctx).
		Model(&models.Deployment{}).
		Select("state, COUNT(*) AS count").
		Where("deleted_at IS NULL").
		Group("state").
		Scan(&rows).Error; err != nil {
		slog.Error("reconciler: state count query", "error", err)
		return
	}
	counts := make(map[models.DeploymentState]int64, len(rows))
	for _, row := range rows {
		counts[row.State] = row.Count
	}
	setStateGauges(counts)
}

// setStateGauges sets deployment_state_total for every state in
// models.AllDeploymentStates, writing 0 for states missing from counts so a
// state that empties out stops reporting its last value.
func setStateGauges(counts map[models.DeploymentState]int64) {
	for _, s := range models.AllDeploymentStates {
		metrics.DeploymentStateGauge(string(s)).Set(float64(counts[s]))
	}
}

// pollStatus queries every non-terminal deployment that has an Argo CD
// application name, calls Orchestrator.Status, and updates the DB row when
// the phase changes.
func (r *Reconciler) pollStatus(ctx context.Context) {
	// States where we have nothing to poll: either already terminal, or not
	// yet submitted to the orchestrator.
	terminalStates := []models.DeploymentState{
		models.DeploymentReady,
		models.DeploymentDegraded,
		models.DeploymentDeleted,
		models.DeploymentWaitingForCluster,
		models.DeploymentFailedOrchestrator,
		models.DeploymentFailedRender,
		models.DeploymentFailedVault,
		models.DeploymentFailedSecretSync,
		models.DeploymentPendingOrchestrator,
	}

	var rows []models.Deployment
	if err := r.db.RW.WithContext(ctx).
		Where("last_argocd_app_name IS NOT NULL").
		Where("deleted_at IS NULL").
		Where("state NOT IN ?", terminalStates).
		Find(&rows).Error; err != nil {
		slog.Error("reconciler: poll query", "error", err)
		return
	}

	for _, dep := range rows {
		r.reconcileOne(ctx, dep)
	}
}

func (r *Reconciler) reconcileOne(ctx context.Context, dep models.Deployment) {
	if dep.LastArgocdAppName == nil {
		return
	}
	ref := orchestrator.NewDeploymentRef(*dep.LastArgocdAppName, dep.ClusterID)

	status, err := r.orchestrator.Status(ctx, ref)
	if err != nil {
		if errors.Is(err, orchestrator.ErrApplicationNotFound) {
			// Argo CD no longer knows about this application — treat as deleted.
			if dep.State == models.DeploymentDeleting {
				r.transition(ctx, dep, models.DeploymentDeleted, "reconciler: application gone from orchestrator")
			} else {
				r.transition(ctx, dep, models.DeploymentFailedOrchestrator, "reconciler: application not found in orchestrator")
			}
		}
		// Transient errors are silently retried next tick.
		return
	}

	dbState := phaseToState(status.Phase)
	if dbState == "" || dbState == dep.State {
		return
	}

	r.transition(ctx, dep, dbState, fmt.Sprintf("reconciler: orchestrator phase=%s", status.Phase))

	if dbState == models.DeploymentReady || dbState == models.DeploymentFailedOrchestrator {
		evType := "deployment.ready"
		if dbState == models.DeploymentFailedOrchestrator {
			evType = "deployment.failed"
		}
		_ = r.audit.Log(ctx, audit.Event{
			Type:        evType,
			SubjectType: "deployment",
			SubjectID:   dep.ID,
			Payload:     map[string]any{"phase": string(status.Phase), "revision": status.Revision, "source": "reconciler"},
		})
	}
}

// retryPending picks up deployments in pending_orchestrator, rebuilds the
// ApplicationSpec, and calls Orchestrator.Apply. Rows that have been stuck
// longer than retryTimeout are permanently failed.
func (r *Reconciler) retryPending(ctx context.Context) {
	var rows []models.Deployment
	if err := r.db.RW.WithContext(ctx).
		Where("state = ?", models.DeploymentPendingOrchestrator).
		Where("deleted_at IS NULL").
		Find(&rows).Error; err != nil {
		slog.Error("reconciler: pending query", "error", err)
		return
	}

	for _, dep := range rows {
		r.retryOne(ctx, dep)
	}
}

func (r *Reconciler) retryOne(ctx context.Context, dep models.Deployment) {
	if time.Since(dep.LastStateChangeAt) > retryTimeout {
		slog.Warn("reconciler: deployment exceeded retry timeout", "deployment_id", dep.ID)
		r.transition(ctx, dep, models.DeploymentFailedOrchestrator, "reconciler: retry timeout exceeded")
		_ = r.audit.Log(ctx, audit.Event{
			Type:        "deployment.failed",
			SubjectType: "deployment",
			SubjectID:   dep.ID,
			Payload:     map[string]any{"reason": "pending_orchestrator retry timeout", "source": "reconciler"},
		})
		return
	}

	spec, err := r.buildSpec(ctx, dep)
	if err != nil {
		slog.Error("reconciler: build spec failed", "error", err, "deployment_id", dep.ID)
		return
	}

	ref, applyErr := r.orchestrator.Apply(ctx, *spec)
	if applyErr != nil {
		if errors.Is(applyErr, orchestrator.ErrTransient) {
			// Still transient — leave in pending_orchestrator for the next tick.
			return
		}
		slog.Error("reconciler: apply failed permanently", "error", applyErr, "deployment_id", dep.ID)
		r.transition(ctx, dep, models.DeploymentFailedOrchestrator, "reconciler: apply failed: "+applyErr.Error())
		_ = r.audit.Log(ctx, audit.Event{
			Type:        "deployment.failed",
			SubjectType: "deployment",
			SubjectID:   dep.ID,
			Payload:     map[string]any{"error": applyErr.Error(), "source": "reconciler"},
		})
		return
	}

	if err := deploystate.Set(ctx, r.db.RW, &dep, models.DeploymentApplying, map[string]any{
		"last_argocd_app_name": ref.ApplicationName,
	}); err != nil {
		slog.Error("reconciler: update to applying", "error", err, "deployment_id", dep.ID)
	}
	slog.Info("reconciler: deployment retried, now applying", "deployment_id", dep.ID)
}

// buildSpec reconstructs an ApplicationSpec from a Deployment row by loading
// the associated App, AppVersion, and Cluster.
func (r *Reconciler) buildSpec(ctx context.Context, dep models.Deployment) (*orchestrator.ApplicationSpec, error) {
	var app models.App
	if err := r.db.RW.WithContext(ctx).First(&app, "id = ?", dep.AppID).Error; err != nil {
		return nil, fmt.Errorf("load app: %w", err)
	}
	var version models.AppVersion
	if err := r.db.RW.WithContext(ctx).First(&version, "id = ?", dep.AppVersionID).Error; err != nil {
		return nil, fmt.Errorf("load version: %w", err)
	}
	var cluster models.Cluster
	if err := r.db.RW.WithContext(ctx).First(&cluster, "id = ?", dep.ClusterID).Error; err != nil {
		return nil, fmt.Errorf("load cluster: %w", err)
	}

	var refs []orchestrator.SecretRef
	if err := json.Unmarshal(dep.SecretRefs, &refs); err != nil {
		refs = nil // graceful degradation — no secrets
	}

	return &orchestrator.ApplicationSpec{
		DeploymentID:  dep.ID,
		ClusterID:     cluster.ID,
		AppSlug:       app.Slug,
		ClusterName:   cluster.EffectiveOrchestratorName(),
		Project:       cluster.EffectiveArgocdProject(),
		Namespace:     dep.Namespace,
		GitHubRepoURL: fmt.Sprintf("https://github.com/%s/%s", app.GitHubRepoOwner, app.GitHubRepoName),
		GitSHA:        version.GitSHA,
		ChartPath:     chartPathFromVersion(version),
		Values:        json.RawMessage(dep.Values),
		SecretRefs:    refs,
	}, nil
}

// chartPathFromVersion reads spec.chart.path from the persisted AppstoreYAML.
func chartPathFromVersion(v models.AppVersion) string {
	if len(v.AppstoreYAML) == 0 {
		return "chart"
	}
	var m struct {
		Spec struct {
			Chart struct {
				Path string `json:"path"`
			} `json:"chart"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(v.AppstoreYAML, &m); err != nil {
		return "chart"
	}
	return strings.TrimPrefix(m.Spec.Chart.Path, "./")
}

// transition updates the Deployment.state column and logs the change.
func (r *Reconciler) transition(ctx context.Context, dep models.Deployment, newState models.DeploymentState, reason string) {
	from := dep.State
	if err := deploystate.Set(ctx, r.db.RW, &dep, newState, nil); err != nil {
		slog.Error("reconciler: state transition failed", "error", err, "deployment_id", dep.ID, "new_state", string(newState))
		return
	}
	slog.Info("reconciler: deployment state changed", "deployment_id", dep.ID, "from", from, "to", newState, "reason", reason)
}

// clusterHealthCheck marks clusters as unhealthy when they have not sent a
// heartbeat within the heartbeatTimeout window. It also promotes
// waiting_for_cluster deployments to pending_orchestrator when their cluster
// has transitioned back to a healthy state — covering the case where the
// agent reconnected between reconciler ticks and the heartbeat handler
// already restored the cluster but the requeue step failed or was skipped.
func (r *Reconciler) clusterHealthCheck(ctx context.Context) {
	cutoff := time.Now().UTC().Add(-heartbeatTimeout)

	// 1. Mark stale clusters unhealthy.
	res := r.db.RW.WithContext(ctx).
		Model(&models.Cluster{}).
		Where("state IN ?", []models.ClusterState{models.ClusterReady, models.ClusterReadyNoESO}).
		Where("last_heartbeat_at IS NOT NULL AND last_heartbeat_at < ?", cutoff).
		Update("state", models.ClusterUnhealthy)
	if res.Error != nil {
		slog.Error("reconciler: cluster health check mark unhealthy", "error", res.Error)
	} else if res.RowsAffected > 0 {
		slog.Warn("reconciler: clusters marked unhealthy", "count", res.RowsAffected, "timeout", heartbeatTimeout)
	}

	// 2. Re-queue waiting_for_cluster deployments whose cluster is now healthy.
	// This handles the case where a cluster recovered but the heartbeat
	// handler's requeue step failed.
	requeued, err := deploystate.SetWhere(r.db.RW.WithContext(ctx).
		Model(&models.Deployment{}).
		Where(`deleted_at IS NULL
			AND cluster_id IN (
				SELECT id FROM clusters
				WHERE state IN ? AND deleted_at IS NULL
			)`,
			[]models.ClusterState{models.ClusterReady, models.ClusterReadyNoESO},
		),
		models.DeploymentWaitingForCluster, models.DeploymentPendingOrchestrator)
	if err != nil {
		slog.Error("reconciler: cluster health check requeue", "error", err)
	} else if requeued > 0 {
		slog.Info("reconciler: re-queued waiting deployments", "count", requeued)
	}
}

// warnExpiringCerts logs a warning for any cluster whose mTLS agent certificate
// expires within the next 14 days. It does not alter DB state — the operator
// must renew the cert by having the agent re-run the handshake.
func (r *Reconciler) warnExpiringCerts(ctx context.Context) {
	cutoff := time.Now().UTC().Add(14 * 24 * time.Hour)

	var clusters []models.Cluster
	if err := r.db.RW.WithContext(ctx).
		Where("agent_cert_expires_at IS NOT NULL AND agent_cert_expires_at < ? AND state != ?",
			cutoff, models.ClusterDecommissioned).
		Find(&clusters).Error; err != nil {
		slog.Error("reconciler: cert expiry check", "error", err)
		return
	}
	for _, c := range clusters {
		daysLeft := int(time.Until(*c.AgentCertExpiresAt) / (24 * time.Hour))
		slog.Warn("reconciler: cluster agent cert expiring soon",
			"cluster_id", c.ID, "cluster_name", c.Name, "days_left", daysLeft)
		_ = r.audit.Log(ctx, audit.Event{
			Type:        "cluster.cert_expiring_soon",
			SubjectType: "cluster",
			SubjectID:   c.ID,
			Payload: map[string]any{
				"days_until_expiry": daysLeft,
				"cert_serial":       c.AgentCertSerial,
				"cert_expires_at":   c.AgentCertExpiresAt,
			},
		})
	}
}

// phaseToState maps an orchestrator phase to a DB-level DeploymentState.
// Returns "" when no state change should be written.
func phaseToState(p orchestrator.Phase) models.DeploymentState {
	switch p {
	case orchestrator.PhaseApplying:
		return models.DeploymentApplying
	case orchestrator.PhaseSecretSync:
		return models.DeploymentSecretSync
	case orchestrator.PhaseReconciling:
		return models.DeploymentReconciling
	case orchestrator.PhaseReady:
		return models.DeploymentReady
	case orchestrator.PhaseDegraded:
		return models.DeploymentDegraded
	case orchestrator.PhaseFailed:
		return models.DeploymentFailedOrchestrator
	default:
		return ""
	}
}
