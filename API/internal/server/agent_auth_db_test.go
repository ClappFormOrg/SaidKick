package server

import (
	"bytes"
	"crypto/tls"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
	"github.com/ClappFormOrg/SaidKick/API/internal/config"
	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
)

func strPtr(s string) *string { return &s }

func TestDBAgentCertSerial(t *testing.T) {
	t.Parallel()
	handle := testutil.NewPostgresDB(t)
	withSerial := seed.SeedCluster(t, handle, models.Cluster{
		AgentCertSerial: strPtr("123456789"), AgentCertPendingSerial: strPtr("987654321"),
	})
	withoutSerial := seed.SeedCluster(t, handle, models.Cluster{})
	store := dbAgentCertStore{handle: handle}

	serials, found, err := store.serials(t.Context(), withSerial.ID)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, agentCertSerials{Active: "123456789", Pending: "987654321"}, serials)

	serials, found, err = store.serials(t.Context(), withoutSerial.ID)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, agentCertSerials{}, serials)

	_, found, err = store.serials(t.Context(), uuid.New())
	require.NoError(t, err)
	assert.False(t, found)
}

func TestDBAgentCertStorePromote(t *testing.T) {
	t.Parallel()
	handle := testutil.NewPostgresDB(t)
	store := dbAgentCertStore{handle: handle}
	notAfter := time.Now().Add(90 * 24 * time.Hour).UTC().Truncate(time.Second)

	cluster := seed.SeedCluster(t, handle, models.Cluster{
		AgentCertSerial: strPtr("1"), AgentCertPendingSerial: strPtr("2"),
	})

	promoted, err := store.promote(t.Context(), cluster.ID, "3", notAfter)
	require.NoError(t, err)
	assert.False(t, promoted, "a serial that is not the pending one is never promoted")

	promoted, err = store.promote(t.Context(), cluster.ID, "2", notAfter)
	require.NoError(t, err)
	assert.True(t, promoted)

	var got models.Cluster
	require.NoError(t, handle.RW.First(&got, "id = ?", cluster.ID).Error)
	require.NotNil(t, got.AgentCertSerial)
	assert.Equal(t, "2", *got.AgentCertSerial)
	assert.Nil(t, got.AgentCertPendingSerial)
	require.NotNil(t, got.AgentCertNotAfter)
	assert.True(t, notAfter.Equal(*got.AgentCertNotAfter))
	require.NotNil(t, got.AgentCertExpiresAt)
	assert.True(t, notAfter.Equal(*got.AgentCertExpiresAt))

	promoted, err = store.promote(t.Context(), cluster.ID, "2", notAfter)
	require.NoError(t, err)
	assert.False(t, promoted, "a second promotion of the same serial matches no row")
}

func setAgentCertSerial(t *testing.T, handle *db.Handle, clusterID uuid.UUID, cert tls.Certificate) {
	t.Helper()
	// The same column and decimal format HandshakeCluster writes.
	require.NoError(t, handle.RW.Model(&models.Cluster{}).Where("id = ?", clusterID).
		Update("agent_cert_serial", cert.Leaf.SerialNumber.String()).Error)
}

