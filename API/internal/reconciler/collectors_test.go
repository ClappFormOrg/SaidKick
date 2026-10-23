package reconciler

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"

	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
)

// The gauges are process-wide, so the collector tests do not run in
// parallel.

// collectorFixture seeds an organisation and a user to own apps.
type collectorFixture struct {
	handle *db.Handle
	org    models.Organization
	user   models.User
}

func newCollectorFixture(t *testing.T) collectorFixture {
	t.Helper()
	handle := testutil.NewPostgresDB(t)
	org := seed.SeedOrg(t, handle, models.Organization{Slug: "collector-org", DisplayName: "Collector Org", ContactEmail: "c@example.com"})
	user := seed.SeedUser(t, handle, models.User{
		OrganizationID: org.ID, ExternalSubject: "collector-sub", Email: "c@collector-org.nl", DisplayName: "Collector",
	})
	return collectorFixture{handle: handle, org: org, user: user}
}

func (f collectorFixture) app(t *testing.T) models.App {
	t.Helper()
	return seed.SeedApp(t, f.handle, models.App{OwningOrganizationID: f.org.ID, OwningUserID: f.user.ID})
}

func (f collectorFixture) reconciler() *Reconciler {
	return New(f.handle, nil, audit.NoopLogger{}, 0)
}

func TestRecordAgentGauges(t *testing.T) {
	f := newCollectorFixture(t)
	now := time.Now().UTC()
	at := func(d time.Duration) *time.Time { ts := now.Add(d); return &ts }

	// The stalest cluster that counts (unhealthy) reported ten minutes ago.
	// The decommissioned cluster is older but does not count, nor does a
	// cluster that never reported.
	live := seed.SeedCluster(t, f.handle, models.Cluster{State: models.ClusterReady, LastHeartbeatAt: at(-30 * time.Second)})
	seed.SeedCluster(t, f.handle, models.Cluster{State: models.ClusterUnhealthy, LastHeartbeatAt: at(-10 * time.Minute)})
	seed.SeedCluster(t, f.handle, models.Cluster{State: models.ClusterDecommissioned, LastHeartbeatAt: at(-48 * time.Hour)})
	seed.SeedCluster(t, f.handle, models.Cluster{State: models.ClusterPendingRegistration})

	for _, cmd := range []models.AgentCommand{
		{ClusterID: live.ID, CommandType: "apply", ExpiresAt: now.Add(time.Hour)},                              // pending
		{ClusterID: live.ID, CommandType: "apply", ExpiresAt: now.Add(time.Hour), ClaimedAt: at(-time.Second)}, // claimed, not acked: pending
		{ClusterID: live.ID, CommandType: "apply", ExpiresAt: now.Add(time.Hour), AckedAt: at(-time.Second)},   // acked
		{ClusterID: live.ID, CommandType: "apply", ExpiresAt: now.Add(-time.Minute)},                           // expired
	} {
		cmd.Payload = datatypes.JSON(`{}`)
		require.NoError(t, f.handle.RW.Create(&cmd).Error)
	}

	f.reconciler().recordAgentGauges(t.Context())

	assert.InDelta(t, 600, metrics.AgentHeartbeatAgeSeconds.Get(), 30)
	assert.InDelta(t, 2, metrics.AgentCommandsPending.Get(), 0)

	// Stopping the ready cluster's agent drives the age up on the next tick.
	require.NoError(t, f.handle.RW.Model(&models.Cluster{}).Where("id = ?", live.ID).
		Update("last_heartbeat_at", now.Add(-time.Hour)).Error)
	f.reconciler().recordAgentGauges(t.Context())
	assert.InDelta(t, 3600, metrics.AgentHeartbeatAgeSeconds.Get(), 30)
}

// agentCertMinRemaining returns the value of agent_cert_min_remaining_seconds
// in the hub exposition, or false when the series is absent.
func agentCertMinRemaining(t *testing.T) (float64, bool) {
	t.Helper()
	var buf strings.Builder
	metrics.WritePrometheus(&buf, false)
	for line := range strings.Lines(buf.String()) {
		if v, ok := strings.CutPrefix(line, "agent_cert_min_remaining_seconds "); ok {
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			require.NoError(t, err)
			return f, true
		}
	}
	return 0, false
}

