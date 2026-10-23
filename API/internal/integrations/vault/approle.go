package vault

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// AppRoleCache authenticates to Vault using the AppRole method and caches the
// resulting short-lived token per cluster. The cached token is reused until
// 80% of its TTL has elapsed, at which point the next call re-authenticates.
//
// Usage: create once at startup, pass to NewVaultStoreWithAppRole. Thread-safe.
type AppRoleCache struct {
	addr       string // Vault base URL, no trailing slash
	mount      string // AppRole auth mount, e.g. "approle"
	mu         sync.Mutex
	cache      map[uuid.UUID]cachedEntry
	httpClient *http.Client
}

type cachedEntry struct {
	token     string
	expiresAt time.Time // wall-clock deadline at 80% of the Vault-issued TTL
}

// NewAppRoleCache returns a new AppRoleCache for the given Vault address and
// AppRole auth mount name. addr must not have a trailing slash. tlsCfg may be
// nil for plain HTTPS; pass a config from BuildTLSConfig to enable mTLS.
func NewAppRoleCache(addr, mount string, tlsCfg *tls.Config) *AppRoleCache {
	return &AppRoleCache{
		addr:       addr,
		mount:      mount,
		cache:      make(map[uuid.UUID]cachedEntry),
		httpClient: newHTTPClient(10*time.Second, tlsCfg),
	}
}

// GetToken returns a valid Vault token for clusterID, logging in fresh when
// the cached token is absent or nearing expiry. roleID and secretID are the
// AppRole credentials stored on the Cluster row.
func (c *AppRoleCache) GetToken(ctx context.Context, clusterID uuid.UUID, roleID, secretID string) (string, error) {
	// Fast path: check cache under lock without doing any I/O.
	c.mu.Lock()
	if e, ok := c.cache[clusterID]; ok && time.Now().Before(e.expiresAt) {
		tok := e.token
		c.mu.Unlock()
		return tok, nil
	}
	c.mu.Unlock()

	// Slow path: authenticate outside the lock so other goroutines aren't
	// blocked during the Vault HTTP round-trip. Multiple goroutines for the
	// same cluster may race here; the write-back below handles that safely.
	token, ttl, err := c.login(ctx, roleID, secretID)
	if err != nil {
		return "", fmt.Errorf("approle login for cluster %s: %w", clusterID, err)
	}

	deadline := time.Now().Add(time.Duration(float64(ttl)*0.8) * time.Second)

	// Write-back: re-acquire lock and accept a concurrent update if one arrived.
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.cache[clusterID]; ok && time.Now().Before(e.expiresAt) {
		return e.token, nil
	}
	c.cache[clusterID] = cachedEntry{token: token, expiresAt: deadline}
	slog.Info("vault approle: obtained token for cluster",
		"cluster_id", clusterID, "ttl_seconds", ttl, "refresh_in_seconds", int64(float64(ttl)*0.8))
	c.pruneExpiredLocked()
	return token, nil
}

// Invalidate removes the cached token for clusterID. Call this when a cluster
// is deregistered or its AppRole credentials are rotated so the next secret
// operation forces a fresh Vault login.
func (c *AppRoleCache) Invalidate(clusterID uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.cache, clusterID)
}

// pruneExpiredLocked removes all entries whose deadline has passed.
// Must be called with c.mu held.
func (c *AppRoleCache) pruneExpiredLocked() {
	now := time.Now()
	for id, e := range c.cache {
		if now.After(e.expiresAt) {
			delete(c.cache, id)
		}
	}
}

// login authenticates with Vault using the AppRole method and returns the
// client token and its lease duration in seconds.
func (c *AppRoleCache) login(ctx context.Context, roleID, secretID string) (string, int64, error) {
	body, err := json.Marshal(map[string]string{
		"role_id":   roleID,
		"secret_id": secretID,
	})
	if err != nil {
		return "", 0, fmt.Errorf("marshal approle body: %w", err)
	}

	url := fmt.Sprintf("%s/v1/auth/%s/login", c.addr, c.mount)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", 0, fmt.Errorf("build approle request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("approle http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", 0, fmt.Errorf("approle login returned HTTP %d: %q", resp.StatusCode, string(raw))
	}

	var result struct {
		Auth struct {
			ClientToken   string `json:"client_token"`
			LeaseDuration int64  `json:"lease_duration"`
		} `json:"auth"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", 0, fmt.Errorf("decode approle response: %w", err)
	}
	if result.Auth.ClientToken == "" {
		return "", 0, errors.New("approle login: empty client_token in response")
	}
	ttl := result.Auth.LeaseDuration
	if ttl == 0 {
		ttl = 3600 // assume 1h when Vault omits the field
	}
	return result.Auth.ClientToken, ttl, nil
}
