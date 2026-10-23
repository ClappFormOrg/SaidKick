package auth

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
	"golang.org/x/sync/singleflight"
)

// discoveryTimeout caps a single OIDC discovery HTTP call, independent of
// the originating request context. Prevents a slow IdP from blocking callers
// for an unbounded duration.
const discoveryTimeout = 10 * time.Second

// cachedProvider bundles the OIDC provider and the oauth2 config derived
// from it. Both are re-constructed together when the TTL expires.
type cachedProvider struct {
	provider     *oidc.Provider
	oauth2Config oauth2.Config
	expiry       time.Time
}

type providerCache struct {
	ttl time.Duration
	mu  sync.RWMutex
	m   map[uuid.UUID]*cachedProvider
	sf  singleflight.Group // coalesces concurrent misses for the same issuer
}

func newProviderCache(ttl time.Duration) *providerCache {
	return &providerCache{ttl: ttl, m: make(map[uuid.UUID]*cachedProvider)}
}

// get returns a cached provider for the org, re-discovering if stale.
//
// The write lock is never held across network I/O. Cache reads use RLock;
// discovery runs outside any lock, coalesced per-issuer via singleflight so
// a thundering herd of concurrent logins does not fan out N parallel
// discovery calls when a cache entry expires.
func (c *providerCache) get(
	orgID uuid.UUID,
	issuer, clientID, clientSecret, redirectURL string,
) (*cachedProvider, error) {
	// Fast path: read lock only.
	c.mu.RLock()
	if p, ok := c.m[orgID]; ok && time.Now().Before(p.expiry) {
		c.mu.RUnlock()
		return p, nil
	}
	c.mu.RUnlock()

	// Slow path: discover outside any lock, coalescing concurrent misses for
	// the same issuer URL. context.Background() + discoveryTimeout is used
	// instead of the request ctx so cancellation of one request does not abort
	// the shared discovery call that other goroutines are waiting on.
	v, err, _ := c.sf.Do(issuer, func() (any, error) {
		discCtx, cancel := context.WithTimeout(context.Background(), discoveryTimeout)
		defer cancel()
		return oidc.NewProvider(discCtx, issuer)
	})
	if err != nil {
		return nil, fmt.Errorf("oidc: discover %q: %w", issuer, err)
	}
	provider, ok := v.(*oidc.Provider)
	if !ok || provider == nil {
		return nil, fmt.Errorf("oidc: unexpected provider type %T", v)
	}

	p := &cachedProvider{
		provider: provider,
		oauth2Config: oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  redirectURL,
			Endpoint:     provider.Endpoint(),
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		},
		expiry: time.Now().Add(c.ttl),
	}

	c.mu.Lock()
	c.m[orgID] = p
	c.pruneExpiredLocked()
	c.mu.Unlock()
	return p, nil
}

// pruneExpiredLocked removes stale entries from the map. Must be called with
// c.mu held for writing.
func (c *providerCache) pruneExpiredLocked() {
	now := time.Now()
	for id, p := range c.m {
		if now.After(p.expiry) {
			delete(c.m, id)
		}
	}
}

// invalidate removes the cached provider for an org. Call this after
// updating an org's OIDC configuration to force re-discovery on the next
// login attempt.
func (c *providerCache) invalidate(orgID uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, orgID)
}
