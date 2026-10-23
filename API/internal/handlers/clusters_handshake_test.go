package handlers_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── Test doubles ────────────────────────────────────────────────────────────

// stubCertSerial is the serial number stubPKI puts on every certificate.
const stubCertSerial = 4242

// stubPKI is a vault.PKISigner that signs CSRs with a CA generated per call.
// With useCSRCommonName set it behaves like a PKI role with
// use_csr_common_name=true: the CSR's CN wins over the requested one.
type stubPKI struct {
	useCSRCommonName bool
}

// notPEMPKI is a vault.PKISigner whose issued certificate is not PEM.
type notPEMPKI struct{}

func (notPEMPKI) SignCSR(context.Context, string, string, time.Duration) (certPEM, caPEM string, err error) {
	return "not a certificate", "", nil
}

func (s stubPKI) SignCSR(_ context.Context, csrPEM, commonName string, ttl time.Duration) (certPEM, caPEM string, err error) {
	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil {
		return "", "", errors.New("stub pki: no PEM block")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return "", "", err
	}

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	now := time.Now()
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "stub-ca"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(ttl),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return "", "", err
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return "", "", err
	}

	if s.useCSRCommonName {
		commonName = csr.Subject.CommonName
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(stubCertSerial),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(ttl),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, ca, csr.PublicKey, caKey)
	if err != nil {
		return "", "", err
	}
	return encodePEM("CERTIFICATE", leafDER), encodePEM("CERTIFICATE", caDER), nil
}

func encodePEM(blockType string, der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}))
}

// newCSR returns a PEM-encoded PKCS#10 request for a fresh P-256 key.
func newCSR(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "agent"},
	}, key)
	require.NoError(t, err)
	return encodePEM("CERTIFICATE REQUEST", der)
}

// ─── Join tokens ─────────────────────────────────────────────────────────────

// joinClaims returns valid join-token claims for clusterID: a fresh JTI and
// a one-hour expiry.
func joinClaims(clusterID uuid.UUID) jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"jti":   uuid.NewString(),
		"sub":   clusterID.String(),
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
		"scope": string(models.ClusterScopePublic),
	}
}

// signJoinToken signs claims with method and key. Tests build the token at
// runtime so no token literal ends up in the source.
func signJoinToken(t *testing.T, method jwt.SigningMethod, key any, claims jwt.MapClaims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(method, claims).SignedString(key)
	require.NoError(t, err)
	return s
}

// validJoinToken signs joinClaims(clusterID) with the test join secret.
func validJoinToken(t *testing.T, clusterID uuid.UUID) string {
	t.Helper()
	return signJoinToken(t, jwt.SigningMethodHS256, []byte(testJoinSecret), joinClaims(clusterID))
}

// handshakeCtx builds an Echo context for POST /clusters/:id/handshake.
// An empty authz sends no Authorization header.
func handshakeCtx(e *echo.Echo, clusterID, authz, body string) (echo.Context, *httptest.ResponseRecorder) {
	req := newRequest(http.MethodPost, "/", body)
	if authz != "" {
		req.Header.Set(echo.HeaderAuthorization, authz)
	}
	return echoContext(e, req, "id", clusterID)
}

// handshakeBody returns a handshake request body carrying csr.
func handshakeBody(csr string) string {
	return fmt.Sprintf(`{"agent_version":"1.4.0","csr":%q}`, csr)
}

// ─── Token checks (no database) ──────────────────────────────────────────────

// TestHandshakeCluster_TokenRejected covers tokens the handler rejects
// before it reads the database.
func TestHandshakeCluster_TokenRejected(t *testing.T) {
	t.Parallel()
	h := newTestHandlers(t, nil)
	clusterID := uuid.New()

	expired := joinClaims(clusterID)
	expired["iat"] = time.Now().Add(-2 * time.Hour).Unix()
	expired["exp"] = time.Now().Add(-time.Hour).Unix()

	noJTI := joinClaims(clusterID)
	delete(noJTI, "jti")

	noSubject := joinClaims(clusterID)
	delete(noSubject, "sub")

	cases := map[string]string{
		"missing_header":    "",
		"not_bearer":        "Basic " + validJoinToken(t, clusterID),
		"empty_bearer":      "Bearer ",
		"garbage_token":     "Bearer not-a-jwt",
		"forged_secret":     "Bearer " + signJoinToken(t, jwt.SigningMethodHS256, []byte("wrong-join-key"), joinClaims(clusterID)),
		"expired":           "Bearer " + signJoinToken(t, jwt.SigningMethodHS256, []byte(testJoinSecret), expired),
		"hs512_not_allowed": "Bearer " + signJoinToken(t, jwt.SigningMethodHS512, []byte(testJoinSecret), joinClaims(clusterID)),
		"alg_none":          "Bearer " + signJoinToken(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, joinClaims(clusterID)),
		"missing_jti":       "Bearer " + signJoinToken(t, jwt.SigningMethodHS256, []byte(testJoinSecret), noJTI),
		"missing_subject":   "Bearer " + signJoinToken(t, jwt.SigningMethodHS256, []byte(testJoinSecret), noSubject),
		"other_cluster":     "Bearer " + validJoinToken(t, uuid.New()),
	}
	for name, authz := range cases {
		t.Run(name+"_returns_401", func(t *testing.T) {
			t.Parallel()
			c, _ := handshakeCtx(echo.New(), clusterID.String(), authz, handshakeBody("unused"))
			requireAPIError(t, h.HandshakeCluster(c), http.StatusUnauthorized)
		})
	}

	t.Run("malformed_cluster_id_returns_404", func(t *testing.T) {
		t.Parallel()
		c, _ := handshakeCtx(echo.New(), "not-a-uuid", "Bearer "+validJoinToken(t, clusterID), handshakeBody("unused"))
		requireAPIError(t, h.HandshakeCluster(c), http.StatusNotFound)
	})
}

