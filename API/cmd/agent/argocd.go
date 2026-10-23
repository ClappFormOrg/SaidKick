package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/orchestrator"
)

const (
	defaultHTTPTimeout = 30 * time.Second
)

// argoClient is a minimal Argo CD REST client for the agent. It talks to
// the local Argo CD installation (not the hub's), using the same API paths
// as the hub's argocd deployer.
type argoClient struct {
	serverURL string
	token     string
	client    *http.Client
}

func newArgoClient(serverURL, token string, client *http.Client) *argoClient {
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return &argoClient{
		serverURL: strings.TrimRight(serverURL, "/"),
		token:     token,
		client:    client,
	}
}

// Apply creates or updates an Argo CD Application on the local cluster.
func (a *argoClient) Apply(ctx context.Context, spec orchestrator.ApplicationSpec, appName, ns string) error {
	project := spec.Project
	if project == "" {
		project = "default"
	}

	valuesYAML := ""
	if len(spec.Values) > 0 {
		valuesYAML = string(spec.Values)
	}

	annotations := map[string]string{
		"saidkick.appstore.gov.nl/deployment-id": spec.DeploymentID.String(),
		"saidkick.appstore.gov.nl/app-slug":      spec.AppSlug,
	}
	for _, ref := range spec.SecretRefs {
		annotations["saidkick.appstore.gov.nl/secret-"+sanitizeFieldName(ref.FieldPath)] = ref.VaultPath
	}

	app := map[string]any{
		"metadata": map[string]any{
			"name":        appName,
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
					"releaseName": appName,
				},
			},
			"destination": map[string]any{
				"server":    "https://kubernetes.default.svc",
				"namespace": spec.Namespace,
			},
			"syncPolicy": map[string]any{
				"automated": map[string]any{
					"prune":    false,
					"selfHeal": false,
				},
				"syncOptions": []string{"CreateNamespace=true"},
			},
			"revisionHistoryLimit": 10,
		},
	}
	body, err := json.Marshal(app)
	if err != nil {
		return fmt.Errorf("marshal application: %w", err)
	}
	endpoint := a.serverURL + "/api/v1/applications?upsert=true&validate=false"
	return a.do(ctx, http.MethodPost, endpoint, body, nil)
}

// Delete removes an Argo CD Application (cascade=true).
func (a *argoClient) Delete(ctx context.Context, appName string) error {
	endpoint := fmt.Sprintf("%s/api/v1/applications/%s?cascade=true", a.serverURL, url.PathEscape(appName))
	err := a.do(ctx, http.MethodDelete, endpoint, nil, nil)
	if isNotFound(err) {
		return nil // already gone — success
	}
	return err
}

// Rollback sets spec.source.targetRevision and triggers a sync.
//
// It uses PATCH /api/v1/applications/<name> with a JSON merge patch, which
// changes only the revision. PUT .../spec replaces the whole ApplicationSpec,
// so a body carrying just the revision loses the destination and Argo CD
// refuses it ("Destination server missing from app spec").
func (a *argoClient) Rollback(ctx context.Context, appName, toRevision string) error {
	escaped := url.PathEscape(appName)
	patch, err := json.Marshal(map[string]any{
		"spec": map[string]any{
			"source": map[string]any{"targetRevision": toRevision},
		},
	})
	if err != nil {
		return fmt.Errorf("marshal rollback patch: %w", err)
	}
	body, err := json.Marshal(map[string]any{
		"name":      appName,
		"patch":     string(patch),
		"patchType": "merge",
	})
	if err != nil {
		return fmt.Errorf("marshal rollback patch request: %w", err)
	}
	if err := a.do(ctx, http.MethodPatch,
		fmt.Sprintf("%s/api/v1/applications/%s", a.serverURL, escaped),
		body, nil); err != nil {
		return fmt.Errorf("rollback patch: %w", err)
	}
	syncBody, _ := json.Marshal(map[string]any{"revision": toRevision, "prune": true})
	if err := a.do(ctx, http.MethodPost,
		fmt.Sprintf("%s/api/v1/applications/%s/sync", a.serverURL, escaped),
		syncBody, nil); err != nil {
		return fmt.Errorf("rollback sync: %w", err)
	}
	return nil
}

