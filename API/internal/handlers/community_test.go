package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for the community discussions proxy (issue #43).
//
// The endpoint is anonymous and public, so what it returns when GitHub misbehaves
// is user-visible. The bug was that every non-success path — a bad token, a repo
// with Discussions turned off, a malformed config — surfaced as 500, reporting
// somebody else's problem as a fault in this platform.
//
// These live in package `handlers` (not handlers_test) to reach the unexported
// transform and query builders directly; no database is involved.

func newTestContext(t *testing.T, target string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, target, http.NoBody)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

func TestCommunityWithoutTokenReturnsEmptyNotAnError(t *testing.T) {
	h := &Handlers{CommunityToken: "", CommunityRepo: "owner/repo"}
	c, rec := newTestContext(t, "/api/v1/community/discussions")

	require.NoError(t, h.ListCommunityDiscussions(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Items      []any `json:"items"`
		Configured bool  `json:"configured"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Empty(t, body.Items)
	assert.False(t, body.Configured, "an unconfigured instance must say so")
}

func TestCommunityMalformedRepoIsNotConfiguredNot500(t *testing.T) {
	// A token is present, so the handler gets as far as building the request —
	// which is where the old code produced a per-request error forever.
	for _, repo := range []string{"", "no-slash", "/repo", "owner/"} {
		t.Run("repo="+repo, func(t *testing.T) {
			h := &Handlers{CommunityToken: "ghp_x", CommunityRepo: repo}
			c, rec := newTestContext(t, "/api/v1/community/discussions")

			err := h.ListCommunityDiscussions(c)
			require.NoError(t, err, "must not surface an error to the error handler")
			require.Equal(t, http.StatusOK, rec.Code)

			var body struct {
				Configured bool `json:"configured"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.False(t, body.Configured)
		})
	}
}

func TestFetchDiscussionsMalformedRepoWrapsNotConfigured(t *testing.T) {
	h := &Handlers{CommunityToken: "ghp_x", CommunityRepo: "nope"}

	_, err := h.fetchDiscussions(t.Context(), "", 20)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCommunityNotConfigured),
		"must be classifiable as not-configured, got %v", err)
}

