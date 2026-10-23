package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/deploystate"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

// maxDeploymentStateUpdatesPerBatch caps how many deployment state-update rows
// an agent may send in a single batch request, preventing oversized payloads.
const maxDeploymentStateUpdatesPerBatch = 100

// agentAllowedTransitions is the set of (from → to) state transitions an
// agent is permitted to report. The agent is trusted for its own cluster, but
// we still guard against nonsensical transitions (e.g. deleted → ready).
//
// The agent reports what Argo CD shows each time it polls, so it can miss
// short phases: a sync that finishes between two polls goes from applying
// straight to ready. applying therefore accepts every phase Argo CD can be
// in once the agent has handed it the change. A report from before the
// agent ran the command is refused separately (see commandOutstanding), so
// the status of the previous revision cannot settle an apply or rollback.
// secret_sync stays reachable for an agent that reports an ExternalSecrets
// step; the current agent does not, as it has no such step.
var agentAllowedTransitions = map[models.DeploymentState][]models.DeploymentState{
	models.DeploymentPending:           {models.DeploymentApplying, models.DeploymentFailedRender, models.DeploymentFailedVault},
	models.DeploymentWaitingForCluster: {models.DeploymentApplying, models.DeploymentFailedRender},
	models.DeploymentApplying: {
		models.DeploymentSecretSync, models.DeploymentReconciling, models.DeploymentReady, models.DeploymentDegraded,
		models.DeploymentFailedRender, models.DeploymentFailedOrchestrator,
	},
	models.DeploymentSecretSync:          {models.DeploymentReconciling, models.DeploymentFailedSecretSync},
	models.DeploymentReconciling:         {models.DeploymentReady, models.DeploymentDegraded, models.DeploymentFailedOrchestrator},
	models.DeploymentReady:               {models.DeploymentDegraded, models.DeploymentDeleting},
	models.DeploymentDegraded:            {models.DeploymentReady, models.DeploymentDeleting},
	models.DeploymentDeleting:            {models.DeploymentDeleted, models.DeploymentFailedOrchestrator},
	models.DeploymentFailedOrchestrator:  {models.DeploymentApplying},   // retry
	models.DeploymentFailedRender:        {models.DeploymentApplying},   // retry
	models.DeploymentFailedSecretSync:    {models.DeploymentSecretSync}, // retry
	models.DeploymentPendingOrchestrator: {models.DeploymentApplying, models.DeploymentFailedOrchestrator},
}

// ─── Heartbeat ────────────────────────────────────────────────────────────────

// AgentHeartbeatRequest is the body of POST /api/v1/clusters/:id/heartbeat.
type AgentHeartbeatRequest struct {
	AgentVersion string `json:"agent_version"`
}

// AgentHeartbeat handles POST /api/v1/clusters/:id/heartbeat.
//
// The saidkick-agent calls this endpoint every ~30 seconds. It:
//  1. Updates cluster.last_heartbeat_at.
//  2. Restores an unhealthy cluster to its prior healthy state.
//  3. Re-queues any waiting_for_cluster deployments for this cluster by
//     transitioning them to pending_orchestrator (picked up by the reconciler).
//  4. Optionally updates agent_version when a new value is supplied.
//
// Auth: AgentAuthMiddleware (mTLS cert CN or X-Agent-Cluster-ID in dev mode).
func (h *Handlers) AgentHeartbeat(c echo.Context) error {
	ctx := c.Request().Context()

	agent := auth.AgentIdentityFromContext(ctx)
	if agent == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "agent identity required")
	}

	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil || clusterID != agent.ClusterID {
		return apierror.New(http.StatusForbidden, "forbidden",
			"certificate cluster ID does not match path parameter")
	}

	var req AgentHeartbeatRequest
	if err := c.Bind(&req); err != nil {
		return apierror.New(http.StatusBadRequest, "bad_request", "invalid JSON")
	}

	cluster, err := h.loadActiveAgentCluster(ctx, clusterID)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	wasUnhealthy := cluster.State == models.ClusterUnhealthy

	updates := map[string]any{"last_heartbeat_at": now}
	if wasUnhealthy {
		// Restore to ready; if the agent reports no ESO at some point we can
		// revisit, but on reconnect assume fully ready.
		updates["state"] = string(models.ClusterReady)
	}
	if v := strings.TrimSpace(req.AgentVersion); v != "" {
		updates["agent_version"] = v
	}

	if err := h.DB.RW.WithContext(ctx).Model(&cluster).Updates(updates).Error; err != nil {
		return fmt.Errorf("update cluster heartbeat: %w", err)
	}

	// When a cluster recovers from unhealthy, promote any deployments that
	// were waiting for it to pending_orchestrator so the reconciler retries.
	if wasUnhealthy {
		if err := h.requeueWaitingDeployments(ctx, clusterID); err != nil {
			// Non-fatal: log and continue. The reconciler will pick these up
			// on the next health-check sweep that detects the cluster is ready.
			slog.Warn("agent heartbeat: requeue waiting deployments", "error", err, "cluster_id", clusterID)
		}
		_ = h.Audit.Log(ctx, audit.Event{
			Type:        "cluster.reconnected",
			SubjectType: "cluster",
			SubjectID:   clusterID,
			Payload:     map[string]any{"agent_version": req.AgentVersion},
		})
	}

	// An admin-requested rotation is delivered on every heartbeat until the
	// agent's renewal clears it.
	renew := cluster.AgentCertRotationRequestedAt != nil

	pending, err := h.claimPendingCommands(ctx, clusterID)
	if err != nil {
		slog.Error("agent heartbeat: claim commands", "error", err, "cluster_id", clusterID)
		// Non-fatal: send no commands; agent retries next heartbeat.
		pending = nil
	}
	return writeHeartbeatResponse(c, pending, renew)
}

