package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// issue returns a self-signed client certificate and its key, valid from
// notBefore to notAfter, both PEM-encoded.
func issue(t *testing.T, notBefore, notAfter time.Time) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "saidkick-agent-test"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func TestCredentialsReusable(t *testing.T) {
	t.Parallel()
	now := time.Now()
	validCert, validKey := issue(t, now.Add(-time.Hour), now.Add(90*24*time.Hour))
	expiredCert, expiredKey := issue(t, now.Add(-48*time.Hour), now.Add(-time.Hour))
	expiringCert, expiringKey := issue(t, now.Add(-time.Hour), now.Add(renewBefore/2))
	futureCert, futureKey := issue(t, now.Add(time.Hour), now.Add(90*24*time.Hour))
	_, otherKey := issue(t, now.Add(-time.Hour), now.Add(90*24*time.Hour))
	ca := validCert // any non-empty PEM; the check only requires it present

	cases := []struct {
		name  string
		creds credentials
		want  bool
	}{
		{"valid cert and key", credentials{validCert, validKey, ca}, true},
		{"empty Secret from the chart", credentials{[]byte{}, []byte{}, []byte{}}, false},
		{"missing", credentials{}, false},
		{"cert without key", credentials{validCert, nil, ca}, false},
		{"key of another cert", credentials{validCert, otherKey, ca}, false},
		{"missing CA", credentials{validCert, validKey, nil}, false},
		{"expired", credentials{expiredCert, expiredKey, ca}, false},
		{"expires within the renewal window", credentials{expiringCert, expiringKey, ca}, false},
		{"not yet valid", credentials{futureCert, futureKey, ca}, false},
		{"garbage", credentials{[]byte("not a cert"), []byte("not a key"), ca}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.creds.reusable(now))
		})
	}
}

func TestCredentialsWriteTo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Now()
	cert, key := issue(t, now.Add(-time.Hour), now.Add(90*24*time.Hour))
	require.NoError(t, credentials{cert: cert, key: key, ca: cert}.writeTo(dir))

	for name, want := range map[string][]byte{"client.crt": cert, "client.key": key, "ca.crt": cert} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		assert.Equal(t, want, got, name)
	}
	// The copied cert satisfies the cert-dir check on the next start.
	assert.True(t, certOK(filepath.Join(dir, "client.crt")))
}
