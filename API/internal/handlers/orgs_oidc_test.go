package handlers_test

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/vault"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// oidcClientValue is the fake OIDC client credential the tests submit.
const oidcClientValue = "fake-client-value"

// oidcBody returns a ConfigureOrganizationOIDC request body.
func oidcBody(issuer, clientID, clientValue string) string {
	return fmt.Sprintf(`{"oidc_issuer":%q,"oidc_client_id":%q,"oidc_client_secret":%q}`, issuer, clientID, clientValue)
}

// oidcFixture holds an organisation and a platform admin from another org.
type oidcFixture struct {
	handle *db.Handle
	org    models.Organization
	admin  models.User
}

func newOIDCFixture(t *testing.T) oidcFixture {
	t.Helper()

	handle := testutil.NewPostgresDB(t)
	f := oidcFixture{handle: handle}
	f.org = seed.SeedOrg(t, handle, models.Organization{Slug: "oidc-org", DisplayName: "OIDC Org", ContactEmail: "oidc@example.com"})
	adminOrg := seed.SeedOrg(t, handle, models.Organization{})
	f.admin = seed.SeedUser(t, handle, models.User{OrganizationID: adminOrg.ID})
	return f
}

// encryptingSecretStore returns a SecretRefStore that encrypts with a fresh
// AES-256 key and has no Vault and no dev-mode fallback.
func encryptingSecretStore(t *testing.T) *auth.SecretRefStore {
	t.Helper()
	raw := make([]byte, 32)
	_, err := rand.Read(raw)
	require.NoError(t, err)
	return auth.NewSecretRefStore(vault.NullClient{}, base64.StdEncoding.EncodeToString(raw), false)
}

// configureOIDCCtx builds an Echo context for POST /orgs/:slug/oidc.
func configureOIDCCtx(e *echo.Echo, slug, body string, p *auth.Principal) (echo.Context, *httptest.ResponseRecorder) {
	return echoContext(e, asPrincipal(newRequest(http.MethodPost, "/", body), p), "slug", slug)
}

func TestConfigureOrganizationOIDC(t *testing.T) {
	t.Parallel()
	t.Run("platform_admin_configures_and_secret_is_encrypted", func(t *testing.T) {
		t.Parallel()
		f := newOIDCFixture(t)
		h := newTestHandlers(t, f.handle, withOIDCSecrets(encryptingSecretStore(t)))
		e := echo.New()

		body := oidcBody("https://idp.example.com", "appstore", oidcClientValue)
		c, rec := configureOIDCCtx(e, f.org.Slug, body, principal(f.admin, models.RolePlatformAdmin))
		require.NoError(t, h.ConfigureOrganizationOIDC(c))
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.NotContains(t, rec.Body.String(), oidcClientValue, "the client credential must not be echoed")
		assert.NotContains(t, rec.Body.String(), "secret_ref")

		var resp map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, "https://idp.example.com", resp["oidc_issuer"])
		assert.Equal(t, "appstore", resp["oidc_client_id"])

		var org models.Organization
		require.NoError(t, f.handle.RW.WithContext(t.Context()).First(&org, "id = ?", f.org.ID).Error)
		require.NotNil(t, org.OIDCIssuer)
		require.NotNil(t, org.OIDCClientID)
		require.NotNil(t, org.OIDCClientSecretRef)
		assert.Equal(t, "https://idp.example.com", *org.OIDCIssuer)
		assert.Equal(t, "appstore", *org.OIDCClientID)
		assert.True(t, strings.HasPrefix(*org.OIDCClientSecretRef, "encrypted:"), "got ref scheme %q", *org.OIDCClientSecretRef)
		assert.NotContains(t, *org.OIDCClientSecretRef, oidcClientValue, "the stored ref must not hold the plaintext")
	})

	t.Run("no_secret_storage_returns_503_and_leaves_org_unchanged", func(t *testing.T) {
		t.Parallel()
		f := newOIDCFixture(t)
		store := auth.NewSecretRefStore(vault.NullClient{}, "", false)
		h := newTestHandlers(t, f.handle, withOIDCSecrets(store))
		e := echo.New()

		body := oidcBody("https://idp.example.com", "appstore", oidcClientValue)
		c, _ := configureOIDCCtx(e, f.org.Slug, body, principal(f.admin, models.RolePlatformAdmin))
		requireAPIError(t, h.ConfigureOrganizationOIDC(c), http.StatusServiceUnavailable)

		var org models.Organization
		require.NoError(t, f.handle.RW.WithContext(t.Context()).First(&org, "id = ?", f.org.ID).Error)
		assert.Nil(t, org.OIDCIssuer)
		assert.Nil(t, org.OIDCClientSecretRef)
	})

	t.Run("unknown_org_returns_404", func(t *testing.T) {
		t.Parallel()
		f := newOIDCFixture(t)
		h := newTestHandlers(t, f.handle, withOIDCSecrets(encryptingSecretStore(t)))
		e := echo.New()

		body := oidcBody("https://idp.example.com", "appstore", oidcClientValue)
		c, _ := configureOIDCCtx(e, "no-such-org", body, principal(f.admin, models.RolePlatformAdmin))
		requireAPIError(t, h.ConfigureOrganizationOIDC(c), http.StatusNotFound)
	})
}

