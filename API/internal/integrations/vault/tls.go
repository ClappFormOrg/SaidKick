package vault

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"time"
)

// BuildTLSConfig constructs a *tls.Config for mTLS connections to Vault.
// Returns nil when all three path arguments are empty — callers then use a
// plain http.Client with no custom transport.
//
// caCertFile may be set alone when server-cert verification against a private
// CA is needed but client certs are not. VaultClientCertFile and
// VaultClientKeyFile must be set together or not at all.
func BuildTLSConfig(caCertFile, clientCertFile, clientKeyFile string) (*tls.Config, error) {
	if caCertFile == "" && clientCertFile == "" && clientKeyFile == "" {
		return nil, nil
	}
	cfg := &tls.Config{}
	if caCertFile != "" {
		pem, err := os.ReadFile(caCertFile)
		if err != nil {
			return nil, fmt.Errorf("vault: read CA cert %q: %w", caCertFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("vault: no valid PEM certificates in %q", caCertFile)
		}
		cfg.RootCAs = pool
	}
	if clientCertFile != "" || clientKeyFile != "" {
		if clientCertFile == "" || clientKeyFile == "" {
			return nil, fmt.Errorf("vault: VAULT_CLIENT_CERT_FILE and VAULT_CLIENT_KEY_FILE must both be set")
		}
		cert, err := tls.LoadX509KeyPair(clientCertFile, clientKeyFile)
		if err != nil {
			return nil, fmt.Errorf("vault: load client key pair: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}

// newHTTPClient returns an *http.Client with the given timeout. When tlsCfg is
// non-nil the client uses a custom transport with that TLS configuration,
// enabling mTLS or private-CA verification.
func newHTTPClient(timeout time.Duration, tlsCfg *tls.Config) *http.Client {
	if tlsCfg == nil {
		return &http.Client{Timeout: timeout}
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}
}
