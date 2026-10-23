package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
)

// hubClient talks to the sAId-Kick hub API on behalf of the agent.
// In production it uses mTLS (clientCert wired into the Transport); in dev
// mode it sends X-Agent-Cluster-ID instead.
type hubClient struct {
	baseURL   string
	clusterID string
	client    *http.Client
	isDevMode bool
}

func newHubClient(baseURL, clusterID string, client *http.Client, isDevMode bool) *hubClient {
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return &hubClient{
		baseURL:   strings.TrimRight(baseURL, "/"),
		clusterID: clusterID,
		client:    client,
		isDevMode: isDevMode,
	}
}

// ─── Heartbeat ────────────────────────────────────────────────────────────────

type heartbeatRequest struct {
	AgentVersion string `json:"agent_version"`
}

// heartbeatResponse is the 200 body of a heartbeat. Renew is true while an
// admin-requested credential rotation is outstanding.
type heartbeatResponse struct {
	PendingCommands []pendingCommand `json:"pending_commands"`
	Renew           bool             `json:"renew"`
}

type pendingCommand struct {
	MsgID       string          `json:"msg_id"`
	CommandType string          `json:"type"`
	Payload     json.RawMessage `json:"payload"`
}

// Heartbeat sends a heartbeat to the hub and returns any pending commands
// and whether the hub asks the agent to renew its certificate. A 204
// response means neither.
func (h *hubClient) Heartbeat(ctx context.Context, agentVersion string) (heartbeatResponse, error) {
	body, _ := json.Marshal(heartbeatRequest{AgentVersion: agentVersion})
	req, err := h.newRequest(ctx, http.MethodPost,
		fmt.Sprintf("/api/v1/clusters/%s/heartbeat", h.clusterID), body)
	if err != nil {
		return heartbeatResponse{}, err
	}

	resp, err := h.do(req)
	if err != nil {
		return heartbeatResponse{}, fmt.Errorf("hub heartbeat: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusNoContent:
		return heartbeatResponse{}, nil
	case http.StatusOK:
		var hbr heartbeatResponse
		if err := readJSON(resp.Body, &hbr); err != nil {
			return heartbeatResponse{}, fmt.Errorf("hub heartbeat: decode response: %w", err)
		}
		return hbr, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return heartbeatResponse{}, fmt.Errorf("hub heartbeat: auth rejected (%d)", resp.StatusCode)
	case http.StatusGone:
		return heartbeatResponse{}, errors.New("hub heartbeat: cluster decommissioned")
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return heartbeatResponse{}, fmt.Errorf("hub heartbeat: unexpected %d: %s", resp.StatusCode, string(b))
	}
}

// ─── Certificate renewal ──────────────────────────────────────────────────────

// renewResponse is the 200 body of POST /clusters/{id}/agent/renew.
type renewResponse struct {
	Certificate   string `json:"certificate"`
	CACertificate string `json:"ca_certificate"`
	Serial        string `json:"serial"`
	NotAfter      string `json:"not_after"`
}

// RenewCertificate asks the hub to sign csrPEM. The request goes out over
// the current client certificate; the hub records the new certificate as
// pending until the agent first presents it.
func (h *hubClient) RenewCertificate(ctx context.Context, csrPEM string) (renewResponse, error) {
	body, _ := json.Marshal(map[string]string{"csr": csrPEM})
	req, err := h.newRequest(ctx, http.MethodPost,
		fmt.Sprintf("/api/v1/clusters/%s/agent/renew", h.clusterID), body)
	if err != nil {
		return renewResponse{}, err
	}

	resp, err := h.do(req)
	if err != nil {
		return renewResponse{}, fmt.Errorf("hub renew: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return renewResponse{}, fmt.Errorf("hub renew: unexpected %d: %s", resp.StatusCode, string(b))
	}
	var out renewResponse
	if err := readJSON(resp.Body, &out); err != nil {
		return renewResponse{}, fmt.Errorf("hub renew: decode response: %w", err)
	}
	if out.Certificate == "" {
		return renewResponse{}, errors.New("hub renew: response has no certificate")
	}
	return out, nil
}

// ─── Deployment state reports ─────────────────────────────────────────────────

type stateUpdate struct {
	DeploymentID string `json:"deployment_id"`
	State        string `json:"state"`
	Message      string `json:"message,omitempty"`
	ArgoAppName  string `json:"argo_app_name,omitempty"`
}

type stateUpdateResult struct {
	DeploymentID string `json:"deployment_id"`
	Accepted     bool   `json:"accepted"`
	Reason       string `json:"reason,omitempty"`
}

// ReportStates posts one or more deployment state changes to the hub.
func (h *hubClient) ReportStates(ctx context.Context, updates []stateUpdate) ([]stateUpdateResult, error) {
	body, _ := json.Marshal(map[string]any{"updates": updates})
	req, err := h.newRequest(ctx, http.MethodPost,
		fmt.Sprintf("/api/v1/clusters/%s/deployment-states", h.clusterID), body)
	if err != nil {
		return nil, err
	}

	resp, err := h.do(req)
	if err != nil {
		return nil, fmt.Errorf("hub report-states: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("hub report-states: unexpected %d: %s", resp.StatusCode, string(b))
	}

	var out struct {
		Results []stateUpdateResult `json:"results"`
	}
	if err := readJSON(resp.Body, &out); err != nil {
		return nil, fmt.Errorf("hub report-states: decode response: %w", err)
	}
	return out.Results, nil
}

// ─── Command acknowledgement ──────────────────────────────────────────────────

// AckCommand acknowledges a command received in a heartbeat response.
// errMsg should be empty on success; non-empty signals a processing failure.
func (h *hubClient) AckCommand(ctx context.Context, cmdID, errMsg string) error {
	body, _ := json.Marshal(map[string]any{"error": errMsg})
	req, err := h.newRequest(ctx, http.MethodPost,
		fmt.Sprintf("/api/v1/clusters/%s/commands/%s/ack", h.clusterID, cmdID), body)
	if err != nil {
		return err
	}

	resp, err := h.do(req)
	if err != nil {
		return fmt.Errorf("hub ack-command: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return fmt.Errorf("hub ack-command: unexpected %d: %s", resp.StatusCode, string(b))
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// do sends req and records the round trip in
// agent_hub_request_duration_seconds, failures included.
func (h *hubClient) do(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := h.client.Do(req)
	metrics.AgentHubRequestDuration.UpdateDuration(start)
	return resp, err
}

func (h *hubClient) newRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.baseURL+path, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// Dev mode: send cluster ID via header instead of mTLS cert.
	if h.isDevMode {
		req.Header.Set("X-Agent-Cluster-ID", h.clusterID)
	}
	return req, nil
}

func readJSON(r io.Reader, v any) error {
	b, err := io.ReadAll(io.LimitReader(r, 10<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
