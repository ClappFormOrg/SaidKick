package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
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

// ─── Fixtures ────────────────────────────────────────────────────────────────

// apiKeyFixture holds two users: a regular developer and a platform admin.
type apiKeyFixture struct {
	handle *db.Handle
	org    models.Organization
	user   models.User // regular developer
	admin  models.User // platform admin
}

func newAPIKeyFixture(t *testing.T) (apiKeyFixture, *handlers.Handlers) {
	t.Helper()

	handle := testutil.NewPostgresDB(t)
	var f apiKeyFixture
	f.handle = handle

	f.org = seed.SeedOrg(t, handle, models.Organization{Slug: "key-org", DisplayName: "Key Org", ContactEmail: "keys@example.com"})
	f.user = seed.SeedUser(t, handle, models.User{
		OrganizationID:  f.org.ID,
		ExternalSubject: "user-sub",
		Email:           "user@key-org.nl",
		DisplayName:     "Developer User",
	})
	f.admin = seed.SeedUser(t, handle, models.User{
		OrganizationID:  f.org.ID,
		ExternalSubject: "admin-sub",
		Email:           "admin@key-org.nl",
		DisplayName:     "Platform Admin",
	})

	return f, newTestHandlers(t, handle)
}

// postAPIKeyCtx builds an Echo context for POST /api-keys.
func postAPIKeyCtx(e *echo.Echo, body string, p *auth.Principal) (echo.Context, *httptest.ResponseRecorder) {
	return echoContext(e, asPrincipal(newRequest(http.MethodPost, "/", body), p))
}

// getAPIKeysCtx builds an Echo context for GET /api-keys.
func getAPIKeysCtx(e *echo.Echo, p *auth.Principal) (echo.Context, *httptest.ResponseRecorder) {
	return echoContext(e, asPrincipal(newRequest(http.MethodGet, "/api-keys", ""), p))
}

// deleteAPIKeyCtx builds an Echo context for DELETE /api-keys/:id.
func deleteAPIKeyCtx(e *echo.Echo, keyID uuid.UUID, p *auth.Principal) (echo.Context, *httptest.ResponseRecorder) {
	return echoContext(e, asPrincipal(newRequest(http.MethodDelete, "/", ""), p), "id", keyID.String())
}

// ─── CreateAPIKey ─────────────────────────────────────────────────────────────

