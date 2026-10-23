package vault

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
)

// ErrPKINotConfigured is returned by NullPKIClient when no Vault PKI backend
// is wired.
var ErrPKINotConfigured = errors.New("vault: PKI client not configured")

// PKISigner signs x509 CSRs via the Vault PKI secrets engine.
type PKISigner interface {
	// SignCSR signs the PEM-encoded CSR and returns the signed cert PEM and
	// the issuing CA cert PEM. commonName is written into the certificate's
	// CN field. ttl controls the certificate lifetime; the Vault role caps it.
	SignCSR(ctx context.Context, csr, commonName string, ttl time.Duration) (certPEM, caPEM string, err error)
}

// PKIClient is the production PKISigner backed by a real Vault PKI
// secrets engine. Auth uses the same static token as the KV client.
type PKIClient struct {
	addr       string
	token      string
	mount      string // e.g. "pki"
	role       string // e.g. "agent"
	httpClient *http.Client
}

// NewPKIClient returns a PKIClient. addr must not have a trailing slash.
// mount is the PKI secrets engine path and role is the signing role name.
// tlsCfg may be nil for plain HTTPS; pass a config from BuildTLSConfig to
// enable mTLS.
func NewPKIClient(addr, token, mount, role string, tlsCfg *tls.Config) *PKIClient {
	return &PKIClient{
		addr:       addr,
		token:      token,
		mount:      mount,
		role:       role,
		httpClient: newHTTPClient(15*time.Second, tlsCfg),
	}
}

// SignCSR calls POST /v1/<mount>/sign/<role> on the Vault PKI engine and
// returns the signed certificate PEM and issuing CA certificate PEM.
func (c *PKIClient) SignCSR(ctx context.Context, csr, commonName string, ttl time.Duration) (string, string, error) {
	body, err := json.Marshal(map[string]any{
		"csr":         csr,
		"common_name": commonName,
		"ttl":         fmt.Sprintf("%dh", int(math.Ceil(ttl.Hours()))),
	})
	if err != nil {
		return "", "", fmt.Errorf("vault pki sign marshal: %w", err)
	}

	url := fmt.Sprintf("%s/v1/%s/sign/%s", c.addr, c.mount, c.role)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", "", fmt.Errorf("vault pki sign request: %w", err)
	}
	req.Header.Set("X-Vault-Token", c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("vault pki sign: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("vault pki sign returned HTTP %d: %q", resp.StatusCode, string(raw))
	}

	var result struct {
		Data struct {
			Certificate string `json:"certificate"`
			IssuingCA   string `json:"issuing_ca"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", fmt.Errorf("vault pki sign decode: %w", err)
	}
	if result.Data.Certificate == "" {
		return "", "", errors.New("vault pki sign: empty certificate in response")
	}
	return result.Data.Certificate, result.Data.IssuingCA, nil
}

// NullPKIClient is the no-op PKISigner returned when Vault PKI is not
// configured. SignCSR always returns ErrPKINotConfigured.
type NullPKIClient struct{}

func (NullPKIClient) SignCSR(_ context.Context, _, _ string, _ time.Duration) (string, string, error) {
	return "", "", ErrPKINotConfigured
}
