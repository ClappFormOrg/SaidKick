// Command agent is the sAId-Kick cluster agent.
//
// It runs inside a customer cluster alongside Argo CD. On every heartbeat
// tick it calls the hub to pick up pending orchestration commands (apply /
// delete / rollback), executes them against the local Argo CD, and reports
// the resulting Argo CD status back to the hub.
//
// Usage:
//
//	saidkick-agent                # normal operation
//	saidkick-agent -version       # print version and exit
//
// Configuration is via environment variables:
//
//	SAIDKICK_HUB_URL            Hub base URL (required unless SAIDKICK_HUB_AGENT_URL is set)
//	SAIDKICK_HUB_AGENT_URL      Base URL of the hub's agent mTLS listener, e.g.
//	                            https://agents.appstore.gov.nl:8443. Agent traffic
//	                            goes here; falls back to SAIDKICK_HUB_URL when unset.
//	                            The hub serves agent routes only on that listener.
//	SAIDKICK_CLUSTER_ID         Cluster UUID (required)
//	ARGOCD_SERVER_URL           Argo CD server URL (required)
//	ARGOCD_AUTH_TOKEN           Argo CD auth token (required)
//	ARGOCD_NAMESPACE            Namespace for Argo CD Application CRs (default: argocd)
//	SAIDKICK_AGENT_CERT         Path to mTLS client cert PEM (optional; not needed in dev mode)
//	SAIDKICK_AGENT_KEY          Path to mTLS client key PEM (optional; not needed in dev mode)
//	SAIDKICK_HUB_CA             Path to the CA PEM that signed the hub agent listener's
//	                            serving certificate (optional; system roots when unset)
//	SAIDKICK_K8S_SECRET_NAME    Credentials Secret a renewed certificate is written to
//	                            (tls.crt, tls.key, ca.crt), so a restarted pod reuses it
//	SAIDKICK_CERT_RENEW_BEFORE  Renew the client certificate once less than this much
//	                            validity remains (default: 720h, 30 days)
//	SAIDKICK_DEV_MODE           Set to "true" to skip mTLS and send X-Agent-Cluster-ID header
//	SAIDKICK_HEARTBEAT_INTERVAL Heartbeat cadence (default: 30s)
//	SAIDKICK_STATUS_INTERVAL    Argo CD status poll cadence (default: 15s)
//	SAIDKICK_AGENT_VERSION      Version string reported in heartbeats (default: "0.1.0")
//	SAIDKICK_HEALTH_ADDR        Listen address for /healthz and /readyz (default: :8081)
//	SAIDKICK_METRICS_ADDR       Listen address for /metrics (default: :9090)
//
// /readyz reports ready once a hub heartbeat has succeeded within the last
// three heartbeat intervals.
//
// The client certificate is renewed in place: each heartbeat cycle the
// agent renews it when it nears expiry or when the hub's heartbeat response
// carries "renew": true, writes the new material to the credentials Secret
// and switches to it without a restart.
//
// /metrics is served on its own listener, separate from the health probes,
// and carries no authentication: bind it to a port that only the pod network
// reaches and never route it through an ingress.
package main

import (
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/agentcreds"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/orchestrator"
	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
	"github.com/google/uuid"
)

const agentVersion = "0.1.0"

// ─── Config ───────────────────────────────────────────────────────────────────

// agentConfig holds all runtime configuration for the agent process.
type agentConfig struct {
	// HubURL is the base URL for agent traffic: SAIDKICK_HUB_AGENT_URL, or
	// SAIDKICK_HUB_URL when that is unset.
	HubURL            string
	ClusterID         string
	ArgoServerURL     string
	ArgoAuthToken     string
	ArgoNamespace     string
	CertFile          string
	KeyFile           string
	CAFile            string
	K8sSecretName     string
	CertRenewBefore   time.Duration
	DevMode           bool
	HeartbeatInterval time.Duration
	StatusInterval    time.Duration
	AgentVersionStr   string
	HealthAddr        string
	MetricsAddr       string
}

// readyHeartbeatIntervals is how many heartbeat intervals may pass without a
// successful heartbeat before /readyz reports not ready.
const readyHeartbeatIntervals = 3

