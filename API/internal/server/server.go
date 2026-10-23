// Package server constructs and starts the Echo HTTP server. It wires all
// middleware (CORS, request logging, error handling, authentication) and
// registers every route group. The exported New function is the single
// entry point; callers invoke Start to begin serving and block until the
// context is cancelled.
package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/config"
	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/handlers"
	gh "github.com/ClappFormOrg/SaidKick/API/internal/integrations/github"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/orchestrator"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/secrets"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/vault"
	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
	"github.com/labstack/echo/v4"
)

// Server bundles the Echo instances with the dependencies the handlers need.
// echo serves the main API listener; agentEcho serves the saidkick-agent
// routes on the dedicated mTLS listener; metrics serves GET /metrics on the
// metrics listener.
type Server struct {
	echo      *echo.Echo
	agentEcho *echo.Echo
	metrics   http.Handler
	cfg       config.HTTPConfig
	handle    *db.Handle
}

// New constructs the Server. It does not start listening — call Start().
func New(
	cfg config.Config,
	handle *db.Handle,
	logger audit.Logger,
	ghClient gh.Client,
	deployer orchestrator.Deployer,
	secretStore secrets.Store,
	kvClient vault.KVClient,
	pkiClient vault.PKISigner,
) *Server {
	e := newEcho()
	e.Use(CORSMiddleware(cfg.HTTP.CORSAllowedOrigins))

	// SecretRefStore is shared by the OIDC authenticator (reads secrets at
	// login time) and the org handler (writes secrets on OIDC config).
	oidcSecrets := auth.NewSecretRefStore(kvClient, cfg.Auth.OIDC.SecretEncryptionKey, cfg.IsDevMode)

	// Authentication. DevModeAuthenticator and OIDCAuthenticator are mutually
	// exclusive: dev mode takes precedence; OIDC is wired when its config is
	// present and dev mode is off. Neither wired = all requests are anonymous.
	// The APIKeyAuthenticator runs last in every mode: it short-circuits if
	// another authenticator already identified the caller, and handles
	// Authorization: Bearer sak_… for CI/CD pipelines.
	var oidcAuth *auth.OIDCAuthenticator
	if cfg.IsDevMode {
		dev := &auth.DevModeAuthenticator{
			Handle: handle,
			Header: cfg.Auth.DevUserHeader,
		}
		e.Use(dev.Middleware)
	} else if cfg.Auth.OIDC.Configured() {
		oidcAuth = auth.NewOIDCAuthenticator(handle, logger, cfg.Auth.OIDC, oidcSecrets)
		e.Use(oidcAuth.Middleware)
		oidcAuth.RegisterRoutes(e)
	}
	e.Use((&auth.APIKeyAuthenticator{Handle: handle}).Middleware)

	// Wire handlers.
	agentAuth := AgentAuthMiddleware(cfg.IsDevMode, handle)
	h := handlers.New(handle, logger, ghClient, deployer, secretStore, oidcSecrets, oidcAuth,
		pkiClient, cfg.Cluster.JoinSecret, cfg.Cluster.HubEndpoint, agentAuth,
		cfg.Community.DiscussionsToken, cfg.Community.Repo)
	h.VaultMount = cfg.Secrets.VaultMount
	h.HubAgentEndpoint = cfg.Cluster.HubAgentEndpoint
	h.AgentChartRepo = cfg.Cluster.AgentChartRepo
	h.AgentChartVersion = cfg.Cluster.AgentChartVersion
	h.AgentCertTTL = cfg.Cluster.AgentCertTTL
	h.SARIFGateMode = cfg.SARIFGateMode
	h.Register(e)

	// Agent routes live on their own Echo instance, served only by the mTLS
	// listener. It carries no session or API-key authenticator: the client
	// certificate is the only credential it accepts.
	agentEcho := newEcho()
	h.RegisterAgent(agentEcho)

	// Dev mode keeps the X-Agent-Cluster-ID header bypass reachable over
	// plain HTTP, so local agents and scripts work without certificates.
	// Production never mounts agent routes on the main listener.
	if cfg.IsDevMode {
		h.RegisterAgent(e)
	}

	return &Server{
		echo:      e,
		agentEcho: agentEcho,
		metrics:   newMetricsHandler(),
		cfg:       cfg.HTTP,
		handle:    handle,
	}
}

// newMetricsHandler returns the handler for the metrics listener: GET
// /metrics in Prometheus text format, including Go runtime and process
// series, and 404 for every other path. It carries no authentication, so
// it must only be bound to a port the ingress never routes.
func newMetricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		metrics.WritePrometheus(w, true)
	})
	return mux
}

