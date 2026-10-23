package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ClappFormOrg/SaidKick/API/internal/agentcreds"
	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
)

const testClusterID = "0190c3a4-0000-7000-8000-000000000001"

// testCA is a certificate authority generated at test runtime; no key
// material is ever written outside t.TempDir().
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "agent test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &testCA{cert: cert, key: key}
}

func (ca *testCA) pemBytes() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.cert.Raw})
}

func (ca *testCA) pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.cert)
	return p
}

// sign issues a certificate for pub with the given serial and lifetime.
func (ca *testCA) sign(t *testing.T, pub any, cn string, serial int64, ttl time.Duration, usage x509.ExtKeyUsage, ips ...string) []byte {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(ttl),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
	}
	for range ips {
		tmpl.IPAddresses = append(tmpl.IPAddresses, []byte{127, 0, 0, 1})
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, pub, ca.key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// clientPair issues an agent client certificate and returns it as a
// tls.Certificate with Leaf set.
func (ca *testCA) clientPair(t *testing.T, serial int64, ttl time.Duration) tls.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	certPEM := ca.sign(t, &key.PublicKey, agentcreds.CommonName(testClusterID), serial, ttl, x509.ExtKeyUsageClientAuth)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	return pair
}

// fakeRenewer signs the CSR it receives with ca, as the hub's renew route
// would, or fails with err.
type fakeRenewer struct {
	t      *testing.T
	ca     *testCA
	ttl    time.Duration
	serial int64
	err    error
	calls  int
	csrs   []*x509.CertificateRequest
}

func (f *fakeRenewer) RenewCertificate(_ context.Context, csrPEM string) (renewResponse, error) {
	f.calls++
	if f.err != nil {
		return renewResponse{}, f.err
	}
	block, _ := pem.Decode([]byte(csrPEM))
	require.NotNil(f.t, block)
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	require.NoError(f.t, err)
	require.NoError(f.t, csr.CheckSignature())
	f.csrs = append(f.csrs, csr)
	f.serial++
	certPEM := f.ca.sign(f.t, csr.PublicKey, csr.Subject.CommonName, f.serial, f.ttl, x509.ExtKeyUsageClientAuth)
	return renewResponse{
		Certificate:   string(certPEM),
		CACertificate: string(f.ca.pemBytes()),
		Serial:        fmt.Sprint(f.serial),
		NotAfter:      time.Now().Add(f.ttl).UTC().Format(time.RFC3339),
	}, nil
}

// fakeSecrets records Secret writes, noting which certificate the manager
// was serving at the time of each write.
type fakeSecrets struct {
	mu          sync.Mutex
	m           *certManager
	err         error
	writes      []agentcreds.Credentials
	servingAtWr []*big.Int
	names       []string
}

func (f *fakeSecrets) PatchCredentials(_ context.Context, name string, c agentcreds.Credentials) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.names = append(f.names, name)
	f.writes = append(f.writes, c)
	f.servingAtWr = append(f.servingAtWr, f.m.current.Load().Leaf.SerialNumber)
	return nil
}

// newTestCertManager returns a manager serving a certificate with the given
// remaining validity, a fake hub and fake Secret, and a clock the test
// controls.
func newTestCertManager(t *testing.T, remaining time.Duration) (*certManager, *fakeRenewer, *fakeSecrets, *time.Time) {
	t.Helper()
	ca := newTestCA(t)
	hub := &fakeRenewer{t: t, ca: ca, ttl: 90 * 24 * time.Hour, serial: 1000}
	secrets := &fakeSecrets{}
	cfg := agentConfig{ClusterID: testClusterID, K8sSecretName: "saidkick-agent-credentials", CertRenewBefore: defaultCertRenewBefore}
	m := newCertManager(cfg, ca.clientPair(t, 1, remaining), hub, secrets)
	secrets.m = m
	now := time.Now()
	m.now = func() time.Time { return now }
	m.jitter = func(time.Duration) time.Duration { return 0 }
	return m, hub, secrets, &now
}

