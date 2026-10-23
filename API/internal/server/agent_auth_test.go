package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
)

func certWithCN(cn string) *x509.Certificate {
	return &x509.Certificate{Subject: pkix.Name{CommonName: cn}, SerialNumber: big.NewInt(1)}
}

func TestParseAgentCertCN(t *testing.T) {
	id := uuid.New()

	cases := []struct {
		name   string
		cn     string
		wantOK bool
	}{
		{"canonical", "saidkick-agent-" + id.String(), true},
		{"uppercase uuid", "saidkick-agent-" + strings.ToUpper(id.String()), true},
		{"empty cn", "", false},
		{"prefix only", "saidkick-agent-", false},
		{"missing prefix", id.String(), false},
		{"wrong prefix", "saidkick-agnt-" + id.String(), false},
		{"prefix with wrong case", "Saidkick-Agent-" + id.String(), false},
		{"leading space", " saidkick-agent-" + id.String(), false},
		{"not a uuid", "saidkick-agent-not-a-uuid", false},
		{"truncated uuid", "saidkick-agent-" + id.String()[:35], false},
		{"trailing junk", "saidkick-agent-" + id.String() + "-x", false},
		{"other service cn", "saidkick-api-" + id.String(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseAgentCertCN(certWithCN(tc.cn))
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, id, got)
			} else {
				assert.Equal(t, uuid.Nil, got)
			}
		})
	}
}

// TestParseAgentCertCNAndSerialOnIssuedCert runs both checks on a
// certificate signed by a CA generated at runtime, the shape the Vault PKI
// handshake produces.
func TestParseAgentCertCNAndSerialOnIssuedCert(t *testing.T) {
	ca := newTestCA(t, "test agent CA")
	clusterID := uuid.New()
	leaf := ca.agentCert(t, clusterID).Leaf

	got, ok := parseAgentCertCN(leaf)
	require.True(t, ok)
	assert.Equal(t, clusterID, got)

	// The handshake stores SerialNumber.String(); that must round-trip.
	assert.True(t, serialMatches(leaf, leaf.SerialNumber.String()))
	other := ca.agentCert(t, clusterID).Leaf
	assert.False(t, serialMatches(leaf, other.SerialNumber.String()),
		"a second certificate for the same cluster must not match the first serial")
}

func TestSerialMatches(t *testing.T) {
	serial, ok := new(big.Int).SetString("264118031538977813394817716155442381567", 10)
	require.True(t, ok)
	cert := &x509.Certificate{SerialNumber: serial}

	cases := []struct {
		name   string
		stored string
		want   bool
	}{
		{"exact decimal", serial.String(), true},
		{"leading zeros", "000" + serial.String(), true},
		{"surrounding whitespace", "  " + serial.String() + "\n", true},
		{"off by one", new(big.Int).Add(serial, big.NewInt(1)).String(), false},
		{"empty", "", false},
		{"not a number", "not-a-serial", false},
		{"hex form is not decimal", serial.Text(16), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, serialMatches(cert, tc.stored))
		})
	}

	t.Run("certificate without serial", func(t *testing.T) {
		assert.False(t, serialMatches(&x509.Certificate{}, "1"))
	})
}

// fakeLookup is an agentCertStore backed by maps of active and pending
// serials. It records the cluster IDs it was asked about. The mutex makes
// it safe to call from server goroutines while a test rotates a serial.
type fakeLookup struct {
	mu         sync.Mutex
	active     map[uuid.UUID]string
	pending    map[uuid.UUID]string
	err        error
	promoteErr error
	// stealPromotion makes promote report that another request promoted
	// the pending serial first, and performs that promotion itself.
	stealPromotion bool
	calls          []uuid.UUID
	promotions     []string
}

func (f *fakeLookup) setSerial(id uuid.UUID, serial string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active[id] = serial
}

func (f *fakeLookup) serials(_ context.Context, id uuid.UUID) (agentCertSerials, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, id)
	if f.err != nil {
		return agentCertSerials{}, false, f.err
	}
	s, ok := f.active[id]
	return agentCertSerials{Active: s, Pending: f.pending[id]}, ok, nil
}

func (f *fakeLookup) promote(_ context.Context, id uuid.UUID, pending string, _ time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.promoteErr != nil {
		return false, f.promoteErr
	}
	if f.pending[id] != pending {
		return false, nil
	}
	f.active[id] = pending
	delete(f.pending, id)
	if f.stealPromotion {
		return false, nil
	}
	f.promotions = append(f.promotions, pending)
	return true, nil
}

// agentCtx builds an Echo context whose request optionally carries the
// dev-mode header and a TLS peer certificate. verified controls whether the
// certificate is reported as chain-verified, as the agent listener does.
func agentCtx(header string, cert *x509.Certificate, verified bool) echo.Context {
	req := httptest.NewRequest(http.MethodPost, "/agent", http.NoBody)
	if header != "" {
		req.Header.Set(agentClusterIDHeader, header)
	}
	if cert != nil {
		state := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
		if verified {
			state.VerifiedChains = [][]*x509.Certificate{{cert}}
		}
		req.TLS = state
	}
	return echo.New().NewContext(req, httptest.NewRecorder())
}

