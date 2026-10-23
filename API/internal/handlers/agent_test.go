package handlers_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/handlers"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

// ─── Fixtures ────────────────────────────────────────────────────────────────

// agentFixture holds the seeded rows needed by agent endpoint tests.
type agentFixture struct {
	handle  *db.Handle
	org     models.Organization
	user    models.User
	cluster models.Cluster
}

// newAgentFixture seeds an organisation, a user, and a ready cluster, then
// returns a Handlers ready to call.
func newAgentFixture(t *testing.T) (agentFixture, *handlers.Handlers) {
	t.Helper()

	handle := testutil.NewPostgresDB(t)
	var f agentFixture
	f.handle = handle

	f.org = seed.SeedOrg(t, handle, models.Organization{Slug: "agent-org", DisplayName: "Agent Org", ContactEmail: "agent@example.com"})
	f.user = seed.SeedUser(t, handle, models.User{
		OrganizationID:  f.org.ID,
		ExternalSubject: "agent-sub",
		Email:           "agent@example.com",
		DisplayName:     "Agent User",
	})
	f.cluster = seed.SeedCluster(t, handle, models.Cluster{
		Name:  "test-cluster",
		Scope: models.ClusterScopePublic,
		State: models.ClusterReady,
	})

	return f, newTestHandlers(t, handle)
}

// createDeployment inserts a Deployment with the given state, creating the
// required App / AppVersion / AuditSubmission dependencies along the way.
func createDeployment(t *testing.T, handle *db.Handle, f agentFixture, state models.DeploymentState) models.Deployment {
	t.Helper()

	// App, version and submission use the seed defaults: a published app and
	// a published version with a minimal manifest.
	app := seed.SeedApp(t, handle, models.App{OwningOrganizationID: f.org.ID, OwningUserID: f.user.ID})
	ver, _ := seed.SeedPublishedVersion(t, handle, models.AppVersion{AppID: app.ID})
	return seed.SeedDeployment(t, handle, models.Deployment{
		AppID:                app.ID,
		AppVersionID:         ver.ID,
		ClusterID:            f.cluster.ID,
		OwningOrganizationID: f.org.ID,
		DeployerUserID:       f.user.ID,
		Namespace:            "default",
		State:                state,
	})
}

// agentCtx builds an Echo context for AgentHeartbeat and
// AgentReportDeploymentStates with the agent identity already stored on the
// request context.
func agentCtx(e *echo.Echo, clusterID uuid.UUID, body string) (echo.Context, *httptest.ResponseRecorder) {
	req := asAgent(newRequest(http.MethodPost, "/", body), clusterID)
	return echoContext(e, req, "id", clusterID.String())
}

// ackCtx builds an Echo context for AgentAckCommand.
func ackCtx(e *echo.Echo, clusterID, cmdID uuid.UUID, body string) (echo.Context, *httptest.ResponseRecorder) {
	req := asAgent(newRequest(http.MethodPost, "/", body), clusterID)
	return echoContext(e, req, "id", clusterID.String(), "cmd_id", cmdID.String())
}

// ─── AgentHeartbeat ──────────────────────────────────────────────────────────

