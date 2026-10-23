package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
	"github.com/labstack/echo/v4"
)

// communityHTTPClient is used for GitHub GraphQL calls. A per-request timeout
// guards against indefinite hangs on slow or unresponsive upstream responses.
var communityHTTPClient = &http.Client{Timeout: 15 * time.Second}

// ─── In-memory cache ──────────────────────────────────────────────────────────

type cachedDiscussions struct {
	payload   []byte
	expiresAt time.Time
}

var (
	discussionsCache   sync.Map
	discussionsCacheTTL = 5 * time.Minute
)

// ─── Handler ──────────────────────────────────────────────────────────────────

// ErrCommunityNotConfigured means this environment has no usable community
// configuration. It is not a failure: the endpoint answers 200 with an empty
// list and `configured: false` so the page can say "not set up here" instead of
// "loading failed".
var ErrCommunityNotConfigured = errors.New("community: not configured")

// notConfiguredResponse is the shape returned when there is nothing to proxy.
func notConfiguredResponse(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{
		"items":      []any{},
		"configured": false,
	})
}

// ListCommunityDiscussions proxies GitHub Discussions for the configured
// community repo. Responses are cached for 5 minutes to stay well within
// GitHub's 5 000 req/hr rate limit.
//
// Three outcomes, deliberately distinct (issue #43):
//
//   - Not configured — no token, or a repo that is not "owner/repo": 200 with an
//     empty list and configured:false. Nothing is wrong; there is just nothing
//     to show.
//   - Upstream refused — GitHub unreachable, rate-limited, credentials rejected,
//     or a GraphQL error such as an unknown repo or Discussions turned off:
//     502 with code community_upstream_unavailable. This used to return a bare
//     error, which the error handler mapped to 500 — reporting somebody else's
//     outage as a fault in this platform, on an anonymous public route.
//   - Success — 200 with the transformed list.
//
// Query params:
//   - category:  filter by category name (optional)
//   - page_size: number of results, 1–50 (default 20)
func (h *Handlers) ListCommunityDiscussions(c echo.Context) error {
	log := slog.With("handler", "ListCommunityDiscussions", "repo", h.CommunityRepo)

	if h.CommunityToken == "" {
		log.Warn("community discussions token not configured — returning empty list")
		return notConfiguredResponse(c)
	}

	category := strings.TrimSpace(c.QueryParam("category"))
	pageSize := clampInt(parseIntParam(c, "page_size", 20), 1, 50)
	cacheKey := fmt.Sprintf("%s|%s|%d", h.CommunityRepo, category, pageSize)

	// Cache hit?
	if v, ok := discussionsCache.Load(cacheKey); ok {
		entry, ok := v.(cachedDiscussions)
		if ok && time.Now().Before(entry.expiresAt) {
			log.Info("community discussions served from cache")
			c.Response().Header().Set(echo.HeaderContentType, "application/json")
			_, err := c.Response().Write(entry.payload)
			return err
		}
	}

	log.Info("fetching community discussions from GitHub", "page_size", pageSize, "category", category)
	payload, err := h.fetchDiscussions(c.Request().Context(), category, pageSize)
	switch {
	case errors.Is(err, ErrCommunityNotConfigured):
		log.Warn("community repo is not a valid owner/repo — returning empty list")
		return notConfiguredResponse(c)
	case err != nil:
		// The detail stays in the log: it can carry a repo name, a rate-limit
		// note or a GraphQL message, none of which belongs in an anonymous
		// response.
		log.Error("failed to fetch community discussions", "error", err)
		return apierror.New(http.StatusBadGateway, "community_upstream_unavailable",
			"community discussions are temporarily unavailable")
	}

	discussionsCache.Store(cacheKey, cachedDiscussions{
		payload:   payload,
		expiresAt: time.Now().Add(discussionsCacheTTL),
	})
	log.Info("community discussions fetched and cached", "bytes", len(payload))

	c.Response().Header().Set(echo.HeaderContentType, "application/json")
	_, err = c.Response().Write(payload)
	return err
}

// ─── GitHub GraphQL ───────────────────────────────────────────────────────────

// githubGraphQLEndpoint is a variable so tests can point it at a local server.
var githubGraphQLEndpoint = "https://api.github.com/graphql"