func loadConfig() (agentConfig, error) {
	cfg := agentConfig{
		HubURL:            cmp.Or(os.Getenv("SAIDKICK_HUB_AGENT_URL"), os.Getenv("SAIDKICK_HUB_URL")),
		ClusterID:         os.Getenv("SAIDKICK_CLUSTER_ID"),
		ArgoServerURL:     os.Getenv("ARGOCD_SERVER_URL"),
		ArgoAuthToken:     os.Getenv("ARGOCD_AUTH_TOKEN"),
		ArgoNamespace:     envOr("ARGOCD_NAMESPACE", "argocd"),
		CertFile:          os.Getenv("SAIDKICK_AGENT_CERT"),
		KeyFile:           os.Getenv("SAIDKICK_AGENT_KEY"),
		CAFile:            os.Getenv("SAIDKICK_HUB_CA"),
		K8sSecretName:     os.Getenv("SAIDKICK_K8S_SECRET_NAME"),
		CertRenewBefore:   parseDuration("SAIDKICK_CERT_RENEW_BEFORE", defaultCertRenewBefore),
		DevMode:           os.Getenv("SAIDKICK_DEV_MODE") == "true",
		HeartbeatInterval: parseDuration("SAIDKICK_HEARTBEAT_INTERVAL", 30*time.Second),
		StatusInterval:    parseDuration("SAIDKICK_STATUS_INTERVAL", 15*time.Second),
		AgentVersionStr:   envOr("SAIDKICK_AGENT_VERSION", agentVersion),
		HealthAddr:        envOr("SAIDKICK_HEALTH_ADDR", ":8081"),
		MetricsAddr:       envOr("SAIDKICK_METRICS_ADDR", ":9090"),
	}

	var errs []string
	if cfg.HubURL == "" {
		errs = append(errs, "SAIDKICK_HUB_AGENT_URL or SAIDKICK_HUB_URL is required")
	}
	if cfg.ClusterID == "" {
		errs = append(errs, "SAIDKICK_CLUSTER_ID is required")
	}
	if cfg.ArgoServerURL == "" {
		errs = append(errs, "ARGOCD_SERVER_URL is required")
	}
	if cfg.ArgoAuthToken == "" {
		errs = append(errs, "ARGOCD_AUTH_TOKEN is required")
	}
	if len(errs) > 0 {
		return agentConfig{}, fmt.Errorf("config: %s", strings.Join(errs, "; "))
	}
	return cfg, nil
}

// ─── Agent ────────────────────────────────────────────────────────────────────

// agent bundles the runtime dependencies shared across the heartbeat and status
// loops, eliminating repeated parameter lists on every internal function.
type agent struct {
	cfg    agentConfig
	hub    *hubClient
	argo   *argoClient
	health *healthState
	// certs renews the mTLS client certificate; nil in dev mode or when no
	// certificate is configured.
	certs *certManager
}

func newAgent(cfg agentConfig) (*agent, error) {
	certs, err := loadCertManager(cfg)
	if err != nil {
		return nil, err
	}
	hubHTTPClient, err := buildHubHTTPClient(cfg, certs)
	if err != nil {
		return nil, fmt.Errorf("build hub HTTP client: %w", err)
	}
	hub := newHubClient(cfg.HubURL, cfg.ClusterID, hubHTTPClient, cfg.DevMode)
	if certs != nil {
		certs.hub = hub
	}
	return &agent{
		cfg:    cfg,
		hub:    hub,
		argo:   newArgoClient(cfg.ArgoServerURL, cfg.ArgoAuthToken, nil),
		health: newHealthState(readyHeartbeatIntervals * cfg.HeartbeatInterval),
		certs:  certs,
	}, nil
}

// loadCertManager loads the client certificate agent-init issued and wraps
// it in a certManager. It returns nil in dev mode and when no certificate
// is configured.
func loadCertManager(cfg agentConfig) (*certManager, error) {
	if cfg.DevMode || cfg.CertFile == "" || cfg.KeyFile == "" {
		return nil, nil
	}
	pair, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load agent cert/key: %w", err)
	}
	if pair.Leaf == nil {
		if pair.Leaf, err = x509.ParseCertificate(pair.Certificate[0]); err != nil {
			return nil, fmt.Errorf("parse agent cert: %w", err)
		}
	}
	// The hub client is wired in by newAgent once it exists.
	return newCertManager(cfg, pair, nil, agentcreds.SecretClient{}), nil
}