// TestTransformDiscussionsGraphQLErrorIsAnError is the path that produced the
// reported 500: GitHub answers HTTP 200 with an `errors` array when the repo is
// unknown or Discussions are disabled.
func TestTransformDiscussionsGraphQLErrorIsAnError(t *testing.T) {
	raw := []byte(`{"data":{"repository":null},"errors":[{"message":"Could not resolve to a Repository with the name 'x/y'."}]}`)

	_, err := transformDiscussions(raw, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Could not resolve to a Repository")
	assert.False(t, errors.Is(err, ErrCommunityNotConfigured),
		"an upstream GraphQL error is not a local misconfiguration")
}

// TestUpstreamFailureMapsTo502 drives the handler against a fake GitHub. A 500
// here would report GitHub's outage as a fault in this platform.
func TestUpstreamFailureMapsTo502(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"credentials_rejected": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
		},
		"graphql_error": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"data":{"repository":null},"errors":[{"message":"Could not resolve to a Repository"}]}`))
		},
	}
	for name, upstream := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(upstream)
			defer srv.Close()
			orig := githubGraphQLEndpoint
			githubGraphQLEndpoint = srv.URL
			defer func() { githubGraphQLEndpoint = orig }()

			// A repo name unique to this case keeps the response cache out of it.
			h := &Handlers{CommunityToken: "ghp_x", CommunityRepo: "owner/" + name}
			c, _ := newTestContext(t, "/api/v1/community/discussions")

			var apiErr *apierror.Error
			require.ErrorAs(t, h.ListCommunityDiscussions(c), &apiErr)
			assert.Equal(t, http.StatusBadGateway, apiErr.Status)
			assert.Equal(t, "community_upstream_unavailable", apiErr.Code)
			assert.NotContains(t, apiErr.Message, "Repository",
				"the anonymous response must not leak upstream detail")
			assert.NotContains(t, apiErr.Message, "credentials")
		})
	}
}

func TestTransformDiscussionsMapsFields(t *testing.T) {
	raw := []byte(`{"data":{"repository":{"discussions":{"nodes":[
	  {"id":"D_1","number":7,"title":"Hoe werkt dit?","url":"https://github.com/o/r/discussions/7",
	   "createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-02T00:00:00Z",
	   "bodyText":"Korte tekst","upvoteCount":3,
	   "author":{"login":"maya","avatarUrl":"https://avatars/1"},
	   "category":{"name":"Vragen"},"comments":{"totalCount":4}}
	]}}}}`)

	out, err := transformDiscussions(raw, "")
	require.NoError(t, err)

	var body struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(t, json.Unmarshal(out, &body))
	require.Len(t, body.Items, 1)

	item := body.Items[0]
	assert.Equal(t, "D_1", item["id"])
	assert.Equal(t, "Hoe werkt dit?", item["title"])
	assert.Equal(t, "maya", item["author"])
	assert.Equal(t, "Vragen", item["category"])
	assert.Equal(t, float64(4), item["comment_count"])
	assert.Equal(t, float64(3), item["upvote_count"])
	assert.Equal(t, "Korte tekst", item["excerpt"])
}

// TestTransformDiscussionsFiltersByCategory covers the filter the doc comment
// promised but the code never applied: `category` was accepted, folded into the
// cache key, and then ignored.
func TestTransformDiscussionsFiltersByCategory(t *testing.T) {
	raw := []byte(`{"data":{"repository":{"discussions":{"nodes":[
	  {"id":"a","title":"one","category":{"name":"Vragen"}},
	  {"id":"b","title":"two","category":{"name":"Aankondigingen"}},
	  {"id":"c","title":"three","category":{"name":"Vragen"}}
	]}}}}`)

	cases := []struct {
		name    string
		filter  string
		wantIDs []string
	}{
		{"no filter keeps everything", "", []string{"a", "b", "c"}},
		{"exact match", "Vragen", []string{"a", "c"}},
		{"case-insensitive", "vragen", []string{"a", "c"}},
		{"whitespace tolerant", "  Vragen  ", []string{"a", "c"}},
		{"other category", "Aankondigingen", []string{"b"}},
		{"unknown category yields nothing", "Nietbestaand", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := transformDiscussions(raw, tc.filter)
			require.NoError(t, err)

			var body struct {
				Items []struct {
					ID string `json:"id"`
				} `json:"items"`
			}
			require.NoError(t, json.Unmarshal(out, &body))

			got := make([]string, 0, len(body.Items))
			for _, i := range body.Items {
				got = append(got, i.ID)
			}
			if tc.wantIDs == nil {
				assert.Empty(t, got)
				return
			}
			assert.Equal(t, tc.wantIDs, got)
		})
	}
}

// TestTransformDiscussionsAlwaysEmitsAnArray — a nil slice would marshal to
// `null` and break `data.items ?? []` on the client.
func TestTransformDiscussionsAlwaysEmitsAnArray(t *testing.T) {
	out, err := transformDiscussions([]byte(`{"data":{"repository":{"discussions":{"nodes":[]}}}}`), "")
	require.NoError(t, err)
	assert.Contains(t, string(out), `"items":[]`)
}

func TestTruncateRunesDoesNotSplitMultibyteCharacters(t *testing.T) {
	// 300 'é' is 600 bytes: a byte-slice at 300 would land mid-rune.
	body := strings.Repeat("é", 400)

	got := truncateRunes(body, 300)

	require.True(t, utf8.ValidString(got), "must stay valid UTF-8")
	assert.Equal(t, 301, len([]rune(got)), "300 runes plus the ellipsis")
	assert.True(t, strings.HasSuffix(got, "…"))
}

func TestTruncateRunesLeavesShortTextAlone(t *testing.T) {
	assert.Equal(t, "kort", truncateRunes("kort", 300))
	assert.Equal(t, "", truncateRunes("", 300))

	exact := strings.Repeat("a", 300)
	assert.Equal(t, exact, truncateRunes(exact, 300), "exactly at the limit is not truncated")
}

func TestBuildDiscussionsQueryEscapesOwnerAndRepo(t *testing.T) {
	q := buildDiscussionsQuery(`o"wner`, `re"po`, 20)

	// %q quoting keeps an injected quote from breaking out of the GraphQL string.
	assert.Contains(t, q, `\"`)
	assert.NotContains(t, q, `owner: "o"wner"`)
	assert.Contains(t, q, "first: 20")
}

func TestParseIntParamAndClamp(t *testing.T) {
	c, _ := newTestContext(t, "/x?page_size=35")
	assert.Equal(t, 35, parseIntParam(c, "page_size", 20))

	c, _ = newTestContext(t, "/x?page_size=abc")
	assert.Equal(t, 20, parseIntParam(c, "page_size", 20), "non-numeric falls back")

	c, _ = newTestContext(t, "/x")
	assert.Equal(t, 20, parseIntParam(c, "page_size", 20), "absent falls back")

	assert.Equal(t, 50, clampInt(999, 1, 50))
	assert.Equal(t, 1, clampInt(0, 1, 50))
	assert.Equal(t, 25, clampInt(25, 1, 50))
}
