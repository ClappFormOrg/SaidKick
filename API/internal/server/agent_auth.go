package server

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

// agentClusterIDHeader is the dev-mode bypass header. Accepted only when
// the server is started with devMode=true; never trusted in production.
const agentClusterIDHeader = "X-Agent-Cluster-ID"

// agentCertSerials are the certificate serials recorded for a cluster, in
// the decimal form the hub stores (big.Int.String). Either may be "".
type agentCertSerials struct {
	// Active is clusters.agent_cert_serial: the certificate in use.
	Active string
	// Pending is clusters.agent_cert_pending_serial: a renewed certificate
	// the agent has not presented yet.
	Pending string
}

// agentCertStore reads and promotes the certificate serials of a cluster.
type agentCertStore interface {
	// serials returns the serials recorded for clusterID. found is false
	// when no cluster row exists.
	serials(ctx context.Context, clusterID uuid.UUID) (s agentCertSerials, found bool, err error)
	// promote makes pending the active serial of clusterID, clears the
	// pending serial and records notAfter as the active certificate's
	// expiry, but only while pending is still the pending serial on record.
	// promoted is false when another request got there first or a newer
	// renewal replaced it.
	promote(ctx context.Context, clusterID uuid.UUID, pending string, notAfter time.Time) (promoted bool, err error)
}

// AgentAuthMiddleware returns a route-level middleware that authenticates
// inbound saidkick-agent requests:
//
//   - Production: requires a verified TLS client certificate whose Subject
//     CN has the form "saidkick-agent-<cluster-uuid>" and whose serial
//     number equals the cluster's active serial (AgentCertSerial) or its
//     pending serial (AgentCertPendingSerial). A missing certificate, a
//     malformed CN, an unknown cluster or a serial matching neither is 401:
//     the credential is wrong, not the permission.
//
//   - Activation on first use (ADR-139-2): a certificate matching the
//     pending serial is promoted to active in the same request. From then
//     on the previous certificate matches nothing and gets 401, so a
//     renewal never leaves a window in which neither certificate works.
//
//   - Dev mode (devMode=true): accepts the X-Agent-Cluster-ID header as a
//     plain cluster UUID, allowing curl/test scripts to call agent endpoints
//     without TLS. Never enable devMode in production.
//
// The extracted cluster UUID is stored on the request context via
// auth.StoreAgentIdentity so handlers can retrieve it with
// auth.AgentIdentityFromContext.
func AgentAuthMiddleware(devMode bool, handle *db.Handle) echo.MiddlewareFunc {
	return agentAuthMiddleware(devMode, dbAgentCertStore{handle: handle})
}

func agentAuthMiddleware(devMode bool, store agentCertStore) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			id, err := authenticateAgent(c, devMode, store)
			if err != nil {
				return err
			}
			ctx := auth.StoreAgentIdentity(c.Request().Context(), auth.AgentIdentity{ClusterID: id})
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
}

// errAgentUnauthorized is returned for every credential failure so a caller
// cannot tell an unknown cluster from a revoked certificate.
var errAgentUnauthorized = echo.NewHTTPError(http.StatusUnauthorized, "agent certificate required")

// authenticateAgent resolves the calling cluster or returns the error the
// middleware should answer with.
func authenticateAgent(c echo.Context, devMode bool, store agentCertStore) (uuid.UUID, error) {
	if id, ok := devModeClusterID(c, devMode); ok {
		return id, nil
	}

	cert := verifiedPeerCertificate(c)
	if cert == nil {
		return uuid.Nil, errAgentUnauthorized
	}
	id, ok := parseAgentCertCN(cert)
	if !ok {
		return uuid.Nil, errAgentUnauthorized
	}

	ctx := c.Request().Context()
	stored, found, err := store.serials(ctx, id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("agent auth: load cluster %s: %w", id, err)
	}
	switch {
	case !found:
		return uuid.Nil, errAgentUnauthorized
	case serialMatches(cert, stored.Active):
		return id, nil
	case serialMatches(cert, stored.Pending):
		return promotePendingCert(ctx, store, id, cert, stored.Pending)
	default:
		return uuid.Nil, errAgentUnauthorized
	}
}

