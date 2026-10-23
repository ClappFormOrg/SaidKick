// Package argocd is the V1 implementation of orchestrator.Deployer
// backed by an Argo CD REST API. Calls a single Argo CD endpoint
// (typically the hub's argocd-server) with a stored service-account
// token; every spoke cluster is expected to be registered with that
// Argo CD via `argocd cluster add`.
//
// See the parent package doc for the ADR-4 caveat: this direct-API
// approach is the V1 spike. The agent-brokered impl (which keeps the
// hub from holding kubeconfigs) lands once the sAId-Kick agent exists,
// and implements the same orchestrator.Deployer interface, so this
// file is the only thing that changes.
package argocd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/orchestrator"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/vault"
	"github.com/google/uuid"
)

// Config holds the connection details for the target Argo CD.
type Config struct {
	// ServerURL is the base URL of the Argo CD API, e.g.
	// "https://argocd.appstore.local". No trailing slash.
	ServerURL string

	// AuthToken is a JWT issued by `argocd account generate-token` (or
	// pulled from the argocd-secret). Sent as the fallback bearer token
	// when no per-cluster token is found in TokenStore.
	// TODO(I2): upgrade to Vault PKI-rotated mTLS client cert once the
	// agent handshake is complete (deferred post-V1, see ADR-D-007).
	AuthToken string

	// TokenStore is an optional Vault KV client used to look up
	// per-cluster Argo CD tokens stored at
	// kv/data/orchestrator/<clusterID>/argocd_token. When the path is
	// absent or TokenStore is nil, AuthToken is used instead.
	TokenStore vault.KVClient

	// NamespaceForApps is the Argo CD installation's namespace, used
	// as the Application.metadata.namespace. Defaults to "argocd".
	NamespaceForApps string

	// HTTPClient lets callers inject a custom client (testing,
	// per-environment TLS config). nil means &http.Client{Timeout: 30s}.
	HTTPClient *http.Client

	// InsecureSkipVerify allows self-signed Argo CD certs in dev. NEVER
	// set this in production. The .env-driven config refuses to set it
	// when APP_ENV=production.
	InsecureSkipVerify bool
}

// Deployer is the Argo CD REST implementation of
// orchestrator.Deployer. Construct via New().
type Deployer struct {
	cfg    Config
	client *http.Client
}

var _ orchestrator.Deployer = (*Deployer)(nil)

// New constructs an Argo CD-backed Deployer.
func New(cfg Config) (*Deployer, error) {
	if strings.TrimSpace(cfg.ServerURL) == "" {
		return nil, errors.New("argocd: server_url is required")
	}
	if strings.TrimSpace(cfg.AuthToken) == "" {
		return nil, errors.New("argocd: auth_token is required")
	}
	if cfg.NamespaceForApps == "" {
		cfg.NamespaceForApps = "argocd"
	}
	cfg.ServerURL = strings.TrimRight(cfg.ServerURL, "/")
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Deployer{cfg: cfg, client: client}, nil
}

// tokenFor returns the Argo CD bearer token to use for a given cluster.
// Tries TokenStore first; falls back to the static AuthToken on ErrNotFound,
// ErrNotConfigured, or a nil TokenStore.
func (d *Deployer) tokenFor(ctx context.Context, clusterID uuid.UUID) string {
	if d.cfg.TokenStore != nil && clusterID != uuid.Nil {
		path := fmt.Sprintf("orchestrator/%s/argocd_token", clusterID)
		if t, err := d.cfg.TokenStore.Get(ctx, path); err == nil && t != "" {
			return t
		}
	}
	return d.cfg.AuthToken
}

// Apply creates or updates an Argo CD Application via the REST API.
// Argo CD's upsert flag means re-applying with the same name is safe.
func (d *Deployer) Apply(ctx context.Context, spec orchestrator.ApplicationSpec) (orchestrator.DeploymentRef, error) {
	appName := applicationName(spec.DeploymentID)

	project := spec.Project
	if project == "" {
		project = "default"
	}

	body, err := buildApplicationManifest(spec, appName, d.cfg.NamespaceForApps, project)
	if err != nil {
		return orchestrator.DeploymentRef{}, fmt.Errorf("argocd: build application: %w", err)
	}

	// Argo CD's REST: POST /api/v1/applications with ?upsert=true
	// creates-or-updates atomically.
	url := d.cfg.ServerURL + "/api/v1/applications?upsert=true&validate=false"
	token := d.tokenFor(ctx, spec.ClusterID)
	if err := d.do(ctx, token, http.MethodPost, url, body, nil); err != nil {
		return orchestrator.DeploymentRef{}, fmt.Errorf("argocd: apply: %w", err)
	}

	return orchestrator.DeploymentRef{
		ApplicationName:       appName,
		OrchestratorNamespace: d.cfg.NamespaceForApps,
		ClusterID:             spec.ClusterID,
	}, nil
}

