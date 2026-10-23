package handlers_test

import (
	"cmp"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/agentcreds"
	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/handlers"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/vault"
	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── Test doubles ────────────────────────────────────────────────────────────

// renewPKI is a vault.PKISigner with one CA for its lifetime and a random
// serial per certificate. err, when set, is returned instead of signing.
type renewPKI struct {
	mu  sync.Mutex
	ca  *x509.Certificate
	key *ecdsa.PrivateKey
	err error
	// issueCN, when set, replaces the requested CN, as a PKI role with
	// use_csr_common_name=true would.
	issueCN string
	calls   []renewPKICall
}

type renewPKICall struct {
	commonName string
	ttl        time.Duration
}

func newRenewPKI(t *testing.T) *renewPKI {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "renew test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(100 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	ca, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &renewPKI{ca: ca, key: key}
}

func (p *renewPKI) SignCSR(_ context.Context, csrPEM, commonName string, ttl time.Duration) (string, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, renewPKICall{commonName: commonName, ttl: ttl})
	if p.err != nil {
		return "", "", p.err
	}
	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil {
		return "", "", errors.New("renew pki: no PEM block")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return "", "", err
	}
	leaf := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cmp.Or(p.issueCN, commonName)},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(ttl).Truncate(time.Second),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, p.ca, csr.PublicKey, p.key)
	if err != nil {
		return "", "", err
	}
	return encodePEM("CERTIFICATE", der), encodePEM("CERTIFICATE", p.ca.Raw), nil
}

func (p *renewPKI) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

// agentCSR returns a CSR for clusterID's agent CN, built the way the agent
// builds it.
func agentCSR(t *testing.T, clusterID uuid.UUID) string {
	t.Helper()
	_, csr, err := agentcreds.NewKeyAndCSR(clusterID.String())
	require.NoError(t, err)
	return csr
}

func renewBody(t *testing.T, csr string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"csr": csr})
	require.NoError(t, err)
	return string(b)
}

// renewCtx builds an Echo context for AgentRenewCertificate: the agent
// identity is certID, the path parameter is pathID.
func renewCtx(certID uuid.UUID, pathID, body string) (echo.Context, *httptest.ResponseRecorder) {
	req := asAgent(newRequest(http.MethodPost, "/", body), certID)
	c, rec := echoContext(echo.New(), req, "id", pathID)
	return c, rec
}

// reloadCluster reads the cluster row back.
func reloadCluster(t *testing.T, f agentFixture) models.Cluster {
	t.Helper()
	var got models.Cluster
	require.NoError(t, f.handle.RW.WithContext(t.Context()).First(&got, "id = ?", f.cluster.ID).Error)
	return got
}

// newRenewFixture is an agent fixture whose cluster has an active
// certificate serial and an outstanding rotation request, served by a
// Handlers wired to pki and recording audit events.
func newRenewFixture(t *testing.T, pki vault.PKISigner) (agentFixture, *handlers.Handlers, *recordingAuditLogger) {
	t.Helper()
	f, _ := newAgentFixture(t)
	requested := time.Now().UTC().Add(-time.Minute)
	require.NoError(t, f.handle.RW.Model(&models.Cluster{}).Where("id = ?", f.cluster.ID).Updates(map[string]any{
		"agent_cert_serial":                "1",
		"agent_cert_rotation_requested_at": requested,
	}).Error)
	h := newTestHandlers(t, f.handle, withPKI(pki))
	rec := &recordingAuditLogger{}
	h.Audit = rec
	return f, h, rec
}

// ─── POST /clusters/:id/agent/renew ─────────────────────────────────────────