func TestCreateAPIKey(t *testing.T) {
	t.Parallel()
	t.Run("success_returns_201_with_token", func(t *testing.T) {
		t.Parallel()
		f, h := newAPIKeyFixture(t)
		e := echo.New()

		c, rec := postAPIKeyCtx(e, apiKeyBody("ci-deploy-key", 30, models.RoleDeveloper), principal(f.user, models.RoleDeveloper))
		require.NoError(t, h.CreateAPIKey(c))
		assert.Equal(t, http.StatusCreated, rec.Code)

		var resp struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Prefix string `json:"prefix"`
			Token  string `json:"token"`
		}
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))

		assert.NotEmpty(t, resp.ID)
		assert.Equal(t, "ci-deploy-key", resp.Name)
		// Token must start with "sak_" and the prefix must match its first 12 chars.
		assert.True(t, strings.HasPrefix(resp.Token, "sak_"), "token must start with 'sak_'")
		assert.Equal(t, resp.Token[:12], resp.Prefix)
	})

	t.Run("token_shown_once_not_stored_in_plaintext", func(t *testing.T) {
		t.Parallel()
		f, h := newAPIKeyFixture(t)
		ctx := t.Context()
		e := echo.New()

		c, rec := postAPIKeyCtx(e, apiKeyBody("once-key", 30, models.RoleDeveloper), principal(f.user, models.RoleDeveloper))
		require.NoError(t, h.CreateAPIKey(c))
		require.Equal(t, http.StatusCreated, rec.Code)

		var resp struct {
			ID    string `json:"id"`
			Token string `json:"token"`
		}
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))

		// The row must exist but must NOT store the raw token — only the hash.
		keyID := uuid.MustParse(resp.ID)
		var key models.APIKey
		require.NoError(t, f.handle.RW.WithContext(ctx).First(&key, "id = ?", keyID).Error)
		assert.NotEqual(t, resp.Token, key.KeyHash, "key_hash must be a digest, not the raw token")
		assert.Len(t, key.KeyHash, 64, "SHA-256 hex digest must be 64 chars")
	})

	t.Run("empty_name_returns_422", func(t *testing.T) {
		t.Parallel()
		f, h := newAPIKeyFixture(t)
		e := echo.New()

		c, _ := postAPIKeyCtx(e, apiKeyBody("", 30, models.RoleDeveloper), principal(f.user, models.RoleDeveloper))
		err := h.CreateAPIKey(c)

		var apiErr *apierror.Error
		require.True(t, errors.As(err, &apiErr))
		assert.Equal(t, http.StatusUnprocessableEntity, apiErr.Status)
	})

	t.Run("name_over_120_chars_returns_422", func(t *testing.T) {
		t.Parallel()
		f, h := newAPIKeyFixture(t)
		e := echo.New()

		longName := strings.Repeat("x", 121)
		body := apiKeyBody(longName, 30, models.RoleDeveloper)
		c, _ := postAPIKeyCtx(e, body, principal(f.user, models.RoleDeveloper))
		err := h.CreateAPIKey(c)

		var apiErr *apierror.Error
		require.True(t, errors.As(err, &apiErr))
		assert.Equal(t, http.StatusUnprocessableEntity, apiErr.Status)
	})

	for _, days := range []int{1, 30, 90} {
		t.Run(fmt.Sprintf("stores_roles_and_expiry_%d_days", days), func(t *testing.T) {
			t.Parallel()
			f, h := newAPIKeyFixture(t)
			ctx := t.Context()
			e := echo.New()

			creator := principal(f.user, models.RoleDeveloper, models.RoleDeployer)
			body := apiKeyBody("expiry-key", days, models.RoleDeployer, models.RoleDeveloper, models.RoleDeveloper)
			before := time.Now().UTC()
			c, rec := postAPIKeyCtx(e, body, creator)
			require.NoError(t, h.CreateAPIKey(c))
			require.Equal(t, http.StatusCreated, rec.Code)

			var resp struct {
				ID    string   `json:"id"`
				Roles []string `json:"roles"`
			}
			require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
			assert.Equal(t, []string{"deployer", "developer"}, resp.Roles)

			var key models.APIKey
			require.NoError(t, f.handle.RW.WithContext(ctx).First(&key, "id = ?", uuid.MustParse(resp.ID)).Error)
			assert.Equal(t, models.StringArray{"deployer", "developer"}, key.Roles,
				"roles are stored sorted and deduplicated")
			require.NotNil(t, key.ExpiresAt, "expires_at is always set")
			want := before.Add(time.Duration(days) * 24 * time.Hour)
			assert.WithinDuration(t, want, *key.ExpiresAt, time.Minute)
		})
	}
}

