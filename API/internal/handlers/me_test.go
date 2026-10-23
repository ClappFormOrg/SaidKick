package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/handlers"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetMe_OrgID checks that /me carries the caller's organisation ID, which
// the webapp compares with a cluster's owning organisation.
func TestGetMe_OrgID(t *testing.T) {
	t.Parallel()
	handle := testutil.NewPostgresDB(t)
	org := seed.SeedOrg(t, handle, models.Organization{Slug: "me-org", DisplayName: "Me Org"})
	user := seed.SeedUser(t, handle, models.User{OrganizationID: org.ID})
	h := newTestHandlers(t, handle)

	c, rec := echoContext(echo.New(), asPrincipal(newRequest(http.MethodGet, "/", ""), principal(user, models.RoleClusterAdmin)))
	require.NoError(t, h.GetMe(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var me handlers.MeResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &me))
	assert.Equal(t, org.ID.String(), me.OrgID)
	assert.Equal(t, "me-org", me.OrgSlug)
	assert.Equal(t, []string{string(models.RoleClusterAdmin)}, me.Roles)
}