// TestAgentRenewCertificate_CheckOrder walks the contract's check order:
// each case fails the check under test while every later check would fail
// too, so the status proves which check ran first.
func TestAgentRenewCertificate_CheckOrder(t *testing.T) {
	t.Parallel()
	failingPKI := newRenewPKI(t)
	failingPKI.err = errors.New("vault down")
	f, h, _ := newRenewFixture(t, failingPKI)
	other := seed.SeedCluster(t, f.handle, models.Cluster{State: models.ClusterReady})
	gone := seed.SeedCluster(t, f.handle, models.Cluster{State: models.ClusterDecommissioned})
	badCSR := renewBody(t, "not a csr")

	t.Run("no_mtls_identity_returns_401", func(t *testing.T) {
		c, _ := echoContext(echo.New(), newRequest(http.MethodPost, "/", badCSR), "id", f.cluster.ID.String())
		requireAPIError(t, h.AgentRenewCertificate(c), http.StatusUnauthorized)
	})

	t.Run("path_id_of_another_cluster_returns_403", func(t *testing.T) {
		c, _ := renewCtx(f.cluster.ID, other.ID.String(), badCSR)
		requireAPIError(t, h.AgentRenewCertificate(c), http.StatusForbidden)
	})

	t.Run("malformed_path_id_returns_403", func(t *testing.T) {
		c, _ := renewCtx(f.cluster.ID, "not-a-uuid", badCSR)
		requireAPIError(t, h.AgentRenewCertificate(c), http.StatusForbidden)
	})

	t.Run("unknown_cluster_returns_404", func(t *testing.T) {
		missing := uuid.New()
		c, _ := renewCtx(missing, missing.String(), badCSR)
		requireAPIError(t, h.AgentRenewCertificate(c), http.StatusNotFound)
	})

	t.Run("decommissioned_cluster_returns_410", func(t *testing.T) {
		c, _ := renewCtx(gone.ID, gone.ID.String(), badCSR)
		requireAPIError(t, h.AgentRenewCertificate(c), http.StatusGone)
	})

	t.Run("invalid_csr_returns_422_before_pki", func(t *testing.T) {
		c, _ := renewCtx(f.cluster.ID, f.cluster.ID.String(), badCSR)
		apiErr := requireAPIError(t, h.AgentRenewCertificate(c), http.StatusUnprocessableEntity)
		assert.Equal(t, "invalid_csr", apiErr.Code)
	})

	t.Run("pki_failure_returns_503", func(t *testing.T) {
		c, _ := renewCtx(f.cluster.ID, f.cluster.ID.String(), renewBody(t, agentCSR(t, f.cluster.ID)))
		apiErr := requireAPIError(t, h.AgentRenewCertificate(c), http.StatusServiceUnavailable)
		assert.Equal(t, "pki_unavailable", apiErr.Code)
	})

	assert.Equal(t, 1, failingPKI.callCount(), "only the request that passed every earlier check reached the PKI")
	got := reloadCluster(t, f)
	assert.Nil(t, got.AgentCertPendingSerial, "no failed renewal records a pending serial")
	assert.NotNil(t, got.AgentCertRotationRequestedAt, "no failed renewal clears the rotation request")
}

func TestAgentRenewCertificate_InvalidCSR(t *testing.T) {
	t.Parallel()
	pki := newRenewPKI(t)
	f, h, _ := newRenewFixture(t, pki)
	otherCluster := uuid.New()

	certPEM, _, err := pki.SignCSR(t.Context(), agentCSR(t, f.cluster.ID), "x", time.Hour)
	require.NoError(t, err)
	pki.calls = nil
	tampered := tamperCSRSignature(t, agentCSR(t, f.cluster.ID))

	cases := map[string]string{
		"missing":             "",
		"not_pem":             "garbage",
		"pem_but_not_pkcs10":  certPEM,
		"wrong_common_name":   agentCSR(t, otherCluster),
		"generic_common_name": newCSR(t),
		"bad_signature":       tampered,
	}
	for name, csr := range cases {
		t.Run(name+"_returns_422", func(t *testing.T) {
			c, _ := renewCtx(f.cluster.ID, f.cluster.ID.String(), renewBody(t, csr))
			apiErr := requireAPIError(t, h.AgentRenewCertificate(c), http.StatusUnprocessableEntity)
			assert.Equal(t, "invalid_csr", apiErr.Code)
		})
	}

	t.Run("invalid_json_returns_400", func(t *testing.T) {
		c, _ := renewCtx(f.cluster.ID, f.cluster.ID.String(), `{"csr":`)
		requireAPIError(t, h.AgentRenewCertificate(c), http.StatusBadRequest)
	})
	assert.Zero(t, pki.callCount())
}

