package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/handlers"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

// launcherManifest is a published manifest with every field the launcher
// projects.
const launcherManifest = `{"apiVersion":"appstore.gov.nl/v1","kind":"Application",
	"metadata":{"name":"govchat","display_name":"GovChat","vendor":"Gemeente Voorbeeld","contact":"support@voorbeeld.example.nl"},
	"spec":{"categories":["chat","kennis"],"ai":{"risk_class":"minimal"}}}`

// launcherFixture is two organisations with deployments in every state the
// launcher distinguishes.
type launcherFixture struct {
	handle   *db.Handle
	h        *handlers.Handlers
	orgA     models.Organization
	orgB     models.Organization
	deployer models.User // org A, deployed everything in org A
	appUser  models.User // org A
	userB    models.User // org B
	chat     models.App  // has launcherManifest
	plain    models.App  // has the seed's default manifest, no display name
	cluster  models.Cluster

	ready, degradedHTTP, applying, failed, noIngress models.Deployment
	deleting, softDeleted, otherOrg                  models.Deployment
}

func newLauncherFixture(t *testing.T) launcherFixture {
	t.Helper()
	handle := testutil.NewPostgresDB(t)
	f := launcherFixture{handle: handle, h: newTestHandlers(t, handle)}

	f.orgA = seed.SeedOrg(t, handle, models.Organization{})
	f.orgB = seed.SeedOrg(t, handle, models.Organization{})
	f.deployer = seed.SeedUser(t, handle, models.User{
		OrganizationID: f.orgA.ID, DisplayName: "Dana Deployer", Email: "dana@org-a.example.nl",
	})
	f.appUser = seed.SeedUser(t, handle, models.User{OrganizationID: f.orgA.ID})
	f.userB = seed.SeedUser(t, handle, models.User{OrganizationID: f.orgB.ID})
	f.cluster = seed.SeedCluster(t, handle, models.Cluster{Name: "prod-01"})

	f.chat = seed.SeedApp(t, handle, models.App{OwningOrganizationID: f.orgB.ID, OwningUserID: f.userB.ID})
	chatVersion, _ := seed.SeedPublishedVersion(t, handle, models.AppVersion{
		AppID: f.chat.ID, AppstoreYAML: datatypes.JSON(launcherManifest),
	})
	f.plain = seed.SeedApp(t, handle, models.App{OwningOrganizationID: f.orgB.ID, OwningUserID: f.userB.ID})
	plainVersion, _ := seed.SeedPublishedVersion(t, handle, models.AppVersion{AppID: f.plain.ID})

	deploy := func(app models.App, version models.AppVersion, org models.Organization, by models.User,
		ns string, state models.DeploymentState, values string,
	) models.Deployment {
		return seed.SeedDeployment(t, handle, models.Deployment{
			AppID: app.ID, AppVersionID: version.ID, ClusterID: f.cluster.ID,
			OwningOrganizationID: org.ID, DeployerUserID: by.ID,
			Namespace: ns, State: state, Values: datatypes.JSON(values),
		})
	}
	const host = `{"ingress":{"host":"chat.org-a.example.nl"}}`
	f.ready = deploy(f.chat, chatVersion, f.orgA, f.deployer, "ready", models.DeploymentReady, host)
	f.degradedHTTP = deploy(f.chat, chatVersion, f.orgA, f.deployer, "degraded", models.DeploymentDegraded,
		`{"ingress":{"host":"chat-test.org-a.example.nl","tls":false},"replicas":3}`)
	f.applying = deploy(f.chat, chatVersion, f.orgA, f.deployer, "applying", models.DeploymentApplying, host)
	f.failed = deploy(f.chat, chatVersion, f.orgA, f.deployer, "failed", models.DeploymentFailedVault, host)
	f.noIngress = deploy(f.plain, plainVersion, f.orgA, f.deployer, "worker", models.DeploymentReady, `{"replicas":1}`)
	f.deleting = deploy(f.chat, chatVersion, f.orgA, f.deployer, "deleting", models.DeploymentDeleting, host)
	f.softDeleted = deploy(f.chat, chatVersion, f.orgA, f.deployer, "gone", models.DeploymentReady, host)
	require.NoError(t, handle.RW.WithContext(t.Context()).Delete(&f.softDeleted).Error)
	f.otherOrg = deploy(f.chat, chatVersion, f.orgB, f.userB, "org-b", models.DeploymentReady, host)
	return f
}

