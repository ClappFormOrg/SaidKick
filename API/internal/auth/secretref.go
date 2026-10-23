package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/vault"
	"github.com/google/uuid"
)

// SecretRefStore manages storage and retrieval of OIDC client secrets.
// It selects the best available scheme at write time and resolves any
// supported scheme at read time, so operators can upgrade their storage
// tier without re-running ConfigureOrganizationOIDC.
//
// Scheme priority (Write):
//  1. vault:<path>      — Vault KV v2; preferred in production.
//  2. encrypted:<b64>   — AES-256-GCM in the DB; no Vault required.
//  3. v0-inline:<secret>— Plaintext; dev / APP_DEV_MODE=true only.
//
// The scheme is detected from the ref prefix at Resolve time so old refs
// remain readable after an operator upgrades (e.g. from encrypted: to vault:
// on next ConfigureOrganizationOIDC call).
type SecretRefStore struct {
	kvClient      vault.KVClient
	encryptionKey string // base64-encoded 32-byte AES-256 key; empty = not set
	isDevMode       bool
}

// NewSecretRefStore constructs a SecretRefStore. kvClient may be a NullClient
// when Vault is not configured. encryptionKey may be empty when DB-level
// encryption is not configured. isDevMode must only be true in dev environments.
func NewSecretRefStore(kvClient vault.KVClient, encryptionKey string, isDevMode bool) *SecretRefStore {
	return &SecretRefStore{
		kvClient:      kvClient,
		encryptionKey: encryptionKey,
		isDevMode:       isDevMode,
	}
}

// Write stores secret using the best available scheme and returns the ref
// string to be persisted in org.OIDCClientSecretRef. Returns an error when
// no storage scheme is available and isDevMode is false.
func (s *SecretRefStore) Write(ctx context.Context, orgID uuid.UUID, secret string) (string, error) {
	// Tier 1: Vault
	path := fmt.Sprintf("oidc/orgs/%s/client_secret", orgID.String())
	if err := s.kvClient.Put(ctx, path, secret); err == nil {
		return "vault:" + path, nil
	} else if !errors.Is(err, vault.ErrNotConfigured) {
		// Vault is configured but the write failed — surface the error rather
		// than silently falling back to a less secure scheme.
		return "", fmt.Errorf("vault write: %w", err)
	}

	// Tier 2: AES-256-GCM in DB
	if s.encryptionKey != "" {
		ref, err := encryptAESGCM(secret, s.encryptionKey)
		if err != nil {
			return "", fmt.Errorf("encrypt secret: %w", err)
		}
		return ref, nil
	}

	// Tier 3: Plaintext — dev mode only
	if s.isDevMode {
		return "v0-inline:" + secret, nil
	}

	return "", errors.New(
		"no secret storage configured for production: set AUTH_SECRET_ENCRYPTION_KEY or configure Vault",
	)
}

// Resolve reads the secret value from wherever the ref points. It supports
// all three schemes so refs written under any previous configuration remain
// readable.
func (s *SecretRefStore) Resolve(ctx context.Context, ref string) (string, error) {
	switch {
	case strings.HasPrefix(ref, "vault:"):
		path := strings.TrimPrefix(ref, "vault:")
		val, err := s.kvClient.Get(ctx, path)
		if err != nil {
			return "", fmt.Errorf("vault read %q: %w", path, err)
		}
		return val, nil

	case strings.HasPrefix(ref, encryptedPrefix):
		if s.encryptionKey == "" {
			return "", errors.New("AUTH_SECRET_ENCRYPTION_KEY is not set; cannot decrypt OIDC client secret")
		}
		return decryptAESGCM(ref, s.encryptionKey)

	case strings.HasPrefix(ref, "v0-inline:"):
		if !s.isDevMode {
			return "", errors.New("v0-inline: secrets are not permitted outside dev mode — re-run ConfigureOrganizationOIDC on this environment to migrate to a supported scheme")
		}
		return strings.TrimPrefix(ref, "v0-inline:"), nil

	default:
		return "", fmt.Errorf("unrecognised OIDC client secret ref scheme in %q", ref)
	}
}
