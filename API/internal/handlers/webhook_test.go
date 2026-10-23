package handlers_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/github/githubtest"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
	gogithub "github.com/google/go-github/v66/github"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── Test doubles ────────────────────────────────────────────────────────────

// testWebhookKey is the HMAC key webhookGitHub verifies signatures with.
const testWebhookKey = "test-webhook-key"

// webhookGitHub is a MemoryClient that verifies and parses webhooks the way
// the real client does, using go-github's helpers.
type webhookGitHub struct {
	githubtest.MemoryClient
}

func (webhookGitHub) VerifyWebhookSignature(payload []byte, signatureHeader string) error {
	return gogithub.ValidateSignature(signatureHeader, payload, []byte(testWebhookKey))
}

func (webhookGitHub) ParseEvent(eventType string, payload []byte) (any, error) {
	return gogithub.ParseWebHook(eventType, payload)
}

// signWebhook returns the X-Hub-Signature-256 value for body under key.
func signWebhook(key, body string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// webhookCtx builds an Echo context for POST /webhooks/github. Empty sig or
// event leave that header unset.
func webhookCtx(e *echo.Echo, body, sig, event string) (echo.Context, *httptest.ResponseRecorder) {
	req := newRequest(http.MethodPost, "/", body)
	if sig != "" {
		req.Header.Set("X-Hub-Signature-256", sig)
	}
	if event != "" {
		req.Header.Set("X-GitHub-Event", event)
	}
	return echoContext(e, req)
}

// mustJSON marshals v for a webhook payload.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// pushPayload returns a push event for owner/repo at ref.
func pushPayload(t *testing.T, owner, repo, ref string) string {
	t.Helper()
	return mustJSON(t, map[string]any{
		"ref":    ref,
		"before": strings.Repeat("a", 40),
		"after":  strings.Repeat("b", 40),
		"repository": map[string]any{
			"name":  repo,
			"owner": map[string]any{"name": owner, "login": owner},
		},
		"pusher": map[string]any{"name": "dev"},
	})
}

// decodeWebhookResponse decodes a webhook JSON response.
func decodeWebhookResponse(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp
}

// ─── Signature and parsing (no database) ─────────────────────────────────────

func TestGitHubWebhook_Rejected(t *testing.T) {
	t.Parallel()
	h := newTestHandlers(t, nil, withGitHub(webhookGitHub{}))
	const body = `{"zen":"hi","hook_id":1}`

	cases := []struct {
		name   string
		body   string
		sig    string
		event  string
		status int
		code   string
	}{
		{"missing_signature", body, "", "ping", http.StatusUnauthorized, "missing_signature"},
		{"wrong_key", body, signWebhook("wrong-webhook-key", body), "ping", http.StatusUnauthorized, "invalid_signature"},
		{"tampered_body", `{"zen":"bye","hook_id":1}`, signWebhook(testWebhookKey, body), "ping", http.StatusUnauthorized, "invalid_signature"},
		{"malformed_signature", body, "sha256=zz", "ping", http.StatusUnauthorized, "invalid_signature"},
		{"sha1_signature", body, "sha1=" + strings.Repeat("0", 40), "ping", http.StatusUnauthorized, "invalid_signature"},
		{"missing_event_type", body, signWebhook(testWebhookKey, body), "", http.StatusBadRequest, "missing_event_type"},
		{"unknown_event_type", body, signWebhook(testWebhookKey, body), "no_such_event", http.StatusBadRequest, "parse_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, _ := webhookCtx(echo.New(), tc.body, tc.sig, tc.event)
			apiErr := requireAPIError(t, h.GitHubWebhook(c), tc.status)
			assert.Equal(t, tc.code, apiErr.Code)
		})
	}
}

func TestGitHubWebhook_NotConfigured(t *testing.T) {
	t.Parallel()
	h := newTestHandlers(t, nil) // default NullClient
	const body = `{"zen":"hi","hook_id":1}`

	c, _ := webhookCtx(echo.New(), body, signWebhook(testWebhookKey, body), "ping")
	apiErr := requireAPIError(t, h.GitHubWebhook(c), http.StatusServiceUnavailable)
	assert.Equal(t, "github_not_configured", apiErr.Code)
}

