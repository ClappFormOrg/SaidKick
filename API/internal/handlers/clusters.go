package handlers

import (
	"cmp"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/config"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/vault"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

// joinTokenClaims is the JWT payload for one-time cluster join-tokens (HS256,
// 1-hour TTL). Subject holds the cluster ID; Scope mirrors Cluster.Scope.
type joinTokenClaims struct {
	jwt.RegisteredClaims
	Scope string `json:"scope"`
}

// errJoinTokenAlreadyUsed is the sentinel returned when the transaction finds
// the JTI already present in used_join_tokens.
var errJoinTokenAlreadyUsed = errors.New("handlers: join token already used")

// ListClusters returns clusters visible to the caller:
//   - PlatformAdmin: all clusters
//   - Others: public + own-org + restricted clusters the caller's org owns or
//     has a grant for
func (h *Handlers) ListClusters(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}

	scopeFilter := strings.TrimSpace(c.QueryParam("scope"))
	stateFilter := strings.TrimSpace(c.QueryParam("state"))

	tx := h.DB.RW.WithContext(c.Request().Context()).Model(&models.Cluster{})
	if !p.Has(models.RolePlatformAdmin) {
		tx = tx.Where(
			`scope = 'public'
			 OR (scope = 'org' AND owning_organization_id = ?)
			 OR (scope = 'restricted' AND (
			       owning_organization_id = ?
			       OR EXISTS (
			         SELECT 1 FROM cluster_access_grants
			         WHERE cluster_id = clusters.id AND organization_id = ?
			       )
			     ))`,
			p.OrganizationID, p.OrganizationID, p.OrganizationID,
		)
	}
	if scopeFilter != "" {
		if !slices.Contains(models.AllClusterScopes, models.ClusterScope(scopeFilter)) {
			return apierror.New(http.StatusBadRequest, "bad_request", "invalid scope filter value")
		}
		tx = tx.Where("scope = ?", scopeFilter)
	}
	if stateFilter != "" {
		if !slices.Contains(models.AllClusterStates, models.ClusterState(stateFilter)) {
			return apierror.New(http.StatusBadRequest, "bad_request", "invalid state filter value")
		}
		tx = tx.Where("state = ?", stateFilter)
	}

	var rows []models.Cluster
	if err := tx.Order("name ASC").Find(&rows).Error; err != nil {
		return err
	}

	out := make([]map[string]any, 0, len(rows))
	for _, cl := range rows {
		out = append(out, clusterToMap(cl))
	}
	return c.JSON(http.StatusOK, map[string]any{"items": out})
}

// CreateClusterRequest is the body of POST /api/v1/clusters.
type CreateClusterRequest struct {
	Name                    string     `json:"name"`
	Description             string     `json:"description"`
	Scope                   string     `json:"scope"`         // "public", "org", or "restricted"
	OwningOrgID             *uuid.UUID `json:"owning_org_id"` // required for "org"/"restricted" when caller is PlatformAdmin
	OrchestratorClusterName string     `json:"orchestrator_cluster_name"`
	IngressClassName        string     `json:"ingress_class_name"` // e.g. "nginx", "traefik"
	BaseDomain              string     `json:"base_domain"`        // e.g. "apps.example.nl"
}