func TestAgentHeartbeat(t *testing.T) {
	t.Parallel()
	t.Run("cluster_not_found_returns_404", func(t *testing.T) {
		t.Parallel()
		_, h := newAgentFixture(t)
		e := echo.New()
		unknownID := uuid.Must(uuid.NewV7())

		c, _ := agentCtx(e, unknownID, `{}`)
		err := h.AgentHeartbeat(c)

		var apiErr *apierror.Error
		require.True(t, errors.As(err, &apiErr), "expected *apierror.Error, got %T", err)
		assert.Equal(t, http.StatusNotFound, apiErr.Status)
	})

	t.Run("decommissioned_cluster_returns_410", func(t *testing.T) {
		t.Parallel()
		f, h := newAgentFixture(t)
		ctx := t.Context()
		require.NoError(t, f.handle.RW.WithContext(ctx).
			Model(&f.cluster).Update("state", models.ClusterDecommissioned).Error)

		e := echo.New()
		c, _ := agentCtx(e, f.cluster.ID, `{}`)
		err := h.AgentHeartbeat(c)

		var apiErr *apierror.Error
		require.True(t, errors.As(err, &apiErr))
		assert.Equal(t, http.StatusGone, apiErr.Status)
	})

	t.Run("healthy_cluster_returns_204_and_stamps_heartbeat", func(t *testing.T) {
		t.Parallel()
		f, h := newAgentFixture(t)
		e := echo.New()

		c, rec := agentCtx(e, f.cluster.ID, `{"agent_version":"1.2.3"}`)
		require.NoError(t, h.AgentHeartbeat(c))
		assert.Equal(t, http.StatusNoContent, rec.Code)

		var updated models.Cluster
		require.NoError(t, f.handle.RW.First(&updated, "id = ?", f.cluster.ID).Error)
		require.NotNil(t, updated.LastHeartbeatAt)
		require.NotNil(t, updated.AgentVersion)
		assert.Equal(t, "1.2.3", *updated.AgentVersion)
	})

	t.Run("reconnect_from_unhealthy_restores_ready_state", func(t *testing.T) {
		t.Parallel()
		f, h := newAgentFixture(t)
		ctx := t.Context()
		require.NoError(t, f.handle.RW.WithContext(ctx).
			Model(&f.cluster).Update("state", models.ClusterUnhealthy).Error)

		e := echo.New()
		c, rec := agentCtx(e, f.cluster.ID, `{}`)
		require.NoError(t, h.AgentHeartbeat(c))
		assert.Equal(t, http.StatusNoContent, rec.Code)

		var updated models.Cluster
		require.NoError(t, f.handle.RW.First(&updated, "id = ?", f.cluster.ID).Error)
		assert.Equal(t, models.ClusterReady, updated.State)
	})

	t.Run("delivers_pending_commands_and_marks_claimed", func(t *testing.T) {
		t.Parallel()
		f, h := newAgentFixture(t)
		ctx := t.Context()

		cmd := models.AgentCommand{
			ClusterID:   f.cluster.ID,
			CommandType: "apply",
			Payload:     datatypes.JSON(`{"deployment_id":"abc"}`),
			ExpiresAt:   time.Now().Add(24 * time.Hour),
		}
		require.NoError(t, f.handle.RW.WithContext(ctx).Create(&cmd).Error)

		e := echo.New()
		c, rec := agentCtx(e, f.cluster.ID, `{}`)
		require.NoError(t, h.AgentHeartbeat(c))
		assert.Equal(t, http.StatusOK, rec.Code)

		var resp struct {
			PendingCommands []struct {
				MsgID string `json:"msg_id"`
				Type  string `json:"type"`
			} `json:"pending_commands"`
		}
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		require.Len(t, resp.PendingCommands, 1)
		assert.Equal(t, cmd.ID.String(), resp.PendingCommands[0].MsgID)
		assert.Equal(t, "apply", resp.PendingCommands[0].Type)

		var updatedCmd models.AgentCommand
		require.NoError(t, f.handle.RW.First(&updatedCmd, "id = ?", cmd.ID).Error)
		assert.NotNil(t, updatedCmd.ClaimedAt, "command must be marked claimed")
	})

	t.Run("expired_commands_not_delivered", func(t *testing.T) {
		t.Parallel()
		f, h := newAgentFixture(t)
		ctx := t.Context()

		expiredCmd := models.AgentCommand{
			ClusterID:   f.cluster.ID,
			CommandType: "apply",
			Payload:     datatypes.JSON(`{}`),
			ExpiresAt:   time.Now().Add(-1 * time.Hour),
		}
		require.NoError(t, f.handle.RW.WithContext(ctx).Create(&expiredCmd).Error)

		e := echo.New()
		c, rec := agentCtx(e, f.cluster.ID, `{}`)
		require.NoError(t, h.AgentHeartbeat(c))
		assert.Equal(t, http.StatusNoContent, rec.Code, "expired commands must not be delivered")
	})

	t.Run("reconnect_requeues_waiting_for_cluster_deployments", func(t *testing.T) {
		t.Parallel()
		f, h := newAgentFixture(t)
		ctx := t.Context()

		// Set cluster to unhealthy so the heartbeat triggers a reconnect.
		require.NoError(t, f.handle.RW.WithContext(ctx).
			Model(&f.cluster).Update("state", models.ClusterUnhealthy).Error)

		dep := createDeployment(t, f.handle, f, models.DeploymentWaitingForCluster)

		e := echo.New()
		c, _ := agentCtx(e, f.cluster.ID, `{}`)
		require.NoError(t, h.AgentHeartbeat(c))

		var updated models.Deployment
		require.NoError(t, f.handle.RW.First(&updated, "id = ?", dep.ID).Error)
		assert.Equal(t, models.DeploymentPendingOrchestrator, updated.State,
			"waiting_for_cluster deployments must be re-queued on reconnect")
	})
}

