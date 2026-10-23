package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/github/githubtest"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

// evidenceCtx builds an Echo context for
// GET /audit-submissions/:id/evidence/:name.
func evidenceCtx(e *echo.Echo, subID uuid.UUID, name string, p *auth.Principal) (echo.Context, *httptest.ResponseRecorder) {
	req := asPrincipal(newRequest(http.MethodGet, "/", ""), p)
	return echoContext(e, req, "id", subID.String(), "name", name)
}

// seedEvidenceBlob caches a DPIA for the fixture's submission.
func seedEvidenceBlob(t *testing.T, f auditFixture) {
	t.Helper()
	require.NoError(t, f.handle.RW.WithContext(t.Context()).Create(&models.EvidenceBlob{
		AuditSubmissionID: f.sub.ID,
		LogicalName:       "dpia",
		RepoPath:          "audit/dpia.pdf",
		ContentType:       "application/pdf",
		Content:           []byte("%PDF-1.4 cached dpia"),
	}).Error)
}

// declareEvidence sets the submission's evidence list to one WCAG report
// that is not cached.
func declareEvidence(t *testing.T, f auditFixture) {
	t.Helper()
	require.NoError(t, f.handle.RW.WithContext(t.Context()).
		Model(&models.AuditSubmission{}).Where("id = ?", f.sub.ID).
		Update("evidence_files", datatypes.JSON(`[{"logical_name":"wcag","repo_path":"audit/wcag.pdf","content_type":"application/pdf"}]`)).Error)
}

func TestGetAuditEvidence(t *testing.T) {
	t.Parallel()
	t.Run("auditor_from_other_org_gets_cached_blob", func(t *testing.T) {
		t.Parallel()
		f, h := newAuditFixture(t)
		seedEvidenceBlob(t, f)

		c, rec := evidenceCtx(echo.New(), f.sub.ID, "dpia", principal(f.audUser, models.RoleAuditor))
		require.NoError(t, h.GetAuditEvidence(c))
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "application/pdf", rec.Header().Get(echo.HeaderContentType))
		assert.Equal(t, `attachment; filename="dpia.pdf"`, rec.Header().Get("Content-Disposition"))
		assert.Equal(t, "%PDF-1.4 cached dpia", rec.Body.String())
	})

	t.Run("auditor_from_app_org_returns_403", func(t *testing.T) {
		t.Parallel()
		f, h := newAuditFixture(t)
		seedEvidenceBlob(t, f)
		sameOrgAuditor := principal(f.devUser, models.RoleAuditor)

		c, rec := evidenceCtx(echo.New(), f.sub.ID, "dpia", sameOrgAuditor)
		apiErr := requireAPIError(t, h.GetAuditEvidence(c), http.StatusForbidden)
		assert.Equal(t, "same_org_audit_forbidden", apiErr.Code)
		assert.Empty(t, rec.Body.String(), "no evidence bytes may be written")
	})

	t.Run("platform_admin_from_app_org_is_allowed", func(t *testing.T) {
		t.Parallel()
		f, h := newAuditFixture(t)
		seedEvidenceBlob(t, f)

		c, rec := evidenceCtx(echo.New(), f.sub.ID, "dpia", principal(f.devUser, models.RoleAuditor, models.RolePlatformAdmin))
		require.NoError(t, h.GetAuditEvidence(c))
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("cache_miss_falls_back_to_github", func(t *testing.T) {
		t.Parallel()
		f, _ := newAuditFixture(t)
		declareEvidence(t, f)
		h := newTestHandlers(t, f.handle, withGitHub(githubtest.MemoryClient{Files: githubtest.EvidenceFiles()}))

		c, rec := evidenceCtx(echo.New(), f.sub.ID, "wcag", principal(f.audUser, models.RoleAuditor))
		require.NoError(t, h.GetAuditEvidence(c))
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, `attachment; filename="wcag.pdf"`, rec.Header().Get("Content-Disposition"))
		assert.Equal(t, "%PDF-1.4 wcag", rec.Body.String())
	})

	t.Run("cache_miss_not_on_github_returns_404", func(t *testing.T) {
		t.Parallel()
		f, _ := newAuditFixture(t)
		declareEvidence(t, f)
		h := newTestHandlers(t, f.handle, withGitHub(githubtest.MemoryClient{}))

		c, _ := evidenceCtx(echo.New(), f.sub.ID, "wcag", principal(f.audUser, models.RoleAuditor))
		requireAPIError(t, h.GetAuditEvidence(c), http.StatusNotFound)
	})

	t.Run("same_org_check_runs_before_github_fallback", func(t *testing.T) {
		t.Parallel()
		f, _ := newAuditFixture(t)
		declareEvidence(t, f)
		h := newTestHandlers(t, f.handle, withGitHub(githubtest.MemoryClient{Files: githubtest.EvidenceFiles()}))

		c, rec := evidenceCtx(echo.New(), f.sub.ID, "wcag", principal(f.devUser, models.RoleAuditor))
		requireAPIError(t, h.GetAuditEvidence(c), http.StatusForbidden)
		assert.Empty(t, rec.Body.String())
	})

	t.Run("undeclared_name_returns_404", func(t *testing.T) {
		t.Parallel()
		f, h := newAuditFixture(t)
		seedEvidenceBlob(t, f)

		c, _ := evidenceCtx(echo.New(), f.sub.ID, "ai-risk", principal(f.audUser, models.RoleAuditor))
		requireAPIError(t, h.GetAuditEvidence(c), http.StatusNotFound)
	})

	t.Run("unknown_submission_returns_404", func(t *testing.T) {
		t.Parallel()
		f, h := newAuditFixture(t)

		c, _ := evidenceCtx(echo.New(), uuid.New(), "dpia", principal(f.audUser, models.RoleAuditor))
		requireAPIError(t, h.GetAuditEvidence(c), http.StatusNotFound)
	})
}

