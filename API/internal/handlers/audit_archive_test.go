package handlers_test

import (
	"net/http"
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// archiveMigrationID is the gormigrate step that moves approved versions the
// old approval code demoted to 'rejected' over to 'archived'.
const archiveMigrationID = "20261006110000"

// seedAuditedVersion seeds a version of f's app in state whose submission
// the fixture's auditor decided with decision. An empty decision leaves the
// submission undecided.
func seedAuditedVersion(t *testing.T, f auditFixture, state models.AppVersionState, decision models.Decision) models.AppVersion {
	t.Helper()
	v, sub := seed.SeedAppVersion(t, f.handle, models.AppVersion{AppID: f.app.ID, State: state})
	if decision != "" {
		seed.SeedAuditDecision(t, f.handle, models.AuditDecision{
			AuditSubmissionID: sub.ID, Decision: decision, DecidedByUserID: f.audUser.ID,
		})
	}
	return v
}

// versionState reads the current state of the version with id.
func versionState(t *testing.T, handle *db.Handle, id uuid.UUID) models.AppVersionState {
	t.Helper()
	var v models.AppVersion
	require.NoError(t, handle.RW.WithContext(t.Context()).First(&v, "id = ?", id).Error)
	return v.State
}

func TestDecideAuditSubmission_ApproveArchivesPreviousVersion(t *testing.T) {
	t.Parallel()
	f, h := newAuditFixture(t)
	previous := seedAuditedVersion(t, f, models.AppVersionPublished, models.DecisionApprove)
	require.NoError(t, f.handle.RW.WithContext(t.Context()).Model(&models.App{}).
		Where("id = ?", f.app.ID).
		Updates(map[string]any{"state": models.AppPublished, "current_published_version_id": previous.ID}).Error)

	rec, err := decide(h, f.sub.ID, principal(f.audUser, models.RoleAuditor))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	assert.Equal(t, models.AppVersionArchived, versionState(t, f.handle, previous.ID),
		"a version that passed audit is archived, not rejected, when a newer one is approved")
	assert.Equal(t, models.AppVersionPublished, versionState(t, f.handle, f.version.ID))

	var app models.App
	require.NoError(t, f.handle.RW.WithContext(t.Context()).First(&app, "id = ?", f.app.ID).Error)
	require.NotNil(t, app.CurrentPublishedVersionID)
	assert.Equal(t, f.version.ID, *app.CurrentPublishedVersionID)
}

// TestArchiveMigration rewinds a migrated database to before the archive
// step, writes versions the way the old approval code left them, and runs
// the migration again.
func TestArchiveMigration(t *testing.T) {
	t.Parallel()
	f, _ := newAuditFixture(t)
	ctx := t.Context()

	require.NoError(t, f.handle.RW.WithContext(ctx).
		Exec("DELETE FROM migrations WHERE id = ?", archiveMigrationID).Error)

	demoted := seedAuditedVersion(t, f, models.AppVersionRejected, models.DecisionApprove)
	auditorRejected := seedAuditedVersion(t, f, models.AppVersionRejected, models.DecisionReject)
	undecided := seedAuditedVersion(t, f, models.AppVersionRejected, "")
	published := seedAuditedVersion(t, f, models.AppVersionPublished, models.DecisionApprove)

	require.NoError(t, db.Migrate(ctx, f.handle))

	assert.Equal(t, models.AppVersionArchived, versionState(t, f.handle, demoted.ID),
		"a version demoted after passing audit becomes archived")
	assert.Equal(t, models.AppVersionRejected, versionState(t, f.handle, auditorRejected.ID),
		"a version the auditor rejected stays rejected")
	assert.Equal(t, models.AppVersionRejected, versionState(t, f.handle, undecided.ID),
		"a rejected version without an approve decision stays rejected")
	assert.Equal(t, models.AppVersionPublished, versionState(t, f.handle, published.ID))
}