func TestAuthenticateAgent(t *testing.T) {
	headerID, certID := uuid.New(), uuid.New()
	cert := certWithCN("saidkick-agent-" + certID.String())
	cert.SerialNumber = big.NewInt(4242)
	matching := map[uuid.UUID]string{certID: "4242"}

	cases := []struct {
		name     string
		devMode  bool
		header   string
		cert     *x509.Certificate
		verified bool
		serials  map[uuid.UUID]string
		wantID   uuid.UUID
		wantOK   bool
	}{
		// Production: the header is never trusted.
		{"prod: header only is rejected", false, headerID.String(), nil, false, matching, uuid.Nil, false},
		{"prod: header ignored, cert used", false, headerID.String(), cert, true, matching, certID, true},
		{"prod: valid cert, matching serial", false, "", cert, true, matching, certID, true},
		{"prod: unverified cert is rejected", false, "", cert, false, matching, uuid.Nil, false},
		{"prod: bad cert cn", false, "", certWithCN("CN=someone-else"), true, matching, uuid.Nil, false},
		{"prod: serial mismatch", false, "", cert, true, map[uuid.UUID]string{certID: "4243"}, uuid.Nil, false},
		{"prod: no serial on record", false, "", cert, true, map[uuid.UUID]string{certID: ""}, uuid.Nil, false},
		{"prod: unknown cluster", false, "", cert, true, map[uuid.UUID]string{}, uuid.Nil, false},
		{"prod: nothing", false, "", nil, false, matching, uuid.Nil, false},

		// Dev mode: the header bypasses mTLS.
		{"dev: header only", true, headerID.String(), nil, false, matching, headerID, true},
		{"dev: header padded with spaces", true, "  " + headerID.String() + "  ", nil, false, matching, headerID, true},
		{"dev: header wins over cert", true, headerID.String(), cert, true, matching, headerID, true},
		{"dev: malformed header falls back to cert", true, "not-a-uuid", cert, true, matching, certID, true},
		{"dev: malformed header, no cert", true, "not-a-uuid", nil, false, matching, uuid.Nil, false},
		{"dev: cert still needs a matching serial", true, "", cert, true, map[uuid.UUID]string{certID: "1"}, uuid.Nil, false},
		{"dev: nothing", true, "", nil, false, matching, uuid.Nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeLookup{active: tc.serials}
			got, err := authenticateAgent(agentCtx(tc.header, tc.cert, tc.verified), tc.devMode, f)
			assert.Equal(t, tc.wantID, got)
			if tc.wantOK {
				require.NoError(t, err)
				return
			}
			var he *echo.HTTPError
			require.ErrorAs(t, err, &he)
			assert.Equal(t, http.StatusUnauthorized, he.Code)
		})
	}
}

func TestAuthenticateAgentEmptyPeerCertificates(t *testing.T) {
	c := agentCtx("", nil, false)
	c.Request().TLS = &tls.ConnectionState{}
	f := &fakeLookup{}
	_, err := authenticateAgent(c, false, f)
	require.ErrorIs(t, err, errAgentUnauthorized, "a TLS connection without a client certificate must be rejected")
	assert.Empty(t, f.calls)
}

func TestAuthenticateAgentLookupError(t *testing.T) {
	id := uuid.New()
	cert := certWithCN("saidkick-agent-" + id.String())
	dbErr := errors.New("connection refused")
	f := &fakeLookup{err: dbErr}

	_, err := authenticateAgent(agentCtx("", cert, true), false, f)
	require.ErrorIs(t, err, dbErr, "a lookup failure is a server error, not a credential failure")
	var he *echo.HTTPError
	assert.False(t, errors.As(err, &he))
	assert.Equal(t, []uuid.UUID{id}, f.calls)
}

// The X-Agent-Cluster-ID bypass must do nothing unless dev mode is on, even
// for a well-formed UUID of a cluster that exists.
func TestDevModeBypassInertOutsideDevMode(t *testing.T) {
	id := uuid.New()
	f := &fakeLookup{active: map[uuid.UUID]string{id: "1"}}

	called := false
	err := agentAuthMiddleware(false, f)(func(echo.Context) error {
		called = true
		return nil
	})(agentCtx(id.String(), nil, false))

	var he *echo.HTTPError
	require.ErrorAs(t, err, &he)
	assert.Equal(t, http.StatusUnauthorized, he.Code)
	assert.False(t, called)
	assert.Empty(t, f.calls, "the header must not even trigger a cluster lookup")

	_, ok := devModeClusterID(agentCtx(id.String(), nil, false), false)
	assert.False(t, ok)
}

