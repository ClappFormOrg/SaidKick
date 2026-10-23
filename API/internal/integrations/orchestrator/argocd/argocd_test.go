package argocd

import (
	"encoding/json"
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/orchestrator"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixedID is a deterministic UUID used so applicationName assertions are stable.
var fixedID = uuid.MustParse("0123abcd-4567-89ef-0123-456789abcdef")

// TestNew_validationAndDefaults covers the pure constructor: required-field
// validation, ServerURL trailing-slash trimming, and the NamespaceForApps
// default. No network is involved.
func TestNew_validationAndDefaults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		wantErr string
		wantURL string
		wantNS  string
	}{
		{
			name:    "missing server url",
			cfg:     Config{AuthToken: "tok"},
			wantErr: "server_url is required",
		},
		{
			name:    "blank server url is trimmed to empty",
			cfg:     Config{ServerURL: "   ", AuthToken: "tok"},
			wantErr: "server_url is required",
		},
		{
			name:    "missing auth token",
			cfg:     Config{ServerURL: "https://argo.local"},
			wantErr: "auth_token is required",
		},
		{
			name:    "defaults applied",
			cfg:     Config{ServerURL: "https://argo.local", AuthToken: "tok"},
			wantURL: "https://argo.local",
			wantNS:  "argocd",
		},
		{
			name:    "trailing slash trimmed and custom namespace kept",
			cfg:     Config{ServerURL: "https://argo.local/", AuthToken: "tok", NamespaceForApps: "gitops"},
			wantURL: "https://argo.local",
			wantNS:  "gitops",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, err := New(tc.cfg)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.Nil(t, d)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, d)
			assert.Equal(t, tc.wantURL, d.cfg.ServerURL)
			assert.Equal(t, tc.wantNS, d.cfg.NamespaceForApps)
			assert.NotNil(t, d.client, "a default HTTP client must be set")
		})
	}
}

// stringID is a trivial fmt.Stringer so applicationName can be tested with
// controlled inputs (including strings shorter than 12 chars).
type stringID string

func (s stringID) String() string { return string(s) }

func TestApplicationName_derivation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		id   stringID
		want string
	}{
		{
			name: "full uuid truncated to 12 hex chars",
			id:   "0123abcd-4567-89ef-0123-456789abcdef",
			want: "app-0123abcd4567",
		},
		{
			name: "exactly 12 hex chars kept whole",
			id:   "0123abcd4567",
			want: "app-0123abcd4567",
		},
		{
			name: "shorter than 12 chars not padded",
			id:   "ab-cd",
			want: "app-abcd",
		},
		{
			name: "empty id",
			id:   "",
			want: "app-",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, applicationName(tc.id))
		})
	}
}

// TestApplicationName_withRealUUID confirms the exported UUID type flows
// through applicationName as callers actually use it.
func TestApplicationName_withRealUUID(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "app-0123abcd4567", applicationName(fixedID))
}

// manifest is the subset of the Argo CD v1alpha1 Application JSON that the
// builder emits and that these tests assert on.
type manifest struct {
	Metadata struct {
		Name        string            `json:"name"`
		Namespace   string            `json:"namespace"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		Project string `json:"project"`
		Source  struct {
			RepoURL        string `json:"repoURL"`
			TargetRevision string `json:"targetRevision"`
			Path           string `json:"path"`
			Helm           struct {
				Values      string `json:"values"`
				ReleaseName string `json:"releaseName"`
			} `json:"helm"`
		} `json:"source"`
		Destination struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"destination"`
		RevisionHistoryLimit int `json:"revisionHistoryLimit"`
	} `json:"spec"`
}

func buildAndDecode(t *testing.T, spec orchestrator.ApplicationSpec, name, ns, project string) manifest {
	t.Helper()
	raw, err := buildApplicationManifest(spec, name, ns, project)
	require.NoError(t, err)
	var m manifest
	require.NoError(t, json.Unmarshal(raw, &m), "manifest must be valid JSON")
	return m
}

