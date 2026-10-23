package handlers

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	gh "github.com/ClappFormOrg/SaidKick/API/internal/integrations/github"
	"github.com/google/go-github/v66/github"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// webhookMaxBodySize caps the GitHub webhook payload read into memory.
const webhookMaxBodySize = 25 << 20 // 25 MiB

// GitHubWebhook ingests GitHub events. The HMAC signature is verified
// against the platform's configured webhook secret; unverified payloads
// are rejected with 401.
//
// V0 processes events synchronously inline:
//   - push: identifies affected Apps by repo, marks them for re-validation
//   - installation: logs and acknowledges (full installation tracking is
//     a Sprint 3 feature)
//   - repository / installation_repositories: marks Apps disconnected if
//     the repo or installation is removed
//
// Full async revalidation (clone repo, re-run Stage-1 validator, transition
// AppVersion) is deferred to the gh-webhook-worker process — V0 just
// flags the App with repo_link_state so the catalogue UI can show a
// warning.
func (h *Handlers) GitHubWebhook(c echo.Context) error {
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, webhookMaxBodySize))
	if err != nil {
		return apierror.New(http.StatusBadRequest, "bad_request", "failed to read webhook body")
	}

	sig := c.Request().Header.Get("X-Hub-Signature-256")
	if sig == "" {
		return apierror.New(http.StatusUnauthorized, "missing_signature",
			"X-Hub-Signature-256 header is required")
	}

	if err := h.GitHub.VerifyWebhookSignature(body, sig); err != nil {
		switch {
		case errors.Is(err, gh.ErrNotConfigured):
			return apierror.New(http.StatusServiceUnavailable, "github_not_configured",
				"GitHub App not configured on this environment")
		default:
			return apierror.New(http.StatusUnauthorized, "invalid_signature",
				"webhook signature did not match the configured secret")
		}
	}

	eventType := c.Request().Header.Get("X-GitHub-Event")
	if eventType == "" {
		return apierror.New(http.StatusBadRequest, "missing_event_type",
			"X-GitHub-Event header is required")
	}

	parsed, err := h.GitHub.ParseEvent(eventType, body)
	if err != nil {
		slog.Error("webhook: parse event", "error", err, "event_type", eventType)
		return apierror.New(http.StatusBadRequest, "parse_failed", "could not parse webhook payload")
	}

	ctx := c.Request().Context()

	switch ev := parsed.(type) {
	case *github.PushEvent:
		// Find any App linked to this repo + branch and flag it for
		// re-validation. We don't run the validator inline — that
		// would block the webhook latency budget. Instead the worker
		// process (deferred) will pick up flagged rows.
		repoOwner := ""
		repoName := ""
		if ev.Repo != nil {
			repoOwner = ev.Repo.GetOwner().GetName()
			repoName = ev.Repo.GetName()
		}
		ref := ev.GetRef() // e.g. "refs/heads/main"

		if repoOwner == "" || repoName == "" {
			return c.JSON(http.StatusOK, map[string]any{"status": "ignored", "reason": "missing repo metadata"})
		}

		var apps []models.App
		if err := h.DB.RW.WithContext(ctx).
			Where("github_repo_owner = ? AND github_repo_name = ?", repoOwner, repoName).
			Find(&apps).Error; err != nil {
			slog.Error("webhook: lookup app", "error", err)
			return apierror.New(http.StatusInternalServerError, "internal_error", "failed to look up linked apps")
		}

		updated := 0
		for _, app := range apps {
			if "refs/heads/"+app.GitHubDefaultBranch != ref {
				continue // a push to a non-audited branch — ignore
			}
			// Create a revalidation job so the background worker picks it up.
			job := models.RevalidationJob{
				AppID:   app.ID,
				NewHead: ev.GetAfter(),
				OldHead: ev.GetBefore(),
				Status:  models.RevalidationPending,
			}
			if err := h.DB.RW.WithContext(ctx).Create(&job).Error; err != nil {
				slog.Error("webhook: create revalidation job", "error", err, "app_id", app.ID)
				// Non-fatal: fall through and still audit-log the push.
			}
			_ = h.Audit.Log(ctx, audit.Event{
				Type:        "app.revalidation_queued",
				SubjectType: "app",
				SubjectID:   app.ID,
				Payload: map[string]any{
					"app_slug": app.Slug,
					"new_head": ev.GetAfter(),
					"old_head": ev.GetBefore(),
					"reason":   "push to audited branch",
					"pusher":   ev.GetPusher().GetName(),
				},
			})
			updated++
		}
		return c.JSON(http.StatusOK, map[string]any{
			"status":      "accepted",
			"event":       "push",
			"apps_queued": updated,
		})

	case *github.InstallationEvent:
		_ = h.Audit.Log(ctx, audit.Event{
			Type:        "github.installation_event",
			SubjectType: "github_installation",
			SubjectID:   githubInstallationSubjectID,
			Payload: map[string]any{
				"action":          ev.GetAction(),
				"installation_id": ev.GetInstallation().GetID(),
				"account_login":   ev.GetInstallation().GetAccount().GetLogin(),
			},
		})
		return c.JSON(http.StatusOK, map[string]any{
			"status": "accepted",
			"event":  "installation",
			"action": ev.GetAction(),
		})

	case *github.InstallationRepositoriesEvent:
		// Mark any Apps whose installation now lacks repo access as
		// disconnected. GitHub's repositories_removed entries carry only
		// id, node_id, name, full_name and private (no owner), so the
		// owner comes from full_name ("owner/name").
		if ev.GetAction() == "removed" {
			for _, repo := range ev.RepositoriesRemoved {
				if repo == nil {
					continue
				}
				owner, name, ok := strings.Cut(repo.GetFullName(), "/")
				if !ok || owner == "" || name == "" {
					continue
				}
				if err := h.DB.RW.WithContext(ctx).
					Model(&models.App{}).
					Where("github_repo_owner = ? AND github_repo_name = ?", owner, name).
					Update("repo_link_state", models.RepoDisconnected).Error; err != nil {
					slog.Error("webhook: mark repo disconnected", "error", err)
				}
			}
		}
		return c.JSON(http.StatusOK, map[string]any{
			"status": "accepted",
			"event":  "installation_repositories",
			"action": ev.GetAction(),
		})

	default:
		// Unhandled event type — accept so GitHub stops retrying, log so
		// we can spot patterns later.
		slog.Info("webhook: unhandled event type", "event_type", eventType)
		return c.JSON(http.StatusOK, map[string]any{
			"status": "ignored",
			"event":  eventType,
		})
	}
}

// githubInstallationSubjectID is the well-known UUIDv5 used as the
// SubjectID for audit log entries that record GitHub installation
// lifecycle events (the installations themselves are identified by an
// int64 GitHub-side id, which lives in the audit payload).
var githubInstallationSubjectID = uuid.NewSHA1(
	uuid.NameSpaceURL,
	[]byte("saidkick-appstore:github:installations"),
)