func TestGitHubWebhook_AcceptedWithoutDatabase(t *testing.T) {
	t.Parallel()
	h := newTestHandlers(t, nil, withGitHub(webhookGitHub{}))

	t.Run("unhandled_event_is_ignored", func(t *testing.T) {
		t.Parallel()
		const body = `{"zen":"hi","hook_id":1}`
		c, rec := webhookCtx(echo.New(), body, signWebhook(testWebhookKey, body), "ping")
		require.NoError(t, h.GitHubWebhook(c))
		assert.Equal(t, http.StatusOK, rec.Code)
		resp := decodeWebhookResponse(t, rec)
		assert.Equal(t, "ignored", resp["status"])
		assert.Equal(t, "ping", resp["event"])
	})

	t.Run("push_without_repo_is_ignored", func(t *testing.T) {
		t.Parallel()
		body := mustJSON(t, map[string]any{"ref": "refs/heads/main"})
		c, rec := webhookCtx(echo.New(), body, signWebhook(testWebhookKey, body), "push")
		require.NoError(t, h.GitHubWebhook(c))
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "ignored", decodeWebhookResponse(t, rec)["status"])
	})

	t.Run("installation_event_is_accepted", func(t *testing.T) {
		t.Parallel()
		body := mustJSON(t, map[string]any{
			"action":       "created",
			"installation": map[string]any{"id": 7, "account": map[string]any{"login": "acme"}},
		})
		c, rec := webhookCtx(echo.New(), body, signWebhook(testWebhookKey, body), "installation")
		require.NoError(t, h.GitHubWebhook(c))
		assert.Equal(t, http.StatusOK, rec.Code)
		resp := decodeWebhookResponse(t, rec)
		assert.Equal(t, "accepted", resp["status"])
		assert.Equal(t, "created", resp["action"])
	})
}

// ─── Push events (database) ──────────────────────────────────────────────────

// webhookFixture holds an app linked to acme/<repo> on branch main.
type webhookFixture struct {
	handle *db.Handle
	app    models.App
}

func newWebhookFixture(t *testing.T) webhookFixture {
	t.Helper()
	handle := testutil.NewPostgresDB(t)
	org := seed.SeedOrg(t, handle, models.Organization{})
	user := seed.SeedUser(t, handle, models.User{OrganizationID: org.ID})
	app := seed.SeedApp(t, handle, models.App{
		OwningOrganizationID: org.ID,
		OwningUserID:         user.ID,
		GitHubRepoOwner:      "acme",
		GitHubRepoName:       "hooked-repo",
		GitHubDefaultBranch:  "main",
	})
	return webhookFixture{handle: handle, app: app}
}

// revalidationJobs returns the jobs queued for the fixture app.
func revalidationJobs(t *testing.T, f webhookFixture) []models.RevalidationJob {
	t.Helper()
	var jobs []models.RevalidationJob
	require.NoError(t, f.handle.RW.WithContext(t.Context()).Where("app_id = ?", f.app.ID).Find(&jobs).Error)
	return jobs
}