// TestCreateAPIKeyValidation covers requests rejected before the handler
// touches the database, so it runs without Docker.
func TestCreateAPIKeyValidation(t *testing.T) {
	t.Parallel()
	h := newTestHandlers(t, nil)
	u := testUser()

	cases := []struct {
		name    string
		body    string
		creator *auth.Principal
		path    string
	}{
		{
			name:    "role_not_held_by_creator",
			body:    apiKeyBody("k", 30, models.RoleDeveloper, models.RolePlatformAdmin),
			creator: principal(u, models.RoleDeveloper),
			path:    "roles[1]",
		},
		{
			name:    "unknown_role",
			body:    apiKeyBody("k", 30, models.Role("superuser")),
			creator: principal(u, models.RoleDeveloper),
			path:    "roles[0]",
		},
		{
			name:    "roles_empty",
			body:    apiKeyBody("k", 30),
			creator: principal(u, models.RoleDeveloper),
			path:    "roles",
		},
		{
			name:    "roles_missing",
			body:    `{"name":"k","expires_days":30}`,
			creator: principal(u, models.RoleDeveloper),
			path:    "roles",
		},
		{
			name:    "expires_days_zero",
			body:    apiKeyBody("k", 0, models.RoleDeveloper),
			creator: principal(u, models.RoleDeveloper),
			path:    "expires_days",
		},
		{
			name:    "expires_days_negative",
			body:    apiKeyBody("k", -1, models.RoleDeveloper),
			creator: principal(u, models.RoleDeveloper),
			path:    "expires_days",
		},
		{
			name:    "expires_days_over_90",
			body:    apiKeyBody("k", 91, models.RoleDeveloper),
			creator: principal(u, models.RoleDeveloper),
			path:    "expires_days",
		},
		{
			name:    "expires_days_missing",
			body:    `{"name":"k","roles":["developer"]}`,
			creator: principal(u, models.RoleDeveloper),
			path:    "expires_days",
		},
		{
			name:    "empty_name",
			body:    apiKeyBody("", 30, models.RoleDeveloper),
			creator: principal(u, models.RoleDeveloper),
			path:    "name",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, rec := postAPIKeyCtx(echo.New(), tc.body, tc.creator)
			apiErr := requireAPIError(t, h.CreateAPIKey(c), http.StatusUnprocessableEntity)

			paths := make([]string, 0, len(apiErr.Errors))
			for _, fe := range apiErr.Errors {
				paths = append(paths, fe.Path)
			}
			assert.Contains(t, paths, tc.path)
			assert.Zero(t, rec.Body.Len(), "nothing is written on a rejected request")
		})
	}
}

// TestCreateAPIKeyRequiresSession covers callers whose auth method may not
// mint keys. The body is invalid on purpose: the method check runs first.
func TestCreateAPIKeyRequiresSession(t *testing.T) {
	t.Parallel()
	h := newTestHandlers(t, nil)
	u := testUser()

	methods := map[string]auth.AuthMethod{
		"api_key":    auth.AuthMethodAPIKey,
		"zero_value": "",
		"unknown":    auth.AuthMethod("bearer"),
	}
	for name, method := range methods {
		t.Run(name+"_returns_403", func(t *testing.T) {
			t.Parallel()
			p := principal(u, models.RoleDeveloper)
			p.AuthMethod = method
			c, rec := postAPIKeyCtx(echo.New(), apiKeyBody("", 0), p)

			apiErr := requireAPIError(t, h.CreateAPIKey(c), http.StatusForbidden)
			assert.Equal(t, "session_required", apiErr.Code)
			assert.Equal(t, "API keys can only be created from a signed-in session", apiErr.Message)
			assert.Empty(t, apiErr.Errors, "the body is not validated")
			assert.Zero(t, rec.Body.Len(), "nothing is written on a rejected request")
		})
	}
}

// recordingAuditLogger keeps every event it is given.
type recordingAuditLogger struct{ events []audit.Event }

func (r *recordingAuditLogger) Log(_ context.Context, e audit.Event) error {
	r.events = append(r.events, e)
	return nil
}

func TestCreateAPIKeyAllowedAuthMethods(t *testing.T) {
	t.Parallel()
	for _, method := range []auth.AuthMethod{auth.AuthMethodSession, auth.AuthMethodDev} {
		t.Run(string(method)+"_returns_201_and_audits_method", func(t *testing.T) {
			t.Parallel()
			f, h := newAPIKeyFixture(t)
			rec := &recordingAuditLogger{}
			h.Audit = rec

			p := principal(f.user, models.RoleDeveloper)
			p.AuthMethod = method
			c, resp := postAPIKeyCtx(echo.New(), apiKeyBody("k", 30, models.RoleDeveloper), p)
			require.NoError(t, h.CreateAPIKey(c))
			assert.Equal(t, http.StatusCreated, resp.Code)

			require.Len(t, rec.events, 1)
			assert.Equal(t, "api_key.created", rec.events[0].Type)
			payload, ok := rec.events[0].Payload.(map[string]any)
			require.True(t, ok, "payload is %T", rec.events[0].Payload)
			assert.Equal(t, string(method), payload["auth_method"])
		})
	}
}