// Status maps Argo CD's sync + health status to our normalised Phase.
func (d *Deployer) Status(ctx context.Context, ref orchestrator.DeploymentRef) (orchestrator.DeploymentStatus, error) {
	url := fmt.Sprintf("%s/api/v1/applications/%s", d.cfg.ServerURL, ref.ApplicationName)
	token := d.tokenFor(ctx, ref.ClusterID)

	var raw argoApplicationResponse
	if err := d.do(ctx, token, http.MethodGet, url, nil, &raw); err != nil {
		if errors.Is(err, orchestrator.ErrApplicationNotFound) {
			return orchestrator.DeploymentStatus{}, orchestrator.ErrApplicationNotFound
		}
		return orchestrator.DeploymentStatus{}, fmt.Errorf("argocd: status: %w", err)
	}

	return mapStatus(raw), nil
}

// Delete tears down an Argo CD Application with cascade=true so child
// resources (pods, services, ingress, secrets) are also removed.
func (d *Deployer) Delete(ctx context.Context, ref orchestrator.DeploymentRef) error {
	url := fmt.Sprintf("%s/api/v1/applications/%s?cascade=true", d.cfg.ServerURL, ref.ApplicationName)
	token := d.tokenFor(ctx, ref.ClusterID)
	if err := d.do(ctx, token, http.MethodDelete, url, nil, nil); err != nil {
		if errors.Is(err, orchestrator.ErrApplicationNotFound) {
			return nil // already gone — treat as success
		}
		return fmt.Errorf("argocd: delete: %w", err)
	}
	return nil
}

// Rollback patches the Application's targetRevision to toRevision and
// triggers a sync. Argo CD's revisionHistoryLimit (we configure 10)
// keeps the rollback target reachable.
func (d *Deployer) Rollback(ctx context.Context, ref orchestrator.DeploymentRef, toRevision string) error {
	token := d.tokenFor(ctx, ref.ClusterID)
	patchBody := map[string]any{
		"spec": map[string]any{
			"source": map[string]any{
				"targetRevision": toRevision,
			},
		},
	}
	body, err := json.Marshal(patchBody)
	if err != nil {
		return fmt.Errorf("argocd: marshal rollback patch: %w", err)
	}
	url := fmt.Sprintf("%s/api/v1/applications/%s/spec", d.cfg.ServerURL, ref.ApplicationName)
	if err := d.do(ctx, token, http.MethodPut, url, body, nil); err != nil {
		return fmt.Errorf("argocd: rollback patch: %w", err)
	}

	syncURL := fmt.Sprintf("%s/api/v1/applications/%s/sync", d.cfg.ServerURL, ref.ApplicationName)
	syncBody, err := json.Marshal(map[string]any{
		"revision": toRevision,
		"prune":    true,
	})
	if err != nil {
		return fmt.Errorf("argocd: marshal sync body: %w", err)
	}
	if err := d.do(ctx, token, http.MethodPost, syncURL, syncBody, nil); err != nil {
		return fmt.Errorf("argocd: rollback sync: %w", err)
	}
	return nil
}

// do is the shared HTTP plumbing: auth header, error mapping for
// 401/403/404, JSON body marshal/unmarshal. token is the bearer token to use.
func (d *Deployer) do(ctx context.Context, token, method, url string, body []byte, out any) error {
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", orchestrator.ErrTransient, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent:
		if out != nil {
			respBody, rErr := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10 MiB cap
			if rErr != nil {
				return fmt.Errorf("read response: %w", rErr)
			}
			if len(respBody) > 0 {
				if dErr := json.Unmarshal(respBody, out); dErr != nil {
					return fmt.Errorf("decode response: %w", dErr)
				}
			}
		}
		return nil
	case http.StatusNotFound:
		return orchestrator.ErrApplicationNotFound
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("argocd: auth rejected (status %d) — check ARGOCD_AUTH_TOKEN", resp.StatusCode)
	default:
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("argocd: unexpected status %d: %q", resp.StatusCode, string(respBody))
	}
}

// applicationName derives the Argo CD application name from the
// deployment id. Format: "app-<first 12 hex chars of UUID>".
func applicationName(deploymentID fmt.Stringer) string {
	raw := strings.ReplaceAll(deploymentID.String(), "-", "")
	if len(raw) > 12 {
		raw = raw[:12]
	}
	return "app-" + raw
}