// TestAgentListenerEndToEnd drives the real agent routes and the database
// lookup through the production TLS configuration.
func TestAgentListenerEndToEnd(t *testing.T) {
	t.Parallel()
	handle := testutil.NewPostgresDB(t)
	ca := newTestCA(t, "agent CA")

	cluster := seed.SeedCluster(t, handle, models.Cluster{})
	other := seed.SeedCluster(t, handle, models.Cluster{})
	first := ca.agentCert(t, cluster.ID)
	rotated := ca.agentCert(t, cluster.ID)
	unknown := ca.agentCert(t, uuid.New())
	setAgentCertSerial(t, handle, cluster.ID, first)

	s := New(config.Config{}, handle, audit.NoopLogger{}, nil, nil, nil, nil, nil)
	ts := startAgentTLSServer(t, ca, s.agentEcho)

	heartbeat := func(clusterID uuid.UUID, cert *tls.Certificate) int {
		t.Helper()
		resp, err := agentClient(ca, cert, 0).Post(
			ts.URL+"/api/v1/clusters/"+clusterID.String()+"/heartbeat",
			"application/json", bytes.NewBufferString(`{"agent_version":"test"}`))
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	lastHeartbeat := func(id uuid.UUID) *time.Time {
		t.Helper()
		var c models.Cluster
		require.NoError(t, handle.RW.First(&c, "id = ?", id).Error)
		return c.LastHeartbeatAt
	}

	t.Run("matching serial authenticates and resolves the right cluster", func(t *testing.T) {
		assert.Equal(t, http.StatusNoContent, heartbeat(cluster.ID, &first))
		assert.NotNil(t, lastHeartbeat(cluster.ID))
		assert.Nil(t, lastHeartbeat(other.ID))
	})

	t.Run("valid certificate with a different serial is 401", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, heartbeat(cluster.ID, &rotated))
	})

	t.Run("certificate for an unknown cluster is 401", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, heartbeat(cluster.ID, &unknown))
	})

	t.Run("rotation invalidates the previous certificate immediately", func(t *testing.T) {
		setAgentCertSerial(t, handle, cluster.ID, rotated)
		assert.Equal(t, http.StatusUnauthorized, heartbeat(cluster.ID, &first))
		assert.Equal(t, http.StatusNoContent, heartbeat(cluster.ID, &rotated))
	})

	t.Run("renewed certificate activates on first use", func(t *testing.T) {
		renewed := ca.agentCert(t, cluster.ID)
		setPendingSerial(t, handle, cluster.ID, renewed)

		// The current certificate works until the renewed one is presented.
		assert.Equal(t, http.StatusNoContent, heartbeat(cluster.ID, &rotated))
		assert.Equal(t, http.StatusNoContent, heartbeat(cluster.ID, &rotated))

		assert.Equal(t, http.StatusNoContent, heartbeat(cluster.ID, &renewed))
		var got models.Cluster
		require.NoError(t, handle.RW.First(&got, "id = ?", cluster.ID).Error)
		require.NotNil(t, got.AgentCertSerial)
		assert.Equal(t, renewed.Leaf.SerialNumber.String(), *got.AgentCertSerial)
		assert.Nil(t, got.AgentCertPendingSerial)
		require.NotNil(t, got.AgentCertNotAfter)
		assert.True(t, renewed.Leaf.NotAfter.Equal(*got.AgentCertNotAfter))

		assert.Equal(t, http.StatusUnauthorized, heartbeat(cluster.ID, &rotated), "the previous certificate is dead after first use")
		assert.Equal(t, http.StatusNoContent, heartbeat(cluster.ID, &renewed))
	})
}

func setPendingSerial(t *testing.T, handle *db.Handle, clusterID uuid.UUID, cert tls.Certificate) {
	t.Helper()
	require.NoError(t, handle.RW.Model(&models.Cluster{}).Where("id = ?", clusterID).
		Update("agent_cert_pending_serial", cert.Leaf.SerialNumber.String()).Error)
}

// TestAgentAuthConcurrentPromotion presents a renewed certificate on many
// connections at once: every request authenticates, the serial is promoted
// exactly once, and the previous certificate is rejected afterwards.
func TestAgentAuthConcurrentPromotion(t *testing.T) {
	t.Parallel()
	handle := testutil.NewPostgresDB(t)
	ca := newTestCA(t, "agent CA")
	cluster := seed.SeedCluster(t, handle, models.Cluster{})
	current := ca.agentCert(t, cluster.ID)
	renewed := ca.agentCert(t, cluster.ID)
	setAgentCertSerial(t, handle, cluster.ID, current)
	setPendingSerial(t, handle, cluster.ID, renewed)

	s := New(config.Config{}, handle, audit.NoopLogger{}, nil, nil, nil, nil, nil)
	ts := startAgentTLSServer(t, ca, s.agentEcho)
	url := ts.URL + "/api/v1/clusters/" + cluster.ID.String() + "/heartbeat"

	const workers = 8
	codes := make([]int, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() {
			resp, err := agentClient(ca, &renewed, 0).Post(url, "application/json", bytes.NewBufferString(`{}`))
			if err != nil {
				errs[i] = err
				return
			}
			_ = resp.Body.Close()
			codes[i] = resp.StatusCode
		})
	}
	wg.Wait()
	for i := range workers {
		require.NoError(t, errs[i])
		assert.Equal(t, http.StatusNoContent, codes[i], "request %d", i)
	}

	var got models.Cluster
	require.NoError(t, handle.RW.First(&got, "id = ?", cluster.ID).Error)
	require.NotNil(t, got.AgentCertSerial)
	assert.Equal(t, renewed.Leaf.SerialNumber.String(), *got.AgentCertSerial)
	assert.Nil(t, got.AgentCertPendingSerial)

	resp, err := agentClient(ca, &current, 0).Post(url, "application/json", bytes.NewBufferString(`{}`))
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}
