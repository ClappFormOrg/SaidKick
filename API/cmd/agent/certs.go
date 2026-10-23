package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/agentcreds"
	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
)

// Renewal backoff after a failed attempt: exponential from
// renewBackoffInitial, capped at renewBackoffMax, with jitter. A success
// resets it.
const (
	renewBackoffInitial = time.Minute
	renewBackoffMax     = time.Hour
)

// defaultCertRenewBefore is SAIDKICK_CERT_RENEW_BEFORE's default: renew once
// fewer than 30 days of validity remain.
const defaultCertRenewBefore = 720 * time.Hour

// certRenewer asks the hub to sign a CSR. *hubClient implements it.
type certRenewer interface {
	RenewCertificate(ctx context.Context, csrPEM string) (renewResponse, error)
}

// credentialsWriter persists certificate material to the credentials
// Secret. agentcreds.SecretClient implements it.
type credentialsWriter interface {
	PatchCredentials(ctx context.Context, secretName string, c agentcreds.Credentials) error
}

// certManager owns the agent's client certificate. The TLS client reads the
// current certificate through getClientCertificate on every handshake, so a
// renewal takes effect on the next connection without a restart.
//
// Each heartbeat cycle calls maybeRenew. It renews when fewer than
// renewBefore of validity remain or when the last heartbeat response asked
// for it: a fresh RSA 2048 key and CSR, the renew call over the current
// certificate, the new material written to the credentials Secret, and only
// then the in-memory swap. The hub activates the new certificate on the
// first request that presents it (ADR-139-2), so a crash at any point
// leaves a certificate the hub still accepts.
type certManager struct {
	clusterID   string
	secretName  string
	renewBefore time.Duration
	hub         certRenewer
	secrets     credentialsWriter
	now         func() time.Time
	// jitter returns a random duration in [0, n). Tests make it fixed.
	jitter func(n time.Duration) time.Duration

	current atomic.Pointer[tls.Certificate]

	// mu guards the renewal state below; maybeRenew runs on the heartbeat
	// goroutine, but the state must stay consistent if that ever changes.
	mu          sync.Mutex
	failures    int
	nextAttempt time.Time
	// unsaved holds renewed material the Secret write has not accepted
	// yet. It is retried every cycle until it succeeds.
	unsaved *agentcreds.Credentials
}

// newCertManager returns a manager serving initial, which must carry its
// parsed Leaf.
func newCertManager(cfg agentConfig, initial tls.Certificate, hub certRenewer, secrets credentialsWriter) *certManager {
	m := &certManager{
		clusterID:   cfg.ClusterID,
		secretName:  cfg.K8sSecretName,
		renewBefore: cfg.CertRenewBefore,
		hub:         hub,
		secrets:     secrets,
		now:         time.Now,
		jitter:      rand.N[time.Duration], // retry jitter, not a secret
	}
	m.current.Store(&initial)
	return m
}

// getClientCertificate is the tls.Config.GetClientCertificate hook: it
// returns whichever certificate is current when the connection is made.
func (m *certManager) getClientCertificate(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	return m.current.Load(), nil
}

// notAfter is the expiry of the current certificate.
func (m *certManager) notAfter() time.Time {
	return m.current.Load().Leaf.NotAfter
}

// needsRenewal reports whether a certificate expiring at notAfter must be
// renewed at now: fewer than renewBefore remaining, or the hub asked.
func needsRenewal(now, notAfter time.Time, renewBefore time.Duration, requested bool) bool {
	return requested || notAfter.Sub(now) < renewBefore
}

// renewBackoff is the wait after the failures-th consecutive failed
// renewal: renewBackoffInitial doubled per earlier failure, capped at
// renewBackoffMax, minus up to a quarter of it as jitter so a fleet of
// agents does not retry in lockstep.
func renewBackoff(failures int, jitter func(time.Duration) time.Duration) time.Duration {
	d := renewBackoffInitial
	for i := 1; i < failures && d < renewBackoffMax; i++ {
		d *= 2
	}
	d = min(d, renewBackoffMax)
	return d - jitter(d/4)
}

// maybeRenew runs once per heartbeat cycle. requested is the "renew" flag of
// the heartbeat response just received.
func (m *certManager) maybeRenew(ctx context.Context, requested bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	metrics.AgentCertRemainingSeconds.Set(max(m.notAfter().Sub(now).Seconds(), 0))

	m.retrySecretWrite(ctx)

	if !needsRenewal(now, m.notAfter(), m.renewBefore, requested) || now.Before(m.nextAttempt) {
		return
	}
	if err := m.renew(ctx); err != nil {
		m.failures++
		wait := renewBackoff(m.failures, m.jitter)
		m.nextAttempt = now.Add(wait)
		metrics.AgentCertRenewalsTotal(metrics.OutcomeFailure).Inc()
		slog.Error("certificate renewal failed, keeping the current certificate",
			"error", err, "attempt", m.failures, "retry_in", wait.String())
		return
	}
	m.failures = 0
	m.nextAttempt = time.Time{}
	metrics.AgentCertRenewalsTotal(metrics.OutcomeSuccess).Inc()
	metrics.AgentCertRemainingSeconds.Set(max(m.notAfter().Sub(now).Seconds(), 0))
}

// renew obtains a new certificate, persists it and swaps it in. A failed
// Secret write does not undo the swap: the hub has issued the certificate,
// so the agent uses it and keeps retrying the write.
func (m *certManager) renew(ctx context.Context) error {
	keyPEM, csrPEM, err := agentcreds.NewKeyAndCSR(m.clusterID)
	if err != nil {
		return err
	}
	resp, err := m.hub.RenewCertificate(ctx, csrPEM)
	if err != nil {
		return err
	}
	pair, err := tls.X509KeyPair([]byte(resp.Certificate), keyPEM)
	if err != nil {
		return fmt.Errorf("renewed certificate does not match the new key: %w", err)
	}
	if pair.Leaf == nil {
		return errors.New("renewed certificate could not be parsed")
	}

	creds := agentcreds.Credentials{Cert: []byte(resp.Certificate), Key: keyPEM, CA: []byte(resp.CACertificate)}
	if err := m.writeSecret(ctx, creds); err != nil {
		m.unsaved = &creds
		slog.Error("renewed certificate not written to the credentials Secret, will retry; using it in memory",
			"secret", m.secretName, "error", err)
	}
	m.current.Store(&pair)
	slog.Info("certificate renewed", "serial", resp.Serial, "not_after", pair.Leaf.NotAfter.UTC().Format(time.RFC3339))
	return nil
}

// retrySecretWrite retries a Secret write that failed after a renewal.
func (m *certManager) retrySecretWrite(ctx context.Context) {
	if m.unsaved == nil {
		return
	}
	if err := m.writeSecret(ctx, *m.unsaved); err != nil {
		slog.Error("credentials Secret write still failing", "secret", m.secretName, "error", err)
		return
	}
	m.unsaved = nil
	slog.Info("renewed certificate written to the credentials Secret", "secret", m.secretName)
}

// writeSecret patches creds into the credentials Secret. Without a
// configured Secret the renewed certificate lives only in memory, and a
// restarted pod falls back to the one agent-init finds.
func (m *certManager) writeSecret(ctx context.Context, creds agentcreds.Credentials) error {
	if m.secretName == "" {
		slog.Warn("SAIDKICK_K8S_SECRET_NAME is unset: the renewed certificate is not persisted")
		return nil
	}
	return m.secrets.PatchCredentials(ctx, m.secretName, creds)
}