func serving(m *certManager) int64 {
	return m.current.Load().Leaf.SerialNumber.Int64()
}

func TestNeedsRenewal(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name      string
		remaining time.Duration
		requested bool
		want      bool
	}{
		{"plenty left", 60 * 24 * time.Hour, false, false},
		{"just above the threshold", 30*24*time.Hour + time.Minute, false, false},
		{"just below the threshold", 30*24*time.Hour - time.Minute, false, true},
		{"expired", -time.Hour, false, true},
		{"hub asked, plenty left", 89 * 24 * time.Hour, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, needsRenewal(now, now.Add(tc.remaining), defaultCertRenewBefore, tc.requested))
		})
	}
}

func TestRenewBackoff(t *testing.T) {
	none := func(time.Duration) time.Duration { return 0 }
	want := []time.Duration{
		time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 32 * time.Minute,
		time.Hour, time.Hour, time.Hour,
	}
	for i, w := range want {
		assert.Equal(t, w, renewBackoff(i+1, none), "failure %d", i+1)
	}
	assert.Equal(t, time.Hour, renewBackoff(1000, none), "no overflow on a long outage")

	// Jitter only shortens the wait, by at most a quarter.
	most := func(n time.Duration) time.Duration { return n - 1 }
	assert.Equal(t, 45*time.Second+time.Nanosecond, renewBackoff(1, most))
	assert.Equal(t, 45*time.Minute+time.Nanosecond, renewBackoff(10, most))

	// The production jitter stays inside its bounds.
	m := newCertManager(agentConfig{}, tls.Certificate{}, nil, nil)
	for range 100 {
		d := renewBackoff(3, m.jitter)
		assert.True(t, d > 3*time.Minute && d <= 4*time.Minute, "got %s", d)
	}
}

func TestMaybeRenew_NotDue(t *testing.T) {
	m, hub, secrets, _ := newTestCertManager(t, 60*24*time.Hour)
	m.maybeRenew(t.Context(), false)
	assert.Zero(t, hub.calls)
	assert.Empty(t, secrets.writes)
	assert.EqualValues(t, 1, serving(m))
	assert.InDelta(t, (60 * 24 * time.Hour).Seconds(), metrics.AgentCertRemainingSeconds.Get(), 120)
}

func TestMaybeRenew_RenewsBeforeExpiry(t *testing.T) {
	success := metrics.AgentCertRenewalsTotal(metrics.OutcomeSuccess)
	s0 := success.Get()
	m, hub, secrets, _ := newTestCertManager(t, 10*24*time.Hour)

	m.maybeRenew(t.Context(), false)

	require.Equal(t, 1, hub.calls)
	csr := hub.csrs[0]
	assert.Equal(t, "saidkick-agent-"+testClusterID, csr.Subject.CommonName)
	rsaKey, ok := csr.PublicKey.(*rsa.PublicKey)
	require.True(t, ok, "the renewal key is RSA")
	assert.Equal(t, 2048, rsaKey.N.BitLen())

	// The Secret is written before the swap: the old certificate is still
	// served while the new material is persisted.
	require.Len(t, secrets.writes, 1)
	assert.Equal(t, []string{"saidkick-agent-credentials"}, secrets.names)
	assert.EqualValues(t, 1, secrets.servingAtWr[0].Int64())
	written, err := tls.X509KeyPair(secrets.writes[0].Cert, secrets.writes[0].Key)
	require.NoError(t, err, "the Secret holds a matching cert and key")
	assert.EqualValues(t, 1001, written.Leaf.SerialNumber.Int64())
	assert.Equal(t, hub.ca.pemBytes(), secrets.writes[0].CA)

	assert.EqualValues(t, 1001, serving(m), "the new certificate is served after the write")
	assert.Equal(t, s0+1, success.Get())
	assert.Greater(t, metrics.AgentCertRemainingSeconds.Get(), (89 * 24 * time.Hour).Seconds())

	// The next cycle sees 90 days left and does nothing.
	m.maybeRenew(t.Context(), false)
	assert.Equal(t, 1, hub.calls)
}

