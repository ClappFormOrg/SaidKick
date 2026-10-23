package secrets

import (
	"context"

	"github.com/google/uuid"
)

// NullStore satisfies Store but returns ErrNotConfigured from every method.
// Wired at boot when Vault credentials are absent and dev mode is off.
type NullStore struct{}

var _ Store = NullStore{}

func (NullStore) Put(_ context.Context, _ uuid.UUID, _ string, _ []byte) (string, error) {
	return "", ErrNotConfigured
}

func (NullStore) Get(_ context.Context, _ uuid.UUID, _ string) ([]byte, error) {
	return nil, ErrNotConfigured
}

func (NullStore) DeleteAll(_ context.Context, _ uuid.UUID) error {
	return ErrNotConfigured
}

func (NullStore) InvalidateCluster(_ uuid.UUID) {}
