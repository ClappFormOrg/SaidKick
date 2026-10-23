package agentcreds

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewKeyAndCSR(t *testing.T) {
	t.Parallel()
	keyPEM, csrPEM, err := NewKeyAndCSR("0190c3a4-0000-7000-8000-000000000001")
	require.NoError(t, err)

	block, _ := pem.Decode([]byte(csrPEM))
	require.NotNil(t, block)
	assert.Equal(t, "CERTIFICATE REQUEST", block.Type)
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	require.NoError(t, err)
	require.NoError(t, csr.CheckSignature())
	assert.Equal(t, "saidkick-agent-0190c3a4-0000-7000-8000-000000000001", csr.Subject.CommonName)
	assert.Equal(t, []string{"sAId-Kick"}, csr.Subject.Organization)

	keyBlock, _ := pem.Decode(keyPEM)
	require.NotNil(t, keyBlock)
	assert.Equal(t, "RSA PRIVATE KEY", keyBlock.Type)
	key, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
	require.NoError(t, err)
	assert.Equal(t, 2048, key.N.BitLen())
	pub, ok := csr.PublicKey.(*rsa.PublicKey)
	require.True(t, ok)
	assert.True(t, key.PublicKey.Equal(pub), "the CSR is for the returned key")

	otherKey, _, err := NewKeyAndCSR("0190c3a4-0000-7000-8000-000000000001")
	require.NoError(t, err)
	assert.NotEqual(t, keyPEM, otherKey, "every call generates a new key")
}

// fakeAPIServer is a Kubernetes API stand-in for one Secret.
type fakeAPIServer struct {
	mu          sync.Mutex
	status      int
	data        map[string][]byte
	method      string
	path        string
	auth        string
	contentType string
	body        []byte
}

func (f *fakeAPIServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.method, f.path, f.auth, f.contentType = r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type")
	f.body, _ = io.ReadAll(r.Body)
	if f.status != 0 {
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(`{"reason":"Forbidden"}`))
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": f.data})
}

// newSecretClient starts a TLS fake API server and writes the
// service-account files the client reads into a temp dir.
func newSecretClient(t *testing.T) (SecretClient, *fakeAPIServer) {
	t.Helper()
	api := &fakeAPIServer{}
	srv := httptest.NewTLSServer(api)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	c := SecretClient{
		TokenFile:     filepath.Join(dir, "token"),
		CAFile:        filepath.Join(dir, "ca.crt"),
		NamespaceFile: filepath.Join(dir, "namespace"),
		APIHost:       srv.URL,
	}
	require.NoError(t, os.WriteFile(c.TokenFile, []byte("sa-token\n"), 0o600))
	require.NoError(t, os.WriteFile(c.NamespaceFile, []byte("saidkick-system"), 0o600))
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	require.NoError(t, os.WriteFile(c.CAFile, caPEM, 0o600))
	return c, api
}

func TestSecretClientReadCredentials(t *testing.T) {
	t.Parallel()
	c, api := newSecretClient(t)
	api.data = map[string][]byte{"tls.crt": []byte("CERT"), "tls.key": []byte("KEY"), "ca.crt": []byte("CA"), "OTHER": []byte("x")}

	got, err := c.ReadCredentials(t.Context(), "saidkick-agent-credentials")
	require.NoError(t, err)
	assert.Equal(t, Credentials{Cert: []byte("CERT"), Key: []byte("KEY"), CA: []byte("CA")}, got)
	assert.Equal(t, http.MethodGet, api.method)
	assert.Equal(t, "/api/v1/namespaces/saidkick-system/secrets/saidkick-agent-credentials", api.path)
	assert.Equal(t, "Bearer sa-token", api.auth)
}

func TestSecretClientPatchCredentials(t *testing.T) {
	t.Parallel()
	c, api := newSecretClient(t)

	require.NoError(t, c.PatchCredentials(t.Context(), "saidkick-agent-credentials",
		Credentials{Cert: []byte("CERT"), Key: []byte("KEY"), CA: []byte("CA")}))
	assert.Equal(t, http.MethodPatch, api.method)
	assert.Equal(t, "application/strategic-merge-patch+json", api.contentType)
	assert.JSONEq(t, `{"data":{"tls.crt":"Q0VSVA==","tls.key":"S0VZ","ca.crt":"Q0E="}}`, string(api.body),
		"only the three certificate keys are patched, base64-encoded")
}

func TestSecretClientErrors(t *testing.T) {
	t.Parallel()

	t.Run("API refuses", func(t *testing.T) {
		c, api := newSecretClient(t)
		api.status = http.StatusForbidden
		_, err := c.ReadCredentials(t.Context(), "s")
		require.ErrorContains(t, err, "k8s API returned 403")
		require.ErrorContains(t, c.PatchCredentials(t.Context(), "s", Credentials{}), "k8s API returned 403")
	})

	t.Run("no service-account token", func(t *testing.T) {
		c, _ := newSecretClient(t)
		c.TokenFile = filepath.Join(t.TempDir(), "missing")
		_, err := c.ReadCredentials(t.Context(), "s")
		require.ErrorContains(t, err, "read sa token")
	})

	t.Run("no namespace file", func(t *testing.T) {
		c, _ := newSecretClient(t)
		c.NamespaceFile = filepath.Join(t.TempDir(), "missing")
		_, err := c.ReadCredentials(t.Context(), "s")
		require.ErrorContains(t, err, "read namespace")
	})

	t.Run("no CA file", func(t *testing.T) {
		c, _ := newSecretClient(t)
		c.CAFile = filepath.Join(t.TempDir(), "missing")
		_, err := c.ReadCredentials(t.Context(), "s")
		require.ErrorContains(t, err, "read k8s CA")
	})

	t.Run("API server unreachable", func(t *testing.T) {
		c, _ := newSecretClient(t)
		c.APIHost = "https://127.0.0.1:1"
		require.ErrorContains(t, c.PatchCredentials(t.Context(), "s", Credentials{}), "PATCH request")
	})

	t.Run("body is not a Secret", func(t *testing.T) {
		c, _ := newSecretClient(t)
		bad := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("not json"))
		}))
		t.Cleanup(bad.Close)
		c.APIHost = bad.URL
		c.CAFile = writeCA(t, bad)
		_, err := c.ReadCredentials(t.Context(), "s")
		require.ErrorContains(t, err, "decode secret")
	})
}

func writeCA(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.crt")
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600))
	return path
}