// ─── AgentReportDeploymentStates ─────────────────────────────────────────────

func TestAgentReportDeploymentStates(t *testing.T) {
	t.Parallel()
	t.Run("valid_transition_accepted_and_persisted", func(t *testing.T) {
		t.Parallel()
		f, h := newAgentFixture(t)
		dep := createDeployment(t, f.handle, f, models.DeploymentPending)
		e := echo.New()

		body := fmt.Sprintf(`{"updates":[{"deployment_id":%q,"state":"applying"}]}`, dep.ID)
		c, rec := agentCtx(e, f.cluster.ID, body)
		require.NoError(t, h.AgentReportDeploymentStates(c))
		assert.Equal(t, http.StatusOK, rec.Code)

		var resp struct {
			Results []struct {
				DeploymentID string `json:"deployment_id"`
				Accepted     bool   `json:"accepted"`
			} `json:"results"`
		}
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		require.Len(t, resp.Results, 1)
		assert.True(t, resp.Results[0].Accepted)
		assert.Equal(t, dep.ID.String(), resp.Results[0].DeploymentID)

		var updated models.Deployment
		require.NoError(t, f.handle.RW.First(&updated, "id = ?", dep.ID).Error)
		assert.Equal(t, models.DeploymentApplying, updated.State)
	})

	t.Run("decommissioned_cluster_returns_410", func(t *testing.T) {
		t.Parallel()
		f, h := newAgentFixture(t)
		ctx := t.Context()
		dep := createDeployment(t, f.handle, f, models.DeploymentPending)
		require.NoError(t, f.handle.RW.WithContext(ctx).
			Model(&f.cluster).Update("state", models.ClusterDecommissioned).Error)

		e := echo.New()
		body := fmt.Sprintf(`{"updates":[{"deployment_id":%q,"state":"applying"}]}`, dep.ID)
		c, _ := agentCtx(e, f.cluster.ID, body)
		err := h.AgentReportDeploymentStates(c)

		var apiErr *apierror.Error
		require.True(t, errors.As(err, &apiErr))
		assert.Equal(t, http.StatusGone, apiErr.Status)

		var unchanged models.Deployment
		require.NoError(t, f.handle.RW.First(&unchanged, "id = ?", dep.ID).Error)
		assert.Equal(t, models.DeploymentPending, unchanged.State)
	})

	t.Run("invalid_transition_rejected", func(t *testing.T) {
		t.Parallel()
		f, h := newAgentFixture(t)
		// ready → pending is not in the allowed transition matrix.
		dep := createDeployment(t, f.handle, f, models.DeploymentReady)
		e := echo.New()

		body := fmt.Sprintf(`{"updates":[{"deployment_id":%q,"state":"pending"}]}`, dep.ID)
		c, rec := agentCtx(e, f.cluster.ID, body)
		require.NoError(t, h.AgentReportDeploymentStates(c))
		assert.Equal(t, http.StatusOK, rec.Code)

		var resp struct {
			Results []struct {
				Accepted bool   `json:"accepted"`
				Reason   string `json:"reason"`
			} `json:"results"`
		}
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		require.Len(t, resp.Results, 1)
		assert.False(t, resp.Results[0].Accepted)
		assert.NotEmpty(t, resp.Results[0].Reason)

		// State must not have changed.
		var unchanged models.Deployment
		require.NoError(t, f.handle.RW.First(&unchanged, "id = ?", dep.ID).Error)
		assert.Equal(t, models.DeploymentReady, unchanged.State)
	})

	t.Run("unknown_deployment_rejected", func(t *testing.T) {
		t.Parallel()
		f, h := newAgentFixture(t)
		e := echo.New()

		body := fmt.Sprintf(`{"updates":[{"deployment_id":%q,"state":"applying"}]}`, uuid.Must(uuid.NewV7()))
		c, rec := agentCtx(e, f.cluster.ID, body)
		require.NoError(t, h.AgentReportDeploymentStates(c))
		assert.Equal(t, http.StatusOK, rec.Code)

		var resp struct {
			Results []struct {
				Accepted bool   `json:"accepted"`
				Reason   string `json:"reason"`
			} `json:"results"`
		}
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		require.Len(t, resp.Results, 1)
		assert.False(t, resp.Results[0].Accepted)
		assert.Contains(t, resp.Results[0].Reason, "not found")
	})

	t.Run("empty_updates_returns_422", func(t *testing.T) {
		t.Parallel()
		f, h := newAgentFixture(t)
		e := echo.New()

		c, _ := agentCtx(e, f.cluster.ID, `{"updates":[]}`)
		err := h.AgentReportDeploymentStates(c)

		var apiErr *apierror.Error
		require.True(t, errors.As(err, &apiErr))
		assert.Equal(t, http.StatusUnprocessableEntity, apiErr.Status)
	})

	t.Run("batch_partial_accept", func(t *testing.T) {
		t.Parallel()
		f, h := newAgentFixture(t)
		DeploymentPending := createDeployment(t, f.handle, f, models.DeploymentPending)
		DeploymentReady := createDeployment(t, f.handle, f, models.DeploymentReady)
		e := echo.New()

		body := fmt.Sprintf(`{"updates":[
			{"deployment_id":%q,"state":"applying"},
			{"deployment_id":%q,"state":"pending"}
		]}`, DeploymentPending.ID, DeploymentReady.ID)
		c, rec := agentCtx(e, f.cluster.ID, body)
		require.NoError(t, h.AgentReportDeploymentStates(c))
		assert.Equal(t, http.StatusOK, rec.Code)

		var resp struct {
			Results []struct {
				Accepted bool `json:"accepted"`
			} `json:"results"`
		}
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		require.Len(t, resp.Results, 2)
		assert.True(t, resp.Results[0].Accepted, "pending → applying must be accepted")
		assert.False(t, resp.Results[1].Accepted, "ready → pending must be rejected")
	})

	t.Run("argo_app_name_stored_with_transition", func(t *testing.T) {
		t.Parallel()
		f, h := newAgentFixture(t)
		dep := createDeployment(t, f.handle, f, models.DeploymentApplying)
		e := echo.New()

		body := fmt.Sprintf(`{"updates":[{"deployment_id":%q,"state":"secret_sync","argo_app_name":"app-abc123"}]}`,
			dep.ID)
		c, _ := agentCtx(e, f.cluster.ID, body)
		require.NoError(t, h.AgentReportDeploymentStates(c))

		var updated models.Deployment
		require.NoError(t, f.handle.RW.First(&updated, "id = ?", dep.ID).Error)
		require.NotNil(t, updated.LastArgocdAppName)
		assert.Equal(t, "app-abc123", *updated.LastArgocdAppName)
	})
}