// writeHeartbeatResponse answers a heartbeat: 204 when there is nothing to
// send, otherwise 200 with the pending commands and, when the agent must
// renew its certificate, "renew": true.
func writeHeartbeatResponse(c echo.Context, pending []PendingCommandItem, renew bool) error {
	if len(pending) == 0 && !renew {
		return c.NoContent(http.StatusNoContent)
	}
	if pending == nil {
		pending = []PendingCommandItem{}
	}
	body := map[string]any{"pending_commands": pending}
	if renew {
		body["renew"] = true
	}
	return c.JSON(http.StatusOK, body)
}

// loadActiveAgentCluster loads the cluster an agent is acting for and rejects
// it when it no longer exists or has been decommissioned. Every agent route
// calls this so a decommissioned cluster's agent cannot keep writing state.
func (h *Handlers) loadActiveAgentCluster(ctx context.Context, clusterID uuid.UUID) (models.Cluster, error) {
	var cluster models.Cluster
	if err := h.DB.RW.WithContext(ctx).First(&cluster, "id = ?", clusterID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return cluster, apierror.New(http.StatusNotFound, "not_found", "cluster not found")
		}
		return cluster, err
	}
	if cluster.State == models.ClusterDecommissioned {
		return cluster, apierror.New(http.StatusGone, "gone", "cluster has been decommissioned")
	}
	return cluster, nil
}

// PendingCommandItem is one element returned in the AgentHeartbeat response.
type PendingCommandItem struct {
	MsgID       string          `json:"msg_id"`
	CommandType string          `json:"type"`
	Payload     json.RawMessage `json:"payload"`
}

// claimPendingCommands fetches unclaimed, unexpired agent commands for the
// given cluster, marks them claimed, and returns them for delivery to the agent.
func (h *Handlers) claimPendingCommands(ctx context.Context, clusterID uuid.UUID) ([]PendingCommandItem, error) {
	now := time.Now().UTC()

	var cmds []models.AgentCommand
	err := h.DB.RW.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.
			Where("cluster_id = ? AND claimed_at IS NULL AND expires_at > ?", clusterID, now).
			Order("id ASC").
			Limit(50).
			Find(&cmds).Error; err != nil {
			return err
		}
		if len(cmds) == 0 {
			return nil
		}
		ids := make([]uuid.UUID, len(cmds))
		for i, c := range cmds {
			ids[i] = c.ID
		}
		return tx.Model(&models.AgentCommand{}).
			Where("id IN ?", ids).
			Update("claimed_at", now).Error
	})
	if err != nil {
		return nil, err
	}

	items := make([]PendingCommandItem, 0, len(cmds))
	for _, cmd := range cmds {
		items = append(items, PendingCommandItem{
			MsgID:       cmd.ID.String(),
			CommandType: cmd.CommandType,
			Payload:     json.RawMessage(cmd.Payload),
		})
	}
	return items, nil
}

