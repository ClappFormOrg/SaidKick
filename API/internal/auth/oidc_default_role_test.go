package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
)

// activeRoles returns the user's unrevoked role names.
func activeRoles(t *testing.T, a *OIDCAuthenticator, userID uuid.UUID) []models.Role {
	t.Helper()
	var rows []models.RoleAssignment
	require.NoError(t, a.handle.RW.WithContext(t.Context()).
		Where("user_id = ? AND revoked_at IS NULL", userID).Find(&rows).Error)
	roles := make([]models.Role, 0, len(rows))
	for _, r := range rows {
		roles = append(roles, r.Role)
	}
	return roles
}

// roleAssignedEvents returns the role.assigned audit rows about userID.
func roleAssignedEvents(t *testing.T, a *OIDCAuthenticator, userID uuid.UUID) []models.AuditLog {
	t.Helper()
	var rows []models.AuditLog
	require.NoError(t, a.handle.RW.WithContext(t.Context()).
		Where("event_type = ? AND subject_id = ?", "role.assigned", userID).Find(&rows).Error)
	return rows
}

// TestOIDCCallback_DefaultRole checks that the first sign-in grants
// app_user once, records it as a system role.assigned event, and that later
// sign-ins and seeded accounts get no extra grant.
func TestOIDCCallback_DefaultRole(t *testing.T) {
	t.Parallel()

	t.Run("first_sign_in_grants_app_user_and_audits_it", func(t *testing.T) {
		t.Parallel()
		const clientID, sub, nonce = "default-role-client", "new-user-sub", "n1"
		idp := newMockIDP(t, clientID, sub, "new.user@gemeente.nl")
		a, e := newTestAuthenticator(t)
		a.auditLog = audit.NewLogger(a.handle)
		org := seedOIDCOrg(t, a, idp.URL, clientID, "secret")

		for _, code := range []string{"code-1", "code-2"} {
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, callbackRequest(code, nonce, mintTestStateCookie(t, org.ID, nonce)))
			require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
		}

		var user models.User
		require.NoError(t, a.handle.RW.WithContext(t.Context()).
			First(&user, "organization_id = ? AND external_subject = ?", org.ID, sub).Error)
		assert.Equal(t, []models.Role{models.RoleAppUser}, activeRoles(t, a, user.ID))

		var grant models.RoleAssignment
		require.NoError(t, a.handle.RW.WithContext(t.Context()).
			First(&grant, "user_id = ? AND role = ?", user.ID, models.RoleAppUser).Error)
		assert.Equal(t, user.ID, grant.GrantedByUserID, "a system grant records the user as its own granter")

		events := roleAssignedEvents(t, a, user.ID)
		require.Len(t, events, 1, "only the first sign-in grants a role")
		assert.Nil(t, events[0].ActorUserID, "the system is the actor")
		require.NotNil(t, events[0].ActorOrganizationID)
		assert.Equal(t, org.ID, *events[0].ActorOrganizationID)
		assert.Equal(t, "user", events[0].SubjectType)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(events[0].Payload, &payload))
		assert.Equal(t, []any{"app_user"}, payload["roles"])
		assert.Equal(t, "first_sign_in", payload["reason"])
	})

	t.Run("seeded_account_keeps_its_roles", func(t *testing.T) {
		t.Parallel()
		const clientID, sub, email, nonce = "seeded-role-client", "seeded-sub", "seeded@gemeente.nl", "n2"
		idp := newMockIDP(t, clientID, sub, email)
		idp.emailVerified = ptrBool(true)
		a, e := newTestAuthenticator(t)
		a.auditLog = audit.NewLogger(a.handle)
		org := seedOIDCOrg(t, a, idp.URL, clientID, "secret")
		seeded := seedPreProvisionedUser(t, a, org.ID, email)
		seed.SeedRoleAssignment(t, a.handle, models.RoleAssignment{UserID: seeded.ID, Role: models.RoleDeployer})

		e.ServeHTTP(httptest.NewRecorder(), callbackRequest("code", nonce, mintTestStateCookie(t, org.ID, nonce)))

		assert.Equal(t, []models.Role{models.RoleDeployer}, activeRoles(t, a, seeded.ID))
		assert.Empty(t, roleAssignedEvents(t, a, seeded.ID))
	})
}
