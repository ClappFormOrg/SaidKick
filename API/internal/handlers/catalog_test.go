package handlers_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/handlers"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

// catalogFixture holds two organisations, each owning one draft app.
type catalogFixture struct {
	orgA  models.Organization
	orgB  models.Organization
	userA models.User
	userB models.User
	appA  models.App
	appB  models.App
}

func newCatalogFixture(t *testing.T) (catalogFixture, *handlers.Handlers) {
	t.Helper()

	handle := testutil.NewPostgresDB(t)
	var f catalogFixture

	f.orgA = seed.SeedOrg(t, handle, models.Organization{Slug: "org-a", DisplayName: "Org A", ContactEmail: "a@example.com"})
	f.orgB = seed.SeedOrg(t, handle, models.Organization{Slug: "org-b", DisplayName: "Org B", ContactEmail: "b@example.com"})

	f.userA = seed.SeedUser(t, handle, models.User{
		OrganizationID:  f.orgA.ID,
		ExternalSubject: "user-a-sub",
		Email:           "user@org-a.nl",
		DisplayName:     "User A",
	})
	f.userB = seed.SeedUser(t, handle, models.User{
		OrganizationID:  f.orgB.ID,
		ExternalSubject: "user-b-sub",
		Email:           "user@org-b.nl",
		DisplayName:     "User B",
	})

	f.appA = seed.SeedApp(t, handle, models.App{
		Slug:                 "draft-app-a",
		OwningOrganizationID: f.orgA.ID,
		OwningUserID:         f.userA.ID,
		GitHubInstallationID: 111,
		GitHubRepoOwner:      "owner-a",
		GitHubRepoName:       "repo-a",
		GitHubDefaultBranch:  "main",
		State:                models.AppDraft,
	})
	f.appB = seed.SeedApp(t, handle, models.App{
		Slug:                 "draft-app-b",
		OwningOrganizationID: f.orgB.ID,
		OwningUserID:         f.userB.ID,
		GitHubInstallationID: 222,
		GitHubRepoOwner:      "owner-b",
		GitHubRepoName:       "repo-b",
		GitHubDefaultBranch:  "main",
		State:                models.AppDraft,
	})

	return f, newTestHandlers(t, handle)
}

func listAppsCtx(e *echo.Echo, query string, p *auth.Principal) (echo.Context, *httptest.ResponseRecorder) {
	return echoContext(e, asPrincipal(newRequest(http.MethodGet, "/?"+query, ""), p))
}

func getAppCtx(e *echo.Echo, slug string, p *auth.Principal) (echo.Context, *httptest.ResponseRecorder) {
	return echoContext(e, asPrincipal(newRequest(http.MethodGet, "/", ""), p), "slug", slug)
}

func listedSlugs(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	var body struct {
		Items []handlers.AppCard `json:"items"`
		Total int64              `json:"total"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	slugs := make([]string, 0, len(body.Items))
	for _, it := range body.Items {
		slugs = append(slugs, it.Slug)
	}
	assert.Equal(t, int64(len(slugs)), body.Total)
	return slugs
}

// TestListAppsScopesUnpublishedToCallerOrg checks that non-published states
// are filtered to the caller's organisation unless the caller is an auditor
// or platform admin.
func TestListAppsScopesUnpublishedToCallerOrg(t *testing.T) {
	t.Parallel()
	f, h := newCatalogFixture(t)
	e := echo.New()

	t.Run("org_b_deployer_sees_only_org_b_drafts", func(t *testing.T) {
		c, rec := listAppsCtx(e, "status=draft", principal(f.userB, models.RoleDeployer))
		require.NoError(t, h.ListApps(c))
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.ElementsMatch(t, []string{f.appB.Slug}, listedSlugs(t, rec))
	})

	t.Run("auditor_sees_all_drafts", func(t *testing.T) {
		c, rec := listAppsCtx(e, "status=draft", principal(f.userB, models.RoleAuditor))
		require.NoError(t, h.ListApps(c))
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.ElementsMatch(t, []string{f.appA.Slug, f.appB.Slug}, listedSlugs(t, rec))
	})

	t.Run("platform_admin_sees_all_drafts", func(t *testing.T) {
		c, rec := listAppsCtx(e, "status=draft", principal(f.userB, models.RolePlatformAdmin))
		require.NoError(t, h.ListApps(c))
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.ElementsMatch(t, []string{f.appA.Slug, f.appB.Slug}, listedSlugs(t, rec))
	})
}

// TestListAppsCarriesChartVersion checks that a catalogue card reports the
// chart version of its published version, so a list view can show it.
func TestListAppsCarriesChartVersion(t *testing.T) {
	t.Parallel()
	handle := testutil.NewPostgresDB(t)
	org := seed.SeedOrg(t, handle, models.Organization{})
	user := seed.SeedUser(t, handle, models.User{OrganizationID: org.ID})
	app := seed.SeedApp(t, handle, models.App{
		Slug:                 "versioned-app",
		OwningOrganizationID: org.ID,
		OwningUserID:         user.ID,
		State:                models.AppPublished,
	})
	ver, _ := seed.SeedPublishedVersion(t, handle, models.AppVersion{
		AppID:         app.ID,
		ChartMetadata: datatypes.JSON(`{"name":"versioned-app","version":"2.3.1"}`),
	})
	require.NoError(t, handle.RW.WithContext(t.Context()).
		Model(&app).Update("current_published_version_id", ver.ID).Error)
	h := newTestHandlers(t, handle)

	c, rec := listAppsCtx(echo.New(), "", nil)
	require.NoError(t, h.ListApps(c))
	var body struct {
		Items []handlers.AppCard `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Items, 1)
	assert.Equal(t, "2.3.1", body.Items[0].ChartVersion)

	c, rec = getAppCtx(echo.New(), app.Slug, nil)
	require.NoError(t, h.GetApp(c))
	var detail struct {
		ChartVersion string `json:"chart_version"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &detail))
	assert.Equal(t, "2.3.1", detail.ChartVersion, "the detail response keeps the field")
}

// TestGetAppHidesUnpublishedFromOtherOrgs checks that a non-published app is
// a 404 for callers outside its organisation, except auditors and platform
// admins.
func TestGetAppHidesUnpublishedFromOtherOrgs(t *testing.T) {
	t.Parallel()
	f, h := newCatalogFixture(t)
	e := echo.New()

	t.Run("other_org_gets_404", func(t *testing.T) {
		c, _ := getAppCtx(e, f.appA.Slug, principal(f.userB, models.RoleDeployer))
		err := h.GetApp(c)
		var apiErr *apierror.Error
		require.True(t, errors.As(err, &apiErr), "expected *apierror.Error, got %T: %v", err, err)
		assert.Equal(t, http.StatusNotFound, apiErr.Status)
	})

	t.Run("own_org_gets_200", func(t *testing.T) {
		c, rec := getAppCtx(e, f.appB.Slug, principal(f.userB, models.RoleDeveloper))
		require.NoError(t, h.GetApp(c))
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("auditor_gets_200", func(t *testing.T) {
		c, rec := getAppCtx(e, f.appA.Slug, principal(f.userB, models.RoleAuditor))
		require.NoError(t, h.GetApp(c))
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("platform_admin_gets_200", func(t *testing.T) {
		c, rec := getAppCtx(e, f.appA.Slug, principal(f.userB, models.RolePlatformAdmin))
		require.NoError(t, h.GetApp(c))
		assert.Equal(t, http.StatusOK, rec.Code)
	})
}