func TestMaybeRenew_HubRequested(t *testing.T) {
	m, hub, _, _ := newTestCertManager(t, 80*24*time.Hour)
	m.maybeRenew(t.Context(), true)
	assert.Equal(t, 1, hub.calls)
	assert.EqualValues(t, 1001, serving(m))
}

func TestMaybeRenew_FailureBacksOff(t *testing.T) {
	failure := metrics.AgentCertRenewalsTotal(metrics.OutcomeFailure)
	f0 := failure.Get()
	m, hub, secrets, now := newTestCertManager(t, 10*24*time.Hour)
	hub.err = errors.New("hub renew: unexpected 503")

	m.maybeRenew(t.Context(), false)
	assert.Equal(t, 1, hub.calls)
	assert.EqualValues(t, 1, serving(m), "a failed renewal keeps the current certificate")
	assert.Empty(t, secrets.writes)
	assert.Equal(t, f0+1, failure.Get())

	*now = now.Add(30 * time.Second)
	m.maybeRenew(t.Context(), true)
	assert.Equal(t, 1, hub.calls, "no retry inside the first minute, even when the hub asks")

	*now = now.Add(31 * time.Second)
	m.maybeRenew(t.Context(), false)
	assert.Equal(t, 2, hub.calls, "retried after one minute")

	*now = now.Add(time.Minute + time.Second)
	m.maybeRenew(t.Context(), false)
	assert.Equal(t, 2, hub.calls, "the second wait is two minutes")
	*now = now.Add(time.Minute)
	m.maybeRenew(t.Context(), false)
	assert.Equal(t, 3, hub.calls)

	// Success resets the backoff.
	hub.err = nil
	*now = now.Add(4 * time.Minute)
	m.maybeRenew(t.Context(), false)
	assert.Equal(t, 4, hub.calls)
	assert.EqualValues(t, 1001, serving(m))
	assert.Zero(t, m.failures)
	assert.True(t, m.nextAttempt.IsZero())
}

func TestMaybeRenew_BadCertificateIsAFailure(t *testing.T) {
	m, _, secrets, _ := newTestCertManager(t, 10*24*time.Hour)
	m.hub = badCertRenewer{}
	m.maybeRenew(t.Context(), false)
	assert.EqualValues(t, 1, serving(m))
	assert.Empty(t, secrets.writes)
	assert.Equal(t, 1, m.failures)
}

// badCertRenewer answers with a certificate for some other key.
type badCertRenewer struct{}

func (badCertRenewer) RenewCertificate(context.Context, string) (renewResponse, error) {
	return renewResponse{Certificate: "not a certificate"}, nil
}

func TestMaybeRenew_SecretWriteFailure(t *testing.T) {
	m, hub, secrets, _ := newTestCertManager(t, 10*24*time.Hour)
	secrets.err = errors.New("k8s API returned 403")

	m.maybeRenew(t.Context(), false)
	assert.Equal(t, 1, hub.calls)
	assert.EqualValues(t, 1001, serving(m), "the issued certificate is used in memory")
	require.NotNil(t, m.unsaved)
	assert.Zero(t, m.failures, "the renewal itself succeeded")

	m.maybeRenew(t.Context(), false)
	assert.Equal(t, 1, hub.calls, "no second renewal")
	assert.NotNil(t, m.unsaved, "the write is still pending")

	secrets.err = nil
	m.maybeRenew(t.Context(), false)
	assert.Nil(t, m.unsaved)
	require.Len(t, secrets.writes, 1)
	written, err := tls.X509KeyPair(secrets.writes[0].Cert, secrets.writes[0].Key)
	require.NoError(t, err)
	assert.EqualValues(t, 1001, written.Leaf.SerialNumber.Int64())
}

