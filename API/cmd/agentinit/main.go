// Command agent-init performs the one-time cluster handshake with the sAId-Kick
// hub and writes the issued mTLS certificate material to a shared volume.
//
// Run it as a Kubernetes init container before the main agent container.
// The init container is idempotent: if a non-expired cert already exists in
// SAIDKICK_CERT_DIR, or in the Kubernetes Secret named by
// SAIDKICK_K8S_SECRET_NAME, it exits 0 without contacting the hub. The
// Secret check is what a restarted pod relies on: its SAIDKICK_CERT_DIR is a
// fresh emptyDir, and the hub accepts each join token only once.
//
// Configuration (env vars):
//
//	SAIDKICK_HUB_URL          Hub base URL, e.g. https://hub.appstore.gov.nl (required)
//	SAIDKICK_CLUSTER_ID       Cluster UUID (required)
//	SAIDKICK_JOIN_TOKEN       One-time join-token JWT from CreateCluster (required on first boot)
//	SAIDKICK_CERT_DIR         Directory to write cert files; default /tmp (init container temp space)
//	SAIDKICK_AGENT_VERSION    Version string to report; default 0.1.0
//	SAIDKICK_K8S_SECRET_NAME  Kubernetes Secret to patch with cert material (optional).
//	                           When set the init container patches tls.crt / tls.key / ca.crt
//	                           into the named Secret using the in-cluster API so the agent
//	                           deployment (which mounts that Secret) picks them up automatically.
//
// Output files (all PEM, written atomically via temp-file rename):
//
//	$SAIDKICK_CERT_DIR/client.crt — signed mTLS client certificate
//	$SAIDKICK_CERT_DIR/client.key — RSA-2048 private key
//	$SAIDKICK_CERT_DIR/ca.crt     — hub CA certificate
package main

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/agentcreds"
)

const (
	defaultCertDir      = "/certs"
	defaultAgentVersion = "0.1.0"
	// Renew if the existing cert expires within this window.
	renewBefore = 24 * time.Hour
	httpTimeout = 30 * time.Second
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if err := run(); err != nil {
		slog.Error("agent-init failed", "error", err)
		os.Exit(1)
	}
	slog.Info("agent-init: certificate material ready")
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	certFile := filepath.Join(cfg.CertDir, "client.crt")

	// Idempotency: if a valid, non-expiring cert already exists, skip the handshake.
	if certOK(certFile) {
		slog.Info("agent-init: existing cert is valid, skipping handshake")
		return nil
	}

	ctx := context.Background()

	// A restarted pod starts with an empty cert dir, but the credentials
	// Secret still holds the certificate from the first handshake.
	secrets := agentcreds.SecretClient{}
	if cfg.K8sSecretName != "" {
		stored, err := secrets.ReadCredentials(ctx, cfg.K8sSecretName)
		creds := credentials{cert: stored.Cert, key: stored.Key, ca: stored.CA}
		switch {
		case err != nil:
			slog.Warn("agent-init: cannot read credentials Secret, handshaking", "secret", cfg.K8sSecretName, "error", err)
		case creds.reusable(time.Now()):
			if err := creds.writeTo(cfg.CertDir); err != nil {
				return err
			}
			slog.Info("agent-init: credentials Secret holds a valid cert, skipping handshake", "secret", cfg.K8sSecretName)
			return nil
		}
	}

	slog.Info("agent-init: starting handshake", "hub", cfg.HubURL, "cluster", cfg.ClusterID)

	// Fresh RSA 2048 key and PKCS#10 CSR, the same way the agent renews.
	keyPEM, csrPEM, err := agentcreds.NewKeyAndCSR(cfg.ClusterID)
	if err != nil {
		return err
	}

	// Call the hub handshake endpoint.
	resp, err := callHandshake(ctx, cfg, csrPEM)
	if err != nil {
		return fmt.Errorf("handshake: %w", err)
	}

	// Write all three files atomically.
	issued := credentials{cert: []byte(resp.ClientCertPEM), key: keyPEM, ca: []byte(resp.CAPEM)}
	if err := issued.writeTo(cfg.CertDir); err != nil {
		return err
	}

	// Optionally patch the Kubernetes Secret so the agent Deployment picks up
	// the cert material without a manual operator step.
	if cfg.K8sSecretName != "" {
		patch := agentcreds.Credentials{Cert: issued.cert, Key: issued.key, CA: issued.ca}
		if err := secrets.PatchCredentials(ctx, cfg.K8sSecretName, patch); err != nil {
			return fmt.Errorf("patch k8s secret: %w", err)
		}
		slog.Info("agent-init: cert material written to Kubernetes Secret", "secret", cfg.K8sSecretName)
	}

	slog.Info("agent-init: handshake complete", "cert_dir", cfg.CertDir)
	return nil
}