// ─── Database-backed paths ───────────────────────────────────────────────────

// handshakeFixture holds a cluster awaiting its agent's handshake.
type handshakeFixture struct {
	handle  *db.Handle
	cluster models.Cluster
}

func newHandshakeFixture(t *testing.T) handshakeFixture {
	t.Helper()
	handle := testutil.NewPostgresDB(t)
	org := seed.SeedOrg(t, handle, models.Organization{})
	cluster := seed.SeedCluster(t, handle, models.Cluster{
		Scope:                models.ClusterScopeOrg,
		OwningOrganizationID: &org.ID,
		State:                models.ClusterPendingRegistration,
	})
	return handshakeFixture{handle: handle, cluster: cluster}
}

// usedJoinTokens counts the burnt JTIs recorded for clusterID.
func usedJoinTokens(t *testing.T, handle *db.Handle, clusterID uuid.UUID) int64 {
	t.Helper()
	var n int64
	require.NoError(t, handle.RW.WithContext(t.Context()).
		Model(&models.UsedJoinToken{}).Where("cluster_id = ?", clusterID).Count(&n).Error)
	return n
}

func TestHandshakeCluster(t *testing.T) {
	t.Parallel()
	t.Run("valid_token_issues_cert_and_marks_cluster_ready", func(t *testing.T) {
		t.Parallel()
		f := newHandshakeFixture(t)
		h := newTestHandlers(t, f.handle, withPKI(stubPKI{}))
		e := echo.New()

		c, rec := handshakeCtx(e, f.cluster.ID.String(), "Bearer "+validJoinToken(t, f.cluster.ID), handshakeBody(newCSR(t)))
		require.NoError(t, h.HandshakeCluster(c))
		assert.Equal(t, http.StatusOK, rec.Code)

		var resp struct {
			ClientCertPEM string `json:"client_cert_pem"`
			CAPEM         string `json:"ca_pem"`
			HubEndpoint   string `json:"hub_endpoint"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, "http://localhost:8080", resp.HubEndpoint)
		assert.NotEmpty(t, resp.CAPEM)
		block, _ := pem.Decode([]byte(resp.ClientCertPEM))
		require.NotNil(t, block, "client_cert_pem must be PEM")
		cert, err := x509.ParseCertificate(block.Bytes)
		require.NoError(t, err)
		assert.Equal(t, "saidkick-agent-"+f.cluster.ID.String(), cert.Subject.CommonName)

		var got models.Cluster
		require.NoError(t, f.handle.RW.WithContext(t.Context()).First(&got, "id = ?", f.cluster.ID).Error)
		assert.Equal(t, models.ClusterReady, got.State)
		require.NotNil(t, got.AgentVersion)
		assert.Equal(t, "1.4.0", *got.AgentVersion)
		require.NotNil(t, got.AgentCertSerial)
		assert.Equal(t, fmt.Sprint(stubCertSerial), *got.AgentCertSerial)
		assert.NotNil(t, got.AgentCertExpiresAt)
		assert.NotNil(t, got.RegisteredAt)
		assert.Equal(t, int64(1), usedJoinTokens(t, f.handle, f.cluster.ID))
	})

	t.Run("reused_token_returns_410", func(t *testing.T) {
		t.Parallel()
		f := newHandshakeFixture(t)
		h := newTestHandlers(t, f.handle, withPKI(stubPKI{}))
		e := echo.New()
		token := validJoinToken(t, f.cluster.ID)

		c, rec := handshakeCtx(e, f.cluster.ID.String(), "Bearer "+token, handshakeBody(newCSR(t)))
		require.NoError(t, h.HandshakeCluster(c))
		require.Equal(t, http.StatusOK, rec.Code)

		c, _ = handshakeCtx(e, f.cluster.ID.String(), "Bearer "+token, handshakeBody(newCSR(t)))
		apiErr := requireAPIError(t, h.HandshakeCluster(c), http.StatusGone)
		assert.Equal(t, "join_token_already_used", apiErr.Code)
		assert.Equal(t, int64(1), usedJoinTokens(t, f.handle, f.cluster.ID))
	})

	t.Run("unknown_cluster_returns_404", func(t *testing.T) {
		t.Parallel()
		f := newHandshakeFixture(t)
		h := newTestHandlers(t, f.handle, withPKI(stubPKI{}))
		missing := uuid.New()

		c, _ := handshakeCtx(echo.New(), missing.String(), "Bearer "+validJoinToken(t, missing), handshakeBody(newCSR(t)))
		requireAPIError(t, h.HandshakeCluster(c), http.StatusNotFound)
	})

	t.Run("decommissioned_cluster_returns_410", func(t *testing.T) {
		t.Parallel()
		f := newHandshakeFixture(t)
		h := newTestHandlers(t, f.handle, withPKI(stubPKI{}))
		require.NoError(t, f.handle.RW.WithContext(t.Context()).
			Model(&f.cluster).Update("state", models.ClusterDecommissioned).Error)

		c, _ := handshakeCtx(echo.New(), f.cluster.ID.String(), "Bearer "+validJoinToken(t, f.cluster.ID), handshakeBody(newCSR(t)))
		apiErr := requireAPIError(t, h.HandshakeCluster(c), http.StatusGone)
		assert.Equal(t, "gone", apiErr.Code)
		assert.Zero(t, usedJoinTokens(t, f.handle, f.cluster.ID))
	})

	csrCases := map[string]string{
		"missing_csr":    handshakeBody(""),
		"csr_not_pem":    handshakeBody("not a pem block"),
		"csr_not_pkcs10": handshakeBody(encodePEM("CERTIFICATE REQUEST", []byte("junk"))),
	}
	for name, body := range csrCases {
		t.Run(name+"_returns_422_without_burning_token", func(t *testing.T) {
			t.Parallel()
			f := newHandshakeFixture(t)
			h := newTestHandlers(t, f.handle, withPKI(stubPKI{}))

			c, _ := handshakeCtx(echo.New(), f.cluster.ID.String(), "Bearer "+validJoinToken(t, f.cluster.ID), body)
			requireAPIError(t, h.HandshakeCluster(c), http.StatusUnprocessableEntity)
			assert.Zero(t, usedJoinTokens(t, f.handle, f.cluster.ID))
		})
	}

	// A certificate the hub cannot vouch for must not mark the cluster ready
	// or burn the join token.
	badIssuers := map[string]handlersOption{
		"issued_cn_mismatch":  withPKI(stubPKI{useCSRCommonName: true}),
		"issued_cert_not_pem": withPKI(notPEMPKI{}),
	}
	for name, pki := range badIssuers {
		t.Run(name+"_fails_without_burning_token", func(t *testing.T) {
			t.Parallel()
			f := newHandshakeFixture(t)
			h := newTestHandlers(t, f.handle, pki)

			c, _ := handshakeCtx(echo.New(), f.cluster.ID.String(), "Bearer "+validJoinToken(t, f.cluster.ID), handshakeBody(newCSR(t)))
			require.Error(t, h.HandshakeCluster(c))
			assert.Zero(t, usedJoinTokens(t, f.handle, f.cluster.ID))

			var got models.Cluster
			require.NoError(t, f.handle.RW.WithContext(t.Context()).First(&got, "id = ?", f.cluster.ID).Error)
			assert.Equal(t, models.ClusterPendingRegistration, got.State)
			assert.Nil(t, got.AgentCertSerial)
		})
	}

	t.Run("pki_not_configured_returns_503_without_burning_token", func(t *testing.T) {
		t.Parallel()
		f := newHandshakeFixture(t)
		h := newTestHandlers(t, f.handle) // default NullPKIClient

		c, _ := handshakeCtx(echo.New(), f.cluster.ID.String(), "Bearer "+validJoinToken(t, f.cluster.ID), handshakeBody(newCSR(t)))
		requireAPIError(t, h.HandshakeCluster(c), http.StatusServiceUnavailable)
		assert.Zero(t, usedJoinTokens(t, f.handle, f.cluster.ID))

		var got models.Cluster
		require.NoError(t, f.handle.RW.WithContext(t.Context()).First(&got, "id = ?", f.cluster.ID).Error)
		assert.Equal(t, models.ClusterPendingRegistration, got.State)
	})
}