func (h *Handlers) fetchDiscussions(ctx context.Context, category string, pageSize int) ([]byte, error) {
	owner, repo, ok := strings.Cut(h.CommunityRepo, "/")
	if !ok || owner == "" || repo == "" {
		// A malformed repo setting is an environment that has not been wired up,
		// not an upstream fault — so it takes the not-configured path rather
		// than reporting a 5xx on every request for the life of the deployment.
		return nil, fmt.Errorf("%w: GITHUB_COMMUNITY_REPO %q is not owner/repo",
			ErrCommunityNotConfigured, h.CommunityRepo)
	}

	query := buildDiscussionsQuery(owner, repo, pageSize)
	body, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubGraphQLEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.CommunityToken)
	req.Header.Set("User-Agent", "saidkick-appstore/1.0")

	resp, err := communityHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github graphql: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		slog.Error("github graphql non-200", "status", resp.StatusCode, "body_preview", string(raw[:min(len(raw), 200)]))
		return nil, fmt.Errorf("github graphql returned status %d", resp.StatusCode)
	}

	return transformDiscussions(raw, category)
}

// buildDiscussionsQuery returns a GraphQL query string.
//
// The query does not filter by category: the GraphQL API wants a categoryId
// (UUID), not a name, and resolving the name would cost an extra round-trip.
// The filter is therefore applied to the decoded response instead — see
// transformDiscussions. 50 results is the practical max for a community page.
func buildDiscussionsQuery(owner, repo string, pageSize int) string {
	return fmt.Sprintf(`{
  repository(owner: %q, name: %q) {
    discussions(first: %d, orderBy: {field: UPDATED_AT, direction: DESC}) {
      nodes {
        id
        number
        title
        url
        createdAt
        updatedAt
        bodyText
        upvoteCount
        author { login avatarUrl }
        category { name }
        comments { totalCount }
      }
    }
  }
}`, owner, repo, pageSize)
}

// ─── Response transformation ──────────────────────────────────────────────────

type ghDiscussionNode struct {
	ID          string `json:"id"`
	Number      int    `json:"number"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
	BodyText    string `json:"bodyText"`
	UpvoteCount int    `json:"upvoteCount"`
	Author      struct {
		Login     string `json:"login"`
		AvatarURL string `json:"avatarUrl"`
	} `json:"author"`
	Category struct {
		Name string `json:"name"`
	} `json:"category"`
	Comments struct {
		TotalCount int `json:"totalCount"`
	} `json:"comments"`
}

type ghGraphQLResponse struct {
	Data struct {
		Repository struct {
			Discussions struct {
				Nodes []ghDiscussionNode `json:"nodes"`
			} `json:"discussions"`
		} `json:"repository"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// transformDiscussions decodes GitHub's GraphQL envelope into the flat shape the
// webapp consumes, optionally keeping only one category.
//
// category is matched case-insensitively against the discussion's category name.
// The filter lives here rather than in the query because the GraphQL API takes a
// categoryId, not a name; the handler's cache key includes the category so each
// filtered view is cached separately.
func transformDiscussions(raw []byte, category string) ([]byte, error) {
	var ghResp ghGraphQLResponse
	if err := json.Unmarshal(raw, &ghResp); err != nil {
		return nil, fmt.Errorf("parse github response: %w", err)
	}
	if len(ghResp.Errors) > 0 {
		msgs := make([]string, len(ghResp.Errors))
		for i, e := range ghResp.Errors {
			msgs[i] = e.Message
		}
		return nil, fmt.Errorf("github graphql errors: %s", strings.Join(msgs, "; "))
	}

	want := strings.TrimSpace(category)

	items := make([]map[string]any, 0, len(ghResp.Data.Repository.Discussions.Nodes))
	for _, n := range ghResp.Data.Repository.Discussions.Nodes {
		if want != "" && !strings.EqualFold(n.Category.Name, want) {
			continue
		}
		excerpt := truncateRunes(n.BodyText, 300)
		items = append(items, map[string]any{
			"id":            n.ID,
			"number":        n.Number,
			"title":         n.Title,
			"url":           n.URL,
			"author":        n.Author.Login,
			"author_avatar": n.Author.AvatarURL,
			"category":      n.Category.Name,
			"created_at":    n.CreatedAt,
			"updated_at":    n.UpdatedAt,
			"comment_count": n.Comments.TotalCount,
			"upvote_count":  n.UpvoteCount,
			"excerpt":       excerpt,
		})
	}

	return json.Marshal(map[string]any{"items": items, "configured": true})
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func clampInt(v, lo, hi int) int {
	return max(lo, min(v, hi))
}

// truncateRunes shortens s to at most n characters, appending an ellipsis when
// it had to cut.
//
// Counts runes, not bytes. Slicing bytes at a fixed offset can land mid-rune and
// leave a broken character, which for Dutch discussion titles is a matter of
// where the cut happens to fall rather than a rare edge case.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n]) + "…"
}

func parseIntParam(c echo.Context, key string, def int) int {
	s := strings.TrimSpace(c.QueryParam(key))
	if s == "" {
		return def
	}
	v := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return def
		}
		v = v*10 + int(ch-'0')
	}
	return v
}
