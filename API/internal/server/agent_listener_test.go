package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/config"
)

func TestAgentTLSConfig(t *testing.T) {
	ca := newTestCA(t, "agent CA")
	cfg := writeAgentListenerFiles(t, ca)

	tlsCfg, err := agentTLSConfig(cfg)
	require.NoError(t, err)
	assert.Equal(t, tls.RequireAndVerifyClientCert, tlsCfg.ClientAuth)
	assert.Equal(t, uint16(tls.VersionTLS13), tlsCfg.MinVersion)
	require.Len(t, tlsCfg.Certificates, 1)
	require.NotNil(t, tlsCfg.ClientCAs)

	// The client CA pool is the configured bundle: an agent certificate it
	// signed verifies, one from another CA does not.
	verify := func(c tls.Certificate) error {
		_, err := c.Leaf.Verify(x509.VerifyOptions{
			Roots:     tlsCfg.ClientCAs,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		})
		return err
	}
	require.NoError(t, verify(ca.agentCert(t, uuid.New())))
	require.Error(t, verify(newTestCA(t, "rogue CA").agentCert(t, uuid.New())))
}

func TestAgentTLSConfigErrors(t *testing.T) {
	ca := newTestCA(t, "agent CA")
	good := writeAgentListenerFiles(t, ca)

	notPEM := filepath.Join(t.TempDir(), "ca.crt")
	require.NoError(t, os.WriteFile(notPEM, []byte("not a certificate"), 0o600))

	cases := []struct {
		name    string
		mutate  func(*config.AgentListenerConfig)
		wantErr string
	}{
		{"missing serving cert", func(c *config.AgentListenerConfig) { c.CertFile += ".missing" }, "load agent serving certificate"},
		{"key does not match", func(c *config.AgentListenerConfig) { c.KeyFile = c.ClientCAFile }, "load agent serving certificate"},
		{"missing client CA", func(c *config.AgentListenerConfig) { c.ClientCAFile += ".missing" }, "read agent client CA"},
		{"client CA not PEM", func(c *config.AgentListenerConfig) { c.ClientCAFile = notPEM }, "no PEM certificates found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := good
			tc.mutate(&cfg)
			_, err := agentTLSConfig(cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// startAgentTLSServer serves handler with the production agent TLS config
// on a random loopback port.
func startAgentTLSServer(t *testing.T, ca *testCA, handler http.Handler) *httptest.Server {
	t.Helper()
	tlsCfg, err := agentTLSConfig(writeAgentListenerFiles(t, ca))
	require.NoError(t, err)
	ts := httptest.NewUnstartedServer(handler)
	ts.TLS = tlsCfg
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return ts
}

// whoamiEcho is an agent Echo instance with one route behind the agent auth
// middleware that echoes the authenticated cluster ID.
func whoamiEcho(store agentCertStore) *echo.Echo {
	e := newEcho()
	e.GET("/whoami", func(c echo.Context) error {
		return c.String(http.StatusOK, auth.AgentIdentityFromContext(c.Request().Context()).ClusterID.String())
	}, agentAuthMiddleware(false, store))
	return e
}

func TestAgentListenerHandshake(t *testing.T) {
	ca := newTestCA(t, "agent CA")
	clusterID := uuid.New()
	good := ca.agentCert(t, clusterID)
	rotated := ca.agentCert(t, clusterID)
	rogue := newTestCA(t, "rogue CA").agentCert(t, clusterID)

	f := &fakeLookup{active: map[uuid.UUID]string{clusterID: good.Leaf.SerialNumber.String()}}
	ts := startAgentTLSServer(t, ca, whoamiEcho(f))

	get := func(c *http.Client) (int, string, error) {
		resp, err := c.Get(ts.URL + "/whoami")
		if err != nil {
			return 0, "", err
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body), nil
	}

	t.Run("no client certificate fails the handshake", func(t *testing.T) {
		_, _, err := get(agentClient(ca, nil, 0))
		require.Error(t, err)
	})

	t.Run("certificate from an unknown CA fails the handshake", func(t *testing.T) {
		_, _, err := get(agentClient(ca, &rogue, 0))
		require.Error(t, err)
	})

	t.Run("TLS 1.2 is refused", func(t *testing.T) {
		_, _, err := get(agentClient(ca, &good, tls.VersionTLS12))
		require.Error(t, err)
	})

	t.Run("matching serial authenticates and resolves the cluster", func(t *testing.T) {
		code, body, err := get(agentClient(ca, &good, 0))
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, code)
		assert.Equal(t, clusterID.String(), body)
	})

	t.Run("valid certificate with a different serial is 401", func(t *testing.T) {
		code, _, err := get(agentClient(ca, &rotated, 0))
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, code)
	})

	t.Run("rotation invalidates the previous certificate", func(t *testing.T) {
		f.setSerial(clusterID, rotated.Leaf.SerialNumber.String())

		code, _, err := get(agentClient(ca, &good, 0))
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, code)

		code, body, err := get(agentClient(ca, &rotated, 0))
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, code)
		assert.Equal(t, clusterID.String(), body)
	})
}

