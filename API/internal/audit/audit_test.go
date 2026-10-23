package audit_test

import (
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAuditLogHashChain verifies that sequential audit writes produce a
// correctly linked hash chain: each row's PreviousHash must equal the
// IngestHash of the immediately preceding row (genesis row excepted).
func TestAuditLogHashChain(t *testing.T) {
	t.Parallel()
	handle := testutil.NewPostgresDB(t)
	ctx := t.Context()
	logger := audit.NewLogger(handle)

	subjectID := uuid.New()

	events := []audit.Event{
		{Type: "test.event_a", SubjectType: "test", SubjectID: subjectID},
		{Type: "test.event_b", SubjectType: "test", SubjectID: subjectID},
		{Type: "test.event_c", SubjectType: "test", SubjectID: subjectID},
	}
	for _, e := range events {
		require.NoError(t, logger.Log(ctx, e))
	}

	// Read all rows ordered chronologically (genesis row + 3 test rows).
	var rows []models.AuditLog
	require.NoError(t,
		handle.RW.Order("occurred_at ASC, id ASC").Find(&rows).Error,
	)
	require.GreaterOrEqual(t, len(rows), 4, "expected genesis row + 3 test rows")

	// Verify chain: each row's PreviousHash == the preceding row's IngestHash.
	for i := 1; i < len(rows); i++ {
		assert.Equal(t, rows[i-1].IngestHash, rows[i].PreviousHash,
			"hash chain broken at row %d (event_type=%s)", i, rows[i].EventType)
	}

	// No row may have an empty IngestHash (genesis included).
	for _, r := range rows {
		assert.NotEmpty(t, r.IngestHash, "row %s has empty IngestHash", r.ID)
	}

	// Genesis row must have an empty PreviousHash.
	assert.Empty(t, rows[0].PreviousHash, "genesis row should have empty PreviousHash")
}

// TestAuditLogAppendOnly verifies that the database trigger refuses both
// UPDATE and DELETE on audit_logs, enforcing the append-only invariant at
// the SQL layer regardless of the application role.
func TestAuditLogAppendOnly(t *testing.T) {
	t.Parallel()
	handle := testutil.NewPostgresDB(t)
	ctx := t.Context()
	logger := audit.NewLogger(handle)

	subjectID := uuid.New()
	require.NoError(t, logger.Log(ctx, audit.Event{
		Type: "test.append_only", SubjectType: "test", SubjectID: subjectID,
	}))

	var row models.AuditLog
	require.NoError(t,
		handle.RW.First(&row, "event_type = ?", "test.append_only").Error,
	)

	t.Run("update_rejected_by_trigger", func(t *testing.T) {
		err := handle.RW.WithContext(ctx).
			Exec("UPDATE audit_logs SET event_type = 'tampered' WHERE id = ?", row.ID).
			Error
		require.Error(t, err, "UPDATE on audit_logs should be rejected by trigger")
		assert.Contains(t, err.Error(), "audit_logs is append-only")
	})

	t.Run("delete_rejected_by_trigger", func(t *testing.T) {
		err := handle.RW.WithContext(ctx).
			Exec("DELETE FROM audit_logs WHERE id = ?", row.ID).
			Error
		require.Error(t, err, "DELETE on audit_logs should be rejected by trigger")
		assert.Contains(t, err.Error(), "audit_logs is append-only")
	})
}