// CreateCluster registers a new cluster and returns a one-time join token.
func (h *Handlers) CreateCluster(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}

	var req CreateClusterRequest
	if err := c.Bind(&req); err != nil {
		return apierror.New(http.StatusBadRequest, "bad_request", "invalid JSON")
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		return apierror.NewValidation("validation failed", []apierror.FieldError{
			{Path: "name", Code: "required", Message: "name is required"},
		})
	}

	// Determine scope and owning org from role + request.
	scope := models.ClusterScope(strings.TrimSpace(req.Scope))
	var owningOrg *uuid.UUID
	switch {
	case p.Has(models.RolePlatformAdmin):
		switch scope {
		case "", models.ClusterScopePublic:
			scope = models.ClusterScopePublic
		case models.ClusterScopeOrg, models.ClusterScopeRestricted:
			if req.OwningOrgID == nil {
				return apierror.NewValidation("invalid request", []apierror.FieldError{
					{Path: "owning_org_id", Code: "required", Message: "owning_org_id is required for org/restricted scope"},
				})
			}
			owningOrg = req.OwningOrgID
		default:
			return apierror.NewValidation("invalid scope", []apierror.FieldError{
				{Path: "scope", Code: "invalid", Message: "scope must be 'public', 'org', or 'restricted'"},
			})
		}
	case p.Has(models.RoleClusterAdmin):
		// ClusterAdmin may only register clusters scoped to their own org.
		if scope != "" && scope != models.ClusterScopeOrg {
			return apierror.New(http.StatusForbidden, "forbidden",
				"cluster_admin may only register org-scoped clusters")
		}
		scope = models.ClusterScopeOrg
		owningOrg = &p.OrganizationID
	default:
		return apierror.New(http.StatusForbidden, "forbidden", "missing required role")
	}

	if owningOrg != nil {
		if err := h.requireOwningOrg(c.Request().Context(), *owningOrg); err != nil {
			return err
		}
	}

	orchName := strings.TrimSpace(req.OrchestratorClusterName)
	cluster := models.Cluster{
		Name:                    name,
		Description:             stringPtr(strings.TrimSpace(req.Description)),
		Scope:                   scope,
		OwningOrganizationID:    owningOrg,
		State:                   models.ClusterPendingRegistration,
		OrchestratorClusterName: orchName,
		IngressClassName:        stringPtr(strings.TrimSpace(req.IngressClassName)),
		BaseDomain:              stringPtr(strings.TrimSpace(req.BaseDomain)),
	}

	if err := h.DB.RW.WithContext(c.Request().Context()).Create(&cluster).Error; err != nil {
		return err
	}

	// Audit-log the registration (best effort).
	_ = h.Audit.Log(c.Request().Context(), audit.Event{
		Type:                "cluster.registered",
		ActorUserID:         &p.UserID,
		ActorOrganizationID: &p.OrganizationID,
		SubjectType:         "cluster",
		SubjectID:           cluster.ID,
		Payload: map[string]any{
			"name":  cluster.Name,
			"scope": cluster.Scope,
		},
	})

	// Mint a one-time HS256 join-token JWT (1-hour TTL, single-use via JTI).
	jti, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("mint join token jti: %w", err)
	}
	now := time.Now()
	joinClaims := joinTokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti.String(),
			Subject:   cluster.ID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
		Scope: string(cluster.Scope),
	}
	joinToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, joinClaims).SignedString(h.JoinSecret)
	if err != nil {
		return fmt.Errorf("sign join token: %w", err)
	}

	return c.JSON(http.StatusCreated, map[string]any{
		"cluster": clusterToMap(cluster),
		"installer": clusterInstaller(
			agentInstallCommand(h.agentInstallFor(cluster.ID.String(), joinToken)),
			joinToken,
		),
	})
}

// clusterInstaller builds the installer block of the POST /clusters response.
// steps lists the commands to run on the spoke in order: External Secrets
// Operator first, because the agent chart's SecretStore needs its CRDs, then
// the agent. helm_command repeats the agent step for older clients.
func clusterInstaller(agentCommand, joinToken string) map[string]any {
	return map[string]any{
		"steps": []map[string]string{
			{"name": "external-secrets", "command": esoInstallCommand()},
			{"name": "saidkick-agent", "command": agentCommand},
		},
		"helm_command":   agentCommand,
		"join_token_jwt": joinToken,
	}
}

// HandshakeClusterRequest is the body of POST /clusters/:id/handshake.
type HandshakeClusterRequest struct {
	AgentVersion string `json:"agent_version"`
	CSR          string `json:"csr"` // PEM-encoded PKCS#10 certificate request
}