// ─── Entry points ─────────────────────────────────────────────────────────────

func main() {
	printVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *printVersion {
		fmt.Printf("saidkick-agent %s\n", agentVersion)
		return
	}

	if err := run(); err != nil {
		log.Fatalf("fatal: %v", err)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	a, err := newAgent(cfg)
	if err != nil {
		return err
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if cfg.DevMode {
		slog.Warn("dev mode enabled — mtls disabled, using X-Agent-Cluster-ID header")
	}
	slog.Info("saidkick-agent starting",
		"version", cfg.AgentVersionStr, "cluster", cfg.ClusterID, "hub", cfg.HubURL, "argocd", cfg.ArgoServerURL)

	healthLn, err := net.Listen("tcp", cfg.HealthAddr)
	if err != nil {
		return fmt.Errorf("health listener: %w", err)
	}
	metricsLn, err := net.Listen("tcp", cfg.MetricsAddr)
	if err != nil {
		_ = healthLn.Close()
		return fmt.Errorf("metrics listener: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	var serverWG sync.WaitGroup
	defer func() {
		cancel()
		serverWG.Wait()
	}()

	errCh := make(chan error, 4)
	serverWG.Go(func() {
		if serveErr := serveHTTP(ctx, healthLn, a.health.handler()); serveErr != nil {
			errCh <- fmt.Errorf("health server: %w", serveErr)
		}
	})
	serverWG.Go(func() {
		if serveErr := serveHTTP(ctx, metricsLn, metricsHandler()); serveErr != nil {
			errCh <- fmt.Errorf("metrics server: %w", serveErr)
		}
	})
	go func() {
		defer func() {
			if r := recover(); r != nil {
				errCh <- fmt.Errorf("heartbeat loop panicked: %v", r)
			}
		}()
		errCh <- a.heartbeatLoop(ctx)
	}()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				errCh <- fmt.Errorf("status loop panicked: %v", r)
			}
		}()
		errCh <- a.statusLoop(ctx)
	}()

	select {
	case <-ctx.Done():
		slog.Info("agent: shutting down")
		return nil
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	}
}

// ─── Heartbeat loop ───────────────────────────────────────────────────────────

func (a *agent) heartbeatLoop(ctx context.Context) error {
	ticker := time.NewTicker(a.cfg.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := a.heartbeatTick(ctx); err != nil {
				slog.Warn("heartbeat error", "error", err)
				if strings.Contains(err.Error(), "decommissioned") {
					return err
				}
			}
		}
	}
}

func (a *agent) heartbeatTick(ctx context.Context) error {
	resp, err := a.hub.Heartbeat(ctx, a.cfg.AgentVersionStr)
	if a.certs != nil {
		// Last in the cycle, after the acks: a renewal swaps the certificate
		// the next request presents.
		defer a.certs.maybeRenew(ctx, resp.Renew)
	}
	if err != nil {
		metrics.AgentHeartbeatsTotal(metrics.OutcomeFailure).Inc()
		return err
	}
	metrics.AgentHeartbeatsTotal(metrics.OutcomeSuccess).Inc()
	a.health.markHeartbeat()

	for _, cmd := range resp.PendingCommands {
		cmdErr := a.processCommand(ctx, cmd)
		if cmdErr != nil {
			slog.Error("command failed", "msg_id", cmd.MsgID, "type", cmd.CommandType, "error", cmdErr)
		}
		// Always ack — hub records failures via the non-empty error field.
		errMsg := ""
		if cmdErr != nil {
			errMsg = cmdErr.Error()
		}
		if ackErr := a.hub.AckCommand(ctx, cmd.MsgID, errMsg); ackErr != nil {
			slog.Error("ack command failed", "msg_id", cmd.MsgID, "error", ackErr)
		}
	}
	return nil
}

