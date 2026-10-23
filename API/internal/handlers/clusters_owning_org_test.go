package handlers_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClusterOwningOrg_Unknown checks that cluster registration and scope
// changes naming an unknown or soft-deleted owning organisation return a 422
// field error and write nothing.
func TestClusterOwningOrg_Unknown(t *testing.T) {
	t.Parallel()
	handle := testutil.NewPostgresDB(t)
	h := newTestHandlers(t, handle)

	adminOrg := seed.SeedOrg(t, handle, models.Organization{})
	admin := principal(seed.SeedUser(t, handle, models.User{OrganizationID: adminOrg.ID}), models.RolePlatformAdmin)
	deleted := seed.SeedOrg(t, handle, models.Organization{})
	require.NoError(t, handle.RW.WithContext(t.Context()).Delete(&deleted).Error)
	cluster := seed.SeedCluster(t, handle, models.Cluster{Scope: models.ClusterScopePublic})

	orgs := map[string]uuid.UUID{
		"unknown":      uuid.New(),
		"soft_deleted": deleted.ID,
	}
	for name, orgID := range orgs {
		for _, scope := range []models.ClusterScope{models.ClusterScopeOrg, models.ClusterScopeRestricted} {
			t.Run("create_"+string(scope)+"_"+name+"_org_returns_422", func(t *testing.T) {
				before := countRows(t, handle, &models.Cluster{})
				body := fmt.Sprintf(`{"name":"c","scope":%q,"owning_org_id":%q}`, scope, orgID)
				c, _ := echoContext(echo.New(), asPrincipal(newRequest(http.MethodPost, "/", body), admin))
				requireOwningOrgNotFound(t, h.CreateCluster(c))
				assert.Equal(t, before, countRows(t, handle, &models.Cluster{}), "a cluster was written")
			})
			t.Run("change_scope_"+string(scope)+"_"+name+"_org_returns_422", func(t *testing.T) {
				body := fmt.Sprintf(`{"new_scope":%q,"owning_org_id":%q,"reason":"move"}`, scope, orgID)
				c, _ := echoContext(echo.New(), asPrincipal(newRequest(http.MethodPost, "/", body), admin), "id", cluster.ID.String())
				requireOwningOrgNotFound(t, h.ChangeClusterScope(c))

				var got models.Cluster
				require.NoError(t, handle.RW.WithContext(t.Context()).First(&got, "id = ?", cluster.ID).Error)
				assert.Equal(t, models.ClusterScopePublic, got.Scope)
				assert.Nil(t, got.OwningOrganizationID)
			})
		}
	}
}

// requireOwningOrgNotFound asserts err is a 422 with the owning_org_id
// not_found field error.
func requireOwningOrgNotFound(t *testing.T, err error) {
	t.Helper()
	apiErr := requireAPIError(t, err, http.StatusUnprocessableEntity)
	assert.Equal(t, []apierror.FieldError{
		{Path: "owning_org_id", Code: "not_found", Message: "organisation not found"},
	}, apiErr.Errors)
}
