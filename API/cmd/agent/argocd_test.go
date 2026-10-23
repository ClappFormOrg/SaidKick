package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/orchestrator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeArgoApps serves one Application the way the Argo CD API treats spec
// writes: PUT .../spec replaces the whole ApplicationSpec and refuses one
// without a destination server, PATCH applies a JSON merge patch.
type fakeArgoApps struct {
	mu        sync.Mutex
	name      string
	spec      map[string]any
	syncedRev string
}

func newFakeArgoApps(name string) *fakeArgoApps {
	return &fakeArgoApps{name: name, spec: map[string]any{
		"project": "default",
		"source": map[string]any{
			"repoURL":        "https://github.com/example/app",
			"path":           "chart",
			"targetRevision": "v2",
			"helm":           map[string]any{"values": `{"replicaCount":2}`},
		},
		"destination": map[string]any{"server": "https://kubernetes.default.svc", "namespace": "apps"},
	}}
}

// mergePatch applies an RFC 7386 JSON merge patch to dst.
func mergePatch(dst, patch map[string]any) {
	for k, v := range patch {
		pv, isObj := v.(map[string]any)
		dv, dstObj := dst[k].(map[string]any)
		switch {
		case v == nil:
			delete(dst, k)
		case isObj && dstObj:
			mergePatch(dv, pv)
		default:
			dst[k] = v
		}
	}
}

func (f *fakeArgoApps) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	base := "/api/v1/applications/" + f.name
	fail := func(msg string) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, `{"error":%q,"code":3,"message":%q}`, msg, msg)
	}
	switch {
	case r.Method == http.MethodPut && r.URL.Path == base+"/spec":
		var spec map[string]any
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			fail(err.Error())
			return
		}
		if dest, _ := spec["destination"].(map[string]any); dest == nil || dest["server"] == nil {
			fail(fmt.Sprintf("application destination spec for %s is invalid: Destination server missing from app spec", f.name))
			return
		}
		f.spec = spec
	case r.Method == http.MethodPatch && r.URL.Path == base:
		var req struct {
			Name      string `json:"name"`
			Patch     string `json:"patch"`
			PatchType string `json:"patchType"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.PatchType != "merge" || req.Name != f.name {
			fail("unsupported patch request")
			return
		}
		var patch struct {
			Spec map[string]any `json:"spec"`
		}
		if err := json.Unmarshal([]byte(req.Patch), &patch); err != nil {
			fail(err.Error())
			return
		}
		mergePatch(f.spec, patch.Spec)
	case r.Method == http.MethodPost && r.URL.Path == base+"/sync":
		var req struct {
			Revision string `json:"revision"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.syncedRev = req.Revision
	default:
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write([]byte(`{}`))
}

func TestArgoRollback_KeepsTheRestOfTheSpec(t *testing.T) {
	t.Parallel()
	fake := newFakeArgoApps("app-0190c3a40000")
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	client := newArgoClient(srv.URL, "token", srv.Client())
	require.NoError(t, client.Rollback(t.Context(), "app-0190c3a40000", "v1"))

	fake.mu.Lock()
	defer fake.mu.Unlock()
	source := fake.spec["source"].(map[string]any)
	assert.Equal(t, "v1", source["targetRevision"])
	assert.Equal(t, "https://github.com/example/app", source["repoURL"])
	assert.Equal(t, map[string]any{"values": `{"replicaCount":2}`}, source["helm"])
	assert.Equal(t, map[string]any{"server": "https://kubernetes.default.svc", "namespace": "apps"}, fake.spec["destination"])
	assert.Equal(t, "v1", fake.syncedRev)
}

// TestFakeArgoApps_RejectsPartialSpec pins the fake to the behaviour seen on
// Argo CD v3.4.9: a revision-only body on PUT .../spec is refused.
func TestFakeArgoApps_RejectsPartialSpec(t *testing.T) {
	t.Parallel()
	fake := newFakeArgoApps("app-1")
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	body := strings.NewReader(`{"spec":{"source":{"targetRevision":"v1"}}}`)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, srv.URL+"/api/v1/applications/app-1/spec", body)
	require.NoError(t, err)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// testApp builds an Application whose spec and last comparison point at the
// given revisions.
func testApp(specRev, comparedRev, syncStatus, health, operation string) argoApp {
	var a argoApp
	a.Spec.Source.TargetRevision = specRev
	a.Spec.Source.Helm.Values = `{"replicaCount":1}`
	a.Status.Sync.ComparedTo.Source.TargetRevision = comparedRev
	a.Status.Sync.ComparedTo.Source.Helm.Values = `{"replicaCount":1}`
	a.Status.Sync.Status = syncStatus
	a.Status.Health.Status = health
	a.Status.OperationState.Phase = operation
	return a
}

func TestMapToPhase(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		app  argoApp
		want orchestrator.Phase
	}{
		{"synced and healthy", testApp("v2", "v2", "Synced", "Healthy", "Succeeded"), orchestrator.PhaseReady},
		{"synced and progressing", testApp("v2", "v2", "Synced", "Progressing", "Succeeded"), orchestrator.PhaseReconciling},
		{"out of sync", testApp("v2", "v2", "OutOfSync", "Healthy", "Succeeded"), orchestrator.PhaseReconciling},
		{"sync running", testApp("v2", "v2", "OutOfSync", "Progressing", "Running"), orchestrator.PhaseApplying},
		{"degraded", testApp("v2", "v2", "Synced", "Degraded", "Succeeded"), orchestrator.PhaseDegraded},
		// Right after a rollback or upgrade, Argo CD still serves the status
		// it computed for the previous revision.
		{"stale healthy status after a revision change", testApp("v1", "v2", "Synced", "Healthy", "Succeeded"), orchestrator.PhaseReconciling},
		{"stale degraded status after a revision change", testApp("v1", "v2", "Synced", "Degraded", "Succeeded"), orchestrator.PhaseReconciling},
		{"new app not compared yet", testApp("v1", "", "", "", ""), orchestrator.PhaseReconciling},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, mapToPhase(tc.app))
		})
	}

	t.Run("stale status after a values change", func(t *testing.T) {
		t.Parallel()
		app := testApp("v2", "v2", "Synced", "Healthy", "Succeeded")
		app.Spec.Source.Helm.Values = `{"replicaCount":2}`
		assert.Equal(t, orchestrator.PhaseReconciling, mapToPhase(app))
	})
}
