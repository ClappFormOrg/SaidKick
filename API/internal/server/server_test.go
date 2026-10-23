package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ClappFormOrg/SaidKick/API/internal/config"
	"github.com/ClappFormOrg/SaidKick/API/internal/server"
)

// newBareServer builds a Server with no backends. Nothing in New touches the
// DB or Vault, so nil dependencies are fine for routes that don't reach a
// handler's data layer.
func newBareServer(cfg config.Config) *server.Server {
	return server.New(cfg, nil, nil, nil, nil, nil, nil, nil)
}

func TestNewWiresGlobalSecurityMiddleware(t *testing.T) {
	var cfg config.Config
	cfg.HTTP.CORSAllowedOrigins = []string{"https://app.example.nl"}
	e := newBareServer(cfg).Echo()

	req := httptest.NewRequest(http.MethodGet, "/healthz", http.NoBody)
	req.Header.Set(echo.HeaderOrigin, "https://app.example.nl")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	h := rec.Header()
	assert.Equal(t, "nosniff", h.Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", h.Get("X-Frame-Options"))
	assert.NotEmpty(t, h.Get("Strict-Transport-Security"))
	assert.NotEmpty(t, h.Get(echo.HeaderXRequestID))
	assert.Equal(t, "https://app.example.nl", h.Get(echo.HeaderAccessControlAllowOrigin))

	// Unmatched routes still go through the error envelope with headers set.
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/no-such-route", http.NoBody))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
}

func TestStartShutsDownOnContextCancel(t *testing.T) {
	var cfg config.Config
	cfg.HTTP.Addr = "127.0.0.1:0"
	cfg.HTTP.ShutdownTimeout = time.Second
	s := newBareServer(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after context cancellation")
	}
}

func TestStartReturnsListenError(t *testing.T) {
	var cfg config.Config
	cfg.HTTP.Addr = "127.0.0.1:-1"
	s := newBareServer(cfg)

	select {
	case err := <-startAsync(s):
		require.Error(t, err)
		assert.Contains(t, err.Error(), "listen")
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return on an invalid address")
	}
}

func startAsync(s *server.Server) <-chan error {
	done := make(chan error, 1)
	go func() { done <- s.Start(context.Background()) }()
	return done
}
