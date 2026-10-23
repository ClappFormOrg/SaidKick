package auth

import (
	"crypto/rand"
	"encoding/base64"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/vault"
)

// newTestEncryptionKey returns a fresh base64-encoded 32-byte AES-256 key,
// matching the format expected by encryptAESGCM/decryptAESGCM.
func newTestEncryptionKey(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 32)
	_, err := io.ReadFull(rand.Reader, raw)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(raw)
}

func TestSecretRefStoreWriteSchemeSelection(t *testing.T) {
	ctx := t.Context()
	orgID := uuid.New()
	const secret = "s3cr3t-value"

	t.Run("encryption_key_set_yields_encrypted_scheme", func(t *testing.T) {
		key := newTestEncryptionKey(t)
		store := NewSecretRefStore(vault.NullClient{}, key, false)

		ref, err := store.Write(ctx, orgID, secret)
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(ref, encryptedPrefix),
			"expected encrypted: scheme, got %q", ref)
		assert.NotContains(t, ref, secret, "encrypted ref must not leak plaintext")
	})

	t.Run("empty_key_dev_mode_yields_inline_scheme", func(t *testing.T) {
		store := NewSecretRefStore(vault.NullClient{}, "", true)

		ref, err := store.Write(ctx, orgID, secret)
		require.NoError(t, err)
		assert.Equal(t, "v0-inline:"+secret, ref)
	})

	t.Run("empty_key_no_dev_mode_errors", func(t *testing.T) {
		store := NewSecretRefStore(vault.NullClient{}, "", false)

		ref, err := store.Write(ctx, orgID, secret)
		require.Error(t, err)
		assert.Empty(t, ref)
	})
}

func TestSecretRefStoreResolve(t *testing.T) {
	ctx := t.Context()
	orgID := uuid.New()
	const secret = "round-trip-secret"

	t.Run("encrypted_round_trip", func(t *testing.T) {
		key := newTestEncryptionKey(t)
		store := NewSecretRefStore(vault.NullClient{}, key, false)

		ref, err := store.Write(ctx, orgID, secret)
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(ref, encryptedPrefix))

		got, err := store.Resolve(ctx, ref)
		require.NoError(t, err)
		assert.Equal(t, secret, got)
	})

	t.Run("inline_resolves_in_dev_mode", func(t *testing.T) {
		store := NewSecretRefStore(vault.NullClient{}, "", true)

		got, err := store.Resolve(ctx, "v0-inline:"+secret)
		require.NoError(t, err)
		assert.Equal(t, secret, got)
	})

	t.Run("inline_refused_outside_dev_mode", func(t *testing.T) {
		store := NewSecretRefStore(vault.NullClient{}, "", false)

		got, err := store.Resolve(ctx, "v0-inline:"+secret)
		require.Error(t, err)
		assert.Empty(t, got)
	})

	t.Run("encrypted_ref_without_key_errors", func(t *testing.T) {
		// Encrypt with a valid key, then attempt to resolve with a store that
		// has no key configured.
		key := newTestEncryptionKey(t)
		encStore := NewSecretRefStore(vault.NullClient{}, key, false)
		ref, err := encStore.Write(ctx, orgID, secret)
		require.NoError(t, err)

		noKeyStore := NewSecretRefStore(vault.NullClient{}, "", false)
		got, err := noKeyStore.Resolve(ctx, ref)
		require.Error(t, err)
		assert.Empty(t, got)
	})

	t.Run("unrecognised_scheme_errors", func(t *testing.T) {
		store := NewSecretRefStore(vault.NullClient{}, "", true)

		got, err := store.Resolve(ctx, "garbage:xyz")
		require.Error(t, err)
		assert.Empty(t, got)
	})

	t.Run("vault_scheme_with_null_client_surfaces_error", func(t *testing.T) {
		store := NewSecretRefStore(vault.NullClient{}, "", false)

		got, err := store.Resolve(ctx, "vault:oidc/orgs/"+orgID.String()+"/client_secret")
		require.Error(t, err)
		assert.Empty(t, got)
		assert.ErrorIs(t, err, vault.ErrNotConfigured,
			"vault resolve error must wrap ErrNotConfigured")
	})
}