// requeueWaitingDeployments transitions all waiting_for_cluster deployments
// on the given cluster to pending_orchestrator so the reconciler retries them.
func (h *Handlers) requeueWaitingDeployments(ctx context.Context, clusterID uuid.UUID) error {
	_, err := deploystate.SetWhere(h.DB.RW.WithContext(ctx).
		Model(&models.Deployment{}).
		Where("cluster_id = ? AND deleted_at IS NULL", clusterID),
		models.DeploymentWaitingForCluster, models.DeploymentPendingOrchestrator)
	return err
}

// ─── Deployment state updates ─────────────────────────────────────────────────

// DeploymentStateUpdate is one element in the agent's state report.
type DeploymentStateUpdate struct {
	DeploymentID string `json:"deployment_id"`
	State        string `json:"state"`
	// Message is an optional human-readable reason (e.g. Helm error output).
	Message string `json:"message,omitempty"`
	// ArgocdAppName, when non-empty, is stored so the reconciler can poll status.
	ArgocdAppName string `json:"argo_app_name,omitempty"`
}

// DeploymentStateUpdateResult is the per-update result returned to the agent.
type DeploymentStateUpdateResult struct {
	DeploymentID string `json:"deployment_id"`
	Accepted     bool   `json:"accepted"`
	Reason       string `json:"reason,omitempty"`
}

// AgentReportDeploymentStatesRequest is the body of
// POST /api/v1/clusters/:id/deployment-states.
type AgentReportDeploymentStatesRequest struct {
	Updates []DeploymentStateUpdate `json:"updates"`
}

// AgentReportDeploymentStates handles POST /api/v1/clusters/:id/deployment-states.
//
// The agent calls this endpoint when Argo CD reports a phase change for one or
// more deployments. Each update is validated against the allowed transition
// matrix and written atomically per deployment. Results are returned per update
// so the agent can identify which transitions were rejected and why.
//
// Auth: AgentAuthMiddleware.
func (h *Handlers) AgentReportDeploymentStates(c echo.Context) error {
	ctx := c.Request().Context()

	agent := auth.AgentIdentityFromContext(ctx)
	if agent == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "agent identity required")
	}

	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil || clusterID != agent.ClusterID {
		return apierror.New(http.StatusForbidden, "forbidden",
			"certificate cluster ID does not match path parameter")
	}

	if _, err := h.loadActiveAgentCluster(ctx, clusterID); err != nil {
		return err
	}

	var req AgentReportDeploymentStatesRequest
	if err := c.Bind(&req); err != nil {
		return apierror.New(http.StatusBadRequest, "bad_request", "invalid JSON")
	}
	if len(req.Updates) == 0 {
		return apierror.NewValidation("validation failed", []apierror.FieldError{
			{Path: "updates", Code: "required", Message: "updates must be a non-empty array"},
		})
	}
	if len(req.Updates) > maxDeploymentStateUpdatesPerBatch {
		return apierror.NewValidation("validation failed", []apierror.FieldError{
			{Path: "updates", Code: "too_many", Message: "at most 100 updates per request"},
		})
	}

	results := make([]DeploymentStateUpdateResult, 0, len(req.Updates))
	for _, u := range req.Updates {
		result := h.applyDeploymentStateUpdate(ctx, clusterID, u)
		results = append(results, result)
	}

	return c.JSON(http.StatusOK, map[string]any{"results": results})
}

