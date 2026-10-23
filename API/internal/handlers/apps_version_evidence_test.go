package handlers_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/handlers"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/github/githubtest"
	"github.com/ClappFormOrg/SaidKick/API/internal/manifest"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// recordingClient is a MemoryClient that records every path GetFileAtRef
// is asked for.
type recordingClient struct {
	githubtest.MemoryClient

	mu    sync.Mutex
	paths []string
}

func (c *recordingClient) GetFileAtRef(ctx context.Context, installationID int64, owner, repo, path, ref string) ([]byte, string, error) {
	c.mu.Lock()
	c.paths = append(c.paths, path)
	c.mu.Unlock()
	return c.MemoryClient.GetFileAtRef(ctx, installationID, owner, repo, path, ref)
}

func (c *recordingClient) fetched() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.paths)
}

// versionEvidenceFixture is an existing govchat app owned by a developer,
// plus an auditor from another organisation.
type versionEvidenceFixture struct {
	handle  *db.Handle
	app     models.App
	dev     models.User
	auditor models.User
}

func newVersionEvidenceFixture(t *testing.T) versionEvidenceFixture {
	t.Helper()
	handle := testutil.NewPostgresDB(t)
	org := seed.SeedOrg(t, handle, models.Organization{Slug: "ver-org", DisplayName: "Version Org", ContactEmail: "ver@example.com"})
	dev := seed.SeedUser(t, handle, models.User{
		OrganizationID:  org.ID,
		ExternalSubject: "ver-dev",
		Email:           "dev@ver-org.nl",
		DisplayName:     "Developer",
	})
	audOrg := seed.SeedOrg(t, handle, models.Organization{Slug: "ver-aud-org", DisplayName: "Auditor Org", ContactEmail: "aud@example.com"})
	auditor := seed.SeedUser(t, handle, models.User{
		OrganizationID:  audOrg.ID,
		ExternalSubject: "ver-aud",
		Email:           "auditor@ver-aud-org.nl",
		DisplayName:     "Auditor",
	})
	app := seed.SeedApp(t, handle, models.App{
		Slug:                 "govchat",
		OwningOrganizationID: org.ID,
		OwningUserID:         dev.ID,
		State:                models.AppDraft,
	})
	return versionEvidenceFixture{handle: handle, app: app, dev: dev, auditor: auditor}
}

// createVersion calls POST /apps/govchat/versions as the app's developer.
func createVersion(h *handlers.Handlers, f versionEvidenceFixture) (int, error) {
	req := asPrincipal(newRequest(http.MethodPost, "/", `{"git_ref":"main"}`), principal(f.dev, models.RoleDeveloper))
	c, rec := echoContext(echo.New(), req, "slug", f.app.Slug)
	err := h.CreateAppVersion(c)
	return rec.Code, err
}

// TestCreateAppVersionStoresEvidence: a new version stores every validated
// evidence file as an EvidenceBlob, auditors are served those blobs without
// a GitHub fetch, and the SARIF reporter runs with the persisted IDs.
func TestCreateAppVersionStoresEvidence(t *testing.T) {
	t.Parallel()
	f := newVersionEvidenceFixture(t)
	repo := createAppRepo(trivyWarningSARIF)
	h := newTestHandlers(t, f.handle, withGitHub(repo))
	reporter := &fakeSARIFReporter{}
	h.SARIFReporter = reporter

	code, err := createVersion(h, f)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, code)

	var ver models.AppVersion
	require.NoError(t, f.handle.RW.Where("app_id = ?", f.app.ID).First(&ver).Error)
	var sub models.AuditSubmission
	require.NoError(t, f.handle.RW.First(&sub, "id = ?", ver.AuditSubmissionID).Error)
	var declared []manifest.EvidenceFileInfo
	require.NoError(t, json.Unmarshal(sub.EvidenceFiles, &declared))
	require.NotEmpty(t, declared)

	var blobs []models.EvidenceBlob
	require.NoError(t, f.handle.RW.Where("audit_submission_id = ?", sub.ID).Find(&blobs).Error)
	require.Len(t, blobs, len(declared), "one blob per declared evidence file")
	byName := make(map[string]models.EvidenceBlob, len(blobs))
	for _, b := range blobs {
		byName[b.LogicalName] = b
	}
	for _, d := range declared {
		b, ok := byName[d.LogicalName]
		require.True(t, ok, "no blob stored for %s", d.LogicalName)
		assert.Equal(t, d.RepoPath, b.RepoPath)
		assert.Equal(t, d.ContentType, b.ContentType)
		assert.Equal(t, repo.Files[d.RepoPath], b.Content, "%s content", d.LogicalName)
		assert.Equal(t, sha256.Sum256(repo.Files[d.RepoPath]), sha256.Sum256(b.Content), "%s sha256", d.LogicalName)
		assert.Len(t, b.Content, d.SizeBytes, "%s size", d.LogicalName)
	}
	assert.Equal(t, []byte(trivyWarningSARIF), byName["security_scan"].Content)

	// The report-only SARIF reporter still runs, after persistence.
	require.Len(t, reporter.reports, 1)
	assert.Equal(t, ver.ID, reporter.reports[0].subject.AppVersionID)
	assert.Equal(t, sub.ID, reporter.reports[0].subject.AuditSubmissionID)

	// GetAuditEvidence serves the stored blob without asking GitHub.
	rc := &recordingClient{MemoryClient: repo}
	auditH := newTestHandlers(t, f.handle, withGitHub(rc))
	c, rec := evidenceCtx(echo.New(), sub.ID, "dpia", principal(f.auditor, models.RoleAuditor))
	require.NoError(t, auditH.GetAuditEvidence(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/pdf", rec.Header().Get(echo.HeaderContentType))
	assert.Equal(t, string(repo.Files["audit/dpia.pdf"]), rec.Body.String())
	assert.NotContains(t, rc.fetched(), "audit/dpia.pdf", "evidence must be served from the cache")
}

// TestCreateAppVersionEvidenceFailureRollsBack: when storing an evidence
// blob fails, the version and submission are rolled back with it and
// nothing is reported.
func TestCreateAppVersionEvidenceFailureRollsBack(t *testing.T) {
	t.Parallel()
	f := newVersionEvidenceFixture(t)
	errBlob := errors.New("evidence blob insert failed")
	require.NoError(t, f.handle.RW.Callback().Create().Before("gorm:create").
		Register("test:fail_evidence_blob", func(tx *gorm.DB) {
			if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "evidence_blobs" {
				_ = tx.AddError(errBlob)
			}
		}))
	h := newTestHandlers(t, f.handle, withGitHub(createAppRepo(trivyWarningSARIF)))
	reporter := &fakeSARIFReporter{}
	h.SARIFReporter = reporter

	_, err := createVersion(h, f)
	require.ErrorIs(t, err, errBlob)

	var versions, subs, blobs int64
	require.NoError(t, f.handle.RW.Model(&models.AppVersion{}).Where("app_id = ?", f.app.ID).Count(&versions).Error)
	require.NoError(t, f.handle.RW.Model(&models.AuditSubmission{}).Count(&subs).Error)
	require.NoError(t, f.handle.RW.Model(&models.EvidenceBlob{}).Count(&blobs).Error)
	assert.Zero(t, versions, "version must be rolled back")
	assert.Zero(t, subs, "submission must be rolled back")
	assert.Zero(t, blobs, "no evidence blob may remain")
	assert.Empty(t, reporter.reports, "a rolled-back version must not be reported")
}
