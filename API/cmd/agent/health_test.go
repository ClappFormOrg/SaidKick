package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClock is a settable time source for healthState.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func newTestHealth(threshold time.Duration) (*healthState, *fakeClock) {
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	h := newHealthState(threshold)
	h.now = clock.now
	return h, clock
}

func probe(t *testing.T, h http.Handler, path string) (code int, body string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, http.NoBody))
	return rec.Code, rec.Body.String()
}

func TestHealthz_OK(t *testing.T) {
	h, _ := newTestHealth(90 * time.Second)
	code, body := probe(t, h.handler(), "/healthz")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "ok\n", body)
}

func TestReadyz_TracksHeartbeats(t *testing.T) {
	h, clock := newTestHealth(90 * time.Second)
	handler := h.handler()

	code, body := probe(t, handler, "/readyz")
	assert.Equal(t, http.StatusServiceUnavailable, code, "unready before first heartbeat")
	assert.Equal(t, "not ready\n", body)

	h.markHeartbeat()
	code, body = probe(t, handler, "/readyz")
	assert.Equal(t, http.StatusOK, code, "ready right after a heartbeat")
	assert.Equal(t, "ok\n", body)

	clock.t = clock.t.Add(90 * time.Second)
	code, _ = probe(t, handler, "/readyz")
	assert.Equal(t, http.StatusOK, code, "ready at the threshold")

	clock.t = clock.t.Add(time.Second)
	code, body = probe(t, handler, "/readyz")
	assert.Equal(t, http.StatusServiceUnavailable, code, "unready once heartbeats stop")
	assert.Equal(t, "not ready\n", body)

	h.markHeartbeat()
	code, _ = probe(t, handler, "/readyz")
	assert.Equal(t, http.StatusOK, code, "ready again after heartbeats resume")
}

func TestServeHTTP_ShutsDownOnCancel(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	h, _ := newTestHealth(time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveHTTP(ctx, ln, h.handler()) }()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+ln.Addr().String()+"/healthz", http.NoBody)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("serveHTTP did not return after cancel")
	}
}