func TestRecordAgentCertMinRemaining(t *testing.T) {
	f := newCollectorFixture(t)
	now := time.Now().UTC()
	at := func(d time.Duration) *time.Time { ts := now.Add(d); return &ts }

	// No cluster with a known expiry: the series is absent, not 0.
	seed.SeedCluster(t, f.handle, models.Cluster{State: models.ClusterReady})
	metrics.SetAgentCertMinRemainingSeconds(1, true)
	f.reconciler().recordAgentGauges(t.Context())
	_, ok := agentCertMinRemaining(t)
	assert.False(t, ok, "the gauge is absent while no expiry is known")

	// The soonest expiry that counts is 20 days out. The decommissioned
	// cluster expires sooner but does not count.
	seed.SeedCluster(t, f.handle, models.Cluster{State: models.ClusterReady, AgentCertNotAfter: at(60 * 24 * time.Hour)})
	seed.SeedCluster(t, f.handle, models.Cluster{State: models.ClusterUnhealthy, AgentCertNotAfter: at(20 * 24 * time.Hour)})
	seed.SeedCluster(t, f.handle, models.Cluster{State: models.ClusterDecommissioned, AgentCertNotAfter: at(time.Hour)})
	f.reconciler().recordAgentGauges(t.Context())
	got, ok := agentCertMinRemaining(t)
	require.True(t, ok)
	assert.InDelta(t, (20 * 24 * time.Hour).Seconds(), got, 30)

	// An expired certificate reads negative.
	seed.SeedCluster(t, f.handle, models.Cluster{State: models.ClusterReady, AgentCertNotAfter: at(-time.Hour)})
	f.reconciler().recordAgentGauges(t.Context())
	got, ok = agentCertMinRemaining(t)
	require.True(t, ok)
	assert.InDelta(t, -3600, got, 30)
	testutil.RequireNoIdentifyingLabels(t)
}

func TestRecordActiveAPIKeys(t *testing.T) {
	f := newCollectorFixture(t)
	now := time.Now().UTC()
	at := func(d time.Duration) *time.Time { ts := now.Add(d); return &ts }
	for i, k := range []models.APIKey{
		{ExpiresAt: at(time.Hour)},                            // active
		{ExpiresAt: at(30 * 24 * time.Hour)},                  // active
		{ExpiresAt: at(-time.Minute)},                         // expired
		{ExpiresAt: at(time.Hour), RevokedAt: at(-time.Hour)}, // revoked
		{}, // no deadline: predates the scoping migration
	} {
		k.Name = "key"
		k.Prefix = "sak_test"
		k.KeyHash = strings.Repeat(strconv.Itoa(i), 64)
		k.OwnerUserID = f.user.ID
		require.NoError(t, f.handle.RW.Create(&k).Error)
	}

	metrics.APIKeyActiveTotal.Set(99)
	f.reconciler().recordActiveAPIKeys(t.Context())
	assert.InDelta(t, 2, metrics.APIKeyActiveTotal.Get(), 0)
}

func TestRecordAgentGaugesWithoutClusters(t *testing.T) {
	f := newCollectorFixture(t)
	metrics.AgentHeartbeatAgeSeconds.Set(99)
	metrics.AgentCommandsPending.Set(99)
	f.reconciler().recordAgentGauges(t.Context())
	assert.Zero(t, metrics.AgentHeartbeatAgeSeconds.Get())
	assert.Zero(t, metrics.AgentCommandsPending.Get())
}

func TestSetAuditSubmissionStateGaugesWritesZeros(t *testing.T) {
	setAuditSubmissionStateGauges(map[models.AppVersionState]int64{models.AppVersionSubmitted: 4})
	setAuditSubmissionStateGauges(map[models.AppVersionState]int64{models.AppVersionPublished: 1})
	for _, s := range models.AllAppVersionStates {
		want := 0.0
		if s == models.AppVersionPublished {
			want = 1
		}
		assert.InDelta(t, want, metrics.AuditSubmissionStateGauge(string(s)).Get(), 0, "state %q", s)
	}
}

func TestRecordAuditSubmissionStates(t *testing.T) {
	f := newCollectorFixture(t)
	app := f.app(t)
	seed.SeedAppVersion(t, f.handle, models.AppVersion{AppID: app.ID, State: models.AppVersionSubmitted})
	seed.SeedAppVersion(t, f.handle, models.AppVersion{AppID: app.ID, State: models.AppVersionSubmitted})
	seed.SeedAppVersion(t, f.handle, models.AppVersion{AppID: app.ID, State: models.AppVersionPublished})

	// A soft-deleted app's submissions are out of the queue.
	gone := f.app(t)
	seed.SeedAppVersion(t, f.handle, models.AppVersion{AppID: gone.ID, State: models.AppVersionSubmitted})
	require.NoError(t, f.handle.RW.Delete(&gone).Error)

	// A stale value for a state with no rows must drop to zero.
	metrics.AuditSubmissionStateGauge(string(models.AppVersionRejected)).Set(7)

	f.reconciler().recordAuditSubmissionStates(t.Context())

	want := map[models.AppVersionState]float64{models.AppVersionSubmitted: 2, models.AppVersionPublished: 1}
	for _, s := range models.AllAppVersionStates {
		assert.InDelta(t, want[s], metrics.AuditSubmissionStateGauge(string(s)).Get(), 0, "state %q", s)
	}
}
