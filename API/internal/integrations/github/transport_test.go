package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// trackedBody records whether Close was called.
type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error {
	b.closed = true
	return nil
}

// scriptedTransport answers each RoundTrip with the next response from
// responses (the last one repeats) and records every call.
type scriptedTransport struct {
	responses []*http.Response
	err       error
	calls     int
}

func (s *scriptedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	i := min(s.calls-1, len(s.responses)-1)
	return s.responses[i], nil
}

func newResp(status int, headers map[string]string) *http.Response {
	h := http.Header{}
	for k, v := range headers {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: status, Header: h, Body: &trackedBody{Reader: strings.NewReader("")}}
}

func newReq(t *testing.T, ctx context.Context) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/o/r", http.NoBody)
	require.NoError(t, err)
	return req
}

func TestIsRateLimited(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		headers map[string]string
		want    bool
	}{
		{"200", http.StatusOK, nil, false},
		{"429", http.StatusTooManyRequests, nil, true},
		{"403 remaining 0", http.StatusForbidden, map[string]string{"x-ratelimit-remaining": "0"}, true},
		{"403 remaining 10", http.StatusForbidden, map[string]string{"x-ratelimit-remaining": "10"}, false},
		{"403 no header", http.StatusForbidden, nil, false},
		{"500", http.StatusInternalServerError, map[string]string{"x-ratelimit-remaining": "0"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isRateLimited(newResp(tc.status, tc.headers)))
		})
	}
}

func TestRateLimitDelay(t *testing.T) {
	future := func(d time.Duration) string { return strconv.FormatInt(time.Now().Add(d).Unix(), 10) }

	cases := []struct {
		name    string
		headers map[string]string
		min     time.Duration
		max     time.Duration
	}{
		{"no headers falls back to 60s", nil, 60 * time.Second, 60 * time.Second},
		{"retry-after seconds", map[string]string{"Retry-After": "7"}, 7 * time.Second, 7 * time.Second},
		{"retry-after capped at 60s", map[string]string{"Retry-After": "3600"}, 60 * time.Second, 60 * time.Second},
		{"retry-after zero ignored", map[string]string{"Retry-After": "0"}, 60 * time.Second, 60 * time.Second},
		{"retry-after negative ignored", map[string]string{"Retry-After": "-5"}, 60 * time.Second, 60 * time.Second},
		{"retry-after http-date ignored", map[string]string{"Retry-After": "Wed, 21 Oct 2015 07:28:00 GMT"}, 60 * time.Second, 60 * time.Second},
		{"reset in the past waits 1s", map[string]string{"x-ratelimit-reset": "1"}, time.Second, time.Second},
		{"reset soon", map[string]string{"x-ratelimit-reset": future(10 * time.Second)}, 8 * time.Second, 10 * time.Second},
		{"reset capped at 60s", map[string]string{"x-ratelimit-reset": future(time.Hour)}, 60 * time.Second, 60 * time.Second},
		{"reset not a number falls back", map[string]string{"x-ratelimit-reset": "soon"}, 60 * time.Second, 60 * time.Second},
		{"retry-after preferred over reset", map[string]string{"Retry-After": "3", "x-ratelimit-reset": future(30 * time.Second)}, 3 * time.Second, 3 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := rateLimitDelay(newResp(http.StatusTooManyRequests, tc.headers))
			assert.GreaterOrEqual(t, d, tc.min)
			assert.LessOrEqual(t, d, tc.max)
		})
	}
}

func TestRateLimitTransportPassesThrough(t *testing.T) {
	ok := newResp(http.StatusOK, nil)
	inner := &scriptedTransport{responses: []*http.Response{ok}}

	resp, err := (&rateLimitTransport{inner: inner}).RoundTrip(newReq(t, context.Background()))
	require.NoError(t, err)
	assert.Same(t, ok, resp)
	assert.Equal(t, 1, inner.calls)
}

func TestRateLimitTransportPropagatesError(t *testing.T) {
	boom := errors.New("dial failed")
	inner := &scriptedTransport{err: boom}

	resp, err := (&rateLimitTransport{inner: inner}).RoundTrip(newReq(t, context.Background()))
	require.ErrorIs(t, err, boom)
	assert.Nil(t, resp)
	assert.Equal(t, 1, inner.calls)
}

func TestRateLimitTransportRetriesThenSucceeds(t *testing.T) {
	limited := newResp(http.StatusTooManyRequests, map[string]string{"Retry-After": "1"})
	ok := newResp(http.StatusOK, nil)
	inner := &scriptedTransport{responses: []*http.Response{limited, ok}}

	start := time.Now()
	resp, err := (&rateLimitTransport{inner: inner}).RoundTrip(newReq(t, context.Background()))
	require.NoError(t, err)
	assert.Same(t, ok, resp)
	assert.Equal(t, 2, inner.calls)
	assert.GreaterOrEqual(t, time.Since(start), time.Second, "must wait Retry-After before retrying")
	assert.True(t, limited.Body.(*trackedBody).closed, "rate-limited body must be closed before retry")
}

func TestRateLimitTransportGivesUpAfterMaxRetries(t *testing.T) {
	if testing.Short() {
		t.Skip("waits maxRateLimitRetries seconds")
	}
	limited := newResp(http.StatusTooManyRequests, map[string]string{"Retry-After": "1"})
	inner := &scriptedTransport{responses: []*http.Response{limited}}

	resp, err := (&rateLimitTransport{inner: inner}).RoundTrip(newReq(t, context.Background()))
	require.NoError(t, err)
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode, "final attempt is returned as-is")
	assert.Equal(t, maxRateLimitRetries+1, inner.calls)
}

func TestRateLimitTransportHonoursCancellation(t *testing.T) {
	// No delay headers: the transport would wait the 60s fallback unless it
	// notices the cancelled context.
	limited := newResp(http.StatusTooManyRequests, nil)
	inner := &scriptedTransport{responses: []*http.Response{limited}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	resp, err := (&rateLimitTransport{inner: inner}).RoundTrip(newReq(t, ctx))
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, resp)
	assert.Less(t, time.Since(start), 5*time.Second)
	assert.Equal(t, 1, inner.calls)
	assert.True(t, limited.Body.(*trackedBody).closed)
}