// apiKeyBody returns a POST /api-keys JSON body.
func apiKeyBody(name string, expiresDays int, roles ...models.Role) string {
	scope := make([]string, 0, len(roles))
	for _, r := range roles {
		scope = append(scope, string(r))
	}
	b, err := json.Marshal(map[string]any{"name": name, "expires_days": expiresDays, "roles": scope})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// ─── ListAPIKeys ──────────────────────────────────────────────────────────────

func TestListAPIKeys(t *testing.T) {
	t.Parallel()
	t.Run("empty_list_when_no_keys", func(t *testing.T) {
		t.Parallel()
		f, h := newAPIKeyFixture(t)
		e := echo.New()

		c, rec := getAPIKeysCtx(e, principal(f.user, models.RoleDeveloper))
		require.NoError(t, h.ListAPIKeys(c))
		assert.Equal(t, http.StatusOK, rec.Code)

		var resp struct {
			Items []any `json:"items"`
			Total int64 `json:"total"`
		}
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Empty(t, resp.Items)
		assert.Equal(t, int64(0), resp.Total)
	})

	t.Run("lists_own_keys_only", func(t *testing.T) {
		t.Parallel()
		f, h := newAPIKeyFixture(t)
		e := echo.New()

		// Create one key as developer, one as admin.
		c1, _ := postAPIKeyCtx(e, apiKeyBody("dev-key", 30, models.RoleDeveloper), principal(f.user, models.RoleDeveloper))
		require.NoError(t, h.CreateAPIKey(c1))

		c2, _ := postAPIKeyCtx(e, apiKeyBody("admin-key", 30, models.RolePlatformAdmin), principal(f.admin, models.RolePlatformAdmin))
		require.NoError(t, h.CreateAPIKey(c2))

		// Developer lists: should see only their own key.
		c, rec := getAPIKeysCtx(e, principal(f.user, models.RoleDeveloper))
		require.NoError(t, h.ListAPIKeys(c))
		assert.Equal(t, http.StatusOK, rec.Code)

		var resp struct {
			Items []struct {
				Name string `json:"name"`
			} `json:"items"`
			Total int64 `json:"total"`
		}
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, int64(1), resp.Total)
		assert.Equal(t, "dev-key", resp.Items[0].Name)
	})

	t.Run("revoked_keys_excluded", func(t *testing.T) {
		t.Parallel()
		f, h := newAPIKeyFixture(t)
		e := echo.New()

		// Create and immediately revoke a key.
		c1, rec1 := postAPIKeyCtx(e, apiKeyBody("soon-revoked", 30, models.RoleDeveloper), principal(f.user, models.RoleDeveloper))
		require.NoError(t, h.CreateAPIKey(c1))
		var created struct {
			ID string `json:"id"`
		}
		require.NoError(t, json.NewDecoder(rec1.Body).Decode(&created))

		c2, _ := deleteAPIKeyCtx(e, uuid.MustParse(created.ID), principal(f.user, models.RoleDeveloper))
		require.NoError(t, h.RevokeAPIKey(c2))

		// List must return zero items.
		c, rec := getAPIKeysCtx(e, principal(f.user, models.RoleDeveloper))
		require.NoError(t, h.ListAPIKeys(c))
		var resp struct {
			Total int64 `json:"total"`
		}
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, int64(0), resp.Total, "revoked keys must not appear in the list")
	})

	t.Run("token_not_present_in_list_response", func(t *testing.T) {
		t.Parallel()
		f, h := newAPIKeyFixture(t)
		e := echo.New()

		c1, _ := postAPIKeyCtx(e, apiKeyBody("check-no-token", 30, models.RoleDeveloper), principal(f.user, models.RoleDeveloper))
		require.NoError(t, h.CreateAPIKey(c1))

		c, rec := getAPIKeysCtx(e, principal(f.user, models.RoleDeveloper))
		require.NoError(t, h.ListAPIKeys(c))

		// Decode the raw response body and assert "token" key is absent.
		var resp struct {
			Items []map[string]any `json:"items"`
		}
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		require.Len(t, resp.Items, 1)
		_, hasToken := resp.Items[0]["token"]
		assert.False(t, hasToken, "full token must never appear in list responses")
	})
}

