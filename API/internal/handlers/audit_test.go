package handlers_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/handlers"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

// auditFixture holds the seeded rows needed by DecideAuditSubmission tests.
type auditFixture struct {
	handle   *db.Handle
	devOrg   models.Organization
	auditOrg models.Organization
	devUser  models.User
	audUser  models.User
	app      models.App
	version  models.AppVersion
	sub      models.AuditSubmission
}

// newAuditFixture creates two orgs, two users, one submitted app + version +
// audit submission, and returns a Handlers ready to call.
func newAuditFixture(t *testing.T) (auditFixture, *handlers.Handlers) {
	t.Helper()

	handle := testutil.NewPostgresDB(t)
	var f auditFixture
	f.handle = handle

	f.devOrg = seed.SeedOrg(t, handle, models.Organization{
		Slug: "dev-org", DisplayName: "Dev Org", ContactEmail: "dev@example.com",
	})
	f.auditOrg = seed.SeedOrg(t, handle, models.Organization{
		Slug: "audit-org", DisplayName: "Audit Org", ContactEmail: "audit@example.com",
	})

	f.devUser = seed.SeedUser(t, handle, models.User{
		OrganizationID:  f.devOrg.ID,
		ExternalSubject: "dev-sub",
		Email:           "developer@dev-org.nl",
		DisplayName:     "Test Developer",
	})
	f.audUser = seed.SeedUser(t, handle, models.User{
		OrganizationID:  f.auditOrg.ID,
		ExternalSubject: "aud-sub",
		Email:           "auditor@audit-org.nl",
		DisplayName:     "Test Auditor",
	})

	// App (owned by devOrg).
	f.app = seed.SeedApp(t, handle, models.App{
		Slug:                 "test-app",
		OwningOrganizationID: f.devOrg.ID,
		OwningUserID:         f.devUser.ID,
		GitHubInstallationID: 12345,
		GitHubRepoOwner:      "test-owner",
		GitHubRepoName:       "test-repo",
		GitHubDefaultBranch:  "main",
		State:                models.AppSubmitted,
	})

	// Submitted version plus its audit submission.
	f.version, f.sub = seed.SeedAppVersion(t, handle, models.AppVersion{
		AppID:         f.app.ID,
		GitRef:        "v1.0.0",
		GitSHA:        "abcdef1234567890abcdef1234567890abcdef12",
		AppstoreYAML:  datatypes.JSON(`{"apiVersion":"appstore.gov.nl/v1","kind":"Application","metadata":{"name":"test-app"},"spec":{"ai":{"risk_class":"minimal"}}}`),
		ValuesSchema:  datatypes.JSON(`{}`),
		ChartMetadata: datatypes.JSON(`{"name":"test-app","version":"1.0.0"}`),
		State:         models.AppVersionSubmitted,
	})

	return f, newTestHandlers(t, handle)
}

// decisionRequest builds an Echo context + recorder for the decision endpoint.
func decisionRequest(e *echo.Echo, subID uuid.UUID, body string, p *auth.Principal) (echo.Context, *httptest.ResponseRecorder) {
	req := asPrincipal(newRequest(http.MethodPost, "/", body), p)
	return echoContext(e, req, "id", subID.String())
}

