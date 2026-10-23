package audit_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
)

// The audit counters are process-wide, so these tests do not call
// t.Parallel: Go runs them before any parallel test in the package resumes,
// and the deltas they assert stay exact.

func auditCounters() (writes, failures uint64) {
	return metrics.AuditWritesTotal.Get(), metrics.AuditWriteFailuresTotal.Get()
}

func TestLogCountsEachAppendedRow(t *testing.T) {
	handle := testutil.NewPostgresDB(t)
	logger := audit.NewLogger(handle)
	subject := uuid.New()

	writes, failures := auditCounters()
	for i := range 3 {
		require.NoError(t, logger.Log(t.Context(), audit.Event{Type: "test.counted", SubjectType: "test", SubjectID: subject}), i)
	}
	w, f := auditCounters()
	assert.Equal(t, writes+3, w, "audit_writes_total must rise by one per row")
	assert.Equal(t, failures, f)

	var n int64
	require.NoError(t, handle.RW.Model(&models.AuditLog{}).Where("subject_id = ?", subject).Count(&n).Error)
	assert.Equal(t, int64(3), n)
}

// A database error inside the insert transaction rolls the row back. It must
// count as a failure, not a write.
func TestLogCountsRolledBackWriteAsFailure(t *testing.T) {
	handle := testutil.NewPostgresDB(t)
	logger := audit.NewLogger(handle)
	subject := uuid.New()

	// Each test owns its database, so this trigger affects no other test.
	require.NoError(t, handle.RW.Exec(`CREATE FUNCTION test_refuse_audit_insert() RETURNS trigger AS $$
		BEGIN RAISE EXCEPTION 'forced audit write failure'; END; $$ LANGUAGE plpgsql`).Error)
	require.NoError(t, handle.RW.Exec(`CREATE TRIGGER test_refuse_audit_insert BEFORE INSERT ON audit_logs
		FOR EACH ROW EXECUTE FUNCTION test_refuse_audit_insert()`).Error)

	writes, failures := auditCounters()
	err := logger.Log(t.Context(), audit.Event{Type: "test.refused", SubjectType: "test", SubjectID: subject})
	require.ErrorContains(t, err, "forced audit write failure")
	w, f := auditCounters()
	assert.Equal(t, writes, w, "a rolled-back write must not count as written")
	assert.Equal(t, failures+1, f)

	var n int64
	require.NoError(t, handle.RW.Model(&models.AuditLog{}).Where("subject_id = ?", subject).Count(&n).Error)
	assert.Zero(t, n, "the row must have been rolled back")
	require.NoError(t, handle.RW.Model(&models.AuditDLQ{}).Where("subject_id = ?", subject).Count(&n).Error)
	assert.Equal(t, int64(1), n, "the failed event must reach the dead-letter table")
}

func TestLogCountsRejectedEventsAsFailures(t *testing.T) {
	// Validation runs before the writer is used, so an empty gorm.DB is enough.
	validating := audit.NewLogger(&db.Handle{AuditWriter: &gorm.DB{}})
	cases := map[string]struct {
		logger audit.Logger
		event  audit.Event
	}{
		"nil handle":      {audit.NewLogger(nil), audit.Event{Type: "t", SubjectType: "t", SubjectID: uuid.New()}},
		"no writer":       {audit.NewLogger(&db.Handle{}), audit.Event{Type: "t", SubjectType: "t", SubjectID: uuid.New()}},
		"no event type":   {validating, audit.Event{SubjectType: "t", SubjectID: uuid.New()}},
		"no subject type": {validating, audit.Event{Type: "t", SubjectID: uuid.New()}},
		"no subject id":   {validating, audit.Event{Type: "t", SubjectType: "t"}},
		"bad payload":     {validating, audit.Event{Type: "t", SubjectType: "t", SubjectID: uuid.New(), Payload: func() {}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			writes, failures := auditCounters()
			require.Error(t, tc.logger.Log(t.Context(), tc.event))
			w, f := auditCounters()
			assert.Equal(t, writes, w)
			assert.Equal(t, failures+1, f)
		})
	}
}

func TestAuditCountersCarryNoLabels(t *testing.T) {
	var buf bytes.Buffer
	metrics.WritePrometheus(&buf, false)
	seen := map[string]bool{}
	for line := range strings.Lines(buf.String()) {
		for _, name := range []string{"audit_writes_total", "audit_write_failures_total"} {
			if strings.HasPrefix(line, name+"{") {
				t.Errorf("%s carries labels: %s", name, line)
			}
			if strings.HasPrefix(line, name+" ") {
				seen[name] = true
			}
		}
	}
	assert.True(t, seen["audit_writes_total"], "audit_writes_total missing from the exposition")
	assert.True(t, seen["audit_write_failures_total"], "audit_write_failures_total missing from the exposition")
}
