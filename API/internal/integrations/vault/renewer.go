package vault

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// TokenRenewer keeps a Vault token alive by calling renew-self at half-TTL
// intervals. Tokens that do not expire (TTL == 0, e.g. root tokens) are a
// no-op. Start it with go renewer.Run(ctx); it exits when ctx is cancelled.
type TokenRenewer struct {
	addr  string
	token string
	httpClient *http.Client
}

// NewTokenRenewer returns a TokenRenewer for the given Vault address and token.
// addr must not have a trailing slash. tlsCfg may be nil for plain HTTPS; pass
// a config from BuildTLSConfig to enable mTLS.
func NewTokenRenewer(addr, token string, tlsCfg *tls.Config) *TokenRenewer {
	return &TokenRenewer{
		addr:       addr,
		token:      token,
		httpClient: newHTTPClient(10*time.Second, tlsCfg),
	}
}

// Run blocks until ctx is cancelled. It looks up the current TTL once, then
// sleeps for half that value before calling renew-self. Transient failures are
// logged and retried after 60 seconds without stopping the loop.
func (r *TokenRenewer) Run(ctx context.Context) {
	ttl, err := r.lookupTTL(ctx)
	switch {
	case err != nil:
		slog.Warn("vault token-renewer: lookup-self failed", "error", err)
		ttl = 120
	case ttl == 0:
		slog.Info("vault token-renewer: token has no expiry, exiting")
		return
	default:
		slog.Info("vault token-renewer: started", "ttl_seconds", ttl)
	}

	for {
		wait := max(time.Duration(ttl/2)*time.Second, 30*time.Second)

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		newTTL, err := r.renew(ctx)
		if err != nil {
			slog.Warn("vault token-renewer: renew-self failed", "error", err)
			ttl = 120
		} else {
			slog.Info("vault token-renewer: token renewed", "ttl_seconds", newTTL)
			ttl = newTTL
		}
	}
}

// lookupTTL calls GET /v1/auth/token/lookup-self and returns the TTL in
// seconds. Returns 0 for non-expiring tokens.
func (r *TokenRenewer) lookupTTL(ctx context.Context) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		r.addr+"/v1/auth/token/lookup-self", http.NoBody)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("X-Vault-Token", r.token)

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var result struct {
		Data struct {
			TTL int64 `json:"ttl"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("decode: %w", err)
	}
	return result.Data.TTL, nil
}

// renew calls POST /v1/auth/token/renew-self and returns the new lease
// duration in seconds.
func (r *TokenRenewer) renew(ctx context.Context) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		r.addr+"/v1/auth/token/renew-self", http.NoBody)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("X-Vault-Token", r.token)

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var result struct {
		Auth struct {
			LeaseDuration int64 `json:"lease_duration"`
		} `json:"auth"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("decode: %w", err)
	}
	if result.Auth.LeaseDuration == 0 {
		return 0, errors.New("lease_duration is 0 in response")
	}
	return result.Auth.LeaseDuration, nil
}