// ─── Command processing ───────────────────────────────────────────────────────

// applyPayload is the JSON body inside an "apply" command.
// Field names must match agentbrokered.applyPayload exactly.
type applyPayload struct {
	DeploymentID  string          `json:"deployment_id"`
	AppSlug       string          `json:"app_slug"`
	ClusterID     string          `json:"cluster_id"`
	Namespace     string          `json:"namespace"`
	Project       string          `json:"project"`
	GitHubRepoURL string          `json:"github_repo_url"`
	GitSHA        string          `json:"git_sha"`
	ChartPath     string          `json:"chart_path"`
	Values        json.RawMessage `json:"values"`
	SecretRefs    []struct {
		FieldPath string `json:"field_path"`
		VaultPath string `json:"vault_path"`
	} `json:"secret_refs"`
}

// deletePayload is the JSON body inside a "delete" command.
type deletePayload struct {
	AppName string `json:"app_name"`
}

// rollbackPayload is the JSON body inside a "rollback" command.
type rollbackPayload struct {
	AppName    string `json:"app_name"`
	ToRevision string `json:"to_revision"`
}

// processCommand executes cmd, counting it in agent_commands_processed_total
// and timing it in agent_command_duration_seconds.
func (a *agent) processCommand(ctx context.Context, cmd pendingCommand) error {
	label := metrics.AgentCommandLabel(cmd.CommandType)
	start := time.Now()
	err := a.dispatchCommand(ctx, cmd)
	metrics.AgentCommandDuration(label).UpdateDuration(start)
	outcome := metrics.OutcomeSuccess
	if err != nil {
		outcome = metrics.OutcomeFailure
	}
	metrics.AgentCommandsProcessedTotal(label, outcome).Inc()
	return err
}

func (a *agent) dispatchCommand(ctx context.Context, cmd pendingCommand) error {
	switch cmd.CommandType {
	case "apply":
		return a.processApply(ctx, cmd)
	case "delete":
		return a.processDelete(ctx, cmd)
	case "rollback":
		return a.processRollback(ctx, cmd)
	default:
		return fmt.Errorf("unknown command type %q", cmd.CommandType)
	}
}

func (a *agent) processApply(ctx context.Context, cmd pendingCommand) error {
	var p applyPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return fmt.Errorf("decode apply payload: %w", err)
	}

	depID, err := uuid.Parse(p.DeploymentID)
	if err != nil {
		return fmt.Errorf("invalid deployment_id: %w", err)
	}
	clusterID, err := uuid.Parse(p.ClusterID)
	if err != nil {
		return fmt.Errorf("invalid cluster_id: %w", err)
	}

	refs := make([]orchestrator.SecretRef, len(p.SecretRefs))
	for i, r := range p.SecretRefs {
		refs[i] = orchestrator.SecretRef{FieldPath: r.FieldPath, VaultPath: r.VaultPath}
	}

	spec := orchestrator.ApplicationSpec{
		DeploymentID:  depID,
		ClusterID:     clusterID,
		AppSlug:       p.AppSlug,
		Project:       p.Project,
		Namespace:     p.Namespace,
		GitHubRepoURL: p.GitHubRepoURL,
		GitSHA:        p.GitSHA,
		ChartPath:     p.ChartPath,
		Values:        p.Values,
		SecretRefs:    refs,
	}

	// Derive the deterministic Argo CD app name (mirrors agentbrokered deployer).
	hex := strings.ReplaceAll(depID.String(), "-", "")
	appName := "app-" + hex[:12]

	if err := a.argo.Apply(ctx, spec, appName, a.cfg.ArgoNamespace); err != nil {
		return fmt.Errorf("argo apply: %w", err)
	}

	// Report the transition to "applying", carrying the Argo app name so the
	// hub can poll it via reconciler/status calls.
	_, reportErr := a.hub.ReportStates(ctx, []stateUpdate{{
		DeploymentID: p.DeploymentID,
		State:        "applying",
		ArgoAppName:  appName,
	}})
	return reportErr
}

