// Package vault provides a thin KV v2 client for HashiCorp Vault.
//
// This client is intentionally minimal: it supports only the Get and Put
// operations needed for arbitrary-path secret storage (e.g. OIDC client
// secrets). Deployment-scoped secrets use the higher-level secrets.Store
// interface in internal/integrations/secrets.
package vault

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ErrNotConfigured is returned by NullClient when no Vault backend is wired.
var ErrNotConfigured = errors.New("vault: client not configured")

// ErrNotFound is returned by Get when the path has no value in Vault.
var ErrNotFound = errors.New("vault: path not found")

// KVClient is the abstraction over a Vault KV v2 backend. Paths are logical
// (e.g. "oidc/orgs/<id>/client_secret") without the mount prefix or the
// "/data/" segment — the implementation adds those.
type KVClient interface {
	// Get retrieves the string value stored at path. Returns ErrNotFound
	// when the path does not exist.
	Get(ctx context.Context, path string) (string, error)

	// Put writes value to path, creating or overwriting.
	Put(ctx context.Context, path string, value string) error
}

// Client is the production KVClient backed by a real Vault instance via
// the KV v2 HTTP API. Auth uses a static token.
type Client struct {
	addr   string // base URL, no trailing slash
	token  string
	mount  string // KV v2 mount, e.g. "kv"
	httpClient *http.Client
}

// New returns a Client for the given Vault address, static token, and KV
// mount name. addr must not have a trailing slash. tlsCfg may be nil for
// plain HTTPS; pass a config from BuildTLSConfig to enable mTLS.
func New(addr, token, mount string, tlsCfg *tls.Config) *Client {
	return &Client{
		addr:       addr,
		token:      token,
		mount:      mount,
		httpClient: newHTTPClient(10*time.Second, tlsCfg),
	}
}

// Get fetches the value at path from Vault KV v2. The value was stored as
// a JSON string field; strings are returned as-is.
func (c *Client) Get(ctx context.Context, path string) (string, error) {
	url := fmt.Sprintf("%s/v1/%s/data/%s", c.addr, c.mount, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("vault get request: %w", err)
	}
	req.Header.Set("X-Vault-Token", c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("vault get: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, resp.Body)
		return "", ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return "", fmt.Errorf("vault get returned HTTP %d", resp.StatusCode)
	}

	var result struct {
		Data struct {
			Data struct {
				Value string `json:"value"`
			} `json:"data"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("vault get decode: %w", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(result.Data.Data.Value)
	if err != nil {
		return "", fmt.Errorf("vault get base64 decode: %w", err)
	}
	return string(decoded), nil
}

// Put writes value to path in Vault KV v2. The value is base64-encoded in
// the JSON payload so arbitrary bytes survive the transport layer.
func (c *Client) Put(ctx context.Context, path, value string) error {
	body, err := json.Marshal(map[string]any{
		"data": map[string]any{
			"value": base64.StdEncoding.EncodeToString([]byte(value)),
		},
	})
	if err != nil {
		return fmt.Errorf("vault put marshal: %w", err)
	}

	url := fmt.Sprintf("%s/v1/%s/data/%s", c.addr, c.mount, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("vault put request: %w", err)
	}
	req.Header.Set("X-Vault-Token", c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("vault put: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK &&
		resp.StatusCode != http.StatusCreated &&
		resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("vault put returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// NullClient is returned when Vault is not configured. Every call returns
// ErrNotConfigured so callers can fall back to another storage scheme.
type NullClient struct{}

func (NullClient) Get(_ context.Context, _ string) (string, error) { return "", ErrNotConfigured }
func (NullClient) Put(_ context.Context, _, _ string) error        { return ErrNotConfigured }