// ─── RevokeAPIKey ─────────────────────────────────────────────────────────────

func TestRevokeAPIKey(t *testing.T) {
	t.Parallel()
	t.Run("owner_can_revoke_own_key", func(t *testing.T) {
		t.Parallel()
		f, h := newAPIKeyFixture(t)
		ctx := t.Context()
		e := echo.New()

		c1, rec1 := postAPIKeyCtx(e, apiKeyBody("my-key", 30, models.RoleDeveloper), principal(f.user, models.RoleDeveloper))
		require.NoError(t, h.CreateAPIKey(c1))
		var created struct {
			ID string `json:"id"`
		}
		require.NoError(t, json.NewDecoder(rec1.Body).Decode(&created))
		keyID := uuid.MustParse(created.ID)

		c2, rec2 := deleteAPIKeyCtx(e, keyID, principal(f.user, models.RoleDeveloper))
		require.NoError(t, h.RevokeAPIKey(c2))
		assert.Equal(t, http.StatusNoContent, rec2.Code)

		var key models.APIKey
		require.NoError(t, f.handle.RW.WithContext(ctx).First(&key, "id = ?", keyID).Error)
		assert.NotNil(t, key.RevokedAt, "revoked_at must be set after revocation")
	})

	t.Run("platform_admin_can_revoke_any_key", func(t *testing.T) {
		t.Parallel()
		f, h := newAPIKeyFixture(t)
		e := echo.New()

		// Developer creates a key.
		c1, rec1 := postAPIKeyCtx(e, apiKeyBody("dev-key", 30, models.RoleDeveloper), principal(f.user, models.RoleDeveloper))
		require.NoError(t, h.CreateAPIKey(c1))
		var created struct {
			ID string `json:"id"`
		}
		require.NoError(t, json.NewDecoder(rec1.Body).Decode(&created))
		keyID := uuid.MustParse(created.ID)

		// Admin revokes it.
		c2, rec2 := deleteAPIKeyCtx(e, keyID, principal(f.admin, models.RolePlatformAdmin))
		require.NoError(t, h.RevokeAPIKey(c2))
		assert.Equal(t, http.StatusNoContent, rec2.Code)
	})

	t.Run("other_user_cannot_revoke_returns_403", func(t *testing.T) {
		t.Parallel()
		f, h := newAPIKeyFixture(t)
		e := echo.New()

		// Create a second user in the same org (a different developer).
		otherUser := seed.SeedUser(t, f.handle, models.User{
			OrganizationID:  f.org.ID,
			ExternalSubject: "other-sub",
			Email:           "other@key-org.nl",
			DisplayName:     "Other User",
		})
		otherPrincipal := principal(otherUser, models.RoleDeveloper)

		// Developer creates a key.
		c1, rec1 := postAPIKeyCtx(e, apiKeyBody("protected-key", 30, models.RoleDeveloper), principal(f.user, models.RoleDeveloper))
		require.NoError(t, h.CreateAPIKey(c1))
		var created struct {
			ID string `json:"id"`
		}
		require.NoError(t, json.NewDecoder(rec1.Body).Decode(&created))
		keyID := uuid.MustParse(created.ID)

		// Different user tries to revoke it.
		c2, _ := deleteAPIKeyCtx(e, keyID, otherPrincipal)
		err := h.RevokeAPIKey(c2)

		var apiErr *apierror.Error
		require.True(t, errors.As(err, &apiErr))
		assert.Equal(t, http.StatusForbidden, apiErr.Status)
	})

	t.Run("not_found_returns_404", func(t *testing.T) {
		t.Parallel()
		f, h := newAPIKeyFixture(t)
		e := echo.New()

		c, _ := deleteAPIKeyCtx(e, uuid.Must(uuid.NewV7()), principal(f.user, models.RoleDeveloper))
		err := h.RevokeAPIKey(c)

		var apiErr *apierror.Error
		require.True(t, errors.As(err, &apiErr))
		assert.Equal(t, http.StatusNotFound, apiErr.Status)
	})
}
