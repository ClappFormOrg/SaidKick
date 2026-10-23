package secrets

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/vault"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// VaultStore is a Store backed by HashiCorp Vault KV v2.
//
// Two auth modes:
//
//   - Static token (default): constructed via NewVaultStore; uses a single
//     VAULT_TOKEN for all requests. Path: kv/data/deployments/<deploymentID>/<field>.
//
//   - Per-cluster AppRole (production): constructed via NewVaultStoreWithAppRole;
//     looks up each deployment's cluster, obtains a cluster-scoped token from
//     AppRoleCache, and uses a cluster-prefixed path:
//     kv/data/deployments/<clusterID>/<deploymentID>/<field>.
type VaultStore struct {
	addr   string // base URL, no trailing slash — e.g. "https://vault.example.com"
	token  string // static token; empty when appRole is set
	mount  string // KV v2 mount path — e.g. "kv"
	client *http.Client

	// AppRole fields — both must be non-nil to enable per-cluster auth.
	dbHandle *db.Handle
	appRole  *vault.AppRoleCache
}

var _ Store = (*VaultStore)(nil)

// NewVaultStore returns a VaultStore that authenticates with a static Vault
// token. addr must not have a trailing slash.
func NewVaultStore(addr, token, mount string) *VaultStore {
	return &VaultStore{
		addr:   addr,
		token:  token,
		mount:  mount,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

// NewVaultStoreWithAppRole returns a VaultStore that obtains a per-cluster
// short-lived token via Vault AppRole on each call. The cluster's role_id and
// secret_id are read from the Cluster row found via the deployment's cluster_id.
// Falls back to ErrNotConfigured when the cluster has no AppRole credentials set.
func NewVaultStoreWithAppRole(addr, mount string, dbHandle *db.Handle, cache *vault.AppRoleCache) *VaultStore {
	return &VaultStore{
		addr:     addr,
		mount:    mount,
		client:   &http.Client{Timeout: 15 * time.Second},
		dbHandle: dbHandle,
		appRole:  cache,
	}
}

// authFor resolves the Vault token and the cluster-scoped path prefix for the
// given deployment. When AppRole is configured it looks up the deployment →
// cluster → AppRole credentials. Returns (token, clusterID, error).
func (v *VaultStore) authFor(ctx context.Context, deploymentID uuid.UUID) (string, uuid.UUID, error) {
	if v.appRole == nil {
		return v.token, uuid.Nil, nil
	}

	var dep models.Deployment
	if err := v.dbHandle.RW.WithContext(ctx).
		Select("id, cluster_id").
		First(&dep, "id = ?", deploymentID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", uuid.Nil, fmt.Errorf("secrets: deployment %s not found", deploymentID)
		}
		return "", uuid.Nil, fmt.Errorf("secrets: lookup deployment: %w", err)
	}

	var cluster models.Cluster
	if err := v.dbHandle.RW.WithContext(ctx).
		Select("id, vault_role_id, vault_secret_id").
		First(&cluster, "id = ?", dep.ClusterID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", uuid.Nil, fmt.Errorf("secrets: cluster %s not found", dep.ClusterID)
		}
		return "", uuid.Nil, fmt.Errorf("secrets: lookup cluster: %w", err)
	}

	if cluster.VaultRoleID == nil || cluster.VaultSecretID == nil ||
		*cluster.VaultRoleID == "" || *cluster.VaultSecretID == "" {
		return "", uuid.Nil, fmt.Errorf("%w: cluster %s has no Vault AppRole credentials configured",
			ErrNotConfigured, cluster.ID)
	}

	token, err := v.appRole.GetToken(ctx, cluster.ID, *cluster.VaultRoleID, *cluster.VaultSecretID)
	if err != nil {
		return "", uuid.Nil, fmt.Errorf("secrets: get approle token: %w", err)
	}
	return token, cluster.ID, nil
}

