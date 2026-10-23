package auth

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAESGCMRoundTrip(t *testing.T) {
	key := newTestEncryptionKey(t)

	for _, plaintext := range []string{"", "s3cr3t", strings.Repeat("x", 4096), "unicode: héllo wörld"} {
		ref, err := encryptAESGCM(plaintext, key)
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(ref, encryptedPrefix), "ciphertext must carry the encrypted: prefix")
		if plaintext != "" {
			assert.NotContains(t, ref, plaintext, "plaintext must not appear in the stored ref")
		}

		got, err := decryptAESGCM(ref, key)
		require.NoError(t, err)
		assert.Equal(t, plaintext, got)
	}
}

func TestAESGCMNonceIsRandom(t *testing.T) {
	key := newTestEncryptionKey(t)
	a, err := encryptAESGCM("same", key)
	require.NoError(t, err)
	b, err := encryptAESGCM("same", key)
	require.NoError(t, err)
	assert.NotEqual(t, a, b, "two encryptions of the same plaintext must differ (fresh nonce)")
}

func TestDecryptAESGCMRejectsTamperedCiphertext(t *testing.T) {
	key := newTestEncryptionKey(t)
	ref, err := encryptAESGCM("s3cr3t-value", key)
	require.NoError(t, err)

	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(ref, encryptedPrefix))
	require.NoError(t, err)

	// Flip one bit in the nonce, the ciphertext body and the GCM tag in turn.
	for _, idx := range []int{0, 12, len(raw) - 1} {
		tampered := make([]byte, len(raw))
		copy(tampered, raw)
		tampered[idx] ^= 0x01
		_, err := decryptAESGCM(encryptedPrefix+base64.StdEncoding.EncodeToString(tampered), key)
		require.Error(t, err, "bit flip at byte %d must fail authentication", idx)
		assert.Contains(t, err.Error(), "crypto: decrypt")
	}

	// Truncating the tag must also fail.
	truncated := raw[:len(raw)-1]
	_, err = decryptAESGCM(encryptedPrefix+base64.StdEncoding.EncodeToString(truncated), key)
	require.Error(t, err)
}

func TestDecryptAESGCMRejectsWrongKey(t *testing.T) {
	ref, err := encryptAESGCM("s3cr3t-value", newTestEncryptionKey(t))
	require.NoError(t, err)

	_, err = decryptAESGCM(ref, newTestEncryptionKey(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wrong key or corrupted ciphertext")
}

func TestAESGCMRejectsBadKeys(t *testing.T) {
	validRef, err := encryptAESGCM("v", newTestEncryptionKey(t))
	require.NoError(t, err)

	cases := []struct {
		name    string
		key     string
		wantErr string
	}{
		{"not base64", "!!!not-base64!!!", "decode key"},
		{"16-byte key", base64.StdEncoding.EncodeToString(make([]byte, 16)), "key must be 32 bytes"},
		{"empty key", "", "key must be 32 bytes"},
		{"33-byte key", base64.StdEncoding.EncodeToString(make([]byte, 33)), "key must be 32 bytes"},
	}
	for _, tc := range cases {
		t.Run("encrypt/"+tc.name, func(t *testing.T) {
			_, err := encryptAESGCM("v", tc.key)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
		t.Run("decrypt/"+tc.name, func(t *testing.T) {
			_, err := decryptAESGCM(validRef, tc.key)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestDecryptAESGCMRejectsShortInput(t *testing.T) {
	key := newTestEncryptionKey(t)

	cases := []struct {
		name    string
		ref     string
		wantErr string
	}{
		{"empty", "", "ref too short"},
		{"prefix only", encryptedPrefix, "ref too short"},
		{"shorter than prefix", "enc", "ref too short"},
		{"payload not base64", encryptedPrefix + "%%%", "decode ciphertext"},
		{"payload shorter than nonce", encryptedPrefix + base64.StdEncoding.EncodeToString(make([]byte, 11)), "ciphertext too short"},
		{"nonce only, no tag", encryptedPrefix + base64.StdEncoding.EncodeToString(make([]byte, 12)), "crypto: decrypt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decryptAESGCM(tc.ref, key)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}
