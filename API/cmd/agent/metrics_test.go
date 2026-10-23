package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
)

// newHeartbeatAgent returns an agent whose hub answers every request with
// status.
func newHeartbeatAgent(t *testing.T, status int) *agent {
	t.Helper()
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	t.Cleanup(hub.Close)
	return &agent{
		cfg:    agentConfig{AgentVersionStr: "test"},
		hub:    newHubClient(hub.URL, "0190c3a4-0000-7000-8000-000000000001", hub.Client(), true),
		health: newHealthState(time.Minute),
	}
}

func TestHeartbeatTick_CountsOutcome(t *testing.T) {
	success := metrics.AgentHeartbeatsTotal(metrics.OutcomeSuccess)
	failure := metrics.AgentHeartbeatsTotal(metrics.OutcomeFailure)

	t.Run("hub accepts", func(t *testing.T) {
		s0, f0, r0 := success.Get(), failure.Get(), testutil.HistogramCount(metrics.AgentHubRequestDuration)
		a := newHeartbeatAgent(t, http.StatusNoContent)
		require.NoError(t, a.heartbeatTick(context.Background()))
		assert.Equal(t, s0+1, success.Get())
		assert.Equal(t, f0, failure.Get())
		assert.Equal(t, r0+1, testutil.HistogramCount(metrics.AgentHubRequestDuration), "hub round trip observed")
		assert.True(t, a.health.ready())
	})

	t.Run("hub fails, agent keeps going", func(t *testing.T) {
		s0, f0, r0 := success.Get(), failure.Get(), testutil.HistogramCount(metrics.AgentHubRequestDuration)
		a := newHeartbeatAgent(t, http.StatusInternalServerError)
		require.Error(t, a.heartbeatTick(context.Background()))
		assert.Equal(t, s0, success.Get())
		assert.Equal(t, f0+1, failure.Get())
		assert.Equal(t, r0+1, testutil.HistogramCount(metrics.AgentHubRequestDuration), "failed round trip observed")
		assert.False(t, a.health.ready())
	})

	t.Run("hub unreachable", func(t *testing.T) {
		f0, r0 := failure.Get(), testutil.HistogramCount(metrics.AgentHubRequestDuration)
		a := newHeartbeatAgent(t, http.StatusNoContent)
		a.hub.baseURL = "http://127.0.0.1:1"
		require.Error(t, a.heartbeatTick(context.Background()))
		assert.Equal(t, f0+1, failure.Get())
		assert.Equal(t, r0+1, testutil.HistogramCount(metrics.AgentHubRequestDuration))
	})
}

func TestProcessCommand_CountsAndTimes(t *testing.T) {
	cases := []struct {
		name    string
		cmdType string
		payload any
		label   string
		outcome string
	}{
		{"delete succeeds", "delete", deletePayload{AppName: "app-1"}, metrics.AgentCommandDelete, metrics.OutcomeSuccess},
		{"rollback succeeds", "rollback", rollbackPayload{AppName: "app-1", ToRevision: "abc123"}, metrics.AgentCommandRollback, metrics.OutcomeSuccess},
		{"rollback rejected", "rollback", rollbackPayload{AppName: "app-1"}, metrics.AgentCommandRollback, metrics.OutcomeFailure},
		{"apply with a bad payload", "apply", applyPayload{DeploymentID: "not-a-uuid"}, metrics.AgentCommandApply, metrics.OutcomeFailure},
		{"unknown type is not a label value", "0190c3a4-0000-7000-8000-000000000001", deletePayload{}, metrics.AgentCommandUnknown, metrics.OutcomeFailure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			counter := metrics.AgentCommandsProcessedTotal(tc.label, tc.outcome)
			hist := metrics.AgentCommandDuration(tc.label)
			c0, h0 := counter.Get(), testutil.HistogramCount(hist)

			a, _ := newTestAgent(t)
			err := a.processCommand(context.Background(), command(t, tc.cmdType, tc.payload))
			if tc.outcome == metrics.OutcomeSuccess {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			assert.Equal(t, c0+1, counter.Get())
			assert.Equal(t, h0+1, testutil.HistogramCount(hist))
		})
	}
	testutil.RequireNoIdentifyingLabels(t)
}

func TestAgentCommandLabel(t *testing.T) {
	for _, c := range []string{"apply", "delete", "rollback"} {
		assert.Equal(t, c, metrics.AgentCommandLabel(c))
	}
	for _, c := range []string{"", "Apply", "sync", "unknown", "x\"y"} {
		assert.Equal(t, metrics.AgentCommandUnknown, metrics.AgentCommandLabel(c))
	}
}

func TestMetricsHandler(t *testing.T) {
	h := metricsHandler()

	code, body := probe(t, h, "/metrics")
	require.Equal(t, http.StatusOK, code)
	for _, want := range []string{
		"agent_up 1\n",
		`agent_heartbeats_total{outcome="success"}`,
		`agent_heartbeats_total{outcome="failure"}`,
		`agent_commands_processed_total{command="apply",outcome="success"}`,
		"agent_hub_request_duration_seconds",
		"go_goroutines ",
		"process_",
	} {
		assert.Contains(t, body, want)
	}
	// The hub's series live in the default set and must not leak here.
	for _, unwanted := range []string{"agent_heartbeat_age_seconds", "audit_writes_total", "http_requests_total", "orchestrator_calls_total"} {
		assert.NotContains(t, body, unwanted)
	}
	for line := range strings.Lines(body) {
		assert.NotContains(t, line, "cluster", "no series may carry a cluster label")
	}

	code, _ = probe(t, h, "/healthz")
	assert.Equal(t, http.StatusNotFound, code, "the metrics listener serves /metrics only")

	health, _ := newTestHealth(time.Minute)
	code, _ = probe(t, health.handler(), "/metrics")
	assert.Equal(t, http.StatusNotFound, code, "the health listener does not serve /metrics")
}
