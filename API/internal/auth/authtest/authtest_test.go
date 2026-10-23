package authtest_test

import (
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/auth/authtest"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestContextWithPrincipalRoundTrip(t *testing.T) {
	t.Parallel()
	p := &auth.Principal{
		UserID:         uuid.New(),
		OrganizationID: uuid.New(),
		Roles:          map[models.Role]struct{}{models.RoleDeveloper: {}},
	}

	ctx := authtest.ContextWithPrincipal(t.Context(), p)

	assert.Same(t, p, auth.PrincipalFromContext(ctx))
	assert.Nil(t, auth.PrincipalFromContext(t.Context()), "the parent context must stay anonymous")
	assert.Empty(t, auth.PrincipalFromContext(ctx).AuthMethod, "the auth method must stay as given")
}

func TestSessionPrincipal(t *testing.T) {
	t.Parallel()
	userID, orgID := uuid.New(), uuid.New()

	p := authtest.SessionPrincipal(userID, orgID, models.RoleDeveloper)

	assert.Equal(t, userID, p.UserID)
	assert.Equal(t, orgID, p.OrganizationID)
	assert.Equal(t, auth.AuthMethodSession, p.AuthMethod)
	assert.True(t, p.Has(models.RoleDeveloper))
	assert.False(t, p.Has(models.RoleDeployer))
}