// HandshakeCluster exchanges a one-time cluster join-token for a 90-day mTLS
// client certificate signed by the platform's Vault PKI engine.
//
// The sAId-Kick agent calls this endpoint exactly once after running the
// installer. On success the cluster transitions to state ready.
//
// Auth:     Authorization: Bearer <join_token_jwt>
// Body:     { "agent_version": "...", "csr": "<PEM PKCS#10>" }
// Response: { "client_cert_pem": "...", "ca_pem": "...", "hub_endpoint": "..." }
// Errors:   401 invalid/expired token · 410 already used · 503 PKI not configured
func (h *Handlers) HandshakeCluster(c echo.Context) error {
	ctx := c.Request().Context()

	// 1. Extract and verify the Bearer join-token.
	authHdr := c.Request().Header.Get("Authorization")
	tokenStr, ok := strings.CutPrefix(authHdr, "Bearer ")
	if !ok || strings.TrimSpace(tokenStr) == "" {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "Bearer join-token required")
	}
	var claims joinTokenClaims
	_, err := jwt.ParseWithClaims(tokenStr, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return h.JoinSecret, nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || claims.Subject == "" || claims.ID == "" {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "invalid or expired join token")
	}

	// 2. Confirm the token targets this cluster.
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return apierror.New(http.StatusNotFound, "not_found", "cluster not found")
	}
	if claims.Subject != clusterID.String() {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "join token is for a different cluster")
	}

	// 3. Load the cluster.
	var cluster models.Cluster
	if err := h.DB.RW.WithContext(ctx).First(&cluster, "id = ?", clusterID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apierror.New(http.StatusNotFound, "not_found", "cluster not found")
		}
		return err
	}
	if cluster.State == models.ClusterDecommissioned {
		return apierror.New(http.StatusGone, "gone", "cluster has been decommissioned")
	}

	// 4. Bind and validate the request body.
	var req HandshakeClusterRequest
	if err := c.Bind(&req); err != nil {
		return apierror.New(http.StatusBadRequest, "bad_request", "invalid JSON")
	}
	if strings.TrimSpace(req.CSR) == "" {
		return apierror.NewValidation("validation failed", []apierror.FieldError{
			{Path: "csr", Code: "required", Message: "csr is required"},
		})
	}
	csrBlock, _ := pem.Decode([]byte(req.CSR))
	if csrBlock == nil {
		return apierror.NewValidation("validation failed", []apierror.FieldError{
			{Path: "csr", Code: "invalid_pem", Message: "csr must be a PEM-encoded certificate request"},
		})
	}
	if _, err := x509.ParseCertificateRequest(csrBlock.Bytes); err != nil {
		return apierror.NewValidation("validation failed", []apierror.FieldError{
			{Path: "csr", Code: "invalid_csr", Message: "csr is not a valid PKCS#10 certificate request"},
		})
	}

	// 5. Sign the CSR via Vault PKI (90-day cert, CN = saidkick-agent-<id>).
	cn := agentCertCN(clusterID)
	certPEM, caPEM, err := h.PKI.SignCSR(ctx, strings.TrimSpace(req.CSR), cn, h.agentCertTTL())
	if err != nil {
		if errors.Is(err, vault.ErrPKINotConfigured) {
			return apierror.New(http.StatusServiceUnavailable, "vault_pki_not_configured",
				"Vault PKI is not configured; set VAULT_PKI_MOUNT and VAULT_PKI_ROLE")
		}
		return fmt.Errorf("pki sign csr: %w", err)
	}

	// Parse the signed cert to check what the PKI issued and to record its
	// serial and expiry. A certificate that cannot be parsed cannot be
	// vouched for, so this fails the handshake.
	certBlock, _ := pem.Decode([]byte(certPEM))
	if certBlock == nil {
		return errors.New("pki sign csr: issued certificate is not PEM-encoded")
	}
	issued, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return fmt.Errorf("pki sign csr: parse issued certificate: %w", err)
	}

	// The agent's cluster identity comes from this CN. A PKI role with
	// use_csr_common_name=true (Vault's default) lets the CSR's CN win over
	// the one requested here, so refuse any certificate whose CN differs,
	// whatever the role is configured to do.
	if issued.Subject.CommonName != cn {
		return fmt.Errorf(
			"pki sign csr: issued certificate CN %q does not match requested %q "+
				"(set use_csr_common_name=false on the PKI role)",
			issued.Subject.CommonName, cn)
	}

	certSerial := issued.SerialNumber.String()
	certExpiresAt := issued.NotAfter

	// 6. Burn the JTI and update the cluster in one transaction.
	agentVersion := strings.TrimSpace(req.AgentVersion)
	registeredAt := time.Now().UTC()
	txErr := h.DB.RW.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		used := models.UsedJoinToken{JTI: claims.ID, ClusterID: clusterID}
		if err := tx.Create(&used).Error; err != nil {
			// PostgreSQL unique violation (23505) means the JTI was already consumed.
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return errJoinTokenAlreadyUsed
			}
			return err
		}
		// A handshake starts a fresh credential: any renewed certificate
		// still pending first use is dropped.
		return tx.Model(&cluster).Updates(map[string]any{
			"state":                     string(models.ClusterReady),
			"agent_version":             agentVersion,
			"agent_cert_serial":         certSerial,
			"agent_cert_pending_serial": nil,
			"agent_cert_expires_at":     certExpiresAt,
			"agent_cert_not_after":      certExpiresAt,
			"registered_at":             registeredAt,
			"last_heartbeat_at":         registeredAt,
		}).Error
	})
	if txErr != nil {
		if errors.Is(txErr, errJoinTokenAlreadyUsed) {
			return apierror.New(http.StatusGone, "join_token_already_used",
				"this join token has already been used")
		}
		return txErr
	}

	// 7. Audit log (best effort — primary action already committed).
	_ = h.Audit.Log(ctx, audit.Event{
		Type:        "cluster.agent_bootstrapped",
		SubjectType: "cluster",
		SubjectID:   clusterID,
		Payload: map[string]any{
			"agent_version":   agentVersion,
			"cert_serial":     certSerial,
			"cert_expires_at": certExpiresAt,
		},
	})

	return c.JSON(http.StatusOK, map[string]any{
		"client_cert_pem": certPEM,
		"ca_pem":          caPEM,
		"hub_endpoint":    h.HubEndpoint,
	})
}

