package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) { os.Exit(testutil.RunMain(m)) }

func TestParseNames(t *testing.T) {
	t.Parallel()
	in := "# demo rows\n\napp:nginx-demo-app\ncluster:Example D\n  cluster : Example-A  \n"
	got, err := parseNames(strings.NewReader(in))
	require.NoError(t, err)
	assert.Equal(t, []string{"nginx-demo-app"}, got.Apps)
	assert.Equal(t, []string{"Example D", "Example-A"}, got.Clusters)

	for _, bad := range []string{"nginx-demo-app", "app:", "org:provincie-voorbeeld"} {
		_, err := parseNames(strings.NewReader(bad))
		assert.Error(t, err, bad)
	}
}

// TestParseNamesFile checks the committed names file parses and lists the
// rows named in issue #39.
func TestParseNamesFile(t *testing.T) {
	t.Parallel()
	f, err := os.Open("../../../scripts/demo-names.txt")
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	got, err := parseNames(f)
	require.NoError(t, err)
	assert.Contains(t, got.Apps, "nginx-demo-app")
	for _, c := range []string{"Example A", "Example-A", "Example-B", "Example-C", "Example D", "asdfasdfasfd", "voorbeeld"} {
		assert.Contains(t, got.Clusters, c)
	}
}

// fixture is a database with demo rows, rows that block their own deletion
// and unrelated rows.
type fixture struct {
	h *db.Handle

	demoApp        models.App
	demoVersion    models.AppVersion
	demoSubmission models.AuditSubmission
	demoDeployment models.Deployment // state deleted, so it goes with the app

	blockedApp models.App // has a live deployment

	demoClusters   []models.Cluster // two clusters share the name "Example D"
	blockedCluster models.Cluster   // referenced by a deployment

	realApp     models.App
	realCluster models.Cluster
	realDeploy  models.Deployment
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	h := testutil.NewPostgresDB(t)
	f := fixture{h: h}

	org := seed.SeedOrg(t, h, models.Organization{})
	user := seed.SeedUser(t, h, models.User{OrganizationID: org.ID})
	app := func(slug string) models.App {
		return seed.SeedApp(t, h, models.App{Slug: slug, OwningOrganizationID: org.ID, OwningUserID: user.ID})
	}
	deploy := func(a models.App, v models.AppVersion, cl models.Cluster, state models.DeploymentState) models.Deployment {
		return seed.SeedDeployment(t, h, models.Deployment{
			AppID: a.ID, AppVersionID: v.ID, ClusterID: cl.ID,
			OwningOrganizationID: org.ID, DeployerUserID: user.ID,
			Namespace: a.Slug, State: state,
		})
	}

	f.realCluster = seed.SeedCluster(t, h, models.Cluster{Name: "prodcluster-keep"})
	f.realApp = app("inspraakhulp")
	realVersion, _ := seed.SeedPublishedVersion(t, h, models.AppVersion{AppID: f.realApp.ID})
	f.realDeploy = deploy(f.realApp, realVersion, f.realCluster, models.DeploymentReady)

	f.demoApp = app("nginx-demo-app")
	f.demoVersion, f.demoSubmission = seed.SeedPublishedVersion(t, h, models.AppVersion{AppID: f.demoApp.ID})
	f.demoDeployment = deploy(f.demoApp, f.demoVersion, f.realCluster, models.DeploymentDeleted)

	f.blockedApp = app("demo-with-live-deployment")
	blockedVersion, _ := seed.SeedPublishedVersion(t, h, models.AppVersion{AppID: f.blockedApp.ID})
	deploy(f.blockedApp, blockedVersion, f.realCluster, models.DeploymentReady)

	for range 2 {
		f.demoClusters = append(f.demoClusters, seed.SeedCluster(t, h, models.Cluster{
			Name: "Example D", State: models.ClusterPendingRegistration,
		}))
	}
	f.blockedCluster = seed.SeedCluster(t, h, models.Cluster{Name: "Example-A", State: models.ClusterPendingRegistration})
	deploy(f.realApp, realVersion, f.blockedCluster, models.DeploymentPending)
	return f
}

var demoNames = names{
	Apps:     []string{"nginx-demo-app", "demo-with-live-deployment", "missing-app"},
	Clusters: []string{"Example D", "Example-A", "missing-cluster"},
}

