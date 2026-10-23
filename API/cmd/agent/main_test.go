package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// argoRecorder is a fake Argo CD server that records each request's method
// and escaped path.
type argoRecorder struct {
	mu   sync.Mutex
	reqs []string
}

func (r *argoRecorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.reqs = append(r.reqs, req.Method+" "+req.URL.EscapedPath())
	r.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (r *argoRecorder) requests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.reqs...)
}

func newTestAgent(t *testing.T) (*agent, *argoRecorder) {
	t.Helper()
	rec := &argoRecorder{}
	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)
	return &agent{argo: newArgoClient(srv.URL, "token", srv.Client())}, rec
}

func command(t *testing.T, cmdType string, payload any) pendingCommand {
	t.Helper()
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	return pendingCommand{MsgID: "msg-1", CommandType: cmdType, Payload: b}
}

func TestProcessCommand_RejectsEmptyFields(t *testing.T) {
	cases := []struct {
		name    string
		cmdType string
		payload any
		wantErr string
	}{
		{"delete without app_name", "delete", deletePayload{}, "app_name is required"},
		{"rollback without app_name", "rollback", rollbackPayload{ToRevision: "abc123"}, "app_name is required"},
		{"rollback without to_revision", "rollback", rollbackPayload{AppName: "app-1"}, "to_revision is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, rec := newTestAgent(t)
			err := a.processCommand(context.Background(), command(t, tc.cmdType, tc.payload))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Empty(t, rec.requests(), "no request should reach Argo CD")
		})
	}
}

func TestProcessDelete_EscapesAppName(t *testing.T) {
	a, rec := newTestAgent(t)
	err := a.processCommand(context.Background(), command(t, "delete", deletePayload{AppName: "team/app"}))
	require.NoError(t, err)
	assert.Equal(t, []string{"DELETE /api/v1/applications/team%2Fapp"}, rec.requests())
}

func TestProcessRollback_EscapesAppName(t *testing.T) {
	a, rec := newTestAgent(t)
	err := a.processCommand(context.Background(), command(t, "rollback",
		rollbackPayload{AppName: "team/app", ToRevision: "abc123"}))
	require.NoError(t, err)
	assert.Equal(t, []string{
		"PATCH /api/v1/applications/team%2Fapp",
		"POST /api/v1/applications/team%2Fapp/sync",
	}, rec.requests())
}

func TestLoadConfig_HubAgentURL(t *testing.T) {
	setRequired := func(t *testing.T) {
		t.Helper()
		t.Setenv("SAIDKICK_CLUSTER_ID", "0190c3a4-0000-7000-8000-000000000001")
		t.Setenv("ARGOCD_SERVER_URL", "https://argocd.local")
		t.Setenv("ARGOCD_AUTH_TOKEN", "token")
	}

	t.Run("agent listener URL wins", func(t *testing.T) {
		setRequired(t)
		t.Setenv("SAIDKICK_HUB_URL", "https://appstore.example.com")
		t.Setenv("SAIDKICK_HUB_AGENT_URL", "https://agents.example.com:8443")
		cfg, err := loadConfig()
		require.NoError(t, err)
		assert.Equal(t, "https://agents.example.com:8443", cfg.HubURL)
	})

	t.Run("falls back to hub URL", func(t *testing.T) {
		setRequired(t)
		t.Setenv("SAIDKICK_HUB_URL", "https://appstore.example.com")
		t.Setenv("SAIDKICK_HUB_AGENT_URL", "")
		cfg, err := loadConfig()
		require.NoError(t, err)
		assert.Equal(t, "https://appstore.example.com", cfg.HubURL)
	})

	t.Run("one of them is required", func(t *testing.T) {
		setRequired(t)
		t.Setenv("SAIDKICK_HUB_URL", "")
		t.Setenv("SAIDKICK_HUB_AGENT_URL", "")
		_, err := loadConfig()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "SAIDKICK_HUB_AGENT_URL or SAIDKICK_HUB_URL is required")
	})
}
