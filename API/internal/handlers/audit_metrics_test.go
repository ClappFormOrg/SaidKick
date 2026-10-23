package handlers_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
)

// The metrics are process-wide counters, so the tests in this file do not
// call t.Parallel: they run before any parallel test in the package resumes,
// and the deltas they assert stay exact.

// stage1Counts snapshots audit_submissions_total and
// manifest_gate_rejections_total.
type stage1Counts struct {
	success, failure uint64
	gates            map[string]uint64
}

func snapshotStage1() stage1Counts {
	s := stage1Counts{
		success: metrics.AuditSubmissionsTotal(metrics.OutcomeSuccess).Get(),
		failure: metrics.AuditSubmissionsTotal(metrics.OutcomeFailure).Get(),
		gates:   map[string]uint64{},
	}
	for _, g := range metrics.ManifestGates {
		s.gates[g] = metrics.ManifestGateRejectionsTotal(g).Get()
	}
	return s
}

// gateDeltas returns how far each gate counter moved since s.
func (s stage1Counts) gateDeltas() map[string]uint64 {
	out := map[string]uint64{}
	for _, g := range metrics.ManifestGates {
		if d := metrics.ManifestGateRejectionsTotal(g).Get() - s.gates[g]; d > 0 {
			out[g] = d
		}
	}
	return out
}

func createAppRequest(p *models.User) echo.Context {
	c, _ := echoContext(echo.New(), asPrincipal(newRequest(http.MethodPost, "/", createAppBody), principal(*p, models.RoleDeveloper)))
	return c
}

func TestCreateAppSARIFRejectionCountsOneGateRejection(t *testing.T) {
	// 30 blocking findings in one scan.
	results := strings.Repeat(`{"level":"error","ruleId":"sqli","properties":{"security-severity":"9.1"}},`, 30)
	sarif := `{"runs":[{"results":[` + strings.TrimSuffix(results, ",") + `]}]}`
	h := newTestHandlers(t, nil, withGitHub(createAppRepo(sarif)))
	u := testUser()

	before := snapshotStage1()
	apiErr := requireAPIError(t, h.CreateApp(createAppRequest(&u)), http.StatusUnprocessableEntity)
	require.True(t, hasFieldCode(apiErr.Errors, "sarif_high_severity_finding"))

	assert.Equal(t, map[string]uint64{metrics.GateSARIF: 1}, before.gateDeltas())
	assert.Equal(t, before.failure+1, metrics.AuditSubmissionsTotal(metrics.OutcomeFailure).Get())
	assert.Equal(t, before.success, metrics.AuditSubmissionsTotal(metrics.OutcomeSuccess).Get())
}

func TestCreateAppMissingChartCountsChartGate(t *testing.T) {
	repo := createAppRepo(legacyErrorSARIF)
	// A clean scan, so the only failure is the missing chart metadata.
	repo.Files["audit/scan.sarif"] = []byte(`{"runs":[{"results":[]}]}`)
	delete(repo.Files, "chart/Chart.yaml")
	h := newTestHandlers(t, nil, withGitHub(repo))
	u := testUser()

	before := snapshotStage1()
	requireAPIError(t, h.CreateApp(createAppRequest(&u)), http.StatusUnprocessableEntity)
	assert.Equal(t, map[string]uint64{metrics.GateChart: 1}, before.gateDeltas())
	assert.Equal(t, before.failure+1, metrics.AuditSubmissionsTotal(metrics.OutcomeFailure).Get())
}

func TestCreateAppInfrastructureErrorIsNotCounted(t *testing.T) {
	// No GitHub client: the handler answers 503 before any gate runs.
	h := newTestHandlers(t, nil)
	u := testUser()

	before := snapshotStage1()
	requireAPIError(t, h.CreateApp(createAppRequest(&u)), http.StatusServiceUnavailable)
	assert.Empty(t, before.gateDeltas())
	assert.Equal(t, before.failure, metrics.AuditSubmissionsTotal(metrics.OutcomeFailure).Get())
	assert.Equal(t, before.success, metrics.AuditSubmissionsTotal(metrics.OutcomeSuccess).Get())
}

