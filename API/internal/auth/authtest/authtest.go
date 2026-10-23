// Package authtest lets tests call handlers directly, without running an
// authenticator middleware. Only test code imports it, so the helper stays
// out of the production binary and package auth exports no way to attach an
// arbitrary principal.
package authtest

import (
	"context"

	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/auth/principalctx"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/google/uuid"
)

// ContextWithPrincipal returns a copy of ctx with p attached, as an
// authenticator would after a successful login. It leaves p.AuthMethod as
// given.
func ContextWithPrincipal(ctx context.Context, p *auth.Principal) context.Context {
	return principalctx.With(ctx, p)
}

// SessionPrincipal returns a principal for userID in orgID holding roles,
// authenticated by a signed-in session.
func SessionPrincipal(userID, orgID uuid.UUID, roles ...models.Role) *auth.Principal {
	set := make(map[models.Role]struct{}, len(roles))
	for _, r := range roles {
		set[r] = struct{}{}
	}
	return &auth.Principal{
		UserID:         userID,
		OrganizationID: orgID,
		Roles:          set,
		AuthMethod:     auth.AuthMethodSession,
	}
}