// evidenceDeniedRoles are the roles, platform_admin included, that may not
// read evidence without also holding auditor.
var evidenceDeniedRoles = []models.Role{models.RoleDeveloper, models.RoleDeployer, models.RoleOrgAdmin, models.RoleClusterAdmin, models.RolePlatformAdmin}

// TestGetAuditEvidence_HandlerRoles calls the handler directly, bypassing
// the route gate, and checks that it refuses a caller without auditor
// before touching the database.
func TestGetAuditEvidence_HandlerRoles(t *testing.T) {
	t.Parallel()
	h := newTestHandlers(t, nil)

	for _, role := range evidenceDeniedRoles {
		t.Run(string(role)+"_returns_403", func(t *testing.T) {
			t.Parallel()
			c, rec := evidenceCtx(echo.New(), uuid.New(), "dpia", principal(testUser(), role))
			apiErr := requireAPIError(t, h.GetAuditEvidence(c), http.StatusForbidden)
			assert.Equal(t, "forbidden", apiErr.Code)
			assert.Empty(t, rec.Body.String())
		})
	}
}

// TestGetAuditEvidence_RouteRoles checks that callers without the auditor
// role are stopped by the route's role gate, platform_admin included.
func TestGetAuditEvidence_RouteRoles(t *testing.T) {
	t.Parallel()
	h := newTestHandlers(t, nil)
	target := "/api/v1/audit-submissions/" + uuid.NewString() + "/evidence/dpia"

	for _, role := range evidenceDeniedRoles {
		t.Run(string(role)+"_returns_403", func(t *testing.T) {
			t.Parallel()
			req := asPrincipal(newRequest(http.MethodGet, target, ""), principal(testUser(), role))
			assert.Equal(t, http.StatusForbidden, serveRoute(h, req).Code)
		})
	}

	t.Run("anonymous_returns_401", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, http.StatusUnauthorized, serveRoute(h, newRequest(http.MethodGet, target, "")).Code)
	})
}