func TestGitHubWebhook_Push(t *testing.T) {
	t.Parallel()
	t.Run("push_to_default_branch_queues_revalidation", func(t *testing.T) {
		t.Parallel()
		f := newWebhookFixture(t)
		h := newTestHandlers(t, f.handle, withGitHub(webhookGitHub{}))

		body := pushPayload(t, "acme", "hooked-repo", "refs/heads/main")
		c, rec := webhookCtx(echo.New(), body, signWebhook(testWebhookKey, body), "push")
		require.NoError(t, h.GitHubWebhook(c))
		assert.Equal(t, http.StatusOK, rec.Code)
		resp := decodeWebhookResponse(t, rec)
		assert.Equal(t, "accepted", resp["status"])
		assert.EqualValues(t, 1, resp["apps_queued"])

		jobs := revalidationJobs(t, f)
		require.Len(t, jobs, 1)
		assert.Equal(t, strings.Repeat("b", 40), jobs[0].NewHead)
		assert.Equal(t, strings.Repeat("a", 40), jobs[0].OldHead)
		assert.Equal(t, models.RevalidationPending, jobs[0].Status)
	})

	t.Run("push_to_other_branch_queues_nothing", func(t *testing.T) {
		t.Parallel()
		f := newWebhookFixture(t)
		h := newTestHandlers(t, f.handle, withGitHub(webhookGitHub{}))

		body := pushPayload(t, "acme", "hooked-repo", "refs/heads/feature")
		c, rec := webhookCtx(echo.New(), body, signWebhook(testWebhookKey, body), "push")
		require.NoError(t, h.GitHubWebhook(c))
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.EqualValues(t, 0, decodeWebhookResponse(t, rec)["apps_queued"])
		assert.Empty(t, revalidationJobs(t, f))
	})

	t.Run("bad_signature_queues_nothing", func(t *testing.T) {
		t.Parallel()
		f := newWebhookFixture(t)
		h := newTestHandlers(t, f.handle, withGitHub(webhookGitHub{}))

		body := pushPayload(t, "acme", "hooked-repo", "refs/heads/main")
		c, _ := webhookCtx(echo.New(), body, signWebhook("wrong-webhook-key", body), "push")
		requireAPIError(t, h.GitHubWebhook(c), http.StatusUnauthorized)
		assert.Empty(t, revalidationJobs(t, f))
	})
}

// ─── Installation repositories events (database) ─────────────────────────────

// repositoriesRemovedPayload returns an installation_repositories "removed"
// event shaped like GitHub's: each repositories_removed entry has id,
// node_id, name, full_name and private, but no owner.
func repositoriesRemovedPayload(t *testing.T, fullNames ...string) string {
	t.Helper()
	removed := make([]map[string]any, 0, len(fullNames))
	for i, fullName := range fullNames {
		_, name, _ := strings.Cut(fullName, "/")
		removed = append(removed, map[string]any{
			"id":        1000 + i,
			"node_id":   "R_test" + name,
			"name":      name,
			"full_name": fullName,
			"private":   true,
		})
	}
	return mustJSON(t, map[string]any{
		"action":               "removed",
		"installation":         map[string]any{"id": 1, "account": map[string]any{"login": "acme"}},
		"repository_selection": "selected",
		"repositories_added":   []any{},
		"repositories_removed": removed,
		"sender":               map[string]any{"login": "acme-admin"},
	})
}

// repoLinkState reloads app's repo_link_state.
func repoLinkState(t *testing.T, handle *db.Handle, app models.App) models.RepoLinkState {
	t.Helper()
	var got models.App
	require.NoError(t, handle.RW.WithContext(t.Context()).First(&got, "id = ?", app.ID).Error)
	return got.RepoLinkState
}

func TestGitHubWebhook_RepositoriesRemoved(t *testing.T) {
	t.Parallel()
	f := newWebhookFixture(t)
	other := seed.SeedApp(t, f.handle, models.App{
		OwningOrganizationID: f.app.OwningOrganizationID,
		OwningUserID:         f.app.OwningUserID,
		GitHubRepoOwner:      "acme",
		GitHubRepoName:       "untouched-repo",
	})
	h := newTestHandlers(t, f.handle, withGitHub(webhookGitHub{}))
	require.Equal(t, models.RepoConnected, repoLinkState(t, f.handle, f.app))

	// someone-else/untouched-repo shares a name with the unrelated app, so
	// matching on name alone would wrongly disconnect it.
	body := repositoriesRemovedPayload(t, "acme/hooked-repo", "someone-else/untouched-repo")
	assert.NotContains(t, body, `"owner"`)
	c, rec := webhookCtx(echo.New(), body, signWebhook(testWebhookKey, body), "installation_repositories")
	require.NoError(t, h.GitHubWebhook(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	resp := decodeWebhookResponse(t, rec)
	assert.Equal(t, "accepted", resp["status"])
	assert.Equal(t, "removed", resp["action"])

	assert.Equal(t, models.RepoDisconnected, repoLinkState(t, f.handle, f.app))
	assert.Equal(t, models.RepoConnected, repoLinkState(t, f.handle, other))
}