// promotePendingCert activates the renewed certificate cert, whose serial
// is the cluster's pending serial. When a concurrent request promoted it
// first the update matches no row; the certificate is then accepted only if
// it is now the active one.
func promotePendingCert(ctx context.Context, store agentCertStore, id uuid.UUID, cert *x509.Certificate, pending string) (uuid.UUID, error) {
	promoted, err := store.promote(ctx, id, pending, cert.NotAfter)
	if err != nil {
		return uuid.Nil, fmt.Errorf("agent auth: promote renewed certificate for cluster %s: %w", id, err)
	}
	if promoted {
		slog.Info("agent auth: renewed certificate activated", "cluster_id", id, "serial", pending)
		return id, nil
	}
	again, found, err := store.serials(ctx, id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("agent auth: reload cluster %s: %w", id, err)
	}
	if found && serialMatches(cert, again.Active) {
		return id, nil
	}
	return uuid.Nil, errAgentUnauthorized
}

// devModeClusterID reads the X-Agent-Cluster-ID bypass header. It is inert
// unless devMode is true.
func devModeClusterID(c echo.Context, devMode bool) (uuid.UUID, bool) {
	if !devMode {
		return uuid.Nil, false
	}
	hdr := strings.TrimSpace(c.Request().Header.Get(agentClusterIDHeader))
	if hdr == "" {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(hdr)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

// verifiedPeerCertificate returns the client leaf certificate, or nil when
// the connection is not TLS or the certificate was not verified against the
// client CA. The agent listener only completes a handshake with a verified
// certificate; requiring VerifiedChains here keeps the check fail-closed if
// an agent route is ever mounted on a listener that does not verify.
func verifiedPeerCertificate(c echo.Context) *x509.Certificate {
	state := c.Request().TLS
	if state == nil || len(state.PeerCertificates) == 0 || len(state.VerifiedChains) == 0 {
		return nil
	}
	return state.PeerCertificates[0]
}

// parseAgentCertCN extracts the cluster UUID from a certificate whose
// Subject CN is "saidkick-agent-<uuid>".
func parseAgentCertCN(cert *x509.Certificate) (uuid.UUID, bool) {
	cn := cert.Subject.CommonName
	rest, ok := strings.CutPrefix(cn, "saidkick-agent-")
	if !ok {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(rest)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

// serialMatches reports whether cert's serial number equals stored, the
// decimal serial the handshake and the renew route record (big.Int.String).
// Both sides are compared as integers, so formatting such as leading zeros
// cannot cause a false mismatch. An empty or unparsable stored serial never
// matches.
func serialMatches(cert *x509.Certificate, stored string) bool {
	if cert.SerialNumber == nil {
		return false
	}
	want, ok := new(big.Int).SetString(strings.TrimSpace(stored), 10)
	if !ok {
		return false
	}
	return cert.SerialNumber.Cmp(want) == 0
}

// dbAgentCertStore is the agentCertStore backed by the clusters table. It
// uses the read-write pool so a serial changed by the handshake, a renewal
// or a promotion is seen by the very next request.
type dbAgentCertStore struct {
	handle *db.Handle
}

func (s dbAgentCertStore) serials(ctx context.Context, clusterID uuid.UUID) (agentCertSerials, bool, error) {
	var cluster models.Cluster
	err := s.handle.RW.WithContext(ctx).Select("id", "agent_cert_serial", "agent_cert_pending_serial").
		First(&cluster, "id = ?", clusterID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return agentCertSerials{}, false, nil
	}
	if err != nil {
		return agentCertSerials{}, false, err
	}
	var out agentCertSerials
	if cluster.AgentCertSerial != nil {
		out.Active = *cluster.AgentCertSerial
	}
	if cluster.AgentCertPendingSerial != nil {
		out.Pending = *cluster.AgentCertPendingSerial
	}
	return out, true, nil
}

// promote swaps the pending serial in with one conditional UPDATE: the
// WHERE clause on the pending serial makes concurrent promotions safe, as
// only one of them can match the row. agent_cert_expires_at, the older
// expiry column the handshake also writes, is kept in step.
func (s dbAgentCertStore) promote(ctx context.Context, clusterID uuid.UUID, pending string, notAfter time.Time) (bool, error) {
	res := s.handle.RW.WithContext(ctx).Model(&models.Cluster{}).
		Where("id = ? AND agent_cert_pending_serial = ?", clusterID, pending).
		Updates(map[string]any{
			"agent_cert_serial":         gorm.Expr("agent_cert_pending_serial"),
			"agent_cert_pending_serial": nil,
			"agent_cert_not_after":      notAfter.UTC(),
			"agent_cert_expires_at":     notAfter.UTC(),
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}