// TestConfigureOrganizationOIDC_Validation covers requests rejected before
// the handler reads the database.
func TestConfigureOrganizationOIDC_Validation(t *testing.T) {
	t.Parallel()
	h := newTestHandlers(t, nil)
	admin := principal(testUser(), models.RolePlatformAdmin)

	missing := map[string]string{
		"missing_issuer":    oidcBody("", "appstore", oidcClientValue),
		"missing_client_id": oidcBody("https://idp.example.com", " ", oidcClientValue),
		"missing_secret":    oidcBody("https://idp.example.com", "appstore", ""),
	}
	for name, body := range missing {
		t.Run(name+"_returns_422", func(t *testing.T) {
			t.Parallel()
			c, _ := configureOIDCCtx(echo.New(), "oidc-org", body, admin)
			requireAPIError(t, h.ConfigureOrganizationOIDC(c), http.StatusUnprocessableEntity)
		})
	}

	t.Run("invalid_slug_returns_404", func(t *testing.T) {
		t.Parallel()
		body := oidcBody("https://idp.example.com", "appstore", oidcClientValue)
		c, _ := configureOIDCCtx(echo.New(), "Not_A_Slug!", body, admin)
		requireAPIError(t, h.ConfigureOrganizationOIDC(c), http.StatusNotFound)
	})

	t.Run("malformed_json_returns_400", func(t *testing.T) {
		t.Parallel()
		c, _ := configureOIDCCtx(echo.New(), "oidc-org", `{"oidc_issuer":`, admin)
		requireAPIError(t, h.ConfigureOrganizationOIDC(c), http.StatusBadRequest)
	})

	t.Run("anonymous_returns_401", func(t *testing.T) {
		t.Parallel()
		body := oidcBody("https://idp.example.com", "appstore", oidcClientValue)
		c, _ := echoContext(echo.New(), newRequest(http.MethodPost, "/", body), "slug", "oidc-org")
		requireAPIError(t, h.ConfigureOrganizationOIDC(c), http.StatusUnauthorized)
	})
}

// oidcDeniedRoles are the roles that may not configure an org's OIDC.
var oidcDeniedRoles = []models.Role{models.RoleOrgAdmin, models.RoleClusterAdmin, models.RoleDeveloper, models.RoleAuditor, models.RoleDeployer}

// TestConfigureOrganizationOIDC_RouteRoles checks that only platform_admin
// passes the route's role gate.
func TestConfigureOrganizationOIDC_RouteRoles(t *testing.T) {
	t.Parallel()
	h := newTestHandlers(t, nil)
	body := oidcBody("https://idp.example.com", "appstore", oidcClientValue)

	for _, role := range oidcDeniedRoles {
		t.Run(string(role)+"_returns_403", func(t *testing.T) {
			t.Parallel()
			req := asPrincipal(newRequest(http.MethodPost, "/api/v1/orgs/oidc-org/oidc", body), principal(testUser(), role))
			assert.Equal(t, http.StatusForbidden, serveRoute(h, req).Code)
		})
	}
}

// TestConfigureOrganizationOIDC_HandlerRoles calls the handler directly,
// bypassing the route gate, and checks that it refuses a caller without
// platform_admin before touching the database.
func TestConfigureOrganizationOIDC_HandlerRoles(t *testing.T) {
	t.Parallel()
	h := newTestHandlers(t, nil)
	body := oidcBody("https://idp.example.com", "appstore", oidcClientValue)

	for _, role := range oidcDeniedRoles {
		t.Run(string(role)+"_returns_403", func(t *testing.T) {
			t.Parallel()
			c, _ := configureOIDCCtx(echo.New(), "oidc-org", body, principal(testUser(), role))
			apiErr := requireAPIError(t, h.ConfigureOrganizationOIDC(c), http.StatusForbidden)
			assert.Equal(t, "forbidden", apiErr.Code)
		})
	}
}
