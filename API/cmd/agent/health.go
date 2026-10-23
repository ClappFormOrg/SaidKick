package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

// serverShutdownTimeout bounds how long the health and metrics listeners
// wait for in-flight requests when the agent shuts down.
const serverShutdownTimeout = 5 * time.Second

// healthState tracks the time of the last successful hub heartbeat so the
// readiness probe can report a stalled heartbeat loop or a lost hub.
type healthState struct {
	lastHeartbeat atomic.Int64 // unix nanoseconds; 0 means no heartbeat yet
	threshold     time.Duration
	now           func() time.Time
}

func newHealthState(threshold time.Duration) *healthState {
	return &healthState{threshold: threshold, now: time.Now}
}

// markHeartbeat records a successful heartbeat at the current time.
func (h *healthState) markHeartbeat() {
	h.lastHeartbeat.Store(h.now().UnixNano())
}

// ready reports whether the last successful heartbeat is within the threshold.
// It is false until the first heartbeat succeeds.
func (h *healthState) ready() bool {
	last := h.lastHeartbeat.Load()
	if last == 0 {
		return false
	}
	return h.now().Sub(time.Unix(0, last)) <= h.threshold
}

// handler serves /healthz (process alive) and /readyz (recent heartbeat).
// Responses carry only "ok" or "not ready" so no agent state leaks.
func (h *healthState) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeHealth(w, http.StatusOK, "ok")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !h.ready() {
			writeHealth(w, http.StatusServiceUnavailable, "not ready")
			return
		}
		writeHealth(w, http.StatusOK, "ok")
	})
	return mux
}

func writeHealth(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body+"\n")
}

// serveHTTP serves h on ln until ctx is cancelled, then shuts the server
// down gracefully. The health and metrics listeners both run on it.
func serveHTTP(ctx context.Context, ln net.Listener, h http.Handler) error {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), serverShutdownTimeout)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