// TestDecideAuditSubmission covers the three critical paths of the decision
// endpoint: successful approval (cross-org), same-org rejection (403), and
// duplicate decision (409).
func TestDecideAuditSubmission(t *testing.T) {
	t.Parallel()
	f, h := newAuditFixture(t)
	e := echo.New()

	const rationale = "The application meets all security requirements and documentation standards for publication."

	t.Run("approve_from_different_org_publishes_version_and_app", func(t *testing.T) {
		auditorPrincipal := principal(f.audUser, models.RoleAuditor)
		c, rec := decisionRequest(e, f.sub.ID,
			`{"decision":"approve","rationale":"`+rationale+`"}`,
			auditorPrincipal)

		require.NoError(t, h.DecideAuditSubmission(c))
		assert.Equal(t, http.StatusOK, rec.Code)

		// Version state must be published.
		var version models.AppVersion
		require.NoError(t, f.handle.RW.First(&version, "id = ?", f.version.ID).Error)
		assert.Equal(t, models.AppVersionPublished, version.State)

		// App must be promoted and current version pointer set.
		var app models.App
		require.NoError(t, f.handle.RW.First(&app, "id = ?", f.app.ID).Error)
		assert.Equal(t, models.AppPublished, app.State)
		require.NotNil(t, app.CurrentPublishedVersionID)
		assert.Equal(t, f.version.ID, *app.CurrentPublishedVersionID)
	})

	t.Run("same_org_returns_403", func(t *testing.T) {
		// Developer from the app's own org attempts to decide — must be rejected.
		devOrgPrincipal := principal(f.devUser, models.RoleAuditor)
		c, _ := decisionRequest(e, f.sub.ID,
			`{"decision":"approve","rationale":"`+rationale+`"}`,
			devOrgPrincipal)

		err := h.DecideAuditSubmission(c)
		var apiErr *apierror.Error
		require.True(t, errors.As(err, &apiErr), "expected *apierror.Error, got %T: %v", err, err)
		assert.Equal(t, http.StatusForbidden, apiErr.Status)
		assert.Equal(t, "same_org_audit_forbidden", apiErr.Code)
	})

	t.Run("double_decision_returns_409", func(t *testing.T) {
		// The approve test above already decided this submission; a second
		// attempt must return 409 (at-most-one decision per submission).
		auditorPrincipal := principal(f.audUser, models.RoleAuditor)
		c, _ := decisionRequest(e, f.sub.ID,
			`{"decision":"approve","rationale":"`+rationale+`"}`,
			auditorPrincipal)

		err := h.DecideAuditSubmission(c)
		var apiErr *apierror.Error
		require.True(t, errors.As(err, &apiErr), "expected *apierror.Error, got %T: %v", err, err)
		assert.Equal(t, http.StatusConflict, apiErr.Status)
		assert.Equal(t, "already_decided", apiErr.Code)
	})

	t.Run("unknown_submission_returns_404", func(t *testing.T) {
		auditorPrincipal := principal(f.audUser, models.RoleAuditor)
		c, _ := decisionRequest(e, uuid.New(),
			`{"decision":"approve","rationale":"`+rationale+`"}`,
			auditorPrincipal)

		apiErr := requireAPIError(t, h.DecideAuditSubmission(c), http.StatusNotFound)
		assert.Equal(t, "not_found", apiErr.Code)
		assert.Equal(t, "submission not found", apiErr.Message)
	})
}

// TestListAuditLog_HandlerRoles calls the handler directly, bypassing the
// route gate, and checks that it refuses every role but platform_admin
// before touching the database.
func TestListAuditLog_HandlerRoles(t *testing.T) {
	t.Parallel()
	h := newTestHandlers(t, nil)

	for _, role := range nonPlatformRoles {
		t.Run(string(role)+"_returns_403", func(t *testing.T) {
			t.Parallel()
			c, _ := echoContext(echo.New(), asPrincipal(newRequest(http.MethodGet, "/", ""), principal(testUser(), role)))
			apiErr := requireAPIError(t, h.ListAuditLog(c), http.StatusForbidden)
			assert.Equal(t, "forbidden", apiErr.Code)
			assert.Equal(t, "missing required role", apiErr.Message)
		})
	}
}

// auditPath calls one of the audit handlers that applies the cross-org rule.
type auditPath struct {
	name string
	call func(h *handlers.Handlers, subID uuid.UUID, p *auth.Principal) (*httptest.ResponseRecorder, error)
}

func getSubmission(h *handlers.Handlers, subID uuid.UUID, p *auth.Principal) (*httptest.ResponseRecorder, error) {
	c, rec := echoContext(echo.New(), asPrincipal(newRequest(http.MethodGet, "/", ""), p), "id", subID.String())
	return rec, h.GetAuditSubmission(c)
}

func commentOnSubmission(h *handlers.Handlers, subID uuid.UUID, p *auth.Principal) (*httptest.ResponseRecorder, error) {
	req := asPrincipal(newRequest(http.MethodPost, "/", `{"body":"Please attach the DPIA."}`), p)
	c, rec := echoContext(echo.New(), req, "id", subID.String())
	return rec, h.CommentOnAuditSubmission(c)
}

func listComments(h *handlers.Handlers, subID uuid.UUID, p *auth.Principal) (*httptest.ResponseRecorder, error) {
	c, rec := echoContext(echo.New(), asPrincipal(newRequest(http.MethodGet, "/", ""), p), "id", subID.String())
	return rec, h.ListAuditComments(c)
}

func getEvidence(h *handlers.Handlers, subID uuid.UUID, p *auth.Principal) (*httptest.ResponseRecorder, error) {
	c, rec := evidenceCtx(echo.New(), subID, "dpia", p)
	return rec, h.GetAuditEvidence(c)
}

func decide(h *handlers.Handlers, subID uuid.UUID, p *auth.Principal) (*httptest.ResponseRecorder, error) {
	const rationale = "The application meets all security requirements and documentation standards for publication."
	c, rec := decisionRequest(echo.New(), subID, `{"decision":"approve","rationale":"`+rationale+`"}`, p)
	return rec, h.DecideAuditSubmission(c)
}