func (h *Handlers) applyDeploymentStateUpdate(
	ctx context.Context,
	clusterID uuid.UUID,
	u DeploymentStateUpdate,
) DeploymentStateUpdateResult {
	reject := func(reason string) DeploymentStateUpdateResult {
		return DeploymentStateUpdateResult{DeploymentID: u.DeploymentID, Accepted: false, Reason: reason}
	}

	depID, err := uuid.Parse(u.DeploymentID)
	if err != nil {
		return reject("invalid deployment_id")
	}

	newState := models.DeploymentState(strings.TrimSpace(u.State))
	if newState == "" {
		return reject("state is required")
	}

	var dep models.Deployment
	if err := h.DB.RW.WithContext(ctx).
		Where("id = ? AND cluster_id = ? AND deleted_at IS NULL", depID, clusterID).
		First(&dep).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return reject("deployment not found on this cluster")
		}
		slog.Error("agent state update: load deployment", "error", err, "deployment_id", depID)
		return reject("internal error")
	}

	appName := strings.TrimSpace(u.ArgocdAppName)

	// The agent repeats the current state on every poll; accept it without
	// a transition.
	if newState == dep.State {
		if appName != "" && (dep.LastArgocdAppName == nil || *dep.LastArgocdAppName != appName) {
			if err := h.DB.RW.WithContext(ctx).Model(&dep).Update("last_argocd_app_name", appName).Error; err != nil {
				slog.Error("agent state update: write app name", "error", err, "deployment_id", depID)
				return reject("internal error")
			}
		}
		return DeploymentStateUpdateResult{DeploymentID: u.DeploymentID, Accepted: true}
	}

	// Until the agent acknowledges the command for this deployment, what it
	// reports describes the spec from before that command.
	outstanding, err := h.commandOutstanding(ctx, clusterID, dep)
	if err != nil {
		slog.Error("agent state update: check outstanding commands", "error", err, "deployment_id", depID)
		return reject("internal error")
	}
	if outstanding {
		return reject("a command for this deployment has not been acknowledged yet")
	}

	// Guard: validate the transition is permitted.
	allowed, ok := agentAllowedTransitions[dep.State]
	if !ok {
		return reject(fmt.Sprintf("no transitions allowed from state %q", dep.State))
	}
	permitted := slices.Contains(allowed, newState)
	if !permitted {
		return reject(fmt.Sprintf("transition %s → %s is not permitted", dep.State, newState))
	}

	var extra map[string]any
	if appName != "" {
		extra = map[string]any{"last_argocd_app_name": appName}
	}
	if err := deploystate.Set(ctx, h.DB.RW, &dep, newState, extra); err != nil {
		slog.Error("agent state update: write deployment", "error", err, "deployment_id", depID)
		return reject("internal error")
	}

	// Emit audit events for terminal / significant transitions.
	switch newState {
	case models.DeploymentReady:
		_ = h.Audit.Log(ctx, audit.Event{
			Type:        "deployment.ready",
			SubjectType: "deployment",
			SubjectID:   depID,
			Payload:     map[string]any{"source": "agent", "cluster_id": clusterID.String()},
		})
	case models.DeploymentDegraded, models.DeploymentFailedOrchestrator, models.DeploymentFailedRender,
		models.DeploymentFailedSecretSync, models.DeploymentFailedVault:
		_ = h.Audit.Log(ctx, audit.Event{
			Type:        "deployment.failed",
			SubjectType: "deployment",
			SubjectID:   depID,
			Payload: map[string]any{
				"state":      string(newState),
				"message":    u.Message,
				"source":     "agent",
				"cluster_id": clusterID.String(),
			},
		})
	case models.DeploymentDeleted:
		_ = h.Audit.Log(ctx, audit.Event{
			Type:        "deployment.deleted",
			SubjectType: "deployment",
			SubjectID:   depID,
			Payload:     map[string]any{"source": "agent", "cluster_id": clusterID.String()},
		})
	}

	return DeploymentStateUpdateResult{DeploymentID: u.DeploymentID, Accepted: true}
}

// commandOutstanding reports whether the cluster has an unexpired apply,
// delete or rollback command for dep that its agent has not acknowledged.
// Apply payloads name the deployment ID; delete and rollback payloads name
// the Argo CD application.
func (h *Handlers) commandOutstanding(ctx context.Context, clusterID uuid.UUID, dep models.Deployment) (bool, error) {
	appName := ""
	if dep.LastArgocdAppName != nil {
		appName = *dep.LastArgocdAppName
	}
	var outstanding bool
	err := h.DB.RW.WithContext(ctx).Raw(`SELECT EXISTS (
		SELECT 1 FROM agent_commands
		WHERE cluster_id = ? AND acked_at IS NULL AND expires_at > ?
		AND (payload->>'deployment_id' = ? OR (? <> '' AND payload->>'app_name' = ?))
	)`, clusterID, time.Now().UTC(), dep.ID.String(), appName, appName).Scan(&outstanding).Error
	return outstanding, err
}

