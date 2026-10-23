package db_test

import (
	"slices"
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const appUserMigrationID = "20261011110000"

// sixRoleCheck is the role CHECK every database had before app_user.
const sixRoleCheck = `DO $$
DECLARE c record;
BEGIN
  FOR c IN SELECT conname FROM pg_constraint
            WHERE conrelid = 'role_assignments'::regclass AND contype = 'c'
              AND pg_get_constraintdef(oid) LIKE '%platform_admin%'
  LOOP
    EXECUTE format('ALTER TABLE role_assignments DROP CONSTRAINT %I', c.conname);
  END LOOP;
  ALTER TABLE role_assignments ADD CONSTRAINT chk_role_assignments_role
    CHECK (role IN ('developer','auditor','deployer','cluster_admin','org_admin','platform_admin'));
END$$`

// grant inserts a role assignment for a fresh user and returns the error.
func grant(t *testing.T, h *db.Handle, role models.Role) error {
	t.Helper()
	org := seed.SeedOrg(t, h, models.Organization{})
	user := seed.SeedUser(t, h, models.User{OrganizationID: org.ID})
	return h.RW.WithContext(t.Context()).Create(&models.RoleAssignment{
		UserID: user.ID, Role: role, GrantedByUserID: user.ID,
	}).Error
}

// The app_user migration sorts after every migration listed before it, and
// every later migration (20261011120000, platform_settings) sorts after it.
func TestAppUserMigration_SortsInOrder(t *testing.T) {
	t.Parallel()
	ids := db.MigrationIDs()
	i := slices.Index(ids, appUserMigrationID)
	require.GreaterOrEqual(t, i, 0, "app_user migration is registered")
	for _, id := range ids[:i] {
		assert.Less(t, id, appUserMigrationID)
	}
	for _, id := range ids[i+1:] {
		assert.Greater(t, id, appUserMigrationID)
	}
}

// TestRoleCheck_FreshDatabase covers the initSchema path: the GORM tag
// alone must admit app_user.
func TestRoleCheck_FreshDatabase(t *testing.T) {
	t.Parallel()
	h := testutil.NewPostgresDB(t)

	for _, role := range models.AllRoles {
		require.NoError(t, grant(t, h, role), role)
	}
	assert.Error(t, grant(t, h, models.Role("superuser")))
}

// TestRoleCheck_MigratedDatabase covers an existing database that still has
// the six-role CHECK and has not run the app_user migration.
func TestRoleCheck_MigratedDatabase(t *testing.T) {
	t.Parallel()
	h := testutil.NewPostgresDB(t)
	ctx := t.Context()
	require.NoError(t, h.RW.WithContext(ctx).Exec(sixRoleCheck).Error)
	require.NoError(t, h.RW.WithContext(ctx).Exec("DELETE FROM migrations WHERE id = ?", appUserMigrationID).Error)
	require.Error(t, grant(t, h, models.RoleAppUser), "precondition: the old CHECK refuses app_user")

	require.NoError(t, db.Migrate(ctx, h))

	require.NoError(t, grant(t, h, models.RoleAppUser))
	assert.Error(t, grant(t, h, models.Role("superuser")))
	var n int64
	require.NoError(t, h.RW.WithContext(ctx).Table("migrations").Where("id = ?", appUserMigrationID).Count(&n).Error)
	assert.Equal(t, int64(1), n)

	// Re-running the migration on a converged database is a no-op.
	require.NoError(t, db.MigrationByID(appUserMigrationID).Migrate(h.RW.WithContext(ctx)))
	require.NoError(t, grant(t, h, models.RoleAppUser))
}

// TestRoleCheck_Rollback checks that rolling back removes app_user rows and
// restores the six-role CHECK.
func TestRoleCheck_Rollback(t *testing.T) {
	t.Parallel()
	h := testutil.NewPostgresDB(t)
	ctx := t.Context()
	require.NoError(t, grant(t, h, models.RoleAppUser))
	require.NoError(t, grant(t, h, models.RoleDeveloper))

	require.NoError(t, db.MigrationByID(appUserMigrationID).Rollback(h.RW.WithContext(ctx)))

	var appUsers int64
	require.NoError(t, h.RW.WithContext(ctx).Model(&models.RoleAssignment{}).
		Where("role = ?", models.RoleAppUser).Count(&appUsers).Error)
	assert.Zero(t, appUsers)
	var others int64
	require.NoError(t, h.RW.WithContext(ctx).Model(&models.RoleAssignment{}).Count(&others).Error)
	assert.Equal(t, int64(1), others, "rollback keeps every other assignment")

	assert.Error(t, grant(t, h, models.RoleAppUser))
	assert.NoError(t, grant(t, h, models.RoleDeveloper))
}