// readAndReviewPaths are the audit paths on which platform admins are
// exempt from the cross-org rule.
var readAndReviewPaths = []auditPath{
	{"get_submission", getSubmission},
	{"comment", commentOnSubmission},
	{"list_comments", listComments},
	{"get_evidence", getEvidence},
}

// TestAuditConflictOfInterest covers the cross-org rule on every audit path
// that enforces it: platform admins may read and review their own org's
// submissions but may never decide on them.
func TestAuditConflictOfInterest(t *testing.T) {
	t.Parallel()

	t.Run("platform_admin_from_app_org_can_list_submissions", func(t *testing.T) {
		t.Parallel()
		f, h := newAuditFixture(t)
		admin := principal(f.devUser, models.RoleAuditor, models.RolePlatformAdmin)

		c, rec := echoContext(echo.New(), asPrincipal(newRequest(http.MethodGet, "/", ""), admin))
		require.NoError(t, h.ListAuditSubmissions(c))
		assert.Equal(t, http.StatusOK, rec.Code)

		var body struct {
			Items []struct {
				ID       string `json:"id"`
				IsOwnOrg bool   `json:"is_own_org"`
			} `json:"items"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Len(t, body.Items, 1)
		assert.Equal(t, f.sub.ID.String(), body.Items[0].ID)
		assert.True(t, body.Items[0].IsOwnOrg)
	})

	for _, path := range readAndReviewPaths {
		t.Run("platform_admin_from_app_org_is_allowed_on_"+path.name, func(t *testing.T) {
			t.Parallel()
			f, h := newAuditFixture(t)
			seedEvidenceBlob(t, f)
			admin := principal(f.devUser, models.RoleAuditor, models.RolePlatformAdmin)

			rec, err := path.call(h, f.sub.ID, admin)
			require.NoError(t, err)
			assert.Less(t, rec.Code, http.StatusMultipleChoices, "expected a 2xx, got %d", rec.Code)
		})
	}

	t.Run("platform_admin_from_app_org_cannot_decide", func(t *testing.T) {
		t.Parallel()
		f, h := newAuditFixture(t)
		admin := principal(f.devUser, models.RoleAuditor, models.RolePlatformAdmin)

		rec, err := decide(h, f.sub.ID, admin)
		apiErr := requireAPIError(t, err, http.StatusForbidden)
		assert.Equal(t, "same_org_audit_forbidden", apiErr.Code)
		assert.Empty(t, rec.Body.String())

		var decisions int64
		require.NoError(t, f.handle.RW.Model(&models.AuditDecision{}).
			Where("audit_submission_id = ?", f.sub.ID).Count(&decisions).Error)
		assert.Zero(t, decisions, "a refused decision must not be persisted")

		var version models.AppVersion
		require.NoError(t, f.handle.RW.First(&version, "id = ?", f.version.ID).Error)
		assert.Equal(t, models.AppVersionSubmitted, version.State)
	})

	t.Run("platform_admin_can_decide_on_other_org_app", func(t *testing.T) {
		t.Parallel()
		f, h := newAuditFixture(t)
		admin := principal(f.devUser, models.RoleAuditor, models.RolePlatformAdmin)

		otherApp := seed.SeedApp(t, f.handle, models.App{
			Slug:                 "other-org-app",
			OwningOrganizationID: f.auditOrg.ID,
			OwningUserID:         f.audUser.ID,
			State:                models.AppSubmitted,
		})
		otherVersion, otherSub := seed.SeedAppVersion(t, f.handle, models.AppVersion{
			AppID: otherApp.ID,
			State: models.AppVersionSubmitted,
		})

		rec, err := decide(h, otherSub.ID, admin)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, rec.Code)

		var version models.AppVersion
		require.NoError(t, f.handle.RW.First(&version, "id = ?", otherVersion.ID).Error)
		assert.Equal(t, models.AppVersionPublished, version.State)
	})

	allPaths := append(slices.Clone(readAndReviewPaths), auditPath{"decide", decide})
	for _, path := range allPaths {
		t.Run("auditor_from_app_org_returns_403_on_"+path.name, func(t *testing.T) {
			t.Parallel()
			f, h := newAuditFixture(t)
			seedEvidenceBlob(t, f)
			auditor := principal(f.devUser, models.RoleAuditor)

			rec, err := path.call(h, f.sub.ID, auditor)
			apiErr := requireAPIError(t, err, http.StatusForbidden)
			assert.Equal(t, "same_org_audit_forbidden", apiErr.Code)
			assert.Empty(t, rec.Body.String())
		})
	}
}