// ChangeClusterScopeRequest is the body of POST /clusters/:id/scope.
type ChangeClusterScopeRequest struct {
	NewScope    string     `json:"new_scope"`     // "public", "org", or "restricted"
	OwningOrgID *uuid.UUID `json:"owning_org_id"` // required when new_scope is "org" or "restricted"
	Reason      string     `json:"reason"`
}

// ChangeClusterScope transitions a cluster between scope tiers. PlatformAdmin only.
// Writes an AuditLog entry with the before/after scope and the supplied reason.
func (h *Handlers) ChangeClusterScope(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}
	// Mirrors the route's role gate so a routing mistake cannot expose it.
	if !p.Has(models.RolePlatformAdmin) {
		return apierror.New(http.StatusForbidden, "forbidden", "missing required role")
	}

	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return apierror.New(http.StatusNotFound, "not_found", "cluster not found")
	}

	var req ChangeClusterScopeRequest
	if err := c.Bind(&req); err != nil {
		return apierror.New(http.StatusBadRequest, "bad_request", "invalid JSON")
	}
	if strings.TrimSpace(req.Reason) == "" {
		return apierror.NewValidation("validation failed", []apierror.FieldError{
			{Path: "reason", Code: "required", Message: "reason is required for scope changes"},
		})
	}

	newScope := models.ClusterScope(strings.TrimSpace(req.NewScope))
	var newOwning *uuid.UUID
	switch newScope {
	case models.ClusterScopePublic:
		// no owning org for public clusters
	case models.ClusterScopeOrg, models.ClusterScopeRestricted:
		if req.OwningOrgID == nil {
			return apierror.NewValidation("invalid request", []apierror.FieldError{
				{Path: "owning_org_id", Code: "required", Message: "owning_org_id is required for org/restricted scope"},
			})
		}
		newOwning = req.OwningOrgID
	default:
		return apierror.NewValidation("invalid scope", []apierror.FieldError{
			{Path: "new_scope", Code: "invalid", Message: "scope must be 'public', 'org', or 'restricted'"},
		})
	}

	ctx := c.Request().Context()
	if newOwning != nil {
		if err := h.requireOwningOrg(ctx, *newOwning); err != nil {
			return err
		}
	}

	var before, after models.Cluster
	if err := h.DB.RW.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&before, "id = ?", id).Error; err != nil {
			return err
		}
		if before.Scope == newScope && equalUUIDs(before.OwningOrganizationID, newOwning) {
			return apierror.New(http.StatusConflict, "no_op", "cluster is already in that scope")
		}
		after = before
		after.Scope = newScope
		after.OwningOrganizationID = newOwning
		return tx.Save(&after).Error
	}); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apierror.New(http.StatusNotFound, "not_found", "cluster not found")
		}
		var apiErr *apierror.Error
		if errors.As(err, &apiErr) {
			return apiErr
		}
		return err
	}

	_ = h.Audit.Log(ctx, audit.Event{
		Type:                "cluster.scope_changed",
		ActorUserID:         &p.UserID,
		ActorOrganizationID: &p.OrganizationID,
		SubjectType:         "cluster",
		SubjectID:           after.ID,
		Payload: map[string]any{
			"before_scope": string(before.Scope),
			"after_scope":  string(after.Scope),
			"reason":       strings.TrimSpace(req.Reason),
		},
	})

	return c.JSON(http.StatusOK, clusterToMap(after))
}

// GrantClusterAccessRequest is the body of POST /clusters/:id/access.
type GrantClusterAccessRequest struct {
	OrganizationID uuid.UUID `json:"organization_id"`
}