// ─── Config ───────────────────────────────────────────────────────────────────

type config struct {
	HubURL        string
	ClusterID     string
	JoinToken     string
	CertDir       string
	AgentVersion  string
	K8sSecretName string // optional: patch this Secret with the cert material
}

func loadConfig() (config, error) {
	cfg := config{
		HubURL:        os.Getenv("SAIDKICK_HUB_URL"),
		ClusterID:     os.Getenv("SAIDKICK_CLUSTER_ID"),
		JoinToken:     os.Getenv("SAIDKICK_JOIN_TOKEN"),
		K8sSecretName: os.Getenv("SAIDKICK_K8S_SECRET_NAME"),
		CertDir:       envOr("SAIDKICK_CERT_DIR", defaultCertDir),
		AgentVersion:  envOr("SAIDKICK_AGENT_VERSION", defaultAgentVersion),
	}

	var errs []string
	if cfg.HubURL == "" {
		errs = append(errs, "SAIDKICK_HUB_URL is required")
	}
	if cfg.ClusterID == "" {
		errs = append(errs, "SAIDKICK_CLUSTER_ID is required")
	}
	// JoinToken is only required when no valid cert exists yet; we check later.

	if err := os.MkdirAll(cfg.CertDir, 0o700); err != nil {
		errs = append(errs, fmt.Sprintf("cannot create cert dir %q: %v", cfg.CertDir, err))
	}

	if len(errs) > 0 {
		return config{}, fmt.Errorf("config: %v", errs)
	}
	return cfg, nil
}

// ─── Idempotency check ────────────────────────────────────────────────────────

// certOK returns true when the file at path contains a valid PEM certificate
// that does not expire within the renewBefore window.
func certOK(path string) bool {
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}
	return time.Until(cert.NotAfter) > renewBefore
}

// credentials is the certificate material agent-init writes: the client
// certificate, its private key and the hub CA, all PEM.
type credentials struct {
	cert, key, ca []byte
}

// reusable reports whether c holds a client certificate and matching key
// that are still valid at now and do not expire within renewBefore.
func (c credentials) reusable(now time.Time) bool {
	if len(c.ca) == 0 {
		return false
	}
	pair, err := tls.X509KeyPair(c.cert, c.key)
	if err != nil {
		return false
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return false
	}
	return !now.Before(leaf.NotBefore) && leaf.NotAfter.Sub(now) > renewBefore
}

// writeTo writes the material into dir as client.crt, client.key and ca.crt.
func (c credentials) writeTo(dir string) error {
	for name, data := range map[string][]byte{"client.crt": c.cert, "client.key": c.key, "ca.crt": c.ca} {
		if err := writeFile(filepath.Join(dir, name), data); err != nil {
			return err
		}
	}
	return nil
}

// ─── Handshake call ───────────────────────────────────────────────────────────

type handshakeResponse struct {
	ClientCertPEM string `json:"client_cert_pem"`
	CAPEM         string `json:"ca_pem"`
	HubEndpoint   string `json:"hub_endpoint"`
}

func callHandshake(ctx context.Context, cfg config, csrPEM string) (*handshakeResponse, error) {
	if cfg.JoinToken == "" {
		return nil, fmt.Errorf("SAIDKICK_JOIN_TOKEN is required for first-boot handshake")
	}

	body, err := json.Marshal(map[string]string{
		"agent_version": cfg.AgentVersion,
		"csr":           csrPEM,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := cfg.HubURL + "/api/v1/clusters/" + cfg.ClusterID + "/handshake"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.JoinToken)

	client := &http.Client{Timeout: httpTimeout}
	httpResp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hub returned %d: %s", httpResp.StatusCode, string(respBytes))
	}

	var resp handshakeResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if resp.ClientCertPEM == "" || resp.CAPEM == "" {
		return nil, fmt.Errorf("hub response missing cert fields")
	}
	return &resp, nil
}

// ─── File I/O ─────────────────────────────────────────────────────────────────

// writeFile writes data to path atomically (write to temp file, then rename).
// The file is readable only by the owner (0o600).
func writeFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return fmt.Errorf("create temp file for %q: %w", path, err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write %q: %w", path, err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("chmod %q: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close %q: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename %q → %q: %w", tmpName, path, err)
	}
	return nil
}

func envOr(key, def string) string {
	return cmp.Or(os.Getenv(key), def)
}
