package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

const encryptedPrefix = "encrypted:"

// encryptAESGCM encrypts plaintext with AES-256-GCM using keyB64 (a
// base64-encoded 32-byte key). Returns a string of the form
// "encrypted:<base64(nonce||ciphertext)>" suitable for storage in the DB.
//
// Generate a key with: openssl rand -base64 32
func encryptAESGCM(plaintext, keyB64 string) (string, error) {
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return "", fmt.Errorf("crypto: decode key: %w", err)
	}
	if len(key) != 32 {
		return "", fmt.Errorf("crypto: key must be 32 bytes (got %d); generate with: openssl rand -base64 32", len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("crypto: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("crypto: new gcm: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("crypto: read nonce: %w", err)
	}

	// Seal appends ciphertext+tag to nonce so the stored blob is nonce||ciphertext||tag.
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return encryptedPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// decryptAESGCM decrypts a value produced by encryptAESGCM. ref must have
// the "encrypted:" prefix; keyB64 must be the same key used to encrypt.
func decryptAESGCM(ref, keyB64 string) (string, error) {
	if len(ref) <= len(encryptedPrefix) {
		return "", errors.New("crypto: ref too short")
	}
	data, err := base64.StdEncoding.DecodeString(ref[len(encryptedPrefix):])
	if err != nil {
		return "", fmt.Errorf("crypto: decode ciphertext: %w", err)
	}

	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return "", fmt.Errorf("crypto: decode key: %w", err)
	}
	if len(key) != 32 {
		return "", fmt.Errorf("crypto: key must be 32 bytes (got %d)", len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("crypto: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("crypto: new gcm: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("crypto: ciphertext too short")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("crypto: decrypt: %w (wrong key or corrupted ciphertext)", err)
	}
	return string(plaintext), nil
}
