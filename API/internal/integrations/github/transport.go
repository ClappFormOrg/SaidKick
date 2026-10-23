package github

import (
	"net/http"
	"strconv"
	"time"
)

const maxRateLimitRetries = 3

// rateLimitTransport wraps an inner RoundTripper and retries on HTTP 429
// (or 403 with an x-ratelimit-remaining: 0 header). It reads Retry-After
// or x-ratelimit-reset to determine how long to wait, capped at 60 seconds
// per attempt, and honours context cancellation.
type rateLimitTransport struct {
	inner http.RoundTripper
}

func (r *rateLimitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for attempt := 0; attempt < maxRateLimitRetries; attempt++ {
		// Clone the request so the body can be re-read on retry.
		clone := req.Clone(req.Context())

		resp, err := r.inner.RoundTrip(clone)
		if err != nil {
			return nil, err
		}
		if !isRateLimited(resp) {
			return resp, nil
		}

		wait := rateLimitDelay(resp)
		_ = resp.Body.Close()

		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(wait):
		}
	}
	// Final attempt — return whatever we get.
	return r.inner.RoundTrip(req.Clone(req.Context()))
}

// isRateLimited reports whether the response indicates GitHub rate limiting.
func isRateLimited(resp *http.Response) bool {
	if resp.StatusCode == http.StatusTooManyRequests {
		return true
	}
	// GitHub sometimes returns 403 with x-ratelimit-remaining: 0.
	if resp.StatusCode == http.StatusForbidden &&
		resp.Header.Get("x-ratelimit-remaining") == "0" {
		return true
	}
	return false
}

// rateLimitDelay returns the duration to wait before the next attempt.
// It prefers Retry-After (seconds), then x-ratelimit-reset (Unix epoch),
// and falls back to 60 seconds.
func rateLimitDelay(resp *http.Response) time.Duration {
	const fallback = 60 * time.Second
	const maxDelay = 60 * time.Second

	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.ParseInt(v, 10, 64); err == nil && secs > 0 {
			d := time.Duration(secs) * time.Second
			if d > maxDelay {
				d = maxDelay
			}
			return d
		}
	}

	if v := resp.Header.Get("x-ratelimit-reset"); v != "" {
		if epoch, err := strconv.ParseInt(v, 10, 64); err == nil {
			reset := time.Unix(epoch, 0)
			d := time.Until(reset)
			if d <= 0 {
				return time.Second
			}
			if d > maxDelay {
				d = maxDelay
			}
			return d
		}
	}

	return fallback
}