// ─── Agent report sequences ──────────────────────────────────────────────────

// reportResult is one entry of the AgentReportDeploymentStates response.
type reportResult struct {
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason"`
}

// reportState sends one state report for dep, as the agent's status loop
// does, and returns the hub's verdict.
func reportState(t *testing.T, h *handlers.Handlers, f agentFixture, depID uuid.UUID, state string) reportResult {
	t.Helper()
	body := fmt.Sprintf(`{"updates":[{"deployment_id":%q,"state":%q,"argo_app_name":"app-seq"}]}`, depID, state)
	c, rec := agentCtx(echo.New(), f.cluster.ID, body)
	require.NoError(t, h.AgentReportDeploymentStates(c))
	var resp struct {
		Results []reportResult `json:"results"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Len(t, resp.Results, 1)
	return resp.Results[0]
}

func deploymentState(t *testing.T, f agentFixture, id uuid.UUID) models.DeploymentState {
	t.Helper()
	var d models.Deployment
	require.NoError(t, f.handle.RW.WithContext(t.Context()).First(&d, "id = ?", id).Error)
	return d.State
}

// queueCommand inserts a command for f.cluster, acked or not.
func queueCommand(t *testing.T, f agentFixture, cmdType, payload string, acked bool) models.AgentCommand {
	t.Helper()
	cmd := models.AgentCommand{
		ClusterID:   f.cluster.ID,
		CommandType: cmdType,
		Payload:     datatypes.JSON(payload),
		ExpiresAt:   time.Now().Add(24 * time.Hour),
	}
	if acked {
		now := time.Now()
		cmd.ClaimedAt, cmd.AckedAt = &now, &now
	}
	require.NoError(t, f.handle.RW.WithContext(t.Context()).Create(&cmd).Error)
	return cmd
}

// TestAgentReportSequences replays the reports cmd/agent sends: "applying"
// from processApply, then one Argo CD status per poll from statusTick.
func TestAgentReportSequences(t *testing.T) {
	t.Parallel()

	t.Run("deploy_reaches_ready", func(t *testing.T) {
		t.Parallel()
		f, h := newAgentFixture(t)
		dep := createDeployment(t, f.handle, f, models.DeploymentApplying)
		queueCommand(t, f, "apply", fmt.Sprintf(`{"deployment_id":%q}`, dep.ID), true)

		steps := []struct {
			state    string
			accepted bool
			want     models.DeploymentState
		}{
			{"applying", true, models.DeploymentApplying},       // processApply after the Argo CD upsert
			{"reconciling", true, models.DeploymentReconciling}, // OutOfSync before the sync starts
			{"applying", false, models.DeploymentReconciling},   // sync operation Running
			{"reconciling", true, models.DeploymentReconciling}, // Synced, Progressing
			{"ready", true, models.DeploymentReady},             // Synced, Healthy
			{"ready", true, models.DeploymentReady},             // every later poll
		}
		for i, s := range steps {
			res := reportState(t, h, f, dep.ID, s.state)
			assert.Equal(t, s.accepted, res.Accepted, "step %d (%s): %s", i, s.state, res.Reason)
			assert.Equal(t, s.want, deploymentState(t, f, dep.ID), "step %d (%s)", i, s.state)
		}
	})

	t.Run("sync_between_polls_goes_applying_to_ready", func(t *testing.T) {
		t.Parallel()
		f, h := newAgentFixture(t)
		dep := createDeployment(t, f.handle, f, models.DeploymentApplying)
		queueCommand(t, f, "apply", fmt.Sprintf(`{"deployment_id":%q}`, dep.ID), true)

		res := reportState(t, h, f, dep.ID, "ready")
		assert.True(t, res.Accepted, res.Reason)
		assert.Equal(t, models.DeploymentReady, deploymentState(t, f, dep.ID))
	})

	t.Run("degraded_after_apply", func(t *testing.T) {
		t.Parallel()
		f, h := newAgentFixture(t)
		dep := createDeployment(t, f.handle, f, models.DeploymentApplying)

		res := reportState(t, h, f, dep.ID, "degraded")
		assert.True(t, res.Accepted, res.Reason)
		assert.Equal(t, models.DeploymentDegraded, deploymentState(t, f, dep.ID))
	})

	t.Run("report_before_rollback_ack_is_refused", func(t *testing.T) {
		t.Parallel()
		f, h := newAgentFixture(t)
		dep := createDeployment(t, f.handle, f, models.DeploymentApplying)
		require.NoError(t, f.handle.RW.WithContext(t.Context()).Model(&dep).
			Update("last_argocd_app_name", "app-rb").Error)
		cmd := queueCommand(t, f, "rollback", `{"app_name":"app-rb","to_revision":"abc"}`, false)

		// The status loop still sees the previous revision, Synced and Healthy.
		res := reportState(t, h, f, dep.ID, "ready")
		assert.False(t, res.Accepted)
		assert.Contains(t, res.Reason, "not been acknowledged")
		assert.Equal(t, models.DeploymentApplying, deploymentState(t, f, dep.ID))

		c, _ := ackCtx(echo.New(), f.cluster.ID, cmd.ID, `{}`)
		require.NoError(t, h.AgentAckCommand(c))

		res = reportState(t, h, f, dep.ID, "ready")
		assert.True(t, res.Accepted, res.Reason)
		assert.Equal(t, models.DeploymentReady, deploymentState(t, f, dep.ID))
	})

	t.Run("report_before_apply_ack_is_refused", func(t *testing.T) {
		t.Parallel()
		f, h := newAgentFixture(t)
		dep := createDeployment(t, f.handle, f, models.DeploymentApplying)
		queueCommand(t, f, "apply", fmt.Sprintf(`{"deployment_id":%q}`, dep.ID), false)

		res := reportState(t, h, f, dep.ID, "ready")
		assert.False(t, res.Accepted)
		assert.Equal(t, models.DeploymentApplying, deploymentState(t, f, dep.ID))
	})

	t.Run("failed_rollback_ack_marks_failed", func(t *testing.T) {
		t.Parallel()
		f, h := newAgentFixture(t)
		dep := createDeployment(t, f.handle, f, models.DeploymentApplying)
		require.NoError(t, f.handle.RW.WithContext(t.Context()).Model(&dep).
			Update("last_argocd_app_name", "app-rb").Error)
		cmd := queueCommand(t, f, "rollback", `{"app_name":"app-rb","to_revision":"abc"}`, false)

		c, _ := ackCtx(echo.New(), f.cluster.ID, cmd.ID, `{"error":"argocd: unexpected 400"}`)
		require.NoError(t, h.AgentAckCommand(c))
		assert.Equal(t, models.DeploymentFailedOrchestrator, deploymentState(t, f, dep.ID))

		// Argo CD still runs the previous revision; that must not read as ready.
		res := reportState(t, h, f, dep.ID, "ready")
		assert.False(t, res.Accepted)
		assert.Equal(t, models.DeploymentFailedOrchestrator, deploymentState(t, f, dep.ID))
	})

	t.Run("failed_apply_ack_marks_failed", func(t *testing.T) {
		t.Parallel()
		f, h := newAgentFixture(t)
		dep := createDeployment(t, f.handle, f, models.DeploymentApplying)
		cmd := queueCommand(t, f, "apply", fmt.Sprintf(`{"deployment_id":%q}`, dep.ID), false)

		c, _ := ackCtx(echo.New(), f.cluster.ID, cmd.ID, `{"error":"argo apply: auth rejected"}`)
		require.NoError(t, h.AgentAckCommand(c))
		assert.Equal(t, models.DeploymentFailedOrchestrator, deploymentState(t, f, dep.ID))
	})

	t.Run("same_state_report_is_a_no_op", func(t *testing.T) {
		t.Parallel()
		f, h := newAgentFixture(t)
		dep := createDeployment(t, f.handle, f, models.DeploymentReady)

		res := reportState(t, h, f, dep.ID, "ready")
		assert.True(t, res.Accepted, res.Reason)
		var d models.Deployment
		require.NoError(t, f.handle.RW.First(&d, "id = ?", dep.ID).Error)
		assert.Equal(t, models.DeploymentReady, d.State)
		require.NotNil(t, d.LastArgocdAppName)
		assert.Equal(t, "app-seq", *d.LastArgocdAppName)
	})

	// Jumps that skip the apply stay refused.
	illegal := map[models.DeploymentState]string{
		models.DeploymentPending:             "ready",
		models.DeploymentWaitingForCluster:   "ready",
		models.DeploymentPendingOrchestrator: "ready",
		models.DeploymentDeleting:            "ready",
		models.DeploymentFailedOrchestrator:  "ready",
		models.DeploymentReady:               "reconciling",
	}
	for from, to := range illegal {
		t.Run(string(from)+"_to_"+to+"_refused", func(t *testing.T) {
			t.Parallel()
			f, h := newAgentFixture(t)
			dep := createDeployment(t, f.handle, f, from)

			res := reportState(t, h, f, dep.ID, to)
			assert.False(t, res.Accepted)
			assert.Contains(t, res.Reason, "is not permitted")
			assert.Equal(t, from, deploymentState(t, f, dep.ID))
		})
	}
}

// ─── AgentAckCommand ─────────────────────────────────────────────────────────

func TestAgentAckCommand(t *testing.T) {
	t.Parallel()
	t.Run("success_sets_acked_at", func(t *testing.T) {
		t.Parallel()
		f, h := newAgentFixture(t)
		ctx := t.Context()

		cmd := models.AgentCommand{
			ClusterID:   f.cluster.ID,
			CommandType: "apply",
			Payload:     datatypes.JSON(`{}`),
			ExpiresAt:   time.Now().Add(24 * time.Hour),
		}
		require.NoError(t, f.handle.RW.WithContext(ctx).Create(&cmd).Error)

		e := echo.New()
		c, rec := ackCtx(e, f.cluster.ID, cmd.ID, `{}`)
		require.NoError(t, h.AgentAckCommand(c))
		assert.Equal(t, http.StatusNoContent, rec.Code)

		var updated models.AgentCommand
		require.NoError(t, f.handle.RW.First(&updated, "id = ?", cmd.ID).Error)
		assert.NotNil(t, updated.AckedAt, "acked_at must be set after ack")
	})

	t.Run("decommissioned_cluster_returns_410", func(t *testing.T) {
		t.Parallel()
		f, h := newAgentFixture(t)
		ctx := t.Context()

		cmd := models.AgentCommand{
			ClusterID:   f.cluster.ID,
			CommandType: "apply",
			Payload:     datatypes.JSON(`{}`),
			ExpiresAt:   time.Now().Add(24 * time.Hour),
		}
		require.NoError(t, f.handle.RW.WithContext(ctx).Create(&cmd).Error)
		require.NoError(t, f.handle.RW.WithContext(ctx).
			Model(&f.cluster).Update("state", models.ClusterDecommissioned).Error)

		e := echo.New()
		c, _ := ackCtx(e, f.cluster.ID, cmd.ID, `{}`)
		err := h.AgentAckCommand(c)

		var apiErr *apierror.Error
		require.True(t, errors.As(err, &apiErr))
		assert.Equal(t, http.StatusGone, apiErr.Status)

		var unchanged models.AgentCommand
		require.NoError(t, f.handle.RW.First(&unchanged, "id = ?", cmd.ID).Error)
		assert.Nil(t, unchanged.AckedAt, "acked_at must stay unset for a decommissioned cluster")
	})

	t.Run("unknown_command_returns_404", func(t *testing.T) {
		t.Parallel()
		f, h := newAgentFixture(t)
		e := echo.New()

		c, _ := ackCtx(e, f.cluster.ID, uuid.Must(uuid.NewV7()), "")
		err := h.AgentAckCommand(c)

		var apiErr *apierror.Error
		require.True(t, errors.As(err, &apiErr))
		assert.Equal(t, http.StatusNotFound, apiErr.Status)
	})

	t.Run("command_from_different_cluster_returns_404", func(t *testing.T) {
		t.Parallel()
		f, h := newAgentFixture(t)
		ctx := t.Context()

		otherCluster := seed.SeedCluster(t, f.handle, models.Cluster{
			Name:  "other-cluster",
			Scope: models.ClusterScopePublic,
			State: models.ClusterReady,
		})

		cmd := models.AgentCommand{
			ClusterID:   otherCluster.ID, // belongs to a different cluster
			CommandType: "apply",
			Payload:     datatypes.JSON(`{}`),
			ExpiresAt:   time.Now().Add(24 * time.Hour),
		}
		require.NoError(t, f.handle.RW.WithContext(ctx).Create(&cmd).Error)

		e := echo.New()
		// Agent for f.cluster tries to ack a command belonging to otherCluster.
		c, _ := ackCtx(e, f.cluster.ID, cmd.ID, "")
		err := h.AgentAckCommand(c)

		var apiErr *apierror.Error
		require.True(t, errors.As(err, &apiErr))
		assert.Equal(t, http.StatusNotFound, apiErr.Status)
	})

	t.Run("ack_with_error_still_returns_204", func(t *testing.T) {
		t.Parallel()
		f, h := newAgentFixture(t)
		ctx := t.Context()

		cmd := models.AgentCommand{
			ClusterID:   f.cluster.ID,
			CommandType: "apply",
			Payload:     datatypes.JSON(`{}`),
			ExpiresAt:   time.Now().Add(24 * time.Hour),
		}
		require.NoError(t, f.handle.RW.WithContext(ctx).Create(&cmd).Error)

		e := echo.New()
		c, rec := ackCtx(e, f.cluster.ID, cmd.ID, `{"error":"helm chart not found"}`)
		require.NoError(t, h.AgentAckCommand(c))
		assert.Equal(t, http.StatusNoContent, rec.Code)
	})
}