func TestAgentAuthMiddleware(t *testing.T) {
	certID := uuid.New()
	cert := certWithCN("saidkick-agent-" + certID.String())
	cert.SerialNumber = big.NewInt(7)

	t.Run("rejects without certificate", func(t *testing.T) {
		called := false
		f := &fakeLookup{}
		err := agentAuthMiddleware(false, f)(func(echo.Context) error {
			called = true
			return nil
		})(agentCtx("", nil, false))

		var he *echo.HTTPError
		require.ErrorAs(t, err, &he)
		assert.Equal(t, http.StatusUnauthorized, he.Code)
		assert.False(t, called)
	})

	t.Run("stores identity from certificate", func(t *testing.T) {
		var seen *auth.AgentIdentity
		f := &fakeLookup{active: map[uuid.UUID]string{certID: "7"}}
		err := agentAuthMiddleware(false, f)(func(c echo.Context) error {
			seen = auth.AgentIdentityFromContext(c.Request().Context())
			return nil
		})(agentCtx("", cert, true))

		require.NoError(t, err)
		require.NotNil(t, seen)
		assert.Equal(t, certID, seen.ClusterID)
	})

	t.Run("exported constructor wires the database lookup", func(t *testing.T) {
		// No certificate, so the lookup (and the nil handle) is never reached.
		err := AgentAuthMiddleware(false, nil)(func(echo.Context) error { return nil })(agentCtx("", nil, false))
		require.ErrorIs(t, err, errAgentUnauthorized)
	})
}

// TestAuthenticateAgentPendingSerial covers the activation rule: the
// previous certificate keeps working until the renewed one is presented,
// which promotes it, and from then on only the renewed one works.
func TestAuthenticateAgentPendingSerial(t *testing.T) {
	id := uuid.New()
	oldCert := certWithCN("saidkick-agent-" + id.String())
	oldCert.SerialNumber = big.NewInt(100)
	newCert := certWithCN("saidkick-agent-" + id.String())
	newCert.SerialNumber = big.NewInt(200)

	f := &fakeLookup{
		active:  map[uuid.UUID]string{id: "100"},
		pending: map[uuid.UUID]string{id: "200"},
	}
	authn := func(cert *x509.Certificate) error {
		_, err := authenticateAgent(agentCtx("", cert, true), false, f)
		return err
	}

	require.NoError(t, authn(oldCert), "the previous certificate works until the renewed one is used")
	require.NoError(t, authn(oldCert))
	assert.Empty(t, f.promotions)

	require.NoError(t, authn(newCert), "the renewed certificate authenticates and is promoted")
	assert.Equal(t, []string{"200"}, f.promotions)
	assert.Equal(t, "200", f.active[id])
	assert.Empty(t, f.pending[id])

	require.ErrorIs(t, authn(oldCert), errAgentUnauthorized, "the previous certificate is dead after first use of the renewed one")
	require.NoError(t, authn(newCert))
	assert.Len(t, f.promotions, 1, "an active certificate is not promoted again")
}

func TestAuthenticateAgentPromotionRace(t *testing.T) {
	id := uuid.New()
	cert := certWithCN("saidkick-agent-" + id.String())
	cert.SerialNumber = big.NewInt(200)
	fresh := func() *fakeLookup {
		return &fakeLookup{
			active:  map[uuid.UUID]string{id: "100"},
			pending: map[uuid.UUID]string{id: "200"},
		}
	}

	t.Run("lost race to a request with the same certificate is accepted", func(t *testing.T) {
		f := fresh()
		f.stealPromotion = true
		got, err := authenticateAgent(agentCtx("", cert, true), false, f)
		require.NoError(t, err)
		assert.Equal(t, id, got)
	})

	t.Run("pending serial replaced by a newer renewal is rejected", func(t *testing.T) {
		f := fresh()
		_, err := authenticateAgent(agentCtx("", cert, true), false, &replacingStore{fakeLookup: f, newPending: "300"})
		require.ErrorIs(t, err, errAgentUnauthorized)
		assert.Equal(t, "100", f.active[id])
	})

	t.Run("promotion error is a server error", func(t *testing.T) {
		dbErr := errors.New("connection reset")
		f := fresh()
		f.promoteErr = dbErr
		_, err := authenticateAgent(agentCtx("", cert, true), false, f)
		require.ErrorIs(t, err, dbErr)
	})

	t.Run("reload error after a lost race is a server error", func(t *testing.T) {
		dbErr := errors.New("connection reset")
		_, err := authenticateAgent(agentCtx("", cert, true), false, &failingReloadStore{fakeLookup: fresh(), err: dbErr})
		require.ErrorIs(t, err, dbErr)
	})
}

// replacingStore simulates a second renewal landing between the lookup and
// the promotion: the pending serial on record changes, so the promotion of
// the older one matches nothing.
type replacingStore struct {
	*fakeLookup
	newPending string
}

func (s *replacingStore) promote(_ context.Context, id uuid.UUID, _ string, _ time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending[id] = s.newPending
	return false, nil
}

// failingReloadStore loses the promotion race and then fails to reload.
type failingReloadStore struct {
	*fakeLookup
	err   error
	loads int
}

func (s *failingReloadStore) serials(ctx context.Context, id uuid.UUID) (agentCertSerials, bool, error) {
	s.loads++
	if s.loads > 1 {
		return agentCertSerials{}, false, s.err
	}
	return s.fakeLookup.serials(ctx, id)
}

func (s *failingReloadStore) promote(context.Context, uuid.UUID, string, time.Time) (bool, error) {
	return false, nil
}