// GrantClusterAccess adds an org to the allow-list of a restricted cluster.
// PlatformAdmin only. Idempotent — granting access to an org that already has
// it returns 200 without creating a duplicate row.
func (h *Handlers) GrantClusterAccess(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}
	// Mirrors the route's role gate so a routing mistake cannot expose it.
	if !p.Has(models.RolePlatformAdmin) {
		return apierror.New(http.StatusForbidden, "forbidden", "missing required role")
	}

	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return apierror.New(http.StatusNotFound, "not_found", "cluster not found")
	}

	var req GrantClusterAccessRequest
	if err := c.Bind(&req); err != nil {
		return apierror.New(http.StatusBadRequest, "bad_request", "invalid JSON")
	}
	if req.OrganizationID == uuid.Nil {
		return apierror.NewValidation("validation failed", []apierror.FieldError{
			{Path: "organization_id", Code: "required", Message: "organization_id is required"},
		})
	}

	ctx := c.Request().Context()
	var cluster models.Cluster
	if err := h.DB.RW.WithContext(ctx).First(&cluster, "id = ?", clusterID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apierror.New(http.StatusNotFound, "not_found", "cluster not found")
		}
		return err
	}
	if cluster.Scope != models.ClusterScopeRestricted {
		return apierror.New(http.StatusConflict, "wrong_scope",
			"access grants are only applicable to restricted-scope clusters")
	}

	var orgCount int64
	if err := h.DB.RW.WithContext(ctx).Model(&models.Organization{}).
		Where("id = ?", req.OrganizationID).Count(&orgCount).Error; err != nil {
		return err
	}
	if orgCount == 0 {
		return apierror.NewValidation("validation failed", []apierror.FieldError{
			{Path: "organization_id", Code: "not_found", Message: "organization_id does not match an organisation"},
		})
	}

	grant := models.ClusterAccessGrant{
		ClusterID:      clusterID,
		OrganizationID: req.OrganizationID,
		GrantedBy:      p.UserID,
	}
	if err := h.DB.RW.WithContext(ctx).
		Where(models.ClusterAccessGrant{ClusterID: clusterID, OrganizationID: req.OrganizationID}).
		FirstOrCreate(&grant).Error; err != nil {
		return err
	}

	_ = h.Audit.Log(ctx, audit.Event{
		Type:                "cluster.access_granted",
		ActorUserID:         &p.UserID,
		ActorOrganizationID: &p.OrganizationID,
		SubjectType:         "cluster",
		SubjectID:           clusterID,
		Payload: map[string]any{
			"organization_id": req.OrganizationID.String(),
		},
	})

	return c.JSON(http.StatusOK, map[string]any{
		"cluster_id":      grant.ClusterID.String(),
		"organization_id": grant.OrganizationID.String(),
		"granted_at":      grant.GrantedAt,
		"granted_by":      grant.GrantedBy.String(),
	})
}

// RevokeClusterAccess removes an org's access grant from a restricted cluster.
// PlatformAdmin only. Returns 204 whether or not the grant existed.
func (h *Handlers) RevokeClusterAccess(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}
	// Mirrors the route's role gate so a routing mistake cannot expose it.
	if !p.Has(models.RolePlatformAdmin) {
		return apierror.New(http.StatusForbidden, "forbidden", "missing required role")
	}

	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return apierror.New(http.StatusNotFound, "not_found", "cluster not found")
	}
	orgID, err := uuid.Parse(c.Param("org_id"))
	if err != nil {
		return apierror.New(http.StatusBadRequest, "bad_request", "invalid organization_id")
	}

	ctx := c.Request().Context()
	if err := h.DB.RW.WithContext(ctx).
		Where("cluster_id = ? AND organization_id = ?", clusterID, orgID).
		Delete(&models.ClusterAccessGrant{}).Error; err != nil {
		return err
	}

	_ = h.Audit.Log(ctx, audit.Event{
		Type:                "cluster.access_revoked",
		ActorUserID:         &p.UserID,
		ActorOrganizationID: &p.OrganizationID,
		SubjectType:         "cluster",
		SubjectID:           clusterID,
		Payload:             map[string]any{"organization_id": orgID.String()},
	})

	return c.NoContent(http.StatusNoContent)
}

// GetCluster returns a single cluster visible to the caller.
// The visibility rules mirror ListClusters: PlatformAdmin sees everything;
// others see public + own-org + restricted clusters they own or have a grant for.
// Returns 404 (not 403) when the cluster exists but the caller has no access,
// to avoid leaking the existence of restricted clusters.
func (h *Handlers) GetCluster(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}

	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return apierror.New(http.StatusNotFound, "not_found", "cluster not found")
	}

	ctx := c.Request().Context()
	var cluster models.Cluster
	if err := h.DB.RW.WithContext(ctx).First(&cluster, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apierror.New(http.StatusNotFound, "not_found", "cluster not found")
		}
		return err
	}

	if !p.Has(models.RolePlatformAdmin) {
		visible, err := h.clusterAccessibleToOrg(ctx, cluster, p.OrganizationID)
		if err != nil {
			return err
		}
		if !visible {
			return apierror.New(http.StatusNotFound, "not_found", "cluster not found")
		}
	}

	return c.JSON(http.StatusOK, clusterDetailToMap(cluster))
}

