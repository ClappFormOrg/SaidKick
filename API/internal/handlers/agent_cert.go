package handlers

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// defaultAgentCertTTL is the agent client certificate lifetime when
// Handlers.AgentCertTTL is unset: 90 days.
const defaultAgentCertTTL = 90 * 24 * time.Hour

// agentCertTTL is the lifetime of the agent certificates h issues.
func (h *Handlers) agentCertTTL() time.Duration {
	if h.AgentCertTTL > 0 {
		return h.AgentCertTTL
	}
	return defaultAgentCertTTL
}

// agentCertCN is the certificate Subject CN of clusterID's agent.
func agentCertCN(clusterID uuid.UUID) string {
	return "saidkick-agent-" + clusterID.String()
}

// AgentRenewCertificateRequest is the body of
// POST /api/v1/clusters/:id/agent/renew.
type AgentRenewCertificateRequest struct {
	CSR string `json:"csr"` // PEM-encoded PKCS#10 request, CN saidkick-agent-<id>
}

// AgentRenewCertificateResponse is the 200 body of
// POST /api/v1/clusters/:id/agent/renew.
type AgentRenewCertificateResponse struct {
	Certificate   string `json:"certificate"`
	CACertificate string `json:"ca_certificate"`
	Serial        string `json:"serial"`    // decimal
	NotAfter      string `json:"not_after"` // RFC 3339
}

// AgentRenewCertificate handles POST /api/v1/clusters/:id/agent/renew on
// the agent mTLS listener. The agent presents its current certificate and a
// CSR for a fresh key; the hub signs a new certificate and records its
// serial as pending. The current certificate keeps working until the agent
// first presents the new one, which AgentAuthMiddleware then promotes
// (ADR-139-2). A pending rotation request is cleared.
//
// Checks, in order: mTLS identity (401), path id matches the certificate
// (403), cluster exists (404), not decommissioned (410), CSR is PEM PKCS#10
// with CN saidkick-agent-<id> (422 invalid_csr), PKI signs (503
// pki_unavailable).
func (h *Handlers) AgentRenewCertificate(c echo.Context) error {
	agent := auth.AgentIdentityFromContext(c.Request().Context())
	if agent == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "agent identity required")
	}
	resp, err := h.renewAgentCertificate(c, agent.ClusterID)
	if err != nil {
		metrics.HubAgentCertRenewalsTotal(metrics.OutcomeFailure).Inc()
		return err
	}
	metrics.HubAgentCertRenewalsTotal(metrics.OutcomeSuccess).Inc()
	return c.JSON(http.StatusOK, resp)
}

// renewAgentCertificate runs every check after the mTLS identity and issues
// the certificate for the authenticated agent of certClusterID.
func (h *Handlers) renewAgentCertificate(c echo.Context, certClusterID uuid.UUID) (AgentRenewCertificateResponse, error) {
	ctx := c.Request().Context()

	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil || clusterID != certClusterID {
		return AgentRenewCertificateResponse{}, apierror.New(http.StatusForbidden, "forbidden",
			"certificate cluster ID does not match path parameter")
	}
	if _, err := h.loadActiveAgentCluster(ctx, clusterID); err != nil {
		return AgentRenewCertificateResponse{}, err
	}

	var req AgentRenewCertificateRequest
	if err := c.Bind(&req); err != nil {
		return AgentRenewCertificateResponse{}, apierror.New(http.StatusBadRequest, "bad_request", "invalid JSON")
	}
	csrPEM := strings.TrimSpace(req.CSR)
	if err := validateRenewalCSR(csrPEM, clusterID); err != nil {
		return AgentRenewCertificateResponse{}, err
	}

	var resp AgentRenewCertificateResponse
	txErr := h.DB.RW.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialise renewals per cluster: a second concurrent renewal waits
		// here and then overwrites the pending serial.
		var locked models.Cluster
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "state").
			First(&locked, "id = ?", clusterID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apierror.New(http.StatusNotFound, "not_found", "cluster not found")
			}
			return err
		}
		if locked.State == models.ClusterDecommissioned {
			return apierror.New(http.StatusGone, "gone", "cluster has been decommissioned")
		}

		certPEM, caPEM, err := h.PKI.SignCSR(ctx, csrPEM, agentCertCN(clusterID), h.agentCertTTL())
		if err != nil {
			slog.Error("agent renew: pki sign csr", "error", err, "cluster_id", clusterID)
			return apierror.New(http.StatusServiceUnavailable, "pki_unavailable",
				"the certificate authority is unavailable; retry later")
		}
		issued, err := parseCertificatePEM(certPEM)
		if err != nil {
			return fmt.Errorf("agent renew: parse signed certificate: %w", err)
		}
		// As in the handshake: the cluster identity comes from the CN, so
		// refuse a certificate whose CN the PKI role took from elsewhere.
		if want := agentCertCN(clusterID); issued.Subject.CommonName != want {
			return fmt.Errorf("agent renew: issued certificate CN %q does not match requested %q "+
				"(set use_csr_common_name=false on the PKI role)", issued.Subject.CommonName, want)
		}

		resp = AgentRenewCertificateResponse{
			Certificate:   certPEM,
			CACertificate: caPEM,
			Serial:        issued.SerialNumber.String(),
			NotAfter:      issued.NotAfter.UTC().Format(time.RFC3339),
		}
		return tx.Model(&models.Cluster{}).Where("id = ?", clusterID).Updates(map[string]any{
			"agent_cert_pending_serial":        resp.Serial,
			"agent_cert_rotation_requested_at": nil,
		}).Error
	})
	if txErr != nil {
		var apiErr *apierror.Error
		if errors.As(txErr, &apiErr) {
			return AgentRenewCertificateResponse{}, apiErr
		}
		return AgentRenewCertificateResponse{}, txErr
	}

	// Best effort: the renewal is already committed.
	_ = h.Audit.Log(ctx, audit.Event{
		Type:        "cluster.credential_renewed",
		SubjectType: "cluster",
		SubjectID:   clusterID,
		Payload: map[string]any{
			"actor":     "agent",
			"serial":    resp.Serial,
			"not_after": resp.NotAfter,
		},
	})
	slog.Info("agent renew: certificate issued, pending first use", "cluster_id", clusterID, "serial", resp.Serial)
	return resp, nil
}

