package vault

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// vaultMock starts an httptest.Server that handles Vault AppRole login
// requests. The server is closed via t.Cleanup.
func vaultMock(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// loginJSON returns a minimal Vault AppRole login response body.
func loginJSON(token string, ttl int64) string {
	return fmt.Sprintf(`{"auth":{"client_token":%q,"lease_duration":%d}}`, token, ttl)
}

// TestAppRoleCache_GetToken_cacheHit asserts that a valid cached entry is
// returned without making any HTTP call to Vault.
func TestAppRoleCache_GetToken_cacheHit(t *testing.T) {
	t.Parallel()

	srv := vaultMock(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("vault must not be called on a cache hit, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	})

	c := NewAppRoleCache(srv.URL, "approle", nil)
	id := uuid.Must(uuid.NewV7())

	c.mu.Lock()
	c.cache[id] = cachedEntry{token: "cached-token", expiresAt: time.Now().Add(10 * time.Minute)}
	c.mu.Unlock()

	tok, err := c.GetToken(t.Context(), id, "role", "secret")
	require.NoError(t, err)
	assert.Equal(t, "cached-token", tok)
}

// TestAppRoleCache_GetToken_cacheMissEmpty asserts that an empty cache causes
// a Vault login, the result is cached, and subsequent calls hit the cache.
func TestAppRoleCache_GetToken_cacheMissEmpty(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	srv := vaultMock(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/auth/approle/login", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "my-role", body["role_id"])
		assert.Equal(t, "my-secret", body["secret_id"])

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, loginJSON("fresh-token", 3600))
	})

	c := NewAppRoleCache(srv.URL, "approle", nil)
	id := uuid.Must(uuid.NewV7())

	tok1, err := c.GetToken(t.Context(), id, "my-role", "my-secret")
	require.NoError(t, err)
	assert.Equal(t, "fresh-token", tok1)
	assert.Equal(t, int32(1), calls.Load())

	// Second call must be served from cache — no additional Vault round-trip.
	tok2, err := c.GetToken(t.Context(), id, "my-role", "my-secret")
	require.NoError(t, err)
	assert.Equal(t, "fresh-token", tok2)
	assert.Equal(t, int32(1), calls.Load(), "cache hit must not call Vault again")
}

// TestAppRoleCache_GetToken_expiredEntry asserts that a cache entry whose
// 80%-TTL deadline has elapsed triggers a fresh login.
func TestAppRoleCache_GetToken_expiredEntry(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	srv := vaultMock(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, loginJSON("new-token", 3600))
	})

	c := NewAppRoleCache(srv.URL, "approle", nil)
	id := uuid.Must(uuid.NewV7())

	// Seed a stale entry: 80% TTL has already elapsed (expired 1 second ago).
	c.mu.Lock()
	c.cache[id] = cachedEntry{token: "stale-token", expiresAt: time.Now().Add(-1 * time.Second)}
	c.mu.Unlock()

	tok, err := c.GetToken(t.Context(), id, "role", "secret")
	require.NoError(t, err)
	assert.Equal(t, "new-token", tok)
	assert.Equal(t, int32(1), calls.Load())
}

// TestAppRoleCache_GetToken_vaultHTTPError asserts that a non-200 Vault
// response is surfaced as an error containing the HTTP status code.
func TestAppRoleCache_GetToken_vaultHTTPError(t *testing.T) {
	t.Parallel()

	srv := vaultMock(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"errors":["permission denied"]}`)
	})

	c := NewAppRoleCache(srv.URL, "approle", nil)
	id := uuid.Must(uuid.NewV7())

	_, err := c.GetToken(t.Context(), id, "bad-role", "bad-secret")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
}

// TestAppRoleCache_GetToken_emptyClientToken asserts that a 200 response
// with an empty client_token is rejected.
func TestAppRoleCache_GetToken_emptyClientToken(t *testing.T) {
	t.Parallel()

	srv := vaultMock(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"auth":{"client_token":"","lease_duration":3600}}`)
	})

	c := NewAppRoleCache(srv.URL, "approle", nil)
	id := uuid.Must(uuid.NewV7())

	_, err := c.GetToken(t.Context(), id, "role", "secret")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty client_token")
}

// TestAppRoleCache_GetToken_zeroTTLFallback asserts that lease_duration:0 in
// the Vault response is treated as the 3600s fallback, so the cache entry
// expires at approximately 80% of that (≈2880s from now).
func TestAppRoleCache_GetToken_zeroTTLFallback(t *testing.T) {
	t.Parallel()

	srv := vaultMock(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"auth":{"client_token":"root-token","lease_duration":0}}`)
	})

	c := NewAppRoleCache(srv.URL, "approle", nil)
	id := uuid.Must(uuid.NewV7())

	tok, err := c.GetToken(t.Context(), id, "role", "secret")
	require.NoError(t, err)
	assert.Equal(t, "root-token", tok)

	c.mu.Lock()
	entry := c.cache[id]
	c.mu.Unlock()

	// 80% of 3600s = 2880s; allow ±15s for execution time.
	wantMin := time.Now().Add(2865 * time.Second)
	wantMax := time.Now().Add(2895 * time.Second)
	assert.True(t, entry.expiresAt.After(wantMin) && entry.expiresAt.Before(wantMax),
		"expiresAt=%v, want between %v and %v", entry.expiresAt, wantMin, wantMax)
}

// TestAppRoleCache_GetToken_concurrent asserts that all goroutines receive
// a valid token when many of them race past the slow-path simultaneously,
// and that the cache is consistent after all calls complete.
func TestAppRoleCache_GetToken_concurrent(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	srv := vaultMock(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		// Small artificial delay to widen the race window.
		time.Sleep(5 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, loginJSON("concurrent-token", 3600))
	})

	c := NewAppRoleCache(srv.URL, "approle", nil)
	id := uuid.Must(uuid.NewV7())

	const n = 20
	tokens := make([]string, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(idx int) {
			defer wg.Done()
			tok, err := c.GetToken(t.Context(), id, "role", "secret")
			require.NoError(t, err)
			tokens[idx] = tok
		}(i)
	}
	wg.Wait()

	for _, tok := range tokens {
		assert.Equal(t, "concurrent-token", tok, "every goroutine must receive the same token")
	}
	// Between 1 and n Vault calls are acceptable; the write-back double-check
	// ensures the cache converges without needing exactly one call.
	assert.GreaterOrEqual(t, calls.Load(), int32(1))
	assert.LessOrEqual(t, calls.Load(), int32(n))
}

// TestAppRoleCache_GetToken_differentClusters asserts that each cluster ID
// has its own independent cache slot.
func TestAppRoleCache_GetToken_differentClusters(t *testing.T) {
	t.Parallel()

	var n atomic.Int32
	srv := vaultMock(t, func(w http.ResponseWriter, _ *http.Request) {
		count := int(n.Add(1))
		token := fmt.Sprintf("token-%d", count)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, loginJSON(token, 3600))
	})

	c := NewAppRoleCache(srv.URL, "approle", nil)
	idA := uuid.Must(uuid.NewV7())
	idB := uuid.Must(uuid.NewV7())

	tokA, err := c.GetToken(t.Context(), idA, "role-a", "secret-a")
	require.NoError(t, err)

	tokB, err := c.GetToken(t.Context(), idB, "role-b", "secret-b")
	require.NoError(t, err)

	assert.NotEqual(t, tokA, tokB, "different clusters must have independent tokens")
}