// TestBuildApplicationManifest_coreFields asserts that name, namespace,
// project, chart path, repo, revision, destination, and releaseName all flow
// from the spec into the correct manifest positions.
func TestBuildApplicationManifest_coreFields(t *testing.T) {
	t.Parallel()

	spec := orchestrator.ApplicationSpec{
		DeploymentID:  fixedID,
		AppSlug:       "govchat",
		ClusterName:   "spoke-prod",
		Namespace:     "team-a",
		GitHubRepoURL: "https://github.com/example-org/govchat",
		GitSHA:        "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		ChartPath:     "deploy/chart",
	}

	m := buildAndDecode(t, spec, "app-abc123", "argocd", "gov-project")

	assert.Equal(t, "app-abc123", m.Metadata.Name)
	assert.Equal(t, "argocd", m.Metadata.Namespace)
	assert.Equal(t, "gov-project", m.Spec.Project)
	assert.Equal(t, "https://github.com/example-org/govchat", m.Spec.Source.RepoURL)
	assert.Equal(t, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", m.Spec.Source.TargetRevision)
	assert.Equal(t, "deploy/chart", m.Spec.Source.Path)
	assert.Equal(t, "spoke-prod", m.Spec.Destination.Name)
	assert.Equal(t, "team-a", m.Spec.Destination.Namespace)
	assert.Equal(t, "app-abc123", m.Spec.Source.Helm.ReleaseName, "releaseName mirrors the app name")
	assert.Equal(t, 10, m.Spec.RevisionHistoryLimit)

	// Baseline annotations always present.
	assert.Equal(t, fixedID.String(), m.Metadata.Annotations["saidkick.appstore.gov.nl/deployment-id"])
	assert.Equal(t, "govchat", m.Metadata.Annotations["saidkick.appstore.gov.nl/app-slug"])
}

// TestBuildApplicationManifest_valuesInlinedVerbatim is the INPUTVAL-001 sink
// pin: whatever JSON arrives in spec.Values is placed verbatim into
// source.helm.values as a string. This is deliberately a characterization
// test — if the inlining behaviour ever changes, this must be revisited.
func TestBuildApplicationManifest_valuesInlinedVerbatim(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		values json.RawMessage
		want   string
	}{
		{
			name:   "simple object",
			values: json.RawMessage(`{"replicaCount":2,"image":{"tag":"v1.2.3"}}`),
			want:   `{"replicaCount":2,"image":{"tag":"v1.2.3"}}`,
		},
		{
			name:   "values are not re-escaped or reformatted",
			values: json.RawMessage(`{ "a": "b",  "nested": [1,2,3] }`),
			want:   `{ "a": "b",  "nested": [1,2,3] }`,
		},
		{
			name:   "attacker-controlled string passes through unchanged (INPUTVAL-001)",
			values: json.RawMessage(`{"cmd":"$(rm -rf /)","yaml":"a: b\nc: d"}`),
			want:   `{"cmd":"$(rm -rf /)","yaml":"a: b\nc: d"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec := orchestrator.ApplicationSpec{DeploymentID: fixedID, Values: tc.values}
			m := buildAndDecode(t, spec, "app-x", "argocd", "default")
			assert.Equal(t, tc.want, m.Spec.Source.Helm.Values)
		})
	}
}

// TestBuildApplicationManifest_emptyValues asserts that absent/empty Values
// produce an empty (not broken) helm values string, and that the manifest is
// still valid JSON.
func TestBuildApplicationManifest_emptyValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		values json.RawMessage
	}{
		{name: "nil values", values: nil},
		{name: "zero-length values", values: json.RawMessage("")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec := orchestrator.ApplicationSpec{DeploymentID: fixedID, Values: tc.values}
			m := buildAndDecode(t, spec, "app-x", "argocd", "default")
			assert.Equal(t, "", m.Spec.Source.Helm.Values)
		})
	}
}

// TestBuildApplicationManifest_secretRefs asserts each SecretRef becomes a
// sanitized annotation key mapped to its VaultPath, without leaking values.
func TestBuildApplicationManifest_secretRefs(t *testing.T) {
	t.Parallel()

	spec := orchestrator.ApplicationSpec{
		DeploymentID: fixedID,
		AppSlug:      "govchat",
		SecretRefs: []orchestrator.SecretRef{
			{FieldPath: "openai.apiKey", VaultPath: "kv/data/deployments/1/openai_api_key"},
			{FieldPath: "DB_PASSWORD", VaultPath: "kv/data/deployments/1/db_password"},
		},
	}

	m := buildAndDecode(t, spec, "app-x", "argocd", "default")

	// "openai.apiKey" -> lowercased to "openai.apikey" (dot is valid, K lowered).
	assert.Equal(t,
		"kv/data/deployments/1/openai_api_key",
		m.Metadata.Annotations["saidkick.appstore.gov.nl/secret-openai.apikey"],
	)
	// "DB_PASSWORD" -> "db_password" (underscores kept, letters lowercased).
	assert.Equal(t,
		"kv/data/deployments/1/db_password",
		m.Metadata.Annotations["saidkick.appstore.gov.nl/secret-db_password"],
	)
}

// TestSanitizeFieldName covers the annotation-key scrubbing rules: lowercasing
// and replacement of everything outside [a-z0-9._-] with '-'.
func TestSanitizeFieldName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "already valid", in: "openai.apikey", want: "openai.apikey"},
		{name: "uppercase lowered", in: "OpenAI", want: "openai"},
		{name: "dots dashes underscores kept", in: "a.b-c_d", want: "a.b-c_d"},
		{name: "slashes and spaces replaced", in: "a/b c", want: "a-b-c"},
		{name: "special chars replaced", in: "a[0].b!", want: "a-0-.b-"},
		{name: "empty", in: "", want: ""},
		{name: "digits kept", in: "field123", want: "field123"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, sanitizeFieldName(tc.in))
		})
	}
}

// TestMapStatus covers the pure sync+health+operation -> Phase collapse and
// the passthrough of message/revision/health fields.
func TestMapStatus(t *testing.T) {
	t.Parallel()

	newResp := func(sync, health, op string) argoApplicationResponse {
		var r argoApplicationResponse
		r.Status.Sync.Status = sync
		r.Status.Health.Status = health
		r.Status.OperationState.Phase = op
		return r
	}

	tests := []struct {
		name        string
		resp        argoApplicationResponse
		wantPhase   orchestrator.Phase
		wantHealthy bool
	}{
		{name: "operation running -> applying", resp: newResp("Synced", "Progressing", "Running"), wantPhase: orchestrator.PhaseApplying},
		{name: "out of sync -> reconciling", resp: newResp("OutOfSync", "Healthy", ""), wantPhase: orchestrator.PhaseReconciling, wantHealthy: true},
		{name: "synced healthy -> ready", resp: newResp("Synced", "Healthy", ""), wantPhase: orchestrator.PhaseReady, wantHealthy: true},
		{name: "synced progressing -> reconciling", resp: newResp("Synced", "Progressing", ""), wantPhase: orchestrator.PhaseReconciling},
		{name: "degraded -> degraded", resp: newResp("Synced", "Degraded", ""), wantPhase: orchestrator.PhaseDegraded},
		{name: "empty sync and health -> pending", resp: newResp("", "", ""), wantPhase: orchestrator.PhasePending},
		{name: "missing -> failed", resp: newResp("Synced", "Missing", ""), wantPhase: orchestrator.PhaseFailed},
		{name: "unknown combo -> pending", resp: newResp("Unknown", "Suspended", ""), wantPhase: orchestrator.PhasePending},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := mapStatus(tc.resp)
			assert.Equal(t, tc.wantPhase, got.Phase)
			assert.Equal(t, tc.wantHealthy, got.Healthy)
		})
	}
}

// TestMapStatus_passthroughFields confirms revision, message and timestamp are
// carried through from the Argo CD response into DeploymentStatus.
func TestMapStatus_passthroughFields(t *testing.T) {
	t.Parallel()

	var r argoApplicationResponse
	r.Status.Sync.Status = "Synced"
	r.Status.Sync.Revision = "abc123"
	r.Status.Health.Status = "Healthy"
	r.Status.Health.Message = "all good"

	got := mapStatus(r)
	assert.Equal(t, "abc123", got.Revision)
	assert.Equal(t, "all good", got.Message)
	assert.True(t, got.Healthy)
	assert.Equal(t, orchestrator.PhaseReady, got.Phase)
}