// counts returns the number of live apps, live deployments, clusters, app
// versions, audit submissions and demo_data.deleted events.
func counts(t *testing.T, h *db.Handle) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	for name, model := range map[string]any{
		"apps": &models.App{}, "deployments": &models.Deployment{}, "clusters": &models.Cluster{},
		"versions": &models.AppVersion{}, "submissions": &models.AuditSubmission{},
	} {
		var n int64
		require.NoError(t, h.RW.Model(model).Count(&n).Error)
		out[name] = n
	}
	var events int64
	require.NoError(t, h.RW.Model(&models.AuditLog{}).Where("event_type = ?", auditEventType).Count(&events).Error)
	out["events"] = events
	return out
}

func TestCleanupDryRunChangesNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	before := counts(t, f.h)

	var out bytes.Buffer
	require.NoError(t, cleanup(t.Context(), f.h, audit.NewLogger(f.h), &out, demoNames, false))

	assert.Equal(t, before, counts(t, f.h))
	text := out.String()
	assert.Contains(t, text, "app nginx-demo-app ("+f.demoApp.ID.String()+", published): would delete")
	assert.Contains(t, text, "  deployment "+f.demoDeployment.ID.String()+" (namespace nginx-demo-app): would delete")
	assert.Contains(t, text, "app demo-with-live-deployment ("+f.blockedApp.ID.String()+"): skipped, 1 live deployment(s)")
	for _, cl := range f.demoClusters {
		assert.Contains(t, text, `cluster "Example D" (`+cl.ID.String()+", pending_registration): would delete")
	}
	assert.Contains(t, text, `cluster "Example-A" (`+f.blockedCluster.ID.String()+"): skipped, referenced by 1 deployment row(s)")
	assert.Contains(t, text, "app:missing-app: not found")
	assert.Contains(t, text, "cluster:missing-cluster: not found")
	assert.NotContains(t, text, "inspraakhulp")
	assert.NotContains(t, text, "prodcluster-keep")
}

func TestCleanupApply(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	h := f.h
	ctx := t.Context()

	var out bytes.Buffer
	require.NoError(t, cleanup(ctx, h, audit.NewLogger(h), &out, demoNames, true))
	assert.Contains(t, out.String(), "deleted 4 rows")

	// The demo app and its deleted-state deployment are soft-deleted.
	var app models.App
	require.NoError(t, h.RW.Unscoped().First(&app, "id = ?", f.demoApp.ID).Error)
	assert.True(t, app.DeletedAt.Valid)
	var dep models.Deployment
	require.NoError(t, h.RW.Unscoped().First(&dep, "id = ?", f.demoDeployment.ID).Error)
	assert.True(t, dep.DeletedAt.Valid)

	// Versions and submissions have no deleted_at; they stay and drop out
	// with the app.
	require.NoError(t, h.RW.First(&models.AppVersion{}, "id = ?", f.demoVersion.ID).Error)
	require.NoError(t, h.RW.First(&models.AuditSubmission{}, "id = ?", f.demoSubmission.ID).Error)

	// Both clusters named "Example D" are gone.
	for _, cl := range f.demoClusters {
		var n int64
		require.NoError(t, h.RW.Model(&models.Cluster{}).Where("id = ?", cl.ID).Count(&n).Error)
		assert.Zero(t, n, cl.ID)
	}

	// Blocked and unrelated rows are untouched.
	for _, id := range []uuid.UUID{f.blockedApp.ID, f.realApp.ID} {
		require.NoError(t, h.RW.First(&models.App{}, "id = ?", id).Error)
	}
	for _, id := range []uuid.UUID{f.blockedCluster.ID, f.realCluster.ID} {
		require.NoError(t, h.RW.First(&models.Cluster{}, "id = ?", id).Error)
	}
	require.NoError(t, h.RW.First(&models.Deployment{}, "id = ?", f.realDeploy.ID).Error)

	// One audit event per deleted row.
	var events []models.AuditLog
	require.NoError(t, h.RW.Where("event_type = ?", auditEventType).Order("subject_type").Find(&events).Error)
	got := map[uuid.UUID]string{}
	for _, e := range events {
		got[e.SubjectID] = e.SubjectType
		assert.Nil(t, e.ActorUserID)
	}
	assert.Len(t, events, 4)
	assert.Equal(t, map[uuid.UUID]string{
		f.demoApp.ID:         "app",
		f.demoDeployment.ID:  "deployment",
		f.demoClusters[0].ID: "cluster",
		f.demoClusters[1].ID: "cluster",
	}, got)

	// A second run finds nothing left to delete.
	out.Reset()
	require.NoError(t, cleanup(ctx, h, audit.NewLogger(h), &out, demoNames, true))
	assert.Contains(t, out.String(), "app:nginx-demo-app: not found")
	assert.Contains(t, out.String(), "deleted 0 rows")
}
