package secrets

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

// InMemoryStore is a process-local Store used for local dev and unit
// tests. NEVER use in any environment shared with humans — its values
// are not persisted and not encrypted.
//
// V0 main.go wires this when VAULT_ADDR is unset. The runtime emits a
// loud warning at boot in that case.
type InMemoryStore struct {
	mu   sync.RWMutex
	data map[string][]byte // key = vault path
}

var _ Store = (*InMemoryStore)(nil)

// NewInMemoryStore returns an empty store.
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{data: map[string][]byte{}}
}

// Put satisfies Store.
func (m *InMemoryStore) Put(_ context.Context, deploymentID uuid.UUID, fieldName string, value []byte) (string, error) {
	path, err := PathFor(deploymentID, fieldName)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Copy so callers can't mutate the stored value via slice aliasing.
	buf := make([]byte, len(value))
	copy(buf, value)
	m.data[path] = buf
	return path, nil
}

// Get satisfies Store.
func (m *InMemoryStore) Get(_ context.Context, deploymentID uuid.UUID, fieldName string) ([]byte, error) {
	path, err := PathFor(deploymentID, fieldName)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.data[path]
	if !ok {
		return nil, ErrNotFound
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out, nil
}

// DeleteAll satisfies Store.
func (m *InMemoryStore) DeleteAll(_ context.Context, deploymentID uuid.UUID) error {
	prefix := "kv/data/deployments/" + deploymentID.String() + "/"
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.data {
		if hasPrefix(k, prefix) {
			delete(m.data, k)
		}
	}
	return nil
}

// InvalidateCluster satisfies Store. InMemoryStore has no per-cluster token
// cache, so this is a no-op.
func (m *InMemoryStore) InvalidateCluster(_ uuid.UUID) {}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
