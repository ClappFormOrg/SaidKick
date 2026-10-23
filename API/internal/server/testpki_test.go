package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/ClappFormOrg/SaidKick/API/internal/config"
)

// testCA is a throwaway certificate authority generated at test runtime.
// No key material is ever written outside t.TempDir().
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newTestCA(t *testing.T, cn string) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          randomSerial(t),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &testCA{cert: cert, key: key}
}

func randomSerial(t *testing.T) *big.Int {
	t.Helper()
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	require.NoError(t, err)
	return n.Add(n, big.NewInt(1))
}

// issue signs tmpl with the CA and returns it as a tls.Certificate with Leaf set.
func (ca *testCA) issue(t *testing.T, tmpl *x509.Certificate) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl.NotBefore = time.Now().Add(-time.Hour)
	tmpl.NotAfter = time.Now().Add(time.Hour)
	tmpl.KeyUsage = x509.KeyUsageDigitalSignature
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

// agentCert issues an agent client certificate for clusterID.
func (ca *testCA) agentCert(t *testing.T, clusterID uuid.UUID) tls.Certificate {
	t.Helper()
	return ca.issue(t, &x509.Certificate{
		SerialNumber: randomSerial(t),
		Subject:      pkix.Name{CommonName: "saidkick-agent-" + clusterID.String()},
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
}

// serverCert issues a serving certificate valid for 127.0.0.1.
func (ca *testCA) serverCert(t *testing.T) tls.Certificate {
	t.Helper()
	return ca.issue(t, &x509.Certificate{
		SerialNumber: randomSerial(t),
		Subject:      pkix.Name{CommonName: "saidkick-hub-agent-listener"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
}

func (ca *testCA) pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.cert)
	return p
}

func (ca *testCA) certPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.cert.Raw})
}

// writeAgentListenerFiles writes a serving certificate signed by ca, its key
// and ca's certificate as the client CA bundle into a temp dir, and returns
// the listener config pointing at them.
func writeAgentListenerFiles(t *testing.T, ca *testCA) config.AgentListenerConfig {
	t.Helper()
	dir := t.TempDir()
	srv := ca.serverCert(t)
	keyDER, err := x509.MarshalPKCS8PrivateKey(srv.PrivateKey)
	require.NoError(t, err)

	cfg := config.AgentListenerConfig{
		Addr:         "127.0.0.1:0",
		CertFile:     filepath.Join(dir, "tls.crt"),
		KeyFile:      filepath.Join(dir, "tls.key"),
		ClientCAFile: filepath.Join(dir, "ca.crt"),
	}
	writePEM(t, cfg.CertFile, "CERTIFICATE", srv.Certificate[0])
	writePEM(t, cfg.KeyFile, "PRIVATE KEY", keyDER)
	require.NoError(t, os.WriteFile(cfg.ClientCAFile, ca.certPEM(), 0o600))
	return cfg
}

func writePEM(t *testing.T, path, blockType string, der []byte) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), 0o600))
}

// agentClient returns an HTTPS client that trusts ca for the server and
// presents clientCert when non-nil. maxVersion 0 means no cap.
func agentClient(ca *testCA, clientCert *tls.Certificate, maxVersion uint16) *http.Client {
	tlsCfg := &tls.Config{RootCAs: ca.pool(), MinVersion: tls.VersionTLS12, MaxVersion: maxVersion}
	if clientCert != nil {
		tlsCfg.Certificates = []tls.Certificate{*clientCert}
	}
	return &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}
}