// tamperCSRSignature flips a bit in the signature of csrPEM so it still
// parses but no longer verifies.
func tamperCSRSignature(t *testing.T, csrPEM string) string {
	t.Helper()
	block, _ := pem.Decode([]byte(csrPEM))
	require.NotNil(t, block)
	der := append([]byte(nil), block.Bytes...)
	der[len(der)-1] ^= 0x01
	_, err := x509.ParseCertificateRequest(der)
	require.NoError(t, err, "the tampered request still parses")
	return encodePEM("CERTIFICATE REQUEST", der)
}

func TestAgentRenewCertificate_PKINotConfigured(t *testing.T) {
	t.Parallel()
	f, h, _ := newRenewFixture(t, vault.NullPKIClient{})
	c, _ := renewCtx(f.cluster.ID, f.cluster.ID.String(), renewBody(t, agentCSR(t, f.cluster.ID)))
	apiErr := requireAPIError(t, h.AgentRenewCertificate(c), http.StatusServiceUnavailable)
	assert.Equal(t, "pki_unavailable", apiErr.Code)
}

func TestAgentRenewCertificate_Success(t *testing.T) {
	t.Parallel()
	pki := newRenewPKI(t)
	f, h, audits := newRenewFixture(t, pki)
	h.AgentCertTTL = 48 * time.Hour
	success := metrics.HubAgentCertRenewalsTotal(metrics.OutcomeSuccess)
	s0 := success.Get()

	c, rec := renewCtx(f.cluster.ID, f.cluster.ID.String(), renewBody(t, agentCSR(t, f.cluster.ID)))
	require.NoError(t, h.AgentRenewCertificate(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp handlers.AgentRenewCertificateResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	block, _ := pem.Decode([]byte(resp.Certificate))
	require.NotNil(t, block)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	assert.Equal(t, "saidkick-agent-"+f.cluster.ID.String(), cert.Subject.CommonName)
	assert.Equal(t, cert.SerialNumber.String(), resp.Serial, "serial is decimal")
	assert.Equal(t, cert.NotAfter.UTC().Format(time.RFC3339), resp.NotAfter)
	assert.Equal(t, encodePEM("CERTIFICATE", pki.ca.Raw), resp.CACertificate)

	require.Len(t, pki.calls, 1)
	assert.Equal(t, "saidkick-agent-"+f.cluster.ID.String(), pki.calls[0].commonName)
	assert.Equal(t, 48*time.Hour, pki.calls[0].ttl, "the configured TTL is used")

	got := reloadCluster(t, f)
	require.NotNil(t, got.AgentCertPendingSerial)
	assert.Equal(t, resp.Serial, *got.AgentCertPendingSerial, "the new serial is pending")
	require.NotNil(t, got.AgentCertSerial)
	assert.Equal(t, "1", *got.AgentCertSerial, "the current certificate stays active until first use")
	assert.Nil(t, got.AgentCertRotationRequestedAt, "the rotation request is cleared")
	assert.GreaterOrEqual(t, success.Get(), s0+1, "other tests run in parallel and may count too")

	require.Len(t, audits.events, 1)
	ev := audits.events[0]
	assert.Equal(t, "cluster.credential_renewed", ev.Type)
	assert.Equal(t, "cluster", ev.SubjectType)
	assert.Equal(t, f.cluster.ID, ev.SubjectID)
	assert.Nil(t, ev.ActorUserID)
	assert.Equal(t, map[string]any{"actor": "agent", "serial": resp.Serial, "not_after": resp.NotAfter}, ev.Payload)

	// A second renewal before first use replaces the pending serial.
	c, rec = renewCtx(f.cluster.ID, f.cluster.ID.String(), renewBody(t, agentCSR(t, f.cluster.ID)))
	require.NoError(t, h.AgentRenewCertificate(c))
	var second handlers.AgentRenewCertificateResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &second))
	got = reloadCluster(t, f)
	assert.Equal(t, second.Serial, *got.AgentCertPendingSerial)
	assert.NotEqual(t, resp.Serial, second.Serial)
}

