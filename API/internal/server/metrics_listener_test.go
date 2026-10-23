package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ClappFormOrg/SaidKick/API/internal/config"
)

func TestMetricsNotServedOnMainListener(t *testing.T) {
	var cfg config.Config
	s := New(cfg, nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
	assert.Equal(t, http.StatusNotFound, serve(s.echo, req).Code)
}

func TestMetricsHandler(t *testing.T) {
	h := newMetricsHandler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/plain; version=0.0.4")
	body := rec.Body.String()
	// Go runtime and process series are exposed.
	assert.Contains(t, body, "go_goroutines ")
	assert.Contains(t, body, "process_cpu_seconds_total ")

	// The metrics listener serves nothing else.
	for _, p := range []string{"/healthz", "/api/v1/me", "/"} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, http.NoBody))
		assert.Equal(t, http.StatusNotFound, rec.Code, p)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/metrics", http.NoBody))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestStartServesMetricsListener(t *testing.T) {
	var cfg config.Config
	cfg.HTTP.Addr = freeAddr(t)
	cfg.HTTP.MetricsAddr = freeAddr(t)
	cfg.HTTP.ShutdownTimeout = time.Second
	s := New(cfg, nil, nil, nil, nil, nil, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()

	get := func(addr string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/metrics", http.NoBody)
		if err != nil {
			return nil, err
		}
		return http.DefaultClient.Do(req)
	}

	var resp *http.Response
	require.Eventually(t, func() bool {
		var err error
		resp, err = get(cfg.HTTP.MetricsAddr)
		return err == nil
	}, 5*time.Second, 20*time.Millisecond, "metrics listener never accepted a connection")
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), "go_goroutines ")

	// The main listener does not serve /metrics.
	require.Eventually(t, func() bool {
		resp, err = get(cfg.HTTP.Addr)
		return err == nil
	}, 5*time.Second, 20*time.Millisecond, "main listener never accepted a connection")
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after context cancellation")
	}
}

func TestStartReturnsMetricsListenError(t *testing.T) {
	var cfg config.Config
	cfg.HTTP.Addr = "127.0.0.1:0"
	cfg.HTTP.MetricsAddr = "127.0.0.1:-1"
	cfg.HTTP.ShutdownTimeout = time.Second
	s := New(cfg, nil, nil, nil, nil, nil, nil, nil)

	done := make(chan error, 1)
	go func() { done <- s.Start(context.Background()) }()
	select {
	case err := <-done:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "metrics")
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return when the metrics listener failed")
	}
}