// validateRenewalCSR checks that csrPEM is a PEM-encoded, self-signed
// PKCS#10 request whose Subject CN is the agent CN of clusterID. Every
// failure is 422 invalid_csr.
func validateRenewalCSR(csrPEM string, clusterID uuid.UUID) error {
	invalid := func(msg string) error {
		return apierror.New(http.StatusUnprocessableEntity, "invalid_csr", msg)
	}
	if csrPEM == "" {
		return invalid("csr is required")
	}
	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil {
		return invalid("csr must be a PEM-encoded certificate request")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return invalid("csr is not a valid PKCS#10 certificate request")
	}
	if err := csr.CheckSignature(); err != nil {
		return invalid("csr signature does not verify")
	}
	if want := agentCertCN(clusterID); csr.Subject.CommonName != want {
		return invalid("csr common name must be " + want)
	}
	return nil
}

// parseCertificatePEM decodes the first PEM block of certPEM as an X.509
// certificate.
func parseCertificatePEM(certPEM string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	return x509.ParseCertificate(block.Bytes)
}

// RotateClusterCredentialResponse is the 202 body of
// POST /api/v1/clusters/:id/rotate-credential.
type RotateClusterCredentialResponse struct {
	RotationRequestedAt string `json:"rotation_requested_at"` // RFC 3339
}

// RotateClusterCredential handles POST /api/v1/clusters/:id/rotate-credential.
// It records a rotation request; the next heartbeat response tells the agent
// to renew its certificate whatever its remaining validity, and the renewal
// clears the request. A cluster admin of the owning organisation or a
// platform admin may call it; a caller who cannot see the cluster gets 404,
// one who can see it but does not own it gets 403.
func (h *Handlers) RotateClusterCredential(c echo.Context) error {
	ctx := c.Request().Context()
	p := auth.PrincipalFromContext(ctx)
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}

	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return apierror.New(http.StatusNotFound, "not_found", "cluster not found")
	}
	var cluster models.Cluster
	if err := h.DB.RW.WithContext(ctx).First(&cluster, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apierror.New(http.StatusNotFound, "not_found", "cluster not found")
		}
		return err
	}
	if err := h.authorizeClusterWrite(ctx, p, cluster); err != nil {
		return err
	}

	requestedAt := time.Now().UTC().Truncate(time.Second)
	if err := h.DB.RW.WithContext(ctx).Model(&models.Cluster{}).Where("id = ?", cluster.ID).
		Update("agent_cert_rotation_requested_at", requestedAt).Error; err != nil {
		return err
	}

	_ = h.Audit.Log(ctx, audit.Event{
		Type:                "cluster.credential_rotation_requested",
		ActorUserID:         &p.UserID,
		ActorOrganizationID: &p.OrganizationID,
		SubjectType:         "cluster",
		SubjectID:           cluster.ID,
		Payload:             map[string]any{"rotation_requested_at": requestedAt.Format(time.RFC3339)},
	})

	return c.JSON(http.StatusAccepted, RotateClusterCredentialResponse{
		RotationRequestedAt: requestedAt.Format(time.RFC3339),
	})
}
