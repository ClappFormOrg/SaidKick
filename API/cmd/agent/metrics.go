package main

import (
	"net/http"

	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
)

// metricsHandler serves GET /metrics in Prometheus text format: the agent's
// own series plus Go runtime and process series. Every other path is 404.
// It carries no authentication, so it runs on its own listener, separate
// from the health probes, on a port only the pod network reaches.
func metricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		metrics.WriteAgentPrometheus(w, true)
	})
	return mux
}
