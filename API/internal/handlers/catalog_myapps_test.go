package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/handlers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// countDeploymentQueries counts SELECTs against deployments on f's handle
// from now on.
func countDeploymentQueries(t *testing.T, f launcherFixture) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	require.NoError(t, f.handle.RW.Callback().Query().After("gorm:query").
		Register("test:count_deployment_queries", func(tx *gorm.DB) {
			if tx.Statement.Table == "deployments" {
				n.Add(1)
			}
		}))
	return &n
}

func catalogRequest(f launcherFixture, target string, p *auth.Principal) *httptest.ResponseRecorder {
	req := newRequest(http.MethodGet, target, "")
	if p != nil {
		req = asPrincipal(req, p)
	}
	return serveRoute(f.h, req)
}

func deployedFlags(t *testing.T, rec *httptest.ResponseRecorder) map[string]bool {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Items []handlers.AppCard `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	out := make(map[string]bool, len(body.Items))
	for _, c := range body.Items {
		out[c.Slug] = c.DeployedInMyOrg
	}
	return out
}

// TestCatalogDeployedInMyOrg uses its own fixture because it registers a
// query callback on the handle.
func TestCatalogDeployedInMyOrg(t *testing.T) {
	t.Parallel()
	f := newLauncherFixture(t)
	queries := countDeploymentQueries(t, f)

	t.Run("anonymous_sees_false_without_querying_deployments", func(t *testing.T) {
		before := queries.Load()
		flags := deployedFlags(t, catalogRequest(f, "/api/v1/apps", nil))
		assert.Equal(t, map[string]bool{f.chat.Slug: false, f.plain.Slug: false}, flags)
		assert.Equal(t, before, queries.Load())
	})

	t.Run("org_member_sees_own_org_deployments_with_one_query", func(t *testing.T) {
		before := queries.Load()
		flags := deployedFlags(t, catalogRequest(f, "/api/v1/apps", principal(f.appUser, models.RoleAppUser)))
		assert.Equal(t, map[string]bool{f.chat.Slug: true, f.plain.Slug: true}, flags)
		assert.Equal(t, before+1, queries.Load(), "one query for the whole page")
	})

	t.Run("other_org_only_counts_its_own", func(t *testing.T) {
		other := principal(f.userB, models.RoleAppUser)
		flags := deployedFlags(t, catalogRequest(f, "/api/v1/apps", other))
		assert.Equal(t, map[string]bool{f.chat.Slug: true, f.plain.Slug: false}, flags)
	})
}

func TestGetAppMyDeployments(t *testing.T) {
	t.Parallel()
	f := newLauncherFixture(t)

	type detail struct {
		DeployedInMyOrg bool                       `json:"deployed_in_my_org"`
		MyDeployments   []handlers.MyDeploymentRef `json:"my_deployments"`
	}
	get := func(t *testing.T, p *auth.Principal) (detail, string) {
		t.Helper()
		rec := catalogRequest(f, "/api/v1/apps/"+f.chat.Slug, p)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var d detail
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &d))
		return d, rec.Body.String()
	}

	t.Run("anonymous_gets_an_empty_list", func(t *testing.T) {
		d, raw := get(t, nil)
		assert.False(t, d.DeployedInMyOrg)
		assert.Contains(t, raw, `"my_deployments":[]`)
	})

	t.Run("org_member_gets_live_deployments_with_urls_when_up", func(t *testing.T) {
		d, raw := get(t, principal(f.appUser))
		assert.True(t, d.DeployedInMyOrg)
		byID := make(map[string]handlers.MyDeploymentRef, len(d.MyDeployments))
		for _, r := range d.MyDeployments {
			byID[r.DeploymentID] = r
		}
		assert.Len(t, byID, 4, "ready, degraded, applying and failed; not deleting, deleted or org B's")
		assert.Equal(t, "https://chat.org-a.example.nl", byID[f.ready.ID.String()].URL)
		assert.Equal(t, "http://chat-test.org-a.example.nl", byID[f.degradedHTTP.ID.String()].URL)
		assert.Empty(t, byID[f.applying.ID.String()].URL)
		assert.Equal(t, handlers.AvailabilityPreparing, byID[f.applying.ID.String()].Availability)
		assert.Empty(t, byID[f.failed.ID.String()].URL)
		assert.NotContains(t, raw, "replicas")
	})

	t.Run("org_without_deployments_gets_an_empty_list", func(t *testing.T) {
		rec := catalogRequest(f, "/api/v1/apps/"+f.plain.Slug, principal(f.userB))
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"my_deployments":[]`)
		assert.Contains(t, rec.Body.String(), `"deployed_in_my_org":false`)
	})
}
