package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) { os.Exit(testutil.RunMain(m)) }

// TestLoadRows runs the report query against Postgres: it returns recent
// submissions on live apps with their stored scan, and a nil scan when none
// was stored.
func TestLoadRows(t *testing.T) {
	t.Parallel()
	h := testutil.NewPostgresDB(t)
	ctx := t.Context()

	org := seed.SeedOrg(t, h, models.Organization{Slug: "report-org", DisplayName: "Report Org", ContactEmail: "r@example.com"})
	user := seed.SeedUser(t, h, models.User{OrganizationID: org.ID, ExternalSubject: "report-sub", Email: "r@report-org.nl", DisplayName: "R"})
	newApp := func(slug string) models.App {
		return seed.SeedApp(t, h, models.App{Slug: slug, OwningOrganizationID: org.ID, OwningUserID: user.ID})
	}

	// Recent submission with a stored scan.
	withScan := newApp("with-scan")
	verScan, subScan := seed.SeedAppVersion(t, h, models.AppVersion{AppID: withScan.ID, State: models.AppVersionDraft})
	scan := []byte(`{"runs":[{"results":[]}]}`)
	for _, b := range []models.EvidenceBlob{
		{AuditSubmissionID: subScan.ID, LogicalName: "security_scan", RepoPath: "audit/scan.sarif", ContentType: "application/sarif+json", Content: scan},
		{AuditSubmissionID: subScan.ID, LogicalName: "dpia", RepoPath: "audit/dpia.pdf", ContentType: "application/pdf", Content: []byte("%PDF")},
	} {
		require.NoError(t, h.RW.WithContext(ctx).Create(&b).Error)
	}

	// Recent submission without a stored scan.
	noScan := newApp("no-scan")
	_, subNoScan := seed.SeedAppVersion(t, h, models.AppVersion{AppID: noScan.ID, State: models.AppVersionDraft})

	// Submission older than the window.
	old := newApp("old")
	_, subOld := seed.SeedAppVersion(t, h, models.AppVersion{AppID: old.ID, State: models.AppVersionDraft})
	require.NoError(t, h.RW.WithContext(ctx).Model(&models.AuditSubmission{}).
		Where("id = ?", subOld.ID).Update("created_at", time.Now().AddDate(0, 0, -90)).Error)

	// Submission on a deleted app.
	deleted := newApp("deleted")
	seed.SeedAppVersion(t, h, models.AppVersion{AppID: deleted.ID, State: models.AppVersionDraft})
	require.NoError(t, h.RW.WithContext(ctx).Delete(&deleted).Error)

	rows, err := loadRows(ctx, h, time.Now().AddDate(0, 0, -30))
	require.NoError(t, err)
	require.Len(t, rows, 2, "want the two recent submissions on live apps")

	byID := map[uuid.UUID]scanRow{}
	for _, r := range rows {
		byID[r.SubmissionID] = r
	}
	got, ok := byID[subScan.ID]
	require.True(t, ok)
	assert.Equal(t, "with-scan", got.AppSlug)
	assert.Equal(t, verScan.ID, got.AppVersionID)
	assert.Equal(t, verScan.GitSHA, got.GitSHA)
	assert.Equal(t, scan, got.SARIF)

	got, ok = byID[subNoScan.ID]
	require.True(t, ok)
	assert.Empty(t, got.SARIF)
}

func TestEvaluateAndReport(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	row := func(slug, sarif string) scanRow {
		r := scanRow{SubmissionID: uuid.New(), CreatedAt: at, AppVersionID: uuid.New(), GitSHA: "abcdef1234567", AppSlug: slug}
		if sarif != "" {
			r.SARIF = []byte(sarif)
		}
		return r
	}
	rows := []scanRow{
		row("clean", `{"runs":[{"results":[]}]}`),
		row("trivy", `{"runs":[{"tool":{"driver":{"rules":[{"id":"CVE-1","properties":{"security-severity":"9.8"}}]}},
			"results":[{"ruleId":"CVE-1","level":"warning"}]}]}`),
		row("legacy", `{"runs":[{"results":[
			{"level":"error","ruleId":"sqli","properties":{"security-severity":"9.1"}},
			{"level":"warning","ruleId":"xss","properties":{"security-severity":"8.0"}}]}]}`),
		row("noscan", ""),
		row("broken", `{not json`),
	}

	s := evaluate(rows)
	if len(s.outcomes) != 5 || s.newly != 1 || s.legacy != 1 || s.noScan != 1 || s.unparseable != 1 {
		t.Fatalf("summary wrong: newly=%d legacy=%d noScan=%d unparseable=%d", s.newly, s.legacy, s.noScan, s.unparseable)
	}

	var buf bytes.Buffer
	printReport(&buf, at, 30, s, false)
	out := buf.String()
	for _, want := range []string{
		"newly blocked under enforce     1",
		"NEW      2026-10-01T12:00:00Z  app=trivy",
		"findings=CVE-1:9.8:rule:warning",
		"LEGACY   ",
		"NO-SCAN  ",
		"INVALID  ",
		"sha=abcdef1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "app=clean") {
		t.Errorf("unaffected submission listed without -v:\n%s", out)
	}

	buf.Reset()
	printReport(&buf, at, 30, s, true)
	if !strings.Contains(buf.String(), "PASS     2026-10-01T12:00:00Z  app=clean") {
		t.Errorf("-v must list unaffected submissions:\n%s", buf.String())
	}
}
