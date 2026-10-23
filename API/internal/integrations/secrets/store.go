// Package secrets owns the platform's secret store abstraction.
//
// Deployers supply secrets (API keys, database passwords, etc.) at
// deploy time. The platform writes them into a central vault keyed by
// deployment id; an in-cluster ExternalSecrets Operator (or equivalent)
// then materialises them as Kubernetes Secret objects inside the
// target cluster, scoped per-deployment.
//
// V1 ships:
//   - InMemoryStore — process-local map, fine for dev + smoke tests.
//   - VaultStore    — OpenBao / Vault KV v2, used in staging + production.
//
// The Store interface intentionally does not return secret VALUES to
// the orchestrator code — only paths. Values are read by the in-cluster
// agent (ESO) directly from OpenBao, never by the platform's API process
// after the initial write. That keeps the platform out of the
// hot-path-secret-access loop.
//
// When ESO SecretStore manifests are generated (planned post-Sprint 3),
// they must be namespace-scoped (SecretStore, not ClusterSecretStore) with
// credentials bound to a single deployment namespace — no wildcard policies.
package secrets

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/google/uuid"
)

// ErrNotConfigured is returned by a Store that has no backend wired up.
var ErrNotConfigured = errors.New("secrets: store not configured")

// ErrNotFound is returned when a Get / Delete targets a path that has
// no value.
var ErrNotFound = errors.New("secrets: path not found")

// ErrInvalidFieldName is returned when a fieldName fails the regex
// ^[a-zA-Z][a-zA-Z0-9_]{0,62}$. Callers can distinguish this from a
// backend/network error with errors.Is.
var ErrInvalidFieldName = errors.New("secrets: invalid field name")

// Store is the abstraction over a secret backend. Implementations
// MUST scope every read/write/delete by deployment id so a misbehaving
// deployment cannot trample its peers.
type Store interface {
	// Put writes a secret value for one (deploymentID, fieldName) pair.
	// Overwrites silently if the pair already exists — caller is
	// expected to version via the deploymentID itself.
	//
	// fieldName MUST match the regex ^[a-zA-Z][a-zA-Z0-9_]{0,62}$ —
	// implementations validate this and reject invalid names so no
	// user input can sneak into the vault path.
	Put(ctx context.Context, deploymentID uuid.UUID, fieldName string, value []byte) (vaultPath string, err error)

	// Get returns the bytes previously written by Put. The orchestrator
	// itself does NOT call this in V1 — ESO does — but the platform's
	// dev smoke tests use it. Returns ErrNotFound when missing.
	Get(ctx context.Context, deploymentID uuid.UUID, fieldName string) ([]byte, error)

	// DeleteAll purges every secret for one deployment. Called by the
	// deployment-deletion handler after the orchestrator confirms the
	// application is torn down.
	DeleteAll(ctx context.Context, deploymentID uuid.UUID) error

	// InvalidateCluster drops any cached credentials for clusterID from the
	// store's internal token cache. Call this when a cluster is deleted or its
	// AppRole credentials are rotated so the next operation forces a fresh
	// Vault login. Implementations that have no per-cluster cache treat this
	// as a no-op.
	InvalidateCluster(clusterID uuid.UUID)
}

// validFieldNameRegex matches what we allow inside the vault path. The
// reference is duplicated from internal/db/models's design intent so
// the secret-store layer can reject invalid names without depending on
// the models package (which would create a cycle).
var validFieldNameRegex = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,62}$`)

// PathFor returns the canonical vault path for a (deploymentID,
// fieldName) pair. Exported so the orchestrator can construct
// SecretRefs without first calling Put — useful when re-applying an
// existing deployment whose secrets are already in place.
func PathFor(deploymentID uuid.UUID, fieldName string) (string, error) {
	if !validFieldNameRegex.MatchString(fieldName) {
		return "", fmt.Errorf("%w: %q (must match ^[a-zA-Z][a-zA-Z0-9_]{0,62}$)", ErrInvalidFieldName, fieldName)
	}
	return fmt.Sprintf("kv/data/deployments/%s/%s", deploymentID.String(), fieldName), nil
}