// newEcho returns an Echo instance with the error handler and the global
// middleware every listener shares, in dependency order: request-id first
// so it propagates into every other log line and error envelope. Recovery
// comes after the access log and metrics: it writes the 500 for a
// panicking handler before the chain unwinds, so both of them see and
// record the response the client gets.
func newEcho() *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.HTTPErrorHandler = ErrorHandler

	e.Use(RequestIDMiddleware())
	e.Use(SecurityHeadersMiddleware())
	e.Use(AccessLogMiddleware())
	e.Use(MetricsMiddleware())
	e.Use(RecoveryMiddleware())
	return e
}

// agentTLSConfig builds the agent listener's TLS configuration: TLS 1.3
// only, the configured serving certificate, and a client certificate
// required and verified against the agent CA bundle.
func agentTLSConfig(cfg config.AgentListenerConfig) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load agent serving certificate: %w", err)
	}
	caPEM, err := os.ReadFile(cfg.ClientCAFile)
	if err != nil {
		return nil, fmt.Errorf("read agent client CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("agent client CA %s: no PEM certificates found", cfg.ClientCAFile)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// listener pairs an http.Server with the call that runs it.
type listener struct {
	name  string
	srv   *http.Server
	serve func() error
}

// Start runs the main HTTP listener and, when configured, the agent mTLS
// and metrics listeners. It blocks until ctx is cancelled or a listener errors out; either
// way every listener is shut down. Shutdown is graceful: in-flight requests
// get up to cfg.ShutdownTimeout before connections are closed.
func (s *Server) Start(ctx context.Context) error {
	srv := &http.Server{
		Addr:         s.cfg.Addr,
		Handler:      s.echo,
		ReadTimeout:  s.cfg.ReadTimeout,
		WriteTimeout: s.cfg.WriteTimeout,
		IdleTimeout:  s.cfg.IdleTimeout,
	}
	listeners := []listener{{name: "HTTP", srv: srv, serve: srv.ListenAndServe}}

	if s.cfg.Agent.Configured() {
		tlsCfg, err := agentTLSConfig(s.cfg.Agent)
		if err != nil {
			return fmt.Errorf("agent listener: %w", err)
		}
		agentSrv := &http.Server{
			Addr:         s.cfg.Agent.Addr,
			Handler:      s.agentEcho,
			TLSConfig:    tlsCfg,
			ReadTimeout:  s.cfg.ReadTimeout,
			WriteTimeout: s.cfg.WriteTimeout,
			IdleTimeout:  s.cfg.IdleTimeout,
		}
		listeners = append(listeners, listener{
			name:  "agent mTLS",
			srv:   agentSrv,
			serve: func() error { return agentSrv.ListenAndServeTLS("", "") },
		})
	} else {
		s.echo.Logger.Warnf("agent mTLS listener disabled: AGENT_TLS_CERT_FILE, AGENT_TLS_KEY_FILE and AGENT_CLIENT_CA_FILE are unset")
	}

	// config.Load always sets MetricsAddr; an empty one (a hand-built
	// Config) runs without the metrics listener.
	if s.cfg.MetricsAddr != "" {
		metricsSrv := &http.Server{
			Addr:         s.cfg.MetricsAddr,
			Handler:      s.metrics,
			ReadTimeout:  s.cfg.ReadTimeout,
			WriteTimeout: s.cfg.WriteTimeout,
			IdleTimeout:  s.cfg.IdleTimeout,
		}
		listeners = append(listeners, listener{name: "metrics", srv: metricsSrv, serve: metricsSrv.ListenAndServe})
	}

	// Buffered for every listener so no goroutine blocks after Start returns.
	listenErr := make(chan error, len(listeners))
	for _, l := range listeners {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					listenErr <- fmt.Errorf("%s listener panicked: %v", l.name, r)
				}
			}()
			s.echo.Logger.Infof("%s server listening on %s", l.name, l.srv.Addr)
			if err := l.serve(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				listenErr <- fmt.Errorf("%s: %w", l.name, err)
			}
		}()
	}

	var runErr error
	select {
	case <-ctx.Done():
	case err := <-listenErr:
		runErr = fmt.Errorf("listen: %w", err)
	}

	// Drain every listener, including the healthy ones when another failed.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
	defer cancel()
	s.echo.Logger.Infof("shutting down HTTP servers (timeout %s)", s.cfg.ShutdownTimeout)
	var shutdownErrs []error
	for _, l := range listeners {
		if err := l.srv.Shutdown(shutdownCtx); err != nil {
			shutdownErrs = append(shutdownErrs, fmt.Errorf("%s: %w", l.name, err))
		}
	}
	if runErr != nil {
		return runErr
	}
	if len(shutdownErrs) > 0 {
		return fmt.Errorf("shutdown: %w", errors.Join(shutdownErrs...))
	}
	return nil
}

// HealthCheckTimeout is the budget for the readiness DB ping.
const HealthCheckTimeout = 2 * time.Second
