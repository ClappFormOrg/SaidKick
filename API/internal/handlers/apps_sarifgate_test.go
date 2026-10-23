package handlers_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/github/githubtest"
	"github.com/ClappFormOrg/SaidKick/API/internal/manifest"
	"github.com/ClappFormOrg/SaidKick/API/internal/sarifgate"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// trivyWarningSARIF is a Trivy-shaped scan: a CVSS 9.8 finding whose
// severity sits on the rule and whose result is level warning. The legacy
// rule lets it through; the stricter rule blocks it.
const trivyWarningSARIF = `{"version":"2.1.0","runs":[{
	"tool":{"driver":{"name":"Trivy","rules":[{"id":"CVE-2024-0001","properties":{"security-severity":"9.8"}}]}},
	"results":[{"ruleId":"CVE-2024-0001","level":"warning","message":{"text":"Package: openssl"}}]}]}`

// legacyErrorSARIF is blocked by both rules: level error, result-level 9.1.
const legacyErrorSARIF = `{"runs":[{"results":[{"level":"error","ruleId":"sqli","properties":{"security-severity":"9.1"}}]}]}`

type sarifReport struct {
	subject  sarifgate.Subject
	findings []manifest.SARIFFinding
}

// fakeSARIFReporter records ReportWouldBlock calls.
type fakeSARIFReporter struct{ reports []sarifReport }

func (f *fakeSARIFReporter) ReportWouldBlock(_ context.Context, s sarifgate.Subject, findings []manifest.SARIFFinding) {
	f.reports = append(f.reports, sarifReport{subject: s, findings: findings})
}

// createAppRepo returns a fake repo with a valid manifest, chart and
// evidence, serving sarif as the security scan.
func createAppRepo(sarif string) githubtest.MemoryClient {
	files := githubtest.EvidenceFiles()
	files["appstore.yaml"] = []byte(validAppstoreYAML)
	files["chart/Chart.yaml"] = []byte("name: govchat\nversion: 1.0.0\n")
	files["chart/values.schema.json"] = []byte(`{"type":"object"}`)
	files["audit/scan.sarif"] = []byte(sarif)
	return githubtest.MemoryClient{Files: files}
}

const createAppBody = `{"github_installation_id":1,"repo_owner":"clappform","repo_name":"govchat","branch":"main"}`

// TestCreateAppSARIFGateBlocksBeforePersisting covers the cases that reject
// at Stage 1, before the database is touched: the stricter rule in enforce
// mode, and the legacy rule in either mode. None of them report.
func TestCreateAppSARIFGateBlocksBeforePersisting(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		mode  manifest.SARIFGateMode
		sarif string
	}{
		{"enforce blocks rule-level warning", manifest.SARIFGateEnforce, trivyWarningSARIF},
		{"report still blocks legacy finding", manifest.SARIFGateReport, legacyErrorSARIF},
		{"enforce blocks legacy finding", manifest.SARIFGateEnforce, legacyErrorSARIF},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newTestHandlers(t, nil, withGitHub(createAppRepo(tc.sarif)))
			h.SARIFGateMode = tc.mode
			reporter := &fakeSARIFReporter{}
			h.SARIFReporter = reporter

			c, _ := echoContext(echo.New(), asPrincipal(newRequest(http.MethodPost, "/", createAppBody), principal(testUser(), models.RoleDeveloper)))
			apiErr := requireAPIError(t, h.CreateApp(c), http.StatusUnprocessableEntity)
			assert.True(t, hasFieldCode(apiErr.Errors, "sarif_high_severity_finding"),
				"want a blocking SARIF finding, got %+v", apiErr.Errors)
			assert.Empty(t, reporter.reports, "a blocked submission must not be reported")
		})
	}
}

// TestCreateAppSARIFGateReportMode: in report mode a finding only the
// stricter rule catches does not block; once the submission is persisted it
// is reported with the new app, version and submission IDs.
func TestCreateAppSARIFGateReportMode(t *testing.T) {
	t.Parallel()
	handle := testutil.NewPostgresDB(t)

	org := seed.SeedOrg(t, handle, models.Organization{Slug: "sarif-org", DisplayName: "SARIF Org", ContactEmail: "sarif@example.com"})
	user := seed.SeedUser(t, handle, models.User{
		OrganizationID:  org.ID,
		ExternalSubject: "sarif-sub",
		Email:           "dev@sarif-org.nl",
		DisplayName:     "Developer",
	})

	h := newTestHandlers(t, handle, withGitHub(createAppRepo(trivyWarningSARIF)))
	require.Equal(t, manifest.SARIFGateReport, h.SARIFGateMode, "report must be the default mode")
	reporter := &fakeSARIFReporter{}
	h.SARIFReporter = reporter

	c, rec := echoContext(echo.New(), asPrincipal(newRequest(http.MethodPost, "/", createAppBody), principal(user, models.RoleDeveloper)))
	require.NoError(t, h.CreateApp(c))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	require.Len(t, reporter.reports, 1)
	r := reporter.reports[0]
	require.Len(t, r.findings, 1)
	assert.Equal(t, "CVE-2024-0001", r.findings[0].RuleID)
	assert.Equal(t, manifest.SARIFSourceRule, r.findings[0].Source)
	assert.InDelta(t, 9.8, r.findings[0].Severity, 1e-9)

	var app models.App
	require.NoError(t, handle.RW.Where("slug = ?", "govchat").First(&app).Error)
	var ver models.AppVersion
	require.NoError(t, handle.RW.Where("app_id = ?", app.ID).First(&ver).Error)
	assert.Equal(t, app.ID, r.subject.AppID)
	assert.Equal(t, "govchat", r.subject.AppSlug)
	assert.Equal(t, ver.ID, r.subject.AppVersionID)
	assert.Equal(t, ver.AuditSubmissionID, r.subject.AuditSubmissionID)
	assert.Equal(t, githubtest.SHA, r.subject.GitSHA)
	require.NotNil(t, r.subject.ActorUserID)
	assert.Equal(t, user.ID, *r.subject.ActorUserID)
}