// Put writes value to Vault at the canonical path for (deploymentID, fieldName).
// The value is base64-encoded inside the KV payload so arbitrary bytes survive
// the JSON transport layer.
//
// When AppRole mode is active, the path is cluster-scoped:
// kv/data/deployments/<clusterID>/<deploymentID>/<fieldName>.
func (v *VaultStore) Put(ctx context.Context, deploymentID uuid.UUID, fieldName string, value []byte) (string, error) {
	if !validFieldNameRegex.MatchString(fieldName) {
		return "", fmt.Errorf("%w: %q (must match ^[a-zA-Z][a-zA-Z0-9_]{0,62}$)", ErrInvalidFieldName, fieldName)
	}

	token, clusterID, err := v.authFor(ctx, deploymentID)
	if err != nil {
		return "", err
	}

	body, err := json.Marshal(map[string]any{
		"data": map[string]any{
			"value": base64.StdEncoding.EncodeToString(value),
		},
	})
	if err != nil {
		return "", fmt.Errorf("secrets: marshal vault put body: %w", err)
	}

	kvPath := v.kvPath(clusterID, deploymentID, fieldName)
	url := fmt.Sprintf("%s/v1/%s/data/%s", v.addr, v.mount, kvPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("secrets: vault put request: %w", err)
	}
	req.Header.Set("X-Vault-Token", token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := v.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("secrets: vault put: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		return "", fmt.Errorf("secrets: vault put returned HTTP %d", resp.StatusCode)
	}

	return fmt.Sprintf("%s/data/%s", v.mount, kvPath), nil
}

// InvalidateCluster satisfies Store. When AppRole mode is active it drops the
// cached Vault token for clusterID so the next secret operation re-authenticates.
func (v *VaultStore) InvalidateCluster(clusterID uuid.UUID) {
	if v.appRole != nil {
		v.appRole.Invalidate(clusterID)
	}
}

// kvPath returns the KV v2 logical path (without the /v1/<mount>/data/ prefix)
// for a given (deploymentID, fieldName) pair. When AppRole mode is active the
// path is prefixed with the clusterID so Vault policies can be cluster-scoped.
func (v *VaultStore) kvPath(clusterID, deploymentID uuid.UUID, fieldName string) string {
	if clusterID != uuid.Nil {
		return fmt.Sprintf("deployments/%s/%s/%s", clusterID, deploymentID, fieldName)
	}
	return fmt.Sprintf("deployments/%s/%s", deploymentID, fieldName)
}

// Get retrieves the value previously written by Put for (deploymentID, fieldName).
// Returns ErrNotFound when the path does not exist in Vault.
func (v *VaultStore) Get(ctx context.Context, deploymentID uuid.UUID, fieldName string) ([]byte, error) {
	if !validFieldNameRegex.MatchString(fieldName) {
		return nil, fmt.Errorf("%w: %q (must match ^[a-zA-Z][a-zA-Z0-9_]{0,62}$)", ErrInvalidFieldName, fieldName)
	}

	token, clusterID, err := v.authFor(ctx, deploymentID)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/v1/%s/data/%s", v.addr, v.mount, v.kvPath(clusterID, deploymentID, fieldName))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("secrets: vault get request: %w", err)
	}
	req.Header.Set("X-Vault-Token", token)

	resp, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("secrets: vault get: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("secrets: vault get returned HTTP %d", resp.StatusCode)
	}

	var result struct {
		Data struct {
			Data struct {
				Value string `json:"value"`
			} `json:"data"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("secrets: vault get decode: %w", err)
	}

	decoded, err := base64.StdEncoding.DecodeString(result.Data.Data.Value)
	if err != nil {
		return nil, fmt.Errorf("secrets: vault get base64 decode: %w", err)
	}
	return decoded, nil
}

// DeleteAll removes every secret in Vault for the given deploymentID by first
// listing the metadata prefix then deleting each key's full metadata (which
// removes all versions). A 404 on the list is treated as success.
func (v *VaultStore) DeleteAll(ctx context.Context, deploymentID uuid.UUID) error {
	token, clusterID, err := v.authFor(ctx, deploymentID)
	if err != nil {
		return err
	}

	prefix := fmt.Sprintf("deployments/%s/", deploymentID)
	if clusterID != uuid.Nil {
		prefix = fmt.Sprintf("deployments/%s/%s/", clusterID, deploymentID)
	}

	listURL := fmt.Sprintf("%s/v1/%s/metadata/%s", v.addr, v.mount, prefix)
	listReq, err := http.NewRequestWithContext(ctx, "LIST", listURL, http.NoBody)
	if err != nil {
		return fmt.Errorf("secrets: vault list request: %w", err)
	}
	listReq.Header.Set("X-Vault-Token", token)

	listResp, err := v.client.Do(listReq)
	if err != nil {
		return fmt.Errorf("secrets: vault list: %w", err)
	}
	defer func() { _ = listResp.Body.Close() }()

	if listResp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, listResp.Body)
		return nil // nothing to delete
	}
	if listResp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, listResp.Body)
		return fmt.Errorf("secrets: vault list returned HTTP %d", listResp.StatusCode)
	}

	var listResult struct {
		Data struct {
			Keys []string `json:"keys"`
		} `json:"data"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listResult); err != nil {
		return fmt.Errorf("secrets: vault list decode: %w", err)
	}

	var firstErr error
	for _, key := range listResult.Data.Keys {
		if key == "" || key[len(key)-1] == '/' {
			continue // skip nested prefixes — V1 only handles flat field names
		}
		delURL := fmt.Sprintf("%s/v1/%s/metadata/%s%s", v.addr, v.mount, prefix, key)
		delReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, delURL, http.NoBody)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("secrets: vault delete request for %q: %w", key, err)
			}
			continue
		}
		delReq.Header.Set("X-Vault-Token", token)

		if err := func() error {
			delResp, err := v.client.Do(delReq)
			if err != nil {
				return fmt.Errorf("secrets: vault delete %q: %w", key, err)
			}
			defer func() { _ = delResp.Body.Close() }()
			_, _ = io.Copy(io.Discard, delResp.Body)
			if delResp.StatusCode != http.StatusNoContent &&
				delResp.StatusCode != http.StatusOK &&
				delResp.StatusCode != http.StatusNotFound {
				return fmt.Errorf("secrets: vault delete %q returned HTTP %d", key, delResp.StatusCode)
			}
			return nil
		}(); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}
