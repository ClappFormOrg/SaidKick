package handlers_test

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/handlers"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scopingMigrationID is the gormigrate step that adds api_keys.roles and
// backfills existing keys.
const scopingMigrationID = "20261005100000"

// keyOwner seeds an organisation and a user holding roles, and returns the
// handle, the user and handlers on that handle.
func keyOwner(t *testing.T, roles ...models.Role) (*db.Handle, models.User, *handlers.Handlers) {
	t.Helper()
	handle := testutil.NewPostgresDB(t)
	org := seed.SeedOrg(t, handle, models.Organization{})
	owner := seed.SeedUser(t, handle, models.User{OrganizationID: org.ID})
	for _, r := range roles {
		seed.SeedRoleAssignment(t, handle, models.RoleAssignment{UserID: owner.ID, Role: r})
	}
	return handle, owner, newTestHandlers(t, handle)
}

// mintKey creates a key scoped to roles through the handler, as creator,
// and returns the key's id and its one-time token.
func mintKey(t *testing.T, h *handlers.Handlers, creator *auth.Principal, roles ...models.Role) (uuid.UUID, string) {
	t.Helper()
	c, rec := postAPIKeyCtx(echo.New(), apiKeyBody("scoped-key", 30, roles...), creator)
	require.NoError(t, h.CreateAPIKey(c))
	require.Equal(t, http.StatusCreated, rec.Code)

	var resp struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	return uuid.MustParse(resp.ID), resp.Token
}

// keyServer puts the API key authenticator in front of h's routes, as the
// server does in production.
func keyServer(handle *db.Handle, h *handlers.Handlers) *echo.Echo {
	e := echo.New()
	e.Use((&auth.APIKeyAuthenticator{Handle: handle}).Middleware)
	h.Register(e)
	return e
}

// callWithKey sends method target with token as the bearer credential.
func callWithKey(e *echo.Echo, method, target, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, http.NoBody)
	req.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// probeKey authenticates token through the API key middleware alone and
// returns the response status and the roles the resulting principal holds,
// sorted.
func probeKey(t *testing.T, handle *db.Handle, token string) (int, []models.Role) {
	t.Helper()
	var roles []models.Role
	e := echo.New()
	e.Use((&auth.APIKeyAuthenticator{Handle: handle}).Middleware)
	e.GET("/probe", func(c echo.Context) error {
		p := auth.PrincipalFromContext(c.Request().Context())
		if p == nil {
			return c.NoContent(http.StatusTeapot) // anonymous: must not happen
		}
		for r := range p.Roles {
			roles = append(roles, r)
		}
		slices.Sort(roles)
		return c.NoContent(http.StatusNoContent)
	})
	rec := callWithKey(e, http.MethodGet, "/probe", token)
	return rec.Code, roles
}

func TestAPIKeyScope(t *testing.T) {
	t.Parallel()

	t.Run("developer_scoped_key_refused_on_platform_admin_route", func(t *testing.T) {
		t.Parallel()
		handle, owner, h := keyOwner(t, models.RoleDeveloper, models.RolePlatformAdmin)
		creator := principal(owner, models.RoleDeveloper, models.RolePlatformAdmin)
		_, devToken := mintKey(t, h, creator, models.RoleDeveloper)
		_, adminToken := mintKey(t, h, creator, models.RolePlatformAdmin)
		e := keyServer(handle, h)

		rec := callWithKey(e, http.MethodGet, "/api/v1/audit-log", devToken)
		assert.Equal(t, http.StatusForbidden, rec.Code,
			"the owner's platform_admin role must not leak into a developer-scoped key")

		rec = callWithKey(e, http.MethodGet, "/api/v1/audit-log", adminToken)
		assert.Equal(t, http.StatusOK, rec.Code, "a platform_admin-scoped key still reaches the route")
	})

	t.Run("key_holds_only_its_scoped_roles", func(t *testing.T) {
		t.Parallel()
		handle, owner, h := keyOwner(t, models.RoleDeveloper, models.RoleDeployer, models.RoleAuditor)
		_, token := mintKey(t, h,
			principal(owner, models.RoleDeveloper, models.RoleDeployer, models.RoleAuditor),
			models.RoleDeveloper, models.RoleAuditor)

		code, roles := probeKey(t, handle, token)
		require.Equal(t, http.StatusNoContent, code)
		assert.Equal(t, []models.Role{models.RoleAuditor, models.RoleDeveloper}, roles)
	})

	t.Run("revoking_owner_role_stops_key_without_changing_key_row", func(t *testing.T) {
		t.Parallel()
		handle, owner, h := keyOwner(t, models.RoleDeveloper)
		ctx := t.Context()
		keyID, token := mintKey(t, h, principal(owner, models.RoleDeveloper), models.RoleDeveloper)

		code, roles := probeKey(t, handle, token)
		require.Equal(t, http.StatusNoContent, code)
		require.Equal(t, []models.Role{models.RoleDeveloper}, roles)

		var before models.APIKey
		require.NoError(t, handle.RW.WithContext(ctx).First(&before, "id = ?", keyID).Error)

		require.NoError(t, handle.RW.WithContext(ctx).Model(&models.RoleAssignment{}).
			Where("user_id = ? AND role = ?", owner.ID, models.RoleDeveloper).
			Update("revoked_at", time.Now().UTC()).Error)

		code, _ = probeKey(t, handle, token)
		assert.Equal(t, http.StatusUnauthorized, code, "a key whose scope no longer overlaps live roles is refused")

		var after models.APIKey
		require.NoError(t, handle.RW.WithContext(ctx).First(&after, "id = ?", keyID).Error)
		assert.Equal(t, before, after, "revocation works through the owner's roles, not by editing the key")
	})
}