func TestMaybeRenew_WithoutSecretName(t *testing.T) {
	m, hub, secrets, _ := newTestCertManager(t, 10*24*time.Hour)
	m.secretName = ""
	m.maybeRenew(t.Context(), false)
	assert.Equal(t, 1, hub.calls)
	assert.Empty(t, secrets.writes)
	assert.Nil(t, m.unsaved)
	assert.EqualValues(t, 1001, serving(m))
}

// TestGetClientCertificateSwap drives the real hub HTTP client against a TLS
// server that requires a client certificate: after a renewal the very next
// request presents the new certificate, with no new client or restart.
func TestGetClientCertificateSwap(t *testing.T) {
	ca := newTestCA(t)
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	serverKeyDER, err := x509.MarshalECPrivateKey(serverKey)
	require.NoError(t, err)
	serverPair, err := tls.X509KeyPair(
		ca.sign(t, &serverKey.PublicKey, "hub", 7, time.Hour, x509.ExtKeyUsageServerAuth, "127.0.0.1"),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: serverKeyDER}))
	require.NoError(t, err)

	var mu sync.Mutex
	var seen []int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.TLS.PeerCertificates[0].SerialNumber.Int64())
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverPair},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    ca.pool(),
		MinVersion:   tls.VersionTLS13,
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.crt")
	require.NoError(t, os.WriteFile(caFile, ca.pemBytes(), 0o600))
	cfg := agentConfig{HubURL: srv.URL, ClusterID: testClusterID, CAFile: caFile, CertRenewBefore: defaultCertRenewBefore}

	hub := &fakeRenewer{t: t, ca: ca, ttl: 90 * 24 * time.Hour, serial: 1000}
	m := newCertManager(cfg, ca.clientPair(t, 1, 10*24*time.Hour), hub, &fakeSecrets{})
	m.secretName = ""
	client, err := buildHubHTTPClient(cfg, m)
	require.NoError(t, err)
	hc := newHubClient(srv.URL, testClusterID, client, false)

	_, err = hc.Heartbeat(t.Context(), "test")
	require.NoError(t, err)
	m.maybeRenew(t.Context(), false)
	_, err = hc.Heartbeat(t.Context(), "test")
	require.NoError(t, err)
	_, err = hc.Heartbeat(t.Context(), "test")
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []int64{1, 1001, 1001}, seen)
}

func TestHeartbeatTick_RenewFlag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pending_commands":[],"renew":true}`))
	}))
	t.Cleanup(srv.Close)

	m, hub, _, _ := newTestCertManager(t, 80*24*time.Hour)
	a := &agent{
		cfg:    agentConfig{AgentVersionStr: "test"},
		hub:    newHubClient(srv.URL, testClusterID, srv.Client(), true),
		health: newHealthState(time.Minute),
		certs:  m,
	}
	require.NoError(t, a.heartbeatTick(t.Context()))
	assert.Equal(t, 1, hub.calls, "renew: true triggers a renewal despite 80 days left")
	assert.EqualValues(t, 1001, serving(m))
}

func TestHeartbeatTick_NoRenewOn204(t *testing.T) {
	a := newHeartbeatAgent(t, http.StatusNoContent)
	m, hub, _, _ := newTestCertManager(t, 80*24*time.Hour)
	a.certs = m
	require.NoError(t, a.heartbeatTick(t.Context()))
	assert.Zero(t, hub.calls)
}

