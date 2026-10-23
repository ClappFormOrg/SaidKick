// Package agentcreds holds the certificate-material code shared by the
// cluster agent's two binaries: agent-init, which obtains the first client
// certificate at handshake, and the agent, which renews it. Both generate
// the key and CSR the same way and keep the result in the same Kubernetes
// Secret, so a restarted pod reuses whatever certificate was issued last.
package agentcreds

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// keyBits is the RSA key size of every agent client key.
const keyBits = 2048

// CommonName returns the certificate Subject CN of clusterID's agent, the
// form the hub's AgentAuthMiddleware parses.
func CommonName(clusterID string) string {
	return "saidkick-agent-" + clusterID
}

// NewKeyAndCSR generates a fresh RSA 2048 key and a PKCS#10 request for it
// with CN saidkick-agent-<clusterID>. It returns the key as a PKCS#1 PEM
// block and the request as PEM. Every call generates a new key; keys are
// never reused across certificates.
func NewKeyAndCSR(clusterID string) (keyPEM []byte, csrPEM string, err error) {
	key, err := rsa.GenerateKey(rand.Reader, keyBits)
	if err != nil {
		return nil, "", fmt.Errorf("generate key: %w", err)
	}
	template := &x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName:   CommonName(clusterID),
			Organization: []string{"sAId-Kick"},
		},
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, template, key)
	if err != nil {
		return nil, "", fmt.Errorf("build csr: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	csrPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))
	return keyPEM, csrPEM, nil
}

// Credentials is the material kept in the agent's credentials Secret under
// tls.crt, tls.key and ca.crt, all PEM.
type Credentials struct {
	Cert, Key, CA []byte
}

// In-cluster service-account paths and API host.
const (
	defaultTokenFile     = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	defaultCAFile        = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	defaultNamespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
	defaultAPIHost       = "https://kubernetes.default.svc"
	defaultTimeout       = 30 * time.Second
)

// SecretClient reads and patches a Secret in the pod's own namespace
// through the Kubernetes API, authenticated with the pod's service-account
// token. The zero value uses the in-cluster paths; tests point the fields
// at their own files and server.
type SecretClient struct {
	TokenFile     string
	CAFile        string
	NamespaceFile string
	APIHost       string
	Timeout       time.Duration
}

// ReadCredentials reads tls.crt, tls.key and ca.crt from the named Secret.
// The Role bound to the agent ServiceAccount must grant get on secrets in
// its namespace.
func (s SecretClient) ReadCredentials(ctx context.Context, secretName string) (Credentials, error) {
	b, err := s.request(ctx, http.MethodGet, secretName, "", nil)
	if err != nil {
		return Credentials{}, err
	}
	var secret struct {
		Data map[string][]byte `json:"data"` // base64 in JSON, decoded by encoding/json
	}
	if err := json.Unmarshal(b, &secret); err != nil {
		return Credentials{}, fmt.Errorf("decode secret: %w", err)
	}
	return Credentials{Cert: secret.Data["tls.crt"], Key: secret.Data["tls.key"], CA: secret.Data["ca.crt"]}, nil
}

// PatchCredentials writes c into the named Secret as tls.crt, tls.key and
// ca.crt with a strategic merge patch, so only those keys are touched. The
// Role bound to the agent ServiceAccount must grant patch on secrets in its
// namespace.
func (s SecretClient) PatchCredentials(ctx context.Context, secretName string, c Credentials) error {
	patch := map[string]any{
		"data": map[string]string{
			"tls.crt": base64.StdEncoding.EncodeToString(c.Cert),
			"tls.key": base64.StdEncoding.EncodeToString(c.Key),
			"ca.crt":  base64.StdEncoding.EncodeToString(c.CA),
		},
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshal patch: %w", err)
	}
	_, err = s.request(ctx, http.MethodPatch, secretName, "application/strategic-merge-patch+json", body)
	return err
}

// request sends one request for the named Secret and returns the response
// body. Any status outside 2xx is an error.
func (s SecretClient) request(ctx context.Context, method, secretName, contentType string, body []byte) ([]byte, error) {
	token, err := os.ReadFile(cmp.Or(s.TokenFile, defaultTokenFile))
	if err != nil {
		return nil, fmt.Errorf("read sa token: %w", err)
	}
	nsBytes, err := os.ReadFile(cmp.Or(s.NamespaceFile, defaultNamespaceFile))
	if err != nil {
		return nil, fmt.Errorf("read namespace: %w", err)
	}
	caCert, err := os.ReadFile(cmp.Or(s.CAFile, defaultCAFile))
	if err != nil {
		return nil, fmt.Errorf("read k8s CA: %w", err)
	}

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caCert)
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	httpClient := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		},
	}

	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	url := fmt.Sprintf("%s/api/v1/namespaces/%s/secrets/%s",
		strings.TrimRight(cmp.Or(s.APIHost, defaultAPIHost), "/"), strings.TrimSpace(string(nsBytes)), secretName)
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("build %s request: %w", method, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s request: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()

	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read k8s API response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("k8s API returned %d: %s", resp.StatusCode, string(b))
	}
	return b, nil
}