// agentPaths are the agent endpoints RegisterAgent wires.
func agentPaths(id uuid.UUID) []string {
	return []string{
		"/api/v1/clusters/" + id.String() + "/heartbeat",
		"/api/v1/clusters/" + id.String() + "/deployment-states",
		"/api/v1/clusters/" + id.String() + "/commands/" + uuid.NewString() + "/ack",
	}
}

func TestAgentRoutesOnlyOnAgentListener(t *testing.T) {
	var cfg config.Config
	s := New(cfg, nil, nil, nil, nil, nil, nil, nil)
	id := uuid.New()

	for _, p := range agentPaths(id) {
		// Main listener: the route does not exist, even with the dev header.
		req := httptest.NewRequest(http.MethodPost, p, http.NoBody)
		req.Header.Set(agentClusterIDHeader, id.String())
		assert.Equal(t, http.StatusNotFound, serve(s.echo, req).Code, "main listener served %s", p)

		// Agent listener: the route exists and demands a certificate.
		req = httptest.NewRequest(http.MethodPost, p, http.NoBody)
		req.Header.Set(agentClusterIDHeader, id.String())
		assert.Equal(t, http.StatusUnauthorized, serve(s.agentEcho, req).Code, "agent listener %s", p)
	}

	// The agent listener serves nothing but agent routes.
	for _, p := range []string{"/api/v1/me", "/api/v1/apps", "/metrics", "/healthz"} {
		req := httptest.NewRequest(http.MethodGet, p, http.NoBody)
		assert.Equal(t, http.StatusNotFound, serve(s.agentEcho, req).Code, "agent listener served %s", p)
	}
}

func TestDevModeMountsAgentRoutesOnMainListener(t *testing.T) {
	var cfg config.Config
	cfg.IsDevMode = true
	s := New(cfg, nil, nil, nil, nil, nil, nil, nil)

	for _, p := range agentPaths(uuid.New()) {
		// No header and no certificate: the route exists and rejects.
		req := httptest.NewRequest(http.MethodPost, p, http.NoBody)
		assert.Equal(t, http.StatusUnauthorized, serve(s.echo, req).Code, p)
	}
}

// freeAddr returns a loopback address that was free a moment ago.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}

func TestStartServesAgentListener(t *testing.T) {
	ca := newTestCA(t, "agent CA")
	var cfg config.Config
	cfg.HTTP.Addr = "127.0.0.1:0"
	cfg.HTTP.ShutdownTimeout = time.Second
	cfg.HTTP.Agent = writeAgentListenerFiles(t, ca)
	cfg.HTTP.Agent.Addr = freeAddr(t)
	s := New(cfg, nil, nil, nil, nil, nil, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()

	cert := ca.agentCert(t, uuid.New())
	url := fmt.Sprintf("https://%s/not-an-agent-route", cfg.HTTP.Agent.Addr)
	var resp *http.Response
	require.Eventually(t, func() bool {
		var err error
		resp, err = agentClient(ca, &cert, 0).Get(url)
		return err == nil
	}, 5*time.Second, 20*time.Millisecond, "agent listener never accepted a valid client certificate")
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, uint16(tls.VersionTLS13), resp.TLS.Version)

	_, err := agentClient(ca, nil, 0).Get(url)
	require.Error(t, err, "agent listener accepted a connection without a client certificate")

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after context cancellation")
	}
}

func TestStartRejectsBadAgentTLSFiles(t *testing.T) {
	var cfg config.Config
	cfg.HTTP.Addr = "127.0.0.1:0"
	cfg.HTTP.Agent = config.AgentListenerConfig{
		Addr:         "127.0.0.1:0",
		CertFile:     filepath.Join(t.TempDir(), "missing.crt"),
		KeyFile:      filepath.Join(t.TempDir(), "missing.key"),
		ClientCAFile: filepath.Join(t.TempDir(), "missing-ca.crt"),
	}
	err := New(cfg, nil, nil, nil, nil, nil, nil, nil).Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent listener")
}

func TestStartReturnsAgentListenError(t *testing.T) {
	var cfg config.Config
	cfg.HTTP.Addr = "127.0.0.1:0"
	cfg.HTTP.ShutdownTimeout = time.Second
	cfg.HTTP.Agent = writeAgentListenerFiles(t, newTestCA(t, "agent CA"))
	cfg.HTTP.Agent.Addr = "127.0.0.1:-1"
	s := New(cfg, nil, nil, nil, nil, nil, nil, nil)

	done := make(chan error, 1)
	go func() { done <- s.Start(context.Background()) }()
	select {
	case err := <-done:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "listen")
		assert.Contains(t, err.Error(), "agent mTLS")
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return when the agent listener failed")
	}
}