// TestAgentRenewCertificate_IssuedCNMismatch refuses a certificate whose CN
// the PKI took from somewhere other than the request, and records nothing.
func TestAgentRenewCertificate_IssuedCNMismatch(t *testing.T) {
	t.Parallel()
	pki := newRenewPKI(t)
	pki.issueCN = "saidkick-agent-" + uuid.NewString()
	f, h, _ := newRenewFixture(t, pki)
	c, _ := renewCtx(f.cluster.ID, f.cluster.ID.String(), renewBody(t, agentCSR(t, f.cluster.ID)))
	err := h.AgentRenewCertificate(c)
	require.ErrorContains(t, err, "does not match requested")
	assert.Nil(t, reloadCluster(t, f).AgentCertPendingSerial)
}

func TestAgentRenewCertificate_DefaultTTL(t *testing.T) {
	t.Parallel()
	pki := newRenewPKI(t)
	f, h, _ := newRenewFixture(t, pki)
	c, _ := renewCtx(f.cluster.ID, f.cluster.ID.String(), renewBody(t, agentCSR(t, f.cluster.ID)))
	require.NoError(t, h.AgentRenewCertificate(c))
	require.Len(t, pki.calls, 1)
	assert.Equal(t, 90*24*time.Hour, pki.calls[0].ttl)
}

func TestAgentRenewCertificate_CountsFailures(t *testing.T) {
	t.Parallel()
	f, h, _ := newRenewFixture(t, newRenewPKI(t))
	failure := metrics.HubAgentCertRenewalsTotal(metrics.OutcomeFailure)
	f0 := failure.Get()
	c, _ := renewCtx(f.cluster.ID, f.cluster.ID.String(), renewBody(t, "bad"))
	requireAPIError(t, h.AgentRenewCertificate(c), http.StatusUnprocessableEntity)
	assert.GreaterOrEqual(t, failure.Get(), f0+1)
	testutil.RequireNoIdentifyingLabels(t)
}

// TestAgentRenewCertificate_ConcurrentRenewals renews from many requests at
// once: the row lock serialises them, each succeeds, and the pending serial
// is one of the issued ones.
func TestAgentRenewCertificate_ConcurrentRenewals(t *testing.T) {
	t.Parallel()
	pki := newRenewPKI(t)
	f, h, _ := newRenewFixture(t, pki)

	const workers = 4
	serials := make([]string, workers)
	errs := make([]error, workers)
	bodies := make([]string, workers)
	for i := range workers {
		bodies[i] = renewBody(t, agentCSR(t, f.cluster.ID))
	}
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() {
			c, rec := renewCtx(f.cluster.ID, f.cluster.ID.String(), bodies[i])
			if errs[i] = h.AgentRenewCertificate(c); errs[i] != nil {
				return
			}
			var resp handlers.AgentRenewCertificateResponse
			errs[i] = json.Unmarshal(rec.Body.Bytes(), &resp)
			serials[i] = resp.Serial
		})
	}
	wg.Wait()
	for i := range workers {
		require.NoError(t, errs[i])
	}
	got := reloadCluster(t, f)
	require.NotNil(t, got.AgentCertPendingSerial)
	assert.Contains(t, serials, *got.AgentCertPendingSerial)
}