// ─── Command acknowledgement ──────────────────────────────────────────────────

// AgentAckCommandRequest is the body of POST /api/v1/clusters/:id/commands/:cmd_id/ack.
type AgentAckCommandRequest struct {
	// Error, when non-empty, means the agent failed to process the command.
	// The hub logs the failure but does not retry automatically — the operator
	// must re-trigger the deployment.
	Error string `json:"error,omitempty"`
}

// AgentAckCommand handles POST /api/v1/clusters/:id/commands/:cmd_id/ack.
//
// The agent calls this endpoint after processing a command it received in the
// heartbeat response. A non-empty Error field means the command failed on the
// agent side.
//
// Auth: AgentAuthMiddleware.
func (h *Handlers) AgentAckCommand(c echo.Context) error {
	ctx := c.Request().Context()

	agent := auth.AgentIdentityFromContext(ctx)
	if agent == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "agent identity required")
	}

	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil || clusterID != agent.ClusterID {
		return apierror.New(http.StatusForbidden, "forbidden",
			"certificate cluster ID does not match path parameter")
	}

	if _, err := h.loadActiveAgentCluster(ctx, clusterID); err != nil {
		return err
	}

	cmdID, err := uuid.Parse(c.Param("cmd_id"))
	if err != nil {
		return apierror.New(http.StatusNotFound, "not_found", "command not found")
	}

	var req AgentAckCommandRequest
	_ = c.Bind(&req) // body is optional; a missing or malformed body yields zero-value (no error reported)

	now := time.Now().UTC()
	res := h.DB.RW.WithContext(ctx).Model(&models.AgentCommand{}).
		Where("id = ? AND cluster_id = ?", cmdID, clusterID).
		Update("acked_at", now)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return apierror.New(http.StatusNotFound, "not_found", "command not found for this cluster")
	}

	if req.Error != "" {
		slog.Warn("agent command: reported failure", "cmd_id", cmdID, "agent_error", req.Error)
		if err := h.failCommandDeployment(ctx, clusterID, cmdID, req.Error); err != nil {
			slog.Error("agent command: mark deployment failed", "error", err, "cmd_id", cmdID)
		}
	}
	return c.NoContent(http.StatusNoContent)
}

// failCommandDeployment moves the deployment an apply or rollback command
// was for from applying to failed_orchestrator. Without it, the agent's next
// status report (Argo CD still on the old revision, Synced and Healthy) would
// mark the deployment ready although the change never reached Argo CD.
func (h *Handlers) failCommandDeployment(ctx context.Context, clusterID, cmdID uuid.UUID, agentErr string) error {
	var cmd models.AgentCommand
	if err := h.DB.RW.WithContext(ctx).First(&cmd, "id = ?", cmdID).Error; err != nil {
		return err
	}
	if cmd.CommandType != "apply" && cmd.CommandType != "rollback" {
		return nil
	}
	var p struct {
		DeploymentID string `json:"deployment_id"`
		AppName      string `json:"app_name"`
	}
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return fmt.Errorf("decode command payload: %w", err)
	}
	q := h.DB.RW.WithContext(ctx).Model(&models.Deployment{}).
		Where("cluster_id = ? AND state = ? AND deleted_at IS NULL", clusterID, models.DeploymentApplying)
	switch {
	case p.DeploymentID != "":
		q = q.Where("id::text = ?", p.DeploymentID)
	case p.AppName != "":
		q = q.Where("last_argocd_app_name = ?", p.AppName)
	default:
		return nil
	}
	var deps []models.Deployment
	if err := q.Find(&deps).Error; err != nil {
		return err
	}
	for i := range deps {
		dep := deps[i]
		if err := deploystate.Set(ctx, h.DB.RW, &dep, models.DeploymentFailedOrchestrator, nil); err != nil {
			return err
		}
		_ = h.Audit.Log(ctx, audit.Event{
			Type:        "deployment.failed",
			SubjectType: "deployment",
			SubjectID:   dep.ID,
			Payload: map[string]any{
				"state":      string(models.DeploymentFailedOrchestrator),
				"message":    agentErr,
				"command":    cmd.CommandType,
				"source":     "agent",
				"cluster_id": clusterID.String(),
			},
		})
	}
	return nil
}