func (a *agent) processDelete(ctx context.Context, cmd pendingCommand) error {
	var p deletePayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return fmt.Errorf("decode delete payload: %w", err)
	}
	if p.AppName == "" {
		return errors.New("delete payload: app_name is required")
	}
	return a.argo.Delete(ctx, p.AppName)
}

func (a *agent) processRollback(ctx context.Context, cmd pendingCommand) error {
	var p rollbackPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return fmt.Errorf("decode rollback payload: %w", err)
	}
	if p.AppName == "" {
		return errors.New("rollback payload: app_name is required")
	}
	if p.ToRevision == "" {
		return errors.New("rollback payload: to_revision is required")
	}
	return a.argo.Rollback(ctx, p.AppName, p.ToRevision)
}

// ─── Status loop ──────────────────────────────────────────────────────────────

func (a *agent) statusLoop(ctx context.Context) error {
	ticker := time.NewTicker(a.cfg.StatusInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := a.statusTick(ctx); err != nil {
				slog.Warn("status poll error", "error", err)
			}
		}
	}
}

func (a *agent) statusTick(ctx context.Context) error {
	apps, err := a.argo.ListSaidKickApps(ctx)
	if err != nil {
		return fmt.Errorf("list apps: %w", err)
	}

	var updates []stateUpdate
	for _, app := range apps.Items {
		depID, ok := app.Metadata.Annotations["saidkick.appstore.gov.nl/deployment-id"]
		if !ok || depID == "" {
			continue
		}

		state := phaseToState(mapToPhase(app))
		if state == "" {
			continue
		}

		updates = append(updates, stateUpdate{
			DeploymentID: depID,
			State:        state,
			Message:      app.Status.Health.Message,
			ArgoAppName:  app.Metadata.Name,
		})
	}

	if len(updates) == 0 {
		return nil
	}

	results, err := a.hub.ReportStates(ctx, updates)
	if err != nil {
		return fmt.Errorf("report states: %w", err)
	}
	for _, r := range results {
		if !r.Accepted && r.Reason != "" {
			slog.Warn("status poll: hub rejected state", "deployment_id", r.DeploymentID, "reason", r.Reason)
		}
	}
	return nil
}

// phaseToState converts an orchestrator.Phase to the deployment state string
// the hub's /deployment-states endpoint accepts. Returns "" for phases the
// agent should not report (e.g. Pending, which the hub manages itself).
// PhaseFailed maps to "failed_orchestrator" (the hub's DB enum value).
func phaseToState(p orchestrator.Phase) string {
	switch p {
	case orchestrator.PhaseApplying:
		return "applying"
	case orchestrator.PhaseSecretSync:
		return "secret_sync"
	case orchestrator.PhaseReconciling:
		return "reconciling"
	case orchestrator.PhaseReady:
		return "ready"
	case orchestrator.PhaseDegraded:
		return "degraded"
	case orchestrator.PhaseFailed:
		return "failed_orchestrator"
	default:
		return ""
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// buildHubHTTPClient creates the HTTP client for hub communication.
// In production it presents certs' current client certificate on every TLS
// handshake; dev mode skips TLS.
//
// With a client certificate, keep-alives are off: every request opens a
// connection and so presents the certificate current at that moment. A
// pooled connection would keep presenting the certificate it was opened
// with, which the hub rejects once a renewed one has been activated.
func buildHubHTTPClient(cfg agentConfig, certs *certManager) (*http.Client, error) {
	if cfg.DevMode {
		return &http.Client{Timeout: defaultHTTPTimeout}, nil
	}

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13} //nolint:gosec // G402: TLS 1.3 minimum is intentionally strict, not a misconfiguration
	transport := &http.Transport{TLSClientConfig: tlsCfg}

	if certs != nil {
		tlsCfg.GetClientCertificate = certs.getClientCertificate
		transport.DisableKeepAlives = true
	}

	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read hub CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("parse hub CA: no valid certificates found")
		}
		tlsCfg.RootCAs = pool
	}

	return &http.Client{
		Timeout:   defaultHTTPTimeout,
		Transport: transport,
	}, nil
}

func envOr(key, def string) string {
	return cmp.Or(os.Getenv(key), def)
}

func parseDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}