// ─── Route wiring ───────────────────────────────────────────────────────────

func TestAgentRenewRouteOnAgentListenerOnly(t *testing.T) {
	t.Parallel()
	h := newTestHandlers(t, nil)
	path := "/api/v1/clusters/" + uuid.NewString() + "/agent/renew"

	rec := serveRoute(h, newRequest(http.MethodPost, path, `{}`))
	assert.Equal(t, http.StatusNotFound, rec.Code, "the main listener does not serve the renew route")

	agentEcho := echo.New()
	h.RegisterAgent(agentEcho)
	rec = httptest.NewRecorder()
	agentEcho.ServeHTTP(rec, newRequest(http.MethodPost, path, `{}`))
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "the agent listener serves it behind agent auth")
}

// ─── POST /clusters/:id/rotate-credential ───────────────────────────────────

func rotatePath(id uuid.UUID) string {
	return "/api/v1/clusters/" + id.String() + "/rotate-credential"
}

func TestRotateClusterCredential_Authz(t *testing.T) {
	t.Parallel()
	f, h := newClusterWriteFixture(t)
	admin := principal(f.caller, models.RoleClusterAdmin)

	t.Run("anonymous_returns_401", func(t *testing.T) {
		rec := serveRoute(h, newRequest(http.MethodPost, rotatePath(f.public.ID), ""))
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
	for _, role := range []models.Role{models.RoleDeveloper, models.RoleAuditor, models.RoleDeployer, models.RoleOrgAdmin} {
		t.Run(string(role)+"_returns_403", func(t *testing.T) {
			req := asPrincipal(newRequest(http.MethodPost, rotatePath(f.ownRestricted.ID), ""), principal(f.caller, role))
			assert.Equal(t, http.StatusForbidden, serveRoute(h, req).Code)
		})
	}

	cases := []struct {
		name    string
		cluster models.Cluster
		want    int
	}{
		{"other_orgs_org_cluster_is_hidden_404", f.orgScoped, http.StatusNotFound},
		{"other_orgs_restricted_cluster_is_hidden_404", f.restricted, http.StatusNotFound},
		{"granted_restricted_cluster_is_visible_403", f.grantedRestr, http.StatusForbidden},
		{"public_cluster_not_owned_403", f.public, http.StatusForbidden},
	}
	rotate := func(p *auth.Principal, id string) (int, error) {
		c, rec := echoContext(echo.New(), asPrincipal(newRequest(http.MethodPost, "/", ""), p), "id", id)
		err := h.RotateClusterCredential(c)
		return rec.Code, err
	}
	for _, tc := range cases {
		t.Run("cluster_admin_"+tc.name, func(t *testing.T) {
			_, err := rotate(admin, tc.cluster.ID.String())
			requireAPIError(t, err, tc.want)
		})
	}

	t.Run("unknown_cluster_returns_404", func(t *testing.T) {
		_, err := rotate(admin, uuid.NewString())
		requireAPIError(t, err, http.StatusNotFound)
	})

	t.Run("malformed_id_returns_404", func(t *testing.T) {
		_, err := rotate(admin, "nope")
		requireAPIError(t, err, http.StatusNotFound)
	})

	t.Run("cluster_admin_of_owning_org_returns_202", func(t *testing.T) {
		code, err := rotate(admin, f.ownRestricted.ID.String())
		require.NoError(t, err)
		assert.Equal(t, http.StatusAccepted, code)
	})

	t.Run("platform_admin_returns_202_on_any_cluster", func(t *testing.T) {
		code, err := rotate(principal(f.caller, models.RolePlatformAdmin), f.orgScoped.ID.String())
		require.NoError(t, err)
		assert.Equal(t, http.StatusAccepted, code)
	})

	t.Run("route_admits_cluster_admin", func(t *testing.T) {
		req := asPrincipal(newRequest(http.MethodPost, rotatePath(f.ownRestricted.ID), ""), admin)
		assert.Equal(t, http.StatusAccepted, serveRoute(h, req).Code)
	})

	t.Run("no_principal_in_handler_returns_401", func(t *testing.T) {
		c, _ := echoContext(echo.New(), newRequest(http.MethodPost, "/", ""), "id", f.public.ID.String())
		requireAPIError(t, h.RotateClusterCredential(c), http.StatusUnauthorized)
	})
}

// TestRotateClusterCredential_Flow requests a rotation and follows it
// through the heartbeat and the renewal that clears it.
func TestRotateClusterCredential_Flow(t *testing.T) {
	t.Parallel()
	pki := newRenewPKI(t)
	f, _ := newAgentFixture(t)
	h := newTestHandlers(t, f.handle, withPKI(pki))
	audits := &recordingAuditLogger{}
	h.Audit = audits
	ownerOrg := f.org.ID
	require.NoError(t, f.handle.RW.Model(&models.Cluster{}).Where("id = ?", f.cluster.ID).Updates(map[string]any{
		"scope": models.ClusterScopeOrg, "owning_organization_id": ownerOrg,
	}).Error)

	heartbeat := func() (int, map[string]any) {
		t.Helper()
		c, rec := agentCtx(echo.New(), f.cluster.ID, `{}`)
		require.NoError(t, h.AgentHeartbeat(c))
		var body map[string]any
		if rec.Code == http.StatusOK {
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		}
		return rec.Code, body
	}

	code, _ := heartbeat()
	assert.Equal(t, http.StatusNoContent, code, "no rotation requested yet")

	before := time.Now().UTC().Truncate(time.Second)
	c, rec := echoContext(echo.New(),
		asPrincipal(newRequest(http.MethodPost, "/", ""), principal(f.user, models.RoleClusterAdmin)),
		"id", f.cluster.ID.String())
	require.NoError(t, h.RotateClusterCredential(c))
	require.Equal(t, http.StatusAccepted, rec.Code)
	var rotated handlers.RotateClusterCredentialResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rotated))
	at, err := time.Parse(time.RFC3339, rotated.RotationRequestedAt)
	require.NoError(t, err)
	assert.False(t, at.Before(before))

	got := reloadCluster(t, f)
	require.NotNil(t, got.AgentCertRotationRequestedAt)
	assert.True(t, at.Equal(*got.AgentCertRotationRequestedAt))

	require.Len(t, audits.events, 1)
	assert.Equal(t, "cluster.credential_rotation_requested", audits.events[0].Type)
	require.NotNil(t, audits.events[0].ActorUserID)
	assert.Equal(t, f.user.ID, *audits.events[0].ActorUserID)

	// Every heartbeat carries renew: true until the agent renews.
	for range 2 {
		code, body := heartbeat()
		require.Equal(t, http.StatusOK, code)
		assert.Equal(t, true, body["renew"])
		assert.Equal(t, []any{}, body["pending_commands"], "pending_commands stays an array")
	}

	// GetCluster shows the pending rotation.
	gc, grec := echoContext(echo.New(), asPrincipal(newRequest(http.MethodGet, "/", ""), principal(f.user)), "id", f.cluster.ID.String())
	require.NoError(t, h.GetCluster(gc))
	var detail map[string]any
	require.NoError(t, json.Unmarshal(grec.Body.Bytes(), &detail))
	assert.NotNil(t, detail["agent_cert_rotation_requested_at"])

	rc, _ := renewCtx(f.cluster.ID, f.cluster.ID.String(), renewBody(t, agentCSR(t, f.cluster.ID)))
	require.NoError(t, h.AgentRenewCertificate(rc))

	code, _ = heartbeat()
	assert.Equal(t, http.StatusNoContent, code, "the renewal cleared the request")
}