func TestHubClientRenewCertificate(t *testing.T) {
	var gotPath, gotBody string
	status := http.StatusOK
	body := `{"certificate":"CERT","ca_certificate":"CA","serial":"42","not_after":"2027-01-01T00:00:00Z"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	hc := newHubClient(srv.URL, testClusterID, srv.Client(), false)

	resp, err := hc.RenewCertificate(t.Context(), "CSR")
	require.NoError(t, err)
	assert.Equal(t, "/api/v1/clusters/"+testClusterID+"/agent/renew", gotPath)
	assert.JSONEq(t, `{"csr":"CSR"}`, gotBody)
	assert.Equal(t, renewResponse{Certificate: "CERT", CACertificate: "CA", Serial: "42", NotAfter: "2027-01-01T00:00:00Z"}, resp)

	status, body = http.StatusUnprocessableEntity, `{"code":"invalid_csr"}`
	_, err = hc.RenewCertificate(t.Context(), "CSR")
	require.ErrorContains(t, err, "unexpected 422")

	status, body = http.StatusOK, `{}`
	_, err = hc.RenewCertificate(t.Context(), "CSR")
	require.ErrorContains(t, err, "no certificate")

	status, body = http.StatusOK, `not json`
	_, err = hc.RenewCertificate(t.Context(), "CSR")
	require.ErrorContains(t, err, "decode response")

	hc.baseURL = "http://127.0.0.1:1"
	_, err = hc.RenewCertificate(t.Context(), "CSR")
	require.ErrorContains(t, err, "hub renew")
}

func TestLoadCertManager(t *testing.T) {
	ca := newTestCA(t)
	pair := ca.clientPair(t, 5, 40*24*time.Hour)
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	require.NoError(t, os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pair.Certificate[0]}), 0o600))
	keyDER := x509.MarshalPKCS1PrivateKey(pair.PrivateKey.(*rsa.PrivateKey))
	require.NoError(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyDER}), 0o600))

	m, err := loadCertManager(agentConfig{CertFile: certFile, KeyFile: keyFile, CertRenewBefore: time.Hour})
	require.NoError(t, err)
	require.NotNil(t, m)
	assert.EqualValues(t, 5, serving(m))
	assert.Equal(t, time.Hour, m.renewBefore)

	m, err = loadCertManager(agentConfig{DevMode: true, CertFile: certFile, KeyFile: keyFile})
	require.NoError(t, err)
	assert.Nil(t, m, "dev mode has no certificate to renew")

	m, err = loadCertManager(agentConfig{})
	require.NoError(t, err)
	assert.Nil(t, m)

	_, err = loadCertManager(agentConfig{CertFile: filepath.Join(dir, "missing"), KeyFile: keyFile})
	require.ErrorContains(t, err, "load agent cert/key")
}

func TestLoadConfig_CertRenewal(t *testing.T) {
	t.Setenv("SAIDKICK_HUB_URL", "https://appstore.example.com")
	t.Setenv("SAIDKICK_CLUSTER_ID", testClusterID)
	t.Setenv("ARGOCD_SERVER_URL", "https://argocd.local")
	t.Setenv("ARGOCD_AUTH_TOKEN", "token")

	t.Setenv("SAIDKICK_CERT_RENEW_BEFORE", "")
	t.Setenv("SAIDKICK_K8S_SECRET_NAME", "")
	cfg, err := loadConfig()
	require.NoError(t, err)
	assert.Equal(t, 720*time.Hour, cfg.CertRenewBefore)
	assert.Empty(t, cfg.K8sSecretName)

	t.Setenv("SAIDKICK_CERT_RENEW_BEFORE", "47h58m")
	t.Setenv("SAIDKICK_K8S_SECRET_NAME", "saidkick-agent-credentials")
	cfg, err = loadConfig()
	require.NoError(t, err)
	assert.Equal(t, 47*time.Hour+58*time.Minute, cfg.CertRenewBefore)
	assert.Equal(t, "saidkick-agent-credentials", cfg.K8sSecretName)
}

func TestCertMetricsCarryNoIdentifiers(t *testing.T) {
	m, _, _, _ := newTestCertManager(t, 10*24*time.Hour)
	m.maybeRenew(t.Context(), false)

	code, body := probe(t, metricsHandler(), "/metrics")
	require.Equal(t, http.StatusOK, code)
	for _, want := range []string{
		`agent_cert_renewals_total{outcome="success"}`,
		`agent_cert_renewals_total{outcome="failure"}`,
		"agent_cert_remaining_seconds ",
	} {
		assert.Contains(t, body, want)
	}
	assert.NotContains(t, body, "agent_cert_min_remaining_seconds", "the hub gauge stays off the agent's endpoint")
	testutil.RequireNoIdentifyingLabels(t)
}