// ListClusterAccessGrants returns all access grants for a restricted cluster.
// PlatformAdmin only. Includes the org display_name alongside each grant.
func (h *Handlers) ListClusterAccessGrants(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}
	// Mirrors the route's role gate so a routing mistake cannot expose it.
	if !p.Has(models.RolePlatformAdmin) {
		return apierror.New(http.StatusForbidden, "forbidden", "missing required role")
	}

	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return apierror.New(http.StatusNotFound, "not_found", "cluster not found")
	}

	ctx := c.Request().Context()
	var grants []models.ClusterAccessGrant
	if err := h.DB.RW.WithContext(ctx).
		Where("cluster_id = ?", id).
		Order("granted_at ASC").
		Find(&grants).Error; err != nil {
		return err
	}

	orgIDs := make([]uuid.UUID, 0, len(grants))
	for _, g := range grants {
		orgIDs = append(orgIDs, g.OrganizationID)
	}
	orgNames := make(map[uuid.UUID]string, len(orgIDs))
	if len(orgIDs) > 0 {
		var orgs []models.Organization
		if err := h.DB.RW.WithContext(ctx).Where("id IN ?", orgIDs).Find(&orgs).Error; err == nil {
			for _, o := range orgs {
				orgNames[o.ID] = o.DisplayName
			}
		}
	}

	out := make([]map[string]any, 0, len(grants))
	for _, g := range grants {
		out = append(out, map[string]any{
			"cluster_id":        g.ClusterID.String(),
			"organization_id":   g.OrganizationID.String(),
			"organization_name": orgNames[g.OrganizationID],
			"granted_at":        g.GrantedAt,
			"granted_by":        g.GrantedBy.String(),
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"items": out})
}

// equalUUIDs compares two nullable UUID pointers.
func equalUUIDs(a, b *uuid.UUID) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// DeleteCluster removes a cluster. Refuses if any non-terminal deployments
// exist; the caller must drain them first.
func (h *Handlers) DeleteCluster(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}

	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return apierror.New(http.StatusNotFound, "not_found", "cluster not found")
	}

	ctx := c.Request().Context()

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

	// Refuse if active deployments exist.
	var active int64
	if err := h.DB.RW.WithContext(ctx).
		Model(&models.Deployment{}).
		Where("cluster_id = ? AND state NOT IN ('deleted','failed_vault','failed_orchestrator','failed_secret_sync','failed_render')", cluster.ID).
		Count(&active).Error; err != nil {
		return err
	}
	if active > 0 {
		return apierror.New(http.StatusConflict, "active_deployments_blocking",
			fmt.Sprintf("cannot delete cluster — %d active deployments must be drained first", active))
	}

	if err := h.DB.RW.WithContext(ctx).Delete(&cluster).Error; err != nil {
		return err
	}

	// Drop any cached Vault AppRole token for this cluster so it cannot be
	// reused after deletion.
	h.Secrets.InvalidateCluster(cluster.ID)

	_ = h.Audit.Log(ctx, audit.Event{
		Type:                "cluster.deleted",
		ActorUserID:         &p.UserID,
		ActorOrganizationID: &p.OrganizationID,
		SubjectType:         "cluster",
		SubjectID:           cluster.ID,
		Payload:             map[string]any{"name": cluster.Name, "scope": cluster.Scope},
	})

	return c.NoContent(http.StatusNoContent)
}

// authorizeClusterWrite guards PatchCluster and DeleteCluster. A platform
// admin may change any cluster; a cluster admin only one their organisation
// owns. Any other caller who cannot see the cluster through GetCluster gets
// the same 404, so restricted and org clusters cannot be probed; one who can
// see it but does not own it (a public cluster, say) gets 403.
func (h *Handlers) authorizeClusterWrite(ctx context.Context, p *auth.Principal, cluster models.Cluster) error {
	if p.Has(models.RolePlatformAdmin) {
		return nil
	}
	// Mirrors the route's role gate so a routing mistake cannot expose it.
	if !p.Has(models.RoleClusterAdmin) {
		return apierror.New(http.StatusForbidden, "forbidden", "missing required role")
	}
	if cluster.OwningOrganizationID != nil && *cluster.OwningOrganizationID == p.OrganizationID {
		return nil
	}
	visible, err := h.clusterAccessibleToOrg(ctx, cluster, p.OrganizationID)
	if err != nil {
		return err
	}
	if !visible {
		return apierror.New(http.StatusNotFound, "not_found", "cluster not found")
	}
	return apierror.New(http.StatusForbidden, "forbidden", "you do not own this cluster")
}