func TestCreateAppCountsSuccessfulSubmission(t *testing.T) {
	handle := testutil.NewPostgresDB(t)
	org := seed.SeedOrg(t, handle, models.Organization{Slug: "metrics-org", DisplayName: "Metrics Org", ContactEmail: "m@example.com"})
	user := seed.SeedUser(t, handle, models.User{
		OrganizationID: org.ID, ExternalSubject: "metrics-sub", Email: "dev@metrics-org.nl", DisplayName: "Developer",
	})
	h := newTestHandlers(t, handle, withGitHub(createAppRepo(trivyWarningSARIF)))

	before := snapshotStage1()
	c, rec := echoContext(echo.New(), asPrincipal(newRequest(http.MethodPost, "/", createAppBody), principal(user, models.RoleDeveloper)))
	require.NoError(t, h.CreateApp(c))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	assert.Equal(t, before.success+1, metrics.AuditSubmissionsTotal(metrics.OutcomeSuccess).Get())
	assert.Equal(t, before.failure, metrics.AuditSubmissionsTotal(metrics.OutcomeFailure).Get())
	assert.Empty(t, before.gateDeltas())
}

const metricsRationale = "The application meets all security requirements and documentation standards for publication."

func TestDecisionCountsAndObservesLatency(t *testing.T) {
	f, h := newAuditFixture(t)
	approvals := metrics.AuditDecisionsTotal(string(models.DecisionApprove)).Get()
	observed := testutil.HistogramCount(metrics.AuditDecisionLatencySeconds)

	c, rec := decisionRequest(echo.New(), f.sub.ID,
		`{"decision":"approve","rationale":"`+metricsRationale+`"}`, principal(f.audUser, models.RoleAuditor))
	require.NoError(t, h.DecideAuditSubmission(c))
	require.Equal(t, http.StatusOK, rec.Code)

	assert.Equal(t, approvals+1, metrics.AuditDecisionsTotal(string(models.DecisionApprove)).Get())
	assert.Equal(t, observed+1, testutil.HistogramCount(metrics.AuditDecisionLatencySeconds))

	// A second decision is refused (409) and counts nothing.
	c, _ = decisionRequest(echo.New(), f.sub.ID,
		`{"decision":"reject","rationale":"`+metricsRationale+`"}`, principal(f.audUser, models.RoleAuditor))
	requireAPIError(t, h.DecideAuditSubmission(c), http.StatusConflict)
	assert.Equal(t, approvals+1, metrics.AuditDecisionsTotal(string(models.DecisionApprove)).Get())
	assert.Equal(t, observed+1, testutil.HistogramCount(metrics.AuditDecisionLatencySeconds))

	testutil.RequireNoIdentifyingLabels(t)
}

func TestRolledBackDecisionCountsNothing(t *testing.T) {
	f, h := newAuditFixture(t)
	// Fail the AppVersion update that follows the decision insert, inside
	// the same transaction. The fixture owns its database.
	require.NoError(t, f.handle.RW.Exec(`CREATE FUNCTION test_refuse_version_update() RETURNS trigger AS $$
		BEGIN RAISE EXCEPTION 'forced version update failure'; END; $$ LANGUAGE plpgsql`).Error)
	require.NoError(t, f.handle.RW.Exec(`CREATE TRIGGER test_refuse_version_update BEFORE UPDATE ON app_versions
		FOR EACH ROW EXECUTE FUNCTION test_refuse_version_update()`).Error)

	approvals := metrics.AuditDecisionsTotal(string(models.DecisionApprove)).Get()
	observed := testutil.HistogramCount(metrics.AuditDecisionLatencySeconds)

	c, _ := decisionRequest(echo.New(), f.sub.ID,
		`{"decision":"approve","rationale":"`+metricsRationale+`"}`, principal(f.audUser, models.RoleAuditor))
	require.ErrorContains(t, h.DecideAuditSubmission(c), "forced version update failure")

	assert.Equal(t, approvals, metrics.AuditDecisionsTotal(string(models.DecisionApprove)).Get())
	assert.Equal(t, observed, testutil.HistogramCount(metrics.AuditDecisionLatencySeconds))
	var n int64
	require.NoError(t, f.handle.RW.Model(&models.AuditDecision{}).Where("audit_submission_id = ?", f.sub.ID).Count(&n).Error)
	assert.Zero(t, n, "the decision row must have been rolled back")
}