func TestAgentHeartbeat_RenewWithCommands(t *testing.T) {
	t.Parallel()
	f, h := newAgentFixture(t)
	queueCommand(t, f, "delete", `{"app_name":"app-1"}`, false)
	require.NoError(t, f.handle.RW.Model(&models.Cluster{}).Where("id = ?", f.cluster.ID).
		Update("agent_cert_rotation_requested_at", time.Now().UTC()).Error)

	c, rec := agentCtx(echo.New(), f.cluster.ID, `{}`)
	require.NoError(t, h.AgentHeartbeat(c))
	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		PendingCommands []handlers.PendingCommandItem `json:"pending_commands"`
		Renew           bool                          `json:"renew"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.True(t, body.Renew)
	assert.Len(t, body.PendingCommands, 1)
}

func TestAgentHeartbeat_NoRenewKeyWithoutRequest(t *testing.T) {
	t.Parallel()
	f, h := newAgentFixture(t)
	queueCommand(t, f, "delete", `{"app_name":"app-1"}`, false)
	c, rec := agentCtx(echo.New(), f.cluster.ID, `{}`)
	require.NoError(t, h.AgentHeartbeat(c))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "renew")
}

// ─── GetCluster and handshake ───────────────────────────────────────────────

func TestGetCluster_CertificateFields(t *testing.T) {
	t.Parallel()
	f, h := newAgentFixture(t)
	get := func() map[string]any {
		t.Helper()
		c, rec := echoContext(echo.New(), asPrincipal(newRequest(http.MethodGet, "/", ""), principal(f.user)), "id", f.cluster.ID.String())
		require.NoError(t, h.GetCluster(c))
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out
	}

	out := get()
	assert.Contains(t, out, "agent_cert_not_after")
	assert.Nil(t, out["agent_cert_not_after"], "unknown expiry is null")
	assert.Contains(t, out, "agent_cert_rotation_requested_at")
	assert.Nil(t, out["agent_cert_rotation_requested_at"])
	assert.NotContains(t, out, "agent_cert_pending_serial", "the pending serial is internal")

	notAfter := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, f.handle.RW.Model(&models.Cluster{}).Where("id = ?", f.cluster.ID).Updates(map[string]any{
		"agent_cert_not_after":      notAfter,
		"agent_cert_pending_serial": "99",
	}).Error)
	out = get()
	raw, ok := out["agent_cert_not_after"].(string)
	require.True(t, ok)
	parsed, err := time.Parse(time.RFC3339, raw)
	require.NoError(t, err)
	assert.True(t, notAfter.Equal(parsed), "got %s", raw)
	assert.NotContains(t, grecString(out), "agent_cert_pending")
}

func grecString(m map[string]any) string {
	b, _ := json.Marshal(m)
	return string(b)
}

func TestHandshakeCluster_SetsNotAfterAndClearsPending(t *testing.T) {
	t.Parallel()
	f := newHandshakeFixture(t)
	require.NoError(t, f.handle.RW.Model(&models.Cluster{}).Where("id = ?", f.cluster.ID).
		Update("agent_cert_pending_serial", "12345").Error)
	h := newTestHandlers(t, f.handle, withPKI(stubPKI{}))
	h.AgentCertTTL = 48 * time.Hour

	c, rec := handshakeCtx(echo.New(), f.cluster.ID.String(), "Bearer "+validJoinToken(t, f.cluster.ID), handshakeBody(newCSR(t)))
	require.NoError(t, h.HandshakeCluster(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var got models.Cluster
	require.NoError(t, f.handle.RW.First(&got, "id = ?", f.cluster.ID).Error)
	assert.Nil(t, got.AgentCertPendingSerial)
	require.NotNil(t, got.AgentCertNotAfter)
	require.NotNil(t, got.AgentCertExpiresAt)
	assert.True(t, got.AgentCertNotAfter.Equal(*got.AgentCertExpiresAt))
	assert.WithinDuration(t, time.Now().Add(48*time.Hour), *got.AgentCertNotAfter, time.Minute, "the configured TTL is used")
}