// clusterToMap is the canonical wire representation of a Cluster.
func clusterToMap(cl models.Cluster) map[string]any {
	out := map[string]any{
		"id":                        cl.ID.String(),
		"name":                      cl.Name,
		"orchestrator_cluster_name": cl.OrchestratorClusterName,
		"description":               deref(cl.Description),
		"scope":                     cl.Scope,
		"state":                     string(cl.State),
		"agent_version":             deref(cl.AgentVersion),
		"last_heartbeat_at":         cl.LastHeartbeatAt,
		"registered_at":             cl.RegisteredAt,
	}
	if cl.OwningOrganizationID != nil {
		out["owning_organization_id"] = cl.OwningOrganizationID.String()
	}
	if cl.IngressClassName != nil {
		out["ingress_class_name"] = *cl.IngressClassName
	}
	if cl.BaseDomain != nil {
		out["base_domain"] = *cl.BaseDomain
	}
	return out
}

// clusterDetailToMap extends clusterToMap with cert info, Argo CD project,
// and ingress defaults.
func clusterDetailToMap(cl models.Cluster) map[string]any {
	out := clusterToMap(cl)
	out["agent_cert_serial"] = deref(cl.AgentCertSerial)
	if cl.AgentCertExpiresAt != nil {
		out["agent_cert_expires_at"] = cl.AgentCertExpiresAt
	}
	// Certificate health for anyone who can see the cluster: the active
	// certificate's expiry and a pending admin-requested rotation. null
	// when unknown or not requested.
	out["agent_cert_not_after"] = cl.AgentCertNotAfter
	out["agent_cert_rotation_requested_at"] = cl.AgentCertRotationRequestedAt
	if cl.ArgoCDProjectName != nil && *cl.ArgoCDProjectName != "" {
		out["argocd_project_name"] = *cl.ArgoCDProjectName
	}
	if cl.IngressClassName != nil {
		out["ingress_class_name"] = *cl.IngressClassName
	}
	if cl.BaseDomain != nil {
		out["base_domain"] = *cl.BaseDomain
	}
	return out
}

// PatchClusterRequest is the body of PATCH /api/v1/clusters/:id.
// All fields are optional; only non-nil values are applied.
type PatchClusterRequest struct {
	IngressClassName *string `json:"ingress_class_name"`
	BaseDomain       *string `json:"base_domain"`
}

// PatchCluster updates mutable cluster settings. Currently exposes only the
// ingress defaults (ingress_class_name, base_domain). ClusterAdmin (own org)
// or PlatformAdmin.
func (h *Handlers) PatchCluster(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}

	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return apierror.New(http.StatusNotFound, "not_found", "cluster not found")
	}

	var req PatchClusterRequest
	if err := c.Bind(&req); err != nil {
		return apierror.New(http.StatusBadRequest, "bad_request", "invalid JSON")
	}

	ctx := c.Request().Context()
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

	updates := map[string]any{}
	if req.IngressClassName != nil {
		v := strings.TrimSpace(*req.IngressClassName)
		updates["ingress_class_name"] = stringPtr(v)
	}
	if req.BaseDomain != nil {
		v := strings.TrimSpace(*req.BaseDomain)
		updates["base_domain"] = stringPtr(v)
	}

	if len(updates) == 0 {
		return c.JSON(http.StatusOK, clusterDetailToMap(cluster))
	}

	if err := h.DB.RW.WithContext(ctx).Model(&cluster).Updates(updates).Error; err != nil {
		return err
	}

	_ = h.Audit.Log(ctx, audit.Event{
		Type:                "cluster.ingress_updated",
		ActorUserID:         &p.UserID,
		ActorOrganizationID: &p.OrganizationID,
		SubjectType:         "cluster",
		SubjectID:           cluster.ID,
		Payload:             updates,
	})

	return c.JSON(http.StatusOK, clusterDetailToMap(cluster))
}