// buildApplicationManifest produces the JSON body for the Argo CD
// REST POST /api/v1/applications call. We use the v1alpha1 Application
// schema — same shape `argocd app create` writes.
func buildApplicationManifest(spec orchestrator.ApplicationSpec, name, ns, project string) ([]byte, error) {
	// Inline user-supplied values as a YAML string. Argo CD's
	// source.helm.values expects a YAML scalar, not a JSON object.
	// For V1 we pass our JSON through as-is because Helm parses both;
	// Sprint 2 swaps to a json.RawMessage→yaml.v3 conversion if any
	// chart turns out to be strict.
	valuesYAML := ""
	if len(spec.Values) > 0 {
		valuesYAML = string(spec.Values)
	}

	// Translate SecretRefs into Helm parameter overrides that
	// reference Kubernetes Secret keys mounted by the in-cluster
	// ExternalSecrets Operator. For V0 we just include them as
	// annotations on the Application — actual ESO wiring is a
	// follow-up handler concern and the Sprint 1 spike's job.
	annotations := map[string]string{
		"saidkick.appstore.gov.nl/deployment-id": spec.DeploymentID.String(),
		"saidkick.appstore.gov.nl/app-slug":      spec.AppSlug,
	}
	for _, ref := range spec.SecretRefs {
		annotations["saidkick.appstore.gov.nl/secret-"+sanitizeFieldName(ref.FieldPath)] = ref.VaultPath
	}

	app := map[string]any{
		"metadata": map[string]any{
			"name":        name,
			"namespace":   ns,
			"annotations": annotations,
		},
		"spec": map[string]any{
			"project": project,
			"source": map[string]any{
				"repoURL":        spec.GitHubRepoURL,
				"targetRevision": spec.GitSHA,
				"path":           spec.ChartPath,
				"helm": map[string]any{
					"values":      valuesYAML,
					"releaseName": name,
				},
			},
			"destination": map[string]any{
				"name":      spec.ClusterName,
				"namespace": spec.Namespace,
			},
			"syncPolicy": map[string]any{
				"automated": map[string]any{
					"prune":    false,
					"selfHeal": false,
				},
				"syncOptions": []string{
					"CreateNamespace=true",
				},
			},
			"revisionHistoryLimit": 10,
		},
	}
	return json.Marshal(app)
}

// argoApplicationResponse is a partial decode of Argo CD's Application
// resource — only the fields we map into DeploymentStatus.
type argoApplicationResponse struct {
	Status struct {
		Sync struct {
			Status   string `json:"status"`   // "Synced", "OutOfSync", "Unknown"
			Revision string `json:"revision"` // current SHA
		} `json:"sync"`
		Health struct {
			Status  string `json:"status"` // "Healthy", "Progressing", "Degraded", "Suspended", "Missing", "Unknown"
			Message string `json:"message"`
		} `json:"health"`
		OperationState struct {
			Phase      string    `json:"phase"`
			FinishedAt time.Time `json:"finishedAt"`
		} `json:"operationState"`
	} `json:"status"`
}

func mapStatus(r argoApplicationResponse) orchestrator.DeploymentStatus {
	out := orchestrator.DeploymentStatus{
		LastSyncedAt: r.Status.OperationState.FinishedAt,
		Revision:     r.Status.Sync.Revision,
		Message:      r.Status.Health.Message,
	}

	switch r.Status.Health.Status {
	case "Healthy":
		out.Healthy = true
	case "Degraded":
		out.Phase = orchestrator.PhaseDegraded
	}

	// Phase derivation: combine sync + health + operation state.
	switch {
	case r.Status.OperationState.Phase == "Running":
		out.Phase = orchestrator.PhaseApplying
	case r.Status.Sync.Status == "OutOfSync":
		out.Phase = orchestrator.PhaseReconciling
	case r.Status.Sync.Status == "Synced" && r.Status.Health.Status == "Healthy":
		out.Phase = orchestrator.PhaseReady
	case r.Status.Sync.Status == "Synced" && r.Status.Health.Status == "Progressing":
		out.Phase = orchestrator.PhaseReconciling
	case r.Status.Health.Status == "Degraded":
		out.Phase = orchestrator.PhaseDegraded
	case r.Status.Sync.Status == "" && r.Status.Health.Status == "":
		out.Phase = orchestrator.PhasePending
	case r.Status.Health.Status == "Missing":
		out.Phase = orchestrator.PhaseFailed
	default:
		out.Phase = orchestrator.PhasePending
	}

	return out
}

// sanitizeFieldName scrubs a Helm values path for use in a Kubernetes
// annotation key. K8s rejects characters outside [a-z0-9._-].
func sanitizeFieldName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}