// argoSource is the part of an Application source the agent sets and
// changes: Apply writes the revision and values, Rollback the revision.
type argoSource struct {
	TargetRevision string `json:"targetRevision"`
	Helm           struct {
		Values string `json:"values"`
	} `json:"helm"`
}

// argoApp is a partial Argo CD Application.
type argoApp struct {
	Metadata struct {
		Name        string            `json:"name"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		Source argoSource `json:"source"`
	} `json:"spec"`
	Status struct {
		Sync struct {
			Status string `json:"status"`
			// ComparedTo is the source the sync status was computed for.
			ComparedTo struct {
				Source argoSource `json:"source"`
			} `json:"comparedTo"`
		} `json:"sync"`
		Health struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"health"`
		OperationState struct {
			Phase string `json:"phase"`
		} `json:"operationState"`
	} `json:"status"`
}

// argoAppList is the /api/v1/applications list response (partial).
type argoAppList struct {
	Items []argoApp `json:"items"`
}

// ListSaidKickApps returns all Argo CD Applications that carry the
// saidkick deployment-id annotation. Used on agent restart to re-discover
// managed apps without persisted state.
func (a *argoClient) ListSaidKickApps(ctx context.Context) (*argoAppList, error) {
	var out argoAppList
	err := a.do(ctx, http.MethodGet, a.serverURL+"/api/v1/applications", nil, &out)
	if err != nil {
		return nil, err
	}
	// Filter to saidkick-managed apps.
	filtered := out.Items[:0]
	for _, item := range out.Items {
		if _, ok := item.Metadata.Annotations["saidkick.appstore.gov.nl/deployment-id"]; ok {
			filtered = append(filtered, item)
		}
	}
	out.Items = filtered
	return &out, nil
}

// mapToPhase converts Argo CD sync+health status to an orchestrator.Phase.
//
// Right after Apply or Rollback changes the spec, Argo CD still serves the
// sync and health status it computed for the previous source until its
// controller refreshes the app. That status describes the old revision, so
// it is reported as reconciling rather than as ready or degraded.
func mapToPhase(app argoApp) orchestrator.Phase {
	s := app.Status
	switch {
	case s.OperationState.Phase == "Running":
		return orchestrator.PhaseApplying
	case s.Sync.ComparedTo.Source != app.Spec.Source:
		return orchestrator.PhaseReconciling
	case s.Sync.Status == "OutOfSync":
		return orchestrator.PhaseReconciling
	case s.Sync.Status == "Synced" && s.Health.Status == "Healthy":
		return orchestrator.PhaseReady
	case s.Sync.Status == "Synced" && s.Health.Status == "Progressing":
		return orchestrator.PhaseReconciling
	case s.Health.Status == "Degraded":
		return orchestrator.PhaseDegraded
	default:
		return orchestrator.PhasePending
	}
}

func (a *argoClient) do(ctx context.Context, method, endpoint string, body []byte, out any) error {
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("argocd request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent:
		if out != nil {
			b, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
			if err != nil {
				return fmt.Errorf("read response: %w", err)
			}
			if len(b) > 0 {
				return json.Unmarshal(b, out)
			}
		}
		return nil
	case http.StatusNotFound:
		return errNotFound
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("argocd: auth rejected (%d) — check ARGOCD_AUTH_TOKEN", resp.StatusCode)
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("argocd: unexpected %d: %s", resp.StatusCode, string(b))
	}
}

var errNotFound = errors.New("not found")

func isNotFound(err error) bool { return errors.Is(err, errNotFound) }

func sanitizeFieldName(s string) string {
	return strings.NewReplacer(".", "-", "_", "-", "/", "-").Replace(s)
}