// myAppsAs serves GET /api/v1/my-apps through the real route table.
func myAppsAs(f launcherFixture, p *auth.Principal, query string) *httptest.ResponseRecorder {
	req := newRequest(http.MethodGet, "/api/v1/my-apps"+query, "")
	if p != nil {
		req = asPrincipal(req, p)
	}
	return serveRoute(f.h, req)
}

type myAppsBody struct {
	Items    []handlers.MyAppCard `json:"items"`
	Total    int64                `json:"total"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"page_size"`
}

func decodeMyApps(t *testing.T, rec *httptest.ResponseRecorder) myAppsBody {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body myAppsBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body
}

func cardsByID(body myAppsBody) map[uuid.UUID]handlers.MyAppCard {
	out := make(map[uuid.UUID]handlers.MyAppCard, len(body.Items))
	for _, c := range body.Items {
		out[uuid.MustParse(c.DeploymentID)] = c
	}
	return out
}

func TestListMyApps(t *testing.T) {
	t.Parallel()
	f := newLauncherFixture(t)
	live := []uuid.UUID{f.ready.ID, f.degradedHTTP.ID, f.applying.ID, f.failed.ID, f.noIngress.ID}

	t.Run("anonymous_returns_401", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, myAppsAs(f, nil, "").Code)
	})

	t.Run("app_user_sees_own_org_live_deployments_only", func(t *testing.T) {
		body := decodeMyApps(t, myAppsAs(f, principal(f.appUser, models.RoleAppUser), ""))
		assert.Equal(t, int64(len(live)), body.Total)
		got := cardsByID(body)
		assert.Len(t, got, len(live))
		for _, id := range live {
			assert.Contains(t, got, id)
		}
		for _, id := range []uuid.UUID{f.deleting.ID, f.softDeleted.ID, f.otherOrg.ID} {
			assert.NotContains(t, got, id)
		}
	})

	t.Run("cards_project_availability_url_and_maintainer", func(t *testing.T) {
		got := cardsByID(decodeMyApps(t, myAppsAs(f, principal(f.appUser, models.RoleAppUser), "")))

		ready := got[f.ready.ID]
		assert.Equal(t, handlers.AvailabilityAvailable, ready.Availability)
		assert.Equal(t, "web", string(ready.Access))
		assert.Equal(t, "https://chat.org-a.example.nl", ready.URL)
		assert.Equal(t, f.chat.Slug, ready.AppSlug)
		assert.Equal(t, "GovChat", ready.DisplayName)
		assert.Equal(t, []string{"chat", "kennis"}, ready.Categories)
		assert.Equal(t, "prod-01", ready.ClusterName)
		assert.Equal(t, handlers.Maintainer{
			Name: "Dana Deployer", Email: "dana@org-a.example.nl",
			VendorName: "Gemeente Voorbeeld", VendorContact: "support@voorbeeld.example.nl",
		}, ready.Maintainer)

		degraded := got[f.degradedHTTP.ID]
		assert.Equal(t, handlers.AvailabilityDegraded, degraded.Availability)
		assert.Equal(t, "http://chat-test.org-a.example.nl", degraded.URL)

		applying := got[f.applying.ID]
		assert.Equal(t, handlers.AvailabilityPreparing, applying.Availability)
		assert.Equal(t, "web", string(applying.Access))
		assert.Empty(t, applying.URL, "no link until the app is up")

		failed := got[f.failed.ID]
		assert.Equal(t, handlers.AvailabilityUnavailable, failed.Availability)
		assert.Empty(t, failed.URL)

		plain := got[f.noIngress.ID]
		assert.Equal(t, handlers.AvailabilityAvailable, plain.Availability)
		assert.Equal(t, "none", string(plain.Access))
		assert.Empty(t, plain.URL)
		assert.Equal(t, f.plain.Slug, plain.DisplayName, "a manifest without a display name falls back to the slug")
		assert.Equal(t, []string{}, plain.Categories)
	})

	t.Run("response_carries_no_operational_fields", func(t *testing.T) {
		rec := myAppsAs(f, principal(f.appUser, models.RoleAppUser), "")
		require.Equal(t, http.StatusOK, rec.Code)
		var raw struct {
			Items []map[string]json.RawMessage `json:"items"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
		require.NotEmpty(t, raw.Items)
		for _, item := range raw.Items {
			for _, field := range []string{"values", "secret_refs", "namespace", "cluster_id", "state", "values_schema"} {
				assert.NotContains(t, item, field)
			}
		}
		assert.NotContains(t, rec.Body.String(), "replicas", "no part of the values blob may leak")
	})

	t.Run("every_signed_in_principal_may_call_it", func(t *testing.T) {
		for _, roles := range [][]models.Role{
			nil, {models.RoleDeveloper}, {models.RoleDeployer}, {models.RoleAuditor},
			{models.RoleClusterAdmin}, {models.RoleOrgAdmin},
		} {
			body := decodeMyApps(t, myAppsAs(f, principal(f.appUser, roles...), ""))
			assert.Equal(t, int64(len(live)), body.Total, "roles %v", roles)
		}
	})

	t.Run("platform_admin_is_scoped_to_own_org", func(t *testing.T) {
		got := cardsByID(decodeMyApps(t, myAppsAs(f, principal(f.userB, models.RolePlatformAdmin), "")))
		assert.Len(t, got, 1)
		assert.Contains(t, got, f.otherOrg.ID)
	})

	t.Run("paginates", func(t *testing.T) {
		first := decodeMyApps(t, myAppsAs(f, principal(f.appUser, models.RoleAppUser), "?page=1&page_size=2"))
		second := decodeMyApps(t, myAppsAs(f, principal(f.appUser, models.RoleAppUser), "?page=3&page_size=2"))
		assert.Len(t, first.Items, 2)
		assert.Len(t, second.Items, 1)
		assert.Equal(t, int64(len(live)), first.Total)
		assert.Equal(t, 2, first.PageSize)
		assert.Equal(t, 3, second.Page)
	})

	t.Run("adds_no_identifying_metric_labels", func(t *testing.T) {
		myAppsAs(f, nil, "")
		myAppsAs(f, principal(f.appUser, models.RoleAppUser), "")
		testutil.RequireNoIdentifyingLabels(t)
	})
}

// TestAvailabilityFor checks the projection over every deployment state,
// and that a state it does not know fails closed.
func TestAvailabilityFor(t *testing.T) {
	t.Parallel()
	want := map[models.DeploymentState]handlers.Availability{
		models.DeploymentReady:               handlers.AvailabilityAvailable,
		models.DeploymentDegraded:            handlers.AvailabilityDegraded,
		models.DeploymentPending:             handlers.AvailabilityPreparing,
		models.DeploymentWaitingForCluster:   handlers.AvailabilityPreparing,
		models.DeploymentApplying:            handlers.AvailabilityPreparing,
		models.DeploymentSecretSync:          handlers.AvailabilityPreparing,
		models.DeploymentReconciling:         handlers.AvailabilityPreparing,
		models.DeploymentPendingOrchestrator: handlers.AvailabilityPreparing,
		models.DeploymentFailedRender:        handlers.AvailabilityUnavailable,
		models.DeploymentFailedVault:         handlers.AvailabilityUnavailable,
		models.DeploymentFailedOrchestrator:  handlers.AvailabilityUnavailable,
		models.DeploymentFailedSecretSync:    handlers.AvailabilityUnavailable,
		models.DeploymentDeleting:            handlers.AvailabilityUnavailable,
		models.DeploymentDeleted:             handlers.AvailabilityUnavailable,
	}
	require.Len(t, want, len(models.AllDeploymentStates), "a new state needs a decision here")
	for _, s := range models.AllDeploymentStates {
		assert.Equal(t, want[s], handlers.AvailabilityFor(s), s)
	}
	assert.Equal(t, handlers.AvailabilityUnavailable, handlers.AvailabilityFor("some_future_state"))
}
