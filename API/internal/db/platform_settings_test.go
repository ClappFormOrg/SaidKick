package db_test

import (
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/branding"
	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// platformSettingsMigrationID is the versioned migration that adds the
// table to an existing database.
const platformSettingsMigrationID = "20261011120000"

func requireDefaultSettingsRow(t *testing.T, h *db.Handle) models.PlatformSettings {
	t.Helper()
	var rows []models.PlatformSettings
	require.NoError(t, h.RW.WithContext(t.Context()).Find(&rows).Error)
	require.Len(t, rows, 1)
	s := rows[0]
	d := branding.Default()
	assert.Equal(t, d.CTA.Hex(), s.CTA)
	assert.Equal(t, d.Primary.Hex(), s.PrimaryColor)
	assert.Equal(t, d.Secondary.Hex(), s.Secondary)
	assert.Equal(t, d.Surface.Hex(), s.Surface)
	assert.Equal(t, d.BodyText.Hex(), s.BodyText)
	assert.Equal(t, d.Border.Hex(), s.Border)
	assert.False(t, s.HasLogo())
	assert.Nil(t, s.UpdatedByUserID, "the seeded row has no author")
	assert.Equal(t, uuid.Version(7), s.ID.Version())
	return s
}

func TestPlatformSettingsMigration_SortsLast(t *testing.T) {
	t.Parallel()
	ids := db.MigrationIDs()
	require.NotEmpty(t, ids)
	assert.Equal(t, platformSettingsMigrationID, ids[len(ids)-1])
	for _, id := range ids[:len(ids)-1] {
		assert.Less(t, id, platformSettingsMigrationID)
	}
}

// The template database is built by InitSchema, as a fresh install is.
func TestPlatformSettingsOnFreshDatabase(t *testing.T) {
	t.Parallel()
	h := testutil.NewPostgresDB(t)
	requireDefaultSettingsRow(t, h)
}

// An existing database that predates the table gets it from the versioned
// migration.
func TestPlatformSettingsMigration(t *testing.T) {
	t.Parallel()
	h := testutil.NewPostgresDB(t)
	ctx := t.Context()
	require.NoError(t, h.RW.WithContext(ctx).Exec("DROP TABLE platform_settings").Error)
	require.NoError(t, h.RW.WithContext(ctx).
		Exec("DELETE FROM migrations WHERE id = ?", platformSettingsMigrationID).Error)

	require.NoError(t, db.Migrate(ctx, h))
	before := requireDefaultSettingsRow(t, h)

	// Re-running is a no-op: no second row, the seeded row is kept.
	require.NoError(t, h.RW.WithContext(ctx).
		Exec("DELETE FROM migrations WHERE id = ?", platformSettingsMigrationID).Error)
	require.NoError(t, db.Migrate(ctx, h))
	assert.Equal(t, before.ID, requireDefaultSettingsRow(t, h).ID)
}

func TestPlatformSettingsConstraints(t *testing.T) {
	t.Parallel()
	h := testutil.NewPostgresDB(t)
	tx := h.RW.WithContext(t.Context())

	err := tx.Exec(`INSERT INTO platform_settings
		(id, cta, primary_color, secondary, surface, body_text, border)
		VALUES (?, '#000000', '#000000', '#ffffff', '#ffffff', '#000000', '#cccccc')`, uuid.New()).Error
	require.Error(t, err, "a second row violates the singleton index")
	assert.Contains(t, err.Error(), "idx_platform_settings_singleton")

	err = tx.Exec(`UPDATE platform_settings SET singleton = false`).Error
	require.Error(t, err, "singleton cannot be false")

	for _, bad := range []string{"#ABCDEF", "#abc", "red", "#abcdef;", "#abcdeg"} {
		err = tx.Exec(`UPDATE platform_settings SET cta = ?`, bad).Error
		require.Error(t, err, bad)
	}

	err = tx.Exec(`UPDATE platform_settings SET logo_content = '\x89'::bytea`).Error
	require.Error(t, err, "logo columns are set together")
	assert.Contains(t, err.Error(), "platform_settings_logo_together")

	require.NoError(t, tx.Exec(`UPDATE platform_settings SET logo_content = '\x89'::bytea,
		logo_content_type = 'image/png', logo_width = 16, logo_height = 16, logo_alt = 'Logo'`).Error)
}