// TestAPIKeyScopingMigrationBackfill rewinds a migrated database to the
// schema before the scoping step, inserts keys the way the old code stored
// them, and runs the migration again.
func TestAPIKeyScopingMigrationBackfill(t *testing.T) {
	t.Parallel()
	handle, owner, _ := keyOwner(t, models.RoleDeveloper, models.RoleAuditor)
	ctx := t.Context()
	rw := handle.RW.WithContext(ctx)

	// A revoked assignment must not be backfilled into the key's scope.
	revokedAt := time.Now().UTC().Add(-time.Hour)
	seed.SeedRoleAssignment(t, handle, models.RoleAssignment{
		UserID: owner.ID, Role: models.RoleDeployer, RevokedAt: &revokedAt,
	})

	require.NoError(t, rw.Exec("ALTER TABLE api_keys DROP COLUMN roles").Error)
	require.NoError(t, rw.Exec("DELETE FROM migrations WHERE id = ?", scopingMigrationID).Error)

	openID, openToken := insertLegacyKey(t, handle, owner.ID, nil, nil)
	soon := time.Now().UTC().Add(10 * 24 * time.Hour).Truncate(time.Microsecond)
	datedID, _ := insertLegacyKey(t, handle, owner.ID, &soon, nil)
	revokedID, _ := insertLegacyKey(t, handle, owner.ID, nil, &revokedAt)

	migratedAt := time.Now().UTC()
	require.NoError(t, db.Migrate(ctx, handle))

	load := func(id uuid.UUID) models.APIKey {
		t.Helper()
		var k models.APIKey
		require.NoError(t, rw.First(&k, "id = ?", id).Error)
		return k
	}

	open := load(openID)
	assert.Equal(t, models.StringArray{"auditor", "developer"}, open.Roles,
		"roles are backfilled from the owner's active assignments")
	require.NotNil(t, open.ExpiresAt, "open-ended keys get a deadline")
	assert.WithinDuration(t, migratedAt.Add(90*24*time.Hour), *open.ExpiresAt, time.Minute)

	dated := load(datedID)
	require.NotNil(t, dated.ExpiresAt)
	assert.True(t, soon.Equal(*dated.ExpiresAt), "an existing deadline is kept")

	assert.Nil(t, load(revokedID).ExpiresAt, "revoked keys are left alone")

	code, roles := probeKey(t, handle, openToken)
	require.Equal(t, http.StatusNoContent, code, "an existing key keeps working after the backfill")
	assert.Equal(t, []models.Role{models.RoleAuditor, models.RoleDeveloper}, roles,
		"its effective roles are the owner's roles, as before the migration")
}

// insertLegacyKey writes an api_keys row with the pre-scoping columns only
// and returns its id and a freshly generated token.
func insertLegacyKey(t *testing.T, handle *db.Handle, owner uuid.UUID, expiresAt, revokedAt *time.Time) (uuid.UUID, string) {
	t.Helper()
	raw := make([]byte, 32)
	_, err := rand.Read(raw)
	require.NoError(t, err)
	token := "sak_" + base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))

	id := uuid.Must(uuid.NewV7())
	require.NoError(t, handle.RW.WithContext(t.Context()).Exec(
		`INSERT INTO api_keys (id, name, prefix, key_hash, owner_user_id, created_at, expires_at, revoked_at)
		 VALUES (?, ?, ?, ?, ?, now(), ?, ?)`,
		id, "legacy-key", token[:12], hex.EncodeToString(sum[:]), owner, expiresAt, revokedAt,
	).Error)
	return id, token
}