// esoChartVersion is the External Secrets Operator chart version the agent
// chart is tested against (scripts/kind-e2e/run.sh installs it from the
// printed step) and documents as its prerequisite
// (charts/saidkick-agent/README.md).
const esoChartVersion = "0.20.4"

// esoInstallCommand builds the helm command that installs External Secrets
// Operator, with its CRDs, on a spoke. --wait holds the command until ESO's
// webhook serves, so the agent step after it can create its SecretStore.
func esoInstallCommand() string {
	return strings.Join([]string{
		"helm install external-secrets external-secrets --repo https://charts.external-secrets.io",
		"--version " + esoChartVersion,
		"--namespace external-secrets --create-namespace",
		"--wait",
	}, " \\\n  ")
}

// agentInstall holds the values the agent install command is built from.
type agentInstall struct {
	// ChartRepo is the OCI repository holding the saidkick-agent chart
	// (AGENT_CHART_REPO); empty falls back to config.DefaultAgentChartRepo.
	ChartRepo string
	// ChartVersion is printed as --version (AGENT_CHART_VERSION); empty
	// leaves the flag out.
	ChartVersion string
	// HubURL is the hub's main API base URL, used for the handshake.
	HubURL string
	// HubAgentURL is the base URL of the hub's agent mTLS listener. It is
	// printed only when set; the agent falls back to HubURL otherwise.
	HubAgentURL string
	ClusterID   string
	JoinToken   string
	// VaultMount is the KV v2 mount; empty means "kv".
	VaultMount string
}

// agentInstallFor returns the install values for a newly registered cluster.
func (h *Handlers) agentInstallFor(clusterID, joinToken string) agentInstall {
	return agentInstall{
		ChartRepo:    h.AgentChartRepo,
		ChartVersion: h.AgentChartVersion,
		HubURL:       h.HubEndpoint,
		HubAgentURL:  h.HubAgentEndpoint,
		ClusterID:    clusterID,
		JoinToken:    joinToken,
		VaultMount:   h.VaultMount,
	}
}

// agentInstallCommand builds the helm install command printed after cluster
// registration. Keys match charts/saidkick-agent/values.yaml. Values the hub
// cannot know (Argo CD and Vault access on the spoke) are left as shell
// variables the operator exports before running the command.
func agentInstallCommand(in agentInstall) string {
	hubURL := cmp.Or(in.HubURL, `"$HUB_URL"`)
	repo := strings.TrimRight(cmp.Or(in.ChartRepo, config.DefaultAgentChartRepo), "/")
	install := "helm install saidkick-agent " + repo + "/saidkick-agent"
	if in.ChartVersion != "" {
		install += " --version " + in.ChartVersion
	}
	lines := []string{
		install,
		"--namespace saidkick-system --create-namespace",
		"--set agent.hubUrl=" + hubURL,
	}
	if in.HubAgentURL != "" {
		lines = append(lines, "--set agent.hubAgentUrl="+in.HubAgentURL)
	}
	return strings.Join(append(lines,
		"--set agent.clusterID="+in.ClusterID,
		"--set agent.joinToken="+in.JoinToken,
		`--set agent.argocd.serverUrl="$ARGOCD_SERVER_URL"`,
		`--set agent.argocd.authToken="$ARGOCD_AUTH_TOKEN"`,
		`--set vault.addr="$VAULT_ADDR"`,
		"--set vault.mount="+cmp.Or(in.VaultMount, "kv"),
		`--set vault.token="$VAULT_TOKEN"`,
	), " \\\n  ")
}

func stringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// clusterAccessibleToOrg reports whether orgID can access cluster based on its
// scope. Callers that have already verified RolePlatformAdmin can skip this.
func (h *Handlers) clusterAccessibleToOrg(ctx context.Context, cluster models.Cluster, orgID uuid.UUID) (bool, error) {
	switch cluster.Scope {
	case models.ClusterScopePublic:
		return true, nil
	case models.ClusterScopeOrg:
		return cluster.OwningOrganizationID != nil && *cluster.OwningOrganizationID == orgID, nil
	case models.ClusterScopeRestricted:
		// The owner always sees its restricted cluster; other orgs need a grant.
		if cluster.OwningOrganizationID != nil && *cluster.OwningOrganizationID == orgID {
			return true, nil
		}
		var count int64
		if err := h.DB.RW.WithContext(ctx).Model(&models.ClusterAccessGrant{}).
			Where("cluster_id = ? AND organization_id = ?", cluster.ID, orgID).
			Count(&count).Error; err != nil {
			return false, err
		}
		return count > 0, nil
	default:
		return false, nil
	}
}
