package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/deploystate"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/orchestrator"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/secrets"
	"github.com/ClappFormOrg/SaidKick/API/internal/valuesschema"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// ListDeployments returns deployments visible to the caller:
//   - PlatformAdmin: all
//   - OrgAdmin: anything in their org
//   - Other authenticated: their own deployments
//
// Anonymous requests are rejected.
func (h *Handlers) ListDeployments(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}

	page, pageSize := paginationFromQuery(c)
	tx := h.DB.RW.WithContext(c.Request().Context()).Model(&models.Deployment{})

	switch {
	case p.Has(models.RolePlatformAdmin):
		// no filter
	case p.Has(models.RoleOrgAdmin):
		tx = tx.Where("owning_organization_id = ?", p.OrganizationID)
	default:
		tx = tx.Where("deployer_user_id = ?", p.UserID)
	}

	if v := strings.TrimSpace(c.QueryParam("state")); v != "" {
		if !slices.Contains(models.AllDeploymentStates, models.DeploymentState(v)) {
			return apierror.New(http.StatusBadRequest, "bad_request", "invalid state filter value")
		}
		tx = tx.Where("state = ?", v)
	}
	if v := strings.TrimSpace(c.QueryParam("app_id")); v != "" {
		if aid, err := uuid.Parse(v); err == nil {
			tx = tx.Where("app_id = ?", aid)
		}
	}
	if v := strings.TrimSpace(c.QueryParam("cluster_id")); v != "" {
		if cid, err := uuid.Parse(v); err == nil {
			tx = tx.Where("cluster_id = ?", cid)
		}
	}

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return err
	}

	var rows []models.Deployment
	if err := tx.
		Order("created_at DESC").
		Limit(pageSize).
		Offset((page - 1) * pageSize).
		Find(&rows).Error; err != nil {
		return err
	}

	out := make([]map[string]any, 0, len(rows))
	for _, d := range rows {
		out = append(out, deploymentToMap(d))
	}
	return c.JSON(http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// CreateDeploymentRequest is the body of POST /api/v1/deployments.
type CreateDeploymentRequest struct {
	AppID        string            `json:"app_id"`
	AppVersionID string            `json:"app_version_id"`
	ClusterID    string            `json:"cluster_id"`
	Namespace    string            `json:"namespace"`
	Values       json.RawMessage   `json:"values"`
	SecretInputs map[string]string `json:"secret_inputs"`
}

// CreateDeployment is the deploy-initiation handler.
//
// Flow:
//  1. RBAC: Deployer role required
//  2. Resolve App + AppVersion + Cluster; verify they exist + scoping
//  3. Validate values (JSON shape only in V1; schema validation TBD)
//  4. Pre-mint DeploymentID (UUIDv7) so vault paths can be built
//  5. Write user-supplied secrets to the SecretStore
//  6. Persist Deployment row in `pending`
//  7. Call Orchestrator.Apply() to create the Argo CD Application
//  8. Update Deployment.state + last_argocd_app_name
//  9. Audit-log the create event
//
// Returns 202 Accepted with the new deployment row (state may be
// `pending` or `applying` depending on how fast the orchestrator
// responded). Callers should subscribe to /events for live transitions.
func (h *Handlers) CreateDeployment(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}

	orgUUID := p.OrganizationID.String()
	prefix := orgUUID[len(orgUUID)-8:]

	var req CreateDeploymentRequest
	if err := c.Bind(&req); err != nil {
		return apierror.New(http.StatusBadRequest, "bad_request", "invalid JSON")
	}
	effectiveNamespace := prefix + "-" + req.Namespace

	// Request-level validation.
	fields := make([]apierror.FieldError, 0)
	appID, err := uuid.Parse(strings.TrimSpace(req.AppID))
	if err != nil {
		fields = append(fields, apierror.FieldError{Path: "app_id", Code: "invalid_uuid", Message: "app_id must be a UUID"})
	}
	versionID, err := uuid.Parse(strings.TrimSpace(req.AppVersionID))
	if err != nil {
		fields = append(fields, apierror.FieldError{Path: "app_version_id", Code: "invalid_uuid", Message: "app_version_id must be a UUID"})
	}
	clusterID, err := uuid.Parse(strings.TrimSpace(req.ClusterID))
	if err != nil {
		fields = append(fields, apierror.FieldError{Path: "cluster_id", Code: "invalid_uuid", Message: "cluster_id must be a UUID"})
	}
	if !isDNSLabel(req.Namespace) {
		fields = append(fields, apierror.FieldError{
			Path: "namespace", Code: "invalid_dns_label",
			Message: "namespace must be a valid Kubernetes DNS-1123 label",
		})
	} else if len(effectiveNamespace) > 63 {
		fields = append(fields, apierror.FieldError{
			Path:    "namespace",
			Code:    "namespace_too_long",
			Message: fmt.Sprintf("namespace would become %q (%d chars) after org prefix is applied; shorten it to %d chars or fewer", effectiveNamespace, len(effectiveNamespace), 58),
		})
	}
	if len(req.Values) == 0 {
		req.Values = json.RawMessage(`{}`)
	}
	if !json.Valid(req.Values) {
		fields = append(fields, apierror.FieldError{Path: "values", Code: "invalid_json", Message: "values must be valid JSON"})
	}
	if len(fields) > 0 {
		return apierror.NewValidation("validation failed", fields)
	}

	ctx := c.Request().Context()

	// Load App + Version + Cluster in one shot. Errors map to 404 / 422
	// depending on which entity is the source.
	var app models.App
	if err := h.DB.RW.WithContext(ctx).First(&app, "id = ?", appID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apierror.New(http.StatusNotFound, "app_not_found", "app not found")
		}
		return err
	}
	var version models.AppVersion
	if err := h.DB.RW.WithContext(ctx).First(&version, "id = ?", versionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apierror.New(http.StatusNotFound, "version_not_found", "app version not found")
		}
		return err
	}
	if version.AppID != app.ID {
		return apierror.NewValidation("version does not belong to app", []apierror.FieldError{
			{Path: "app_version_id", Code: "wrong_app", Message: "app_version_id is not a version of the supplied app_id"},
		})
	}
	if version.State != models.AppVersionPublished {
		return apierror.NewValidation("only published versions may be deployed", []apierror.FieldError{
			{Path: "app_version_id", Code: "not_published",
				Message: fmt.Sprintf("version is in state %q, must be 'published'", version.State)},
		})
	}

	// Enforce schema: full type/enum validation, no undeclared properties, and
	// the secret/non-secret split. Fails closed on an unenforceable schema.
	if apiErr := validateValuesAgainstSchema(version.ValuesSchema, req.Values, req.SecretInputs); apiErr != nil {
		return apiErr
	}

	var cluster models.Cluster
	if err := h.DB.RW.WithContext(ctx).First(&cluster, "id = ?", clusterID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apierror.New(http.StatusNotFound, "cluster_not_found", "cluster not found")
		}
		return err
	}

	// Cluster scope check: caller must have access.
	if !p.Has(models.RolePlatformAdmin) {
		accessible, err := h.clusterAccessibleToOrg(ctx, cluster, p.OrganizationID)
		if err != nil {
			return err
		}
		if !accessible {
			return apierror.New(http.StatusForbidden, "cluster_not_visible",
				"you do not have access to that cluster")
		}
	}
	if cluster.State == models.ClusterDecommissioned {
		return apierror.New(http.StatusConflict, "cluster_decommissioned",
			"that cluster has been decommissioned and cannot accept new deployments")
	}

	// Duplicate guard: no two non-deleted deployments of the same App
	// in the same (cluster, namespace).
	var dupes int64
	if err := h.DB.RW.WithContext(ctx).Model(&models.Deployment{}).
		Where("app_id = ? AND cluster_id = ? AND namespace = ? AND deleted_at IS NULL",
			app.ID, cluster.ID, effectiveNamespace).
		Count(&dupes).Error; err != nil {
		return err
	}
	if dupes > 0 {
		return apierror.New(http.StatusConflict, "duplicate_deployment",
			"this app is already deployed to that cluster + namespace; upgrade it instead")
	}

	// Pre-mint the deployment id so vault paths use it as a prefix.
	deploymentID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("mint deployment id: %w", err)
	}

	// Persist secrets to the store + collect the SecretRefs we'll hand
	// to the orchestrator. On a failure mid-loop, attempt cleanup.
	refs := make([]orchestrator.SecretRef, 0, len(req.SecretInputs))
	for fieldName, value := range req.SecretInputs {
		vaultPath, err := h.Secrets.Put(ctx, deploymentID, fieldName, []byte(value))
		if err != nil {
			_ = h.Secrets.DeleteAll(ctx, deploymentID) // best-effort cleanup
			switch {
			case errors.Is(err, secrets.ErrNotConfigured):
				return apierror.New(http.StatusServiceUnavailable, "secrets_not_configured",
					"secret store is not configured on this environment")
			case errors.Is(err, secrets.ErrInvalidFieldName):
				return apierror.NewValidation("invalid secret field", []apierror.FieldError{
					{Path: "secret_inputs." + fieldName, Code: "invalid_field_name", Message: err.Error()},
				})
			default:
				return apierror.New(http.StatusServiceUnavailable, "vault_unavailable",
					"secret store backend is unavailable; deployment cannot proceed")
			}
		}
		refs = append(refs, orchestrator.SecretRef{FieldPath: fieldName, VaultPath: vaultPath})
	}

	// Build the Application spec the orchestrator will materialise.
	spec := orchestrator.ApplicationSpec{
		DeploymentID:  deploymentID,
		ClusterID:     cluster.ID,
		AppSlug:       app.Slug,
		ClusterName:   cluster.EffectiveOrchestratorName(),
		Project:       cluster.EffectiveArgocdProject(),
		Namespace:     effectiveNamespace,
		GitHubRepoURL: fmt.Sprintf("https://github.com/%s/%s", app.GitHubRepoOwner, app.GitHubRepoName),
		GitSHA:        version.GitSHA,
		ChartPath:     deriveChartPath(version),
		Values:        req.Values,
		SecretRefs:    refs,
	}

	// Persist Deployment row in `pending` BEFORE calling the
	// orchestrator. That way, even if the API process crashes after
	// the Apply call, the DB has a record we can reconcile.
	dep := models.Deployment{
		AppID:                app.ID,
		AppVersionID:         version.ID,
		ClusterID:            cluster.ID,
		OwningOrganizationID: p.OrganizationID,
		DeployerUserID:       p.UserID,
		Namespace:            effectiveNamespace,
		Values:               datatypes.JSON(req.Values),
		SecretRefs:           mustMarshalRefs(refs),
		State:                models.DeploymentPending,
	}
	dep.ID = deploymentID
	if err := h.DB.RW.WithContext(ctx).Create(&dep).Error; err != nil {
		_ = h.Secrets.DeleteAll(ctx, deploymentID)
		return err
	}
	deploystate.Created(dep)

	// Hand off to the orchestrator. On success, update the row to
	// `applying`; on transient error, set `pending_orchestrator` for the
	// background reconciler to retry; on permanent failure, `failed_orchestrator`.
	ref, applyErr := h.Orchestrator.Apply(ctx, spec)
	if applyErr != nil {
		nextState := models.DeploymentFailedOrchestrator
		if errors.Is(applyErr, orchestrator.ErrTransient) {
			nextState = models.DeploymentPendingOrchestrator
		}
		if dbErr := deploystate.Set(ctx, h.DB.RW, &dep, nextState, nil); dbErr != nil {
			slog.Error("deployments: persist apply failure state", "error", dbErr, "deployment_id", dep.ID, "state", string(nextState))
		}

		_ = h.Audit.Log(ctx, audit.Event{
			Type:                "deployment.apply_failed",
			ActorUserID:         &p.UserID,
			ActorOrganizationID: &p.OrganizationID,
			SubjectType:         "deployment",
			SubjectID:           dep.ID,
			Payload: map[string]any{
				"app_slug":   app.Slug,
				"error":      applyErr.Error(),
				"next_state": string(nextState),
			},
		})

		if errors.Is(applyErr, orchestrator.ErrNotConfigured) {
			return apierror.New(http.StatusServiceUnavailable, "orchestrator_not_configured",
				"deployment orchestrator is not configured on this environment")
		}
		if errors.Is(applyErr, orchestrator.ErrTransient) {
			return apierror.New(http.StatusServiceUnavailable, "orchestrator_transient_error",
				"orchestrator is temporarily unavailable — the deployment will be retried automatically")
		}
		slog.Error("orchestrator apply failed", "err", applyErr)
		return apierror.New(http.StatusBadGateway, "orchestrator_apply_failed",
			"failed to create application on the orchestrator")
	}

	if err := deploystate.Set(ctx, h.DB.RW, &dep, models.DeploymentApplying, map[string]any{
		"last_argocd_app_name": ref.ApplicationName,
	}); err != nil {
		return err
	}
	dep.LastArgocdAppName = &ref.ApplicationName

	_ = h.Audit.Log(ctx, audit.Event{
		Type:                "deployment.created",
		ActorUserID:         &p.UserID,
		ActorOrganizationID: &p.OrganizationID,
		SubjectType:         "deployment",
		SubjectID:           dep.ID,
		Payload: map[string]any{
			"app_slug":           app.Slug,
			"app_version_id":     version.ID.String(),
			"git_sha":            version.GitSHA,
			"cluster_id":         cluster.ID.String(),
			"cluster_name":       cluster.EffectiveOrchestratorName(),
			"namespace":          effectiveNamespace,
			"argocd_app_name":    ref.ApplicationName,
			"secret_field_count": len(refs),
		},
	})

	return c.JSON(http.StatusAccepted, deploymentToMap(dep))
}

// GetDeployment returns one deployment, scoped to what the caller may
// see. If an orchestrator is configured we also poll its current
// status and return it inline as `live_status` so the UI can render
// fresh state without an extra round-trip.
func (h *Handlers) GetDeployment(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}

	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return apierror.New(http.StatusNotFound, "not_found", "deployment not found")
	}

	var dep models.Deployment
	if err := h.DB.RW.WithContext(c.Request().Context()).
		First(&dep, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apierror.New(http.StatusNotFound, "not_found", "deployment not found")
		}
		return err
	}

	if err := requireDeploymentVisible(p, dep); err != nil {
		return err
	}

	out := deploymentToMap(dep)

	// Enrich with app slug and values schema for the reconfigure form.
	var app models.App
	if appErr := h.DB.RW.WithContext(c.Request().Context()).
		First(&app, "id = ?", dep.AppID).Error; appErr == nil {
		out["app_slug"] = app.Slug
	}
	var ver models.AppVersion
	if verErr := h.DB.RW.WithContext(c.Request().Context()).
		First(&ver, "id = ?", dep.AppVersionID).Error; verErr == nil {
		if len(ver.ValuesSchema) > 0 && string(ver.ValuesSchema) != "null" {
			out["values_schema"] = json.RawMessage(ver.ValuesSchema)
		}
	}

	// Best-effort live status pull. Failures are silenced — the
	// persisted state in the response is still authoritative.
	if dep.LastArgocdAppName != nil && !isTerminal(dep.State) {
		ref := orchestrator.NewDeploymentRef(*dep.LastArgocdAppName, dep.ClusterID)
		if status, sErr := h.Orchestrator.Status(c.Request().Context(), ref); sErr == nil {
			out["live_status"] = orchestratorStatusToMap(status)
		}
	}

	return c.JSON(http.StatusOK, out)
}

// DeleteDeployment tears down a deployment via the orchestrator and
// purges its secrets. Returns 202 because the orchestrator-side
// teardown is asynchronous; the row is marked `deleting` immediately
// and transitions to `deleted` once Status confirms.
func (h *Handlers) DeleteDeployment(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}

	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return apierror.New(http.StatusNotFound, "not_found", "deployment not found")
	}

	ctx := c.Request().Context()

	var dep models.Deployment
	if err := h.DB.RW.WithContext(ctx).First(&dep, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apierror.New(http.StatusNotFound, "not_found", "deployment not found")
		}
		return err
	}

	// Authorisation: Deployer owner-only (with PlatformAdmin override);
	// callers who cannot read the deployment get 404. Cluster access is deliberately not re-checked: an org that lost access
	// to the cluster must still be able to tear its deployment down.
	if err := authorizeDeploymentWrite(p, dep); err != nil {
		return err
	}

	// Already deleted? Idempotent 204.
	if dep.State == models.DeploymentDeleted {
		return c.NoContent(http.StatusNoContent)
	}

	// Mark deleting synchronously so callers observe the state change
	// immediately. The orchestrator teardown and secret purge run in the
	// background; the row transitions to deleted once they complete.
	if err := deploystate.Set(ctx, h.DB.RW, &dep, models.DeploymentDeleting, nil); err != nil {
		return err
	}

	go func(depID uuid.UUID, argoName *string, depNS string, depClusterID uuid.UUID, actorUserID uuid.UUID, actorOrgID uuid.UUID) {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("deployments: delete goroutine panicked", "deployment_id", depID, "panic", r)
			}
		}()
		bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		if argoName != nil {
			ref := orchestrator.NewDeploymentRef(*argoName, depClusterID)
			if err := h.Orchestrator.Delete(bgCtx, ref); err != nil &&
				!errors.Is(err, orchestrator.ErrApplicationNotFound) {
				// Leave state as deleting — operator can retry.
				return
			}
		}

		_ = h.Secrets.DeleteAll(bgCtx, depID)

		var d models.Deployment
		if err := h.DB.RW.WithContext(bgCtx).First(&d, "id = ?", depID).Error; err != nil {
			return
		}
		if err := deploystate.Set(bgCtx, h.DB.RW, &d, models.DeploymentDeleted, nil); err != nil {
			slog.Error("deployment delete: update state", "error", err, "id", depID)
			return
		}
		_ = h.DB.RW.WithContext(bgCtx).Delete(&d)

		_ = h.Audit.Log(bgCtx, audit.Event{
			Type:                "deployment.deleted",
			ActorUserID:         &actorUserID,
			ActorOrganizationID: &actorOrgID,
			SubjectType:         "deployment",
			SubjectID:           depID,
			Payload:             map[string]any{"namespace": depNS, "cluster_id": depClusterID.String()},
		})
	}(dep.ID, dep.LastArgocdAppName, dep.Namespace, dep.ClusterID, p.UserID, p.OrganizationID)

	return c.JSON(http.StatusAccepted, map[string]any{
		"id":    dep.ID.String(),
		"state": string(models.DeploymentDeleting),
	})
}

// UpgradeDeploymentRequest is the body of POST /api/v1/deployments/:id/upgrade.
type UpgradeDeploymentRequest struct {
	ToVersionID     string            `json:"to_version_id"`
	NewValues       json.RawMessage   `json:"new_values"`
	NewSecretInputs map[string]string `json:"new_secret_inputs"`
}

// UpgradeDeployment re-applies a deployment against a new (or same) published
// AppVersion, optionally replacing values and/or secrets. Returns 202 with the
// updated deployment row. The orchestrator reconciles asynchronously; callers
// should stream /events for live progress.
func (h *Handlers) UpgradeDeployment(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}

	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return apierror.New(http.StatusNotFound, "not_found", "deployment not found")
	}

	var req UpgradeDeploymentRequest
	if err := c.Bind(&req); err != nil {
		return apierror.New(http.StatusBadRequest, "bad_request", "invalid JSON")
	}

	newVersionID, err := uuid.Parse(strings.TrimSpace(req.ToVersionID))
	if err != nil {
		return apierror.NewValidation("validation failed", []apierror.FieldError{
			{Path: "to_version_id", Code: "invalid_uuid", Message: "to_version_id must be a UUID"},
		})
	}
	if len(req.NewValues) > 0 && !json.Valid(req.NewValues) {
		return apierror.NewValidation("validation failed", []apierror.FieldError{
			{Path: "new_values", Code: "invalid_json", Message: "new_values must be valid JSON"},
		})
	}

	ctx := c.Request().Context()

	var dep models.Deployment
	if err := h.DB.RW.WithContext(ctx).First(&dep, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apierror.New(http.StatusNotFound, "not_found", "deployment not found")
		}
		return err
	}

	if err := h.authorizeDeploymentRedeploy(ctx, p, dep); err != nil {
		return err
	}
	if !canChangeVersion(dep.State) {
		return apierror.New(http.StatusConflict, "deployment_not_upgradeable",
			fmt.Sprintf("deployment is in state %q and cannot be upgraded", dep.State))
	}

	var newVersion models.AppVersion
	if err := h.DB.RW.WithContext(ctx).First(&newVersion, "id = ?", newVersionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apierror.New(http.StatusNotFound, "version_not_found", "app version not found")
		}
		return err
	}
	if newVersion.AppID != dep.AppID {
		return apierror.NewValidation("version does not belong to this app", []apierror.FieldError{
			{Path: "to_version_id", Code: "wrong_app", Message: "to_version_id is not a version of this deployment's app"},
		})
	}
	if newVersion.State != models.AppVersionPublished {
		return apierror.NewValidation("only published versions may be deployed", []apierror.FieldError{
			{Path: "to_version_id", Code: "not_published",
				Message: fmt.Sprintf("version is in state %q, must be 'published'", newVersion.State)},
		})
	}

	// Use existing values when caller omits the field.
	effectiveValues := req.NewValues
	if len(effectiveValues) == 0 {
		effectiveValues = json.RawMessage(dep.Values)
	}

	if apiErr := validateValuesAgainstSchema(newVersion.ValuesSchema, effectiveValues, req.NewSecretInputs); apiErr != nil {
		return apiErr
	}

	// Write any new secrets; carry forward existing refs when none supplied.
	refs := make([]orchestrator.SecretRef, 0, len(req.NewSecretInputs))
	for fieldName, value := range req.NewSecretInputs {
		vaultPath, err := h.Secrets.Put(ctx, dep.ID, fieldName, []byte(value))
		if err != nil {
			if errors.Is(err, secrets.ErrNotConfigured) {
				return apierror.New(http.StatusServiceUnavailable, "secrets_not_configured",
					"secret store is not configured on this environment")
			}
			return apierror.NewValidation("invalid secret field", []apierror.FieldError{
				{Path: "new_secret_inputs." + fieldName, Code: "invalid_field_name", Message: err.Error()},
			})
		}
		refs = append(refs, orchestrator.SecretRef{FieldPath: fieldName, VaultPath: vaultPath})
	}
	if len(refs) == 0 {
		var existingRefs []orchestrator.SecretRef
		if err := json.Unmarshal(dep.SecretRefs, &existingRefs); err == nil {
			refs = existingRefs
		}
	}

	var app models.App
	if err := h.DB.RW.WithContext(ctx).First(&app, "id = ?", dep.AppID).Error; err != nil {
		return err
	}
	var cluster models.Cluster
	if err := h.DB.RW.WithContext(ctx).First(&cluster, "id = ?", dep.ClusterID).Error; err != nil {
		return err
	}

	spec := orchestrator.ApplicationSpec{
		DeploymentID:  dep.ID,
		ClusterID:     cluster.ID,
		AppSlug:       app.Slug,
		ClusterName:   cluster.EffectiveOrchestratorName(),
		Project:       cluster.EffectiveArgocdProject(),
		Namespace:     dep.Namespace,
		GitHubRepoURL: fmt.Sprintf("https://github.com/%s/%s", app.GitHubRepoOwner, app.GitHubRepoName),
		GitSHA:        newVersion.GitSHA,
		ChartPath:     deriveChartPath(newVersion),
		Values:        effectiveValues,
		SecretRefs:    refs,
	}

	ref, applyErr := h.Orchestrator.Apply(ctx, spec)
	if applyErr != nil {
		nextState := models.DeploymentFailedOrchestrator
		if errors.Is(applyErr, orchestrator.ErrTransient) {
			nextState = models.DeploymentPendingOrchestrator
		}
		if dbErr := deploystate.Set(ctx, h.DB.RW, &dep, nextState, nil); dbErr != nil {
			slog.Error("deployments: persist upgrade failure state", "error", dbErr, "deployment_id", dep.ID, "state", string(nextState))
		}
		_ = h.Audit.Log(ctx, audit.Event{
			Type:                "deployment.upgrade_failed",
			ActorUserID:         &p.UserID,
			ActorOrganizationID: &p.OrganizationID,
			SubjectType:         "deployment",
			SubjectID:           dep.ID,
			Payload: map[string]any{
				"app_version_id": newVersionID.String(),
				"error":          applyErr.Error(),
				"next_state":     string(nextState),
			},
		})
		if errors.Is(applyErr, orchestrator.ErrNotConfigured) {
			return apierror.New(http.StatusServiceUnavailable, "orchestrator_not_configured",
				"deployment orchestrator is not configured on this environment")
		}
		if errors.Is(applyErr, orchestrator.ErrTransient) {
			return apierror.New(http.StatusServiceUnavailable, "orchestrator_transient_error",
				"orchestrator is temporarily unavailable — the upgrade will be retried automatically")
		}
		slog.Error("orchestrator apply failed", "err", applyErr)
		return apierror.New(http.StatusBadGateway, "orchestrator_apply_failed",
			"failed to upgrade application on the orchestrator")
	}

	if err := deploystate.Set(ctx, h.DB.RW, &dep, models.DeploymentApplying, map[string]any{
		"app_version_id":       newVersionID,
		"last_argocd_app_name": ref.ApplicationName,
		"values":               datatypes.JSON(effectiveValues),
		"secret_refs":          mustMarshalRefs(refs),
	}); err != nil {
		return err
	}
	dep.AppVersionID = newVersionID
	dep.LastArgocdAppName = &ref.ApplicationName

	_ = h.Audit.Log(ctx, audit.Event{
		Type:                "deployment.upgraded",
		ActorUserID:         &p.UserID,
		ActorOrganizationID: &p.OrganizationID,
		SubjectType:         "deployment",
		SubjectID:           dep.ID,
		Payload: map[string]any{
			"app_version_id":  newVersionID.String(),
			"git_sha":         newVersion.GitSHA,
			"argocd_app_name": ref.ApplicationName,
		},
	})

	return c.JSON(http.StatusAccepted, deploymentToMap(dep))
}

// RollbackDeploymentRequest is the body of POST /api/v1/deployments/:id/rollback.
type RollbackDeploymentRequest struct {
	ToRevision string `json:"to_revision"`
}

// versionApproved reports whether an audit submission for versionID holds
// an approve decision.
func (h *Handlers) versionApproved(ctx context.Context, versionID uuid.UUID) (bool, error) {
	var approved bool
	err := h.DB.RW.WithContext(ctx).Raw(`SELECT EXISTS (
		SELECT 1 FROM audit_submissions s
		JOIN audit_decisions d ON d.audit_submission_id = s.id
		WHERE s.app_version_id = ? AND d.decision = ?
	)`, versionID, models.DecisionApprove).Scan(&approved).Error
	return approved, err
}

// RollbackDeployment reverts a deployment to a prior AppVersion that passed
// audit (published or archived, with an approve decision) by calling
// Orchestrator.Rollback with the target GitSHA. Returns 202; the state
// transitions asynchronously.
func (h *Handlers) RollbackDeployment(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}

	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return apierror.New(http.StatusNotFound, "not_found", "deployment not found")
	}

	var req RollbackDeploymentRequest
	if err := c.Bind(&req); err != nil {
		return apierror.New(http.StatusBadRequest, "bad_request", "invalid JSON")
	}

	targetVersionID, err := uuid.Parse(strings.TrimSpace(req.ToRevision))
	if err != nil {
		return apierror.NewValidation("validation failed", []apierror.FieldError{
			{Path: "to_revision", Code: "invalid_uuid", Message: "to_revision must be a UUID"},
		})
	}

	ctx := c.Request().Context()

	var dep models.Deployment
	if err := h.DB.RW.WithContext(ctx).First(&dep, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apierror.New(http.StatusNotFound, "not_found", "deployment not found")
		}
		return err
	}

	if err := h.authorizeDeploymentRedeploy(ctx, p, dep); err != nil {
		return err
	}
	if !canChangeVersion(dep.State) {
		return apierror.New(http.StatusConflict, "deployment_not_rollbackable",
			fmt.Sprintf("deployment is in state %q and cannot be rolled back", dep.State))
	}
	if dep.LastArgocdAppName == nil {
		return apierror.New(http.StatusConflict, "no_orchestrator_ref",
			"deployment has no orchestrator reference — cannot roll back")
	}

	var targetVersion models.AppVersion
	if err := h.DB.RW.WithContext(ctx).First(&targetVersion, "id = ?", targetVersionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apierror.New(http.StatusNotFound, "version_not_found", "app version not found")
		}
		return err
	}
	if targetVersion.AppID != dep.AppID {
		return apierror.NewValidation("version does not belong to this app", []apierror.FieldError{
			{Path: "to_revision", Code: "wrong_app", Message: "to_revision is not a version of this deployment's app"},
		})
	}
	if targetVersion.State != models.AppVersionPublished && targetVersion.State != models.AppVersionArchived {
		return apierror.NewValidation("only published or archived versions may be targeted", []apierror.FieldError{
			{Path: "to_revision", Code: "not_rollback_eligible",
				Message: fmt.Sprintf("version is in state %q, must be 'published' or 'archived'", targetVersion.State)},
		})
	}
	// The state column alone does not keep an unaudited version off a
	// cluster: the target must also hold an approve decision.
	approved, err := h.versionApproved(ctx, targetVersion.ID)
	if err != nil {
		return err
	}
	if !approved {
		return apierror.NewValidation("only audit-approved versions may be targeted", []apierror.FieldError{
			{Path: "to_revision", Code: "not_rollback_eligible",
				Message: "version has no approved audit decision"},
		})
	}

	orchRef := orchestrator.NewDeploymentRef(*dep.LastArgocdAppName, dep.ClusterID)

	if err := h.Orchestrator.Rollback(ctx, orchRef, targetVersion.GitSHA); err != nil {
		if dbErr := deploystate.Set(ctx, h.DB.RW, &dep, models.DeploymentFailedOrchestrator, nil); dbErr != nil {
			slog.Error("deployments: persist rollback failure state", "error", dbErr, "deployment_id", dep.ID)
		}
		_ = h.Audit.Log(ctx, audit.Event{
			Type:                "deployment.rollback_failed",
			ActorUserID:         &p.UserID,
			ActorOrganizationID: &p.OrganizationID,
			SubjectType:         "deployment",
			SubjectID:           dep.ID,
			Payload:             map[string]any{"target_version_id": targetVersionID.String(), "error": err.Error()},
		})
		if errors.Is(err, orchestrator.ErrNotConfigured) {
			return apierror.New(http.StatusServiceUnavailable, "orchestrator_not_configured",
				"deployment orchestrator is not configured on this environment")
		}
		slog.Error("orchestrator rollback failed", "err", err)
		return apierror.New(http.StatusBadGateway, "orchestrator_rollback_failed",
			"failed to roll back application on the orchestrator")
	}

	if err := deploystate.Set(ctx, h.DB.RW, &dep, models.DeploymentApplying, map[string]any{
		"app_version_id": targetVersionID,
	}); err != nil {
		return err
	}
	dep.AppVersionID = targetVersionID

	_ = h.Audit.Log(ctx, audit.Event{
		Type:                "deployment.rolled_back",
		ActorUserID:         &p.UserID,
		ActorOrganizationID: &p.OrganizationID,
		SubjectType:         "deployment",
		SubjectID:           dep.ID,
		Payload: map[string]any{
			"target_version_id": targetVersionID.String(),
			"git_sha":           targetVersion.GitSHA,
			"argocd_app_name":   *dep.LastArgocdAppName,
		},
	})

	return c.JSON(http.StatusAccepted, deploymentToMap(dep))
}

// StreamDeploymentEvents is the SSE channel for live state transitions.
// V1 implementation polls Orchestrator.Status every 2 seconds and
// emits an event whenever the phase changes. Closes the stream when
// the deployment hits a terminal state (ready/failed/deleted) or when
// the client disconnects.
//
// Sprint 2 swaps the polling for push events from Argo CD webhooks.
func (h *Handlers) StreamDeploymentEvents(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}

	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return apierror.New(http.StatusNotFound, "not_found", "deployment not found")
	}

	var dep models.Deployment
	if err := h.DB.RW.WithContext(c.Request().Context()).First(&dep, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apierror.New(http.StatusNotFound, "not_found", "deployment not found")
		}
		return err
	}
	if err := requireDeploymentVisible(p, dep); err != nil {
		return err
	}

	// SSE headers.
	c.Response().Header().Set(echo.HeaderContentType, "text/event-stream")
	c.Response().Header().Set("Cache-Control", "no-cache")
	c.Response().Header().Set("Connection", "keep-alive")
	c.Response().Header().Set("X-Accel-Buffering", "no") // disable nginx buffering
	c.Response().WriteHeader(http.StatusOK)

	// Helper to push one event line. Returns false if the client is
	// gone so the caller breaks out of the loop.
	push := func(event string, payload any) bool {
		raw, _ := json.Marshal(payload)
		_, err := fmt.Fprintf(c.Response(), "event: %s\ndata: %s\n\n", event, string(raw))
		if err != nil {
			return false
		}
		c.Response().Flush()
		return true
	}

	// Always send the current persisted state first so the client gets
	// an immediate render even if the orchestrator is slow.
	if !push("state", map[string]any{
		"deployment_id": dep.ID.String(),
		"state":         string(dep.State),
		"source":        "db",
	}) {
		return nil
	}

	if dep.LastArgocdAppName == nil {
		// Nothing to poll. Close cleanly after the initial state.
		_ = push("end", map[string]any{"reason": "no_orchestrator_ref"})
		return nil
	}

	ref := orchestrator.NewDeploymentRef(*dep.LastArgocdAppName, dep.ClusterID)

	ctx := c.Request().Context()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// Hard cap on stream duration to release resources if a client
	// forgets to disconnect.
	deadline, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	var lastPhase orchestrator.Phase
	for {
		select {
		case <-deadline.Done():
			_ = push("end", map[string]any{"reason": "timeout"})
			return nil
		case <-ticker.C:
			status, sErr := h.Orchestrator.Status(deadline, ref)
			if sErr != nil {
				if errors.Is(sErr, orchestrator.ErrApplicationNotFound) {
					_ = push("end", map[string]any{"reason": "application_gone"})
					return nil
				}
				if errors.Is(sErr, orchestrator.ErrNotConfigured) {
					_ = push("end", map[string]any{"reason": "orchestrator_not_configured"})
					return nil
				}
				// Transient error: log + keep polling, don't kill the stream.
				slog.Warn("orchestrator status error during stream", "err", sErr)
				_ = push("warn", map[string]any{"message": "orchestrator temporarily unavailable"})
				continue
			}

			if status.Phase != lastPhase {
				if !push("state", map[string]any{
					"deployment_id":  dep.ID.String(),
					"phase":          string(status.Phase),
					"revision":       status.Revision,
					"healthy":        status.Healthy,
					"message":        status.Message,
					"last_synced_at": status.LastSyncedAt,
					"source":         "orchestrator",
				}) {
					return nil
				}
				lastPhase = status.Phase

				// Reflect into DB. Terminal phases also flip the row
				// state so subsequent GETs reflect reality.
				if dbState := mapPhaseToDeploymentState(status.Phase); dbState != "" {
					row := models.Deployment{IDModel: models.IDModel{ID: dep.ID}}
					_ = deploystate.Set(ctx, h.DB.RW, &row, dbState, nil)
				}

				if status.Phase == orchestrator.PhaseReady ||
					status.Phase == orchestrator.PhaseFailed {
					_ = push("end", map[string]any{"reason": "terminal", "phase": string(status.Phase)})
					return nil
				}
			}
		}
	}
}

// --- helpers --------------------------------------------------------------

func authorizeDeploymentRead(p *auth.Principal, dep models.Deployment) error {
	switch {
	case p.Has(models.RolePlatformAdmin):
		return nil
	case p.Has(models.RoleOrgAdmin):
		if dep.OwningOrganizationID != p.OrganizationID {
			return apierror.New(http.StatusForbidden, "forbidden", "not in your organisation")
		}
	default:
		if dep.DeployerUserID != p.UserID {
			return apierror.New(http.StatusForbidden, "forbidden", "you do not own this deployment")
		}
	}
	return nil
}

// requireDeploymentVisible returns GetDeployment's 404 when p may not read
// dep, so callers cannot probe which deployment IDs exist.
func requireDeploymentVisible(p *auth.Principal, dep models.Deployment) error {
	if authorizeDeploymentRead(p, dep) != nil {
		return apierror.New(http.StatusNotFound, "not_found", "deployment not found")
	}
	return nil
}

// authorizeDeploymentWrite allows the deployer and platform admin. A caller
// who cannot see the deployment through GetDeployment gets the same 404; one
// who can see it but does not own it (an org admin in the same org) gets 403.
func authorizeDeploymentWrite(p *auth.Principal, dep models.Deployment) error {
	if err := requireDeploymentVisible(p, dep); err != nil {
		return err
	}
	if p.Has(models.RolePlatformAdmin) {
		return nil
	}
	if dep.DeployerUserID != p.UserID {
		return apierror.New(http.StatusForbidden, "forbidden", "you do not own this deployment")
	}
	return nil
}

// authorizeDeploymentRedeploy guards upgrade and rollback, which push a new
// version onto the deployment's cluster. On top of ownership it re-checks
// the cluster access CreateDeployment enforces, so an org that lost access
// to the cluster (grant revoked or scope narrowed) cannot keep shipping to
// it. Like CreateDeployment it also refuses a decommissioned cluster, for
// every caller including platform admin. Delete only calls
// authorizeDeploymentWrite so the org can still tear the deployment down.
func (h *Handlers) authorizeDeploymentRedeploy(ctx context.Context, p *auth.Principal, dep models.Deployment) error {
	if err := authorizeDeploymentWrite(p, dep); err != nil {
		return err
	}
	var cluster models.Cluster
	if err := h.DB.RW.WithContext(ctx).First(&cluster, "id = ?", dep.ClusterID).Error; err != nil {
		return err
	}
	if !p.Has(models.RolePlatformAdmin) {
		accessible, err := h.clusterAccessibleToOrg(ctx, cluster, p.OrganizationID)
		if err != nil {
			return err
		}
		if !accessible {
			return apierror.New(http.StatusForbidden, "cluster_not_visible",
				"you do not have access to that cluster")
		}
	}
	if cluster.State == models.ClusterDecommissioned {
		return apierror.New(http.StatusConflict, "cluster_decommissioned",
			"that cluster has been decommissioned and cannot accept new deployments")
	}
	return nil
}

func deploymentToMap(d models.Deployment) map[string]any {
	out := map[string]any{
		"id":                     d.ID.String(),
		"app_id":                 d.AppID.String(),
		"app_version_id":         d.AppVersionID.String(),
		"cluster_id":             d.ClusterID.String(),
		"owning_organization_id": d.OwningOrganizationID.String(),
		"deployer_user_id":       d.DeployerUserID.String(),
		"namespace":              d.Namespace,
		"state":                  string(d.State),
		"last_state_change_at":   d.LastStateChangeAt,
		"created_at":             d.CreatedAt,
		"values":                 json.RawMessage(d.Values),
	}
	if d.LastArgocdAppName != nil {
		out["last_argocd_app_name"] = *d.LastArgocdAppName
	}
	return out
}

func orchestratorStatusToMap(s orchestrator.DeploymentStatus) map[string]any {
	return map[string]any{
		"phase":          string(s.Phase),
		"message":        s.Message,
		"last_synced_at": s.LastSyncedAt,
		"revision":       s.Revision,
		"healthy":        s.Healthy,
	}
}

// mapPhaseToDeploymentState narrows the orchestrator phase to one of
// our Deployment.state enum values. Returns "" if no DB-side state
// change is warranted.
func mapPhaseToDeploymentState(phase orchestrator.Phase) models.DeploymentState {
	switch phase {
	case orchestrator.PhaseApplying:
		return models.DeploymentApplying
	case orchestrator.PhaseReconciling:
		return models.DeploymentReconciling
	case orchestrator.PhaseReady:
		return models.DeploymentReady
	case orchestrator.PhaseDegraded:
		return models.DeploymentDegraded
	case orchestrator.PhaseFailed:
		return models.DeploymentFailedOrchestrator
	default:
		return ""
	}
}

func isTerminal(s models.DeploymentState) bool {
	switch s {
	case models.DeploymentReady, models.DeploymentDeleted, models.DeploymentDegraded,
		models.DeploymentFailedOrchestrator, models.DeploymentFailedRender,
		models.DeploymentFailedVault, models.DeploymentFailedSecretSync,
		models.DeploymentPendingOrchestrator:
		return true
	}
	return false
}

// canChangeVersion reports whether a deployment in state s may be upgraded
// or rolled back. Only settled states qualify: ready, degraded and every
// failed_* state (rolling back is the usual way out of a failure). In-flight
// states (pending, waiting_for_cluster, applying, secret_sync, reconciling,
// pending_orchestrator) are rejected so a version change cannot race the
// running apply or the reconciler's retry, and deleting/deleted are rejected
// because the deployment is being torn down.
func canChangeVersion(s models.DeploymentState) bool {
	switch s {
	case models.DeploymentReady, models.DeploymentDegraded,
		models.DeploymentFailedRender, models.DeploymentFailedVault,
		models.DeploymentFailedOrchestrator, models.DeploymentFailedSecretSync:
		return true
	}
	return false
}

// isDNSLabel validates a Kubernetes DNS-1123 label (RFC 1123, 1–63
// chars, lowercase alphanumeric or '-', must start + end with alnum).
func isDNSLabel(s string) bool {
	if l := len(s); l < 1 || l > 63 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' && i != 0 && i != len(s)-1:
		default:
			return false
		}
	}
	return true
}

// deriveChartPath reads spec.chart.path from the persisted AppstoreYAML
// JSON. Falls back to "./chart" if the manifest somehow doesn't carry
// one (shouldn't happen because Stage-1 validation requires it).
func deriveChartPath(v models.AppVersion) string {
	if len(v.AppstoreYAML) == 0 {
		return "chart"
	}
	var manifest struct {
		Spec struct {
			Chart struct {
				Path string `json:"path"`
			} `json:"chart"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(v.AppstoreYAML, &manifest); err != nil {
		return "chart"
	}
	path := strings.TrimPrefix(manifest.Spec.Chart.Path, "./")
	if strings.Contains(path, "..") {
		return "chart"
	}
	return path
}

// validateValuesAgainstSchema enforces the app version's values.schema.json
// against the submitted deployment values.
//
// It delegates to internal/valuesschema, which validates the full schema
// (types, enums, formats, required), rejects any property the schema did not
// declare (additionalProperties:false), and enforces the secret/non-secret
// split. Unlike the previous best-effort check, it FAILS CLOSED: an
// unenforceable schema is rejected rather than skipped, so the audit gate
// (only auditor-approved charts run, with only auditor-reviewed values) cannot
// be bypassed by smuggling extra Helm values (findings INPUTVAL-001/002).
//
// Returns nil when the values are valid, otherwise the *apierror.Error the
// caller should return directly.
func validateValuesAgainstSchema(schema datatypes.JSON, values json.RawMessage, secretInputs map[string]string) *apierror.Error {
	errs, fatal := valuesschema.Validate(schema, values, secretInputs)
	if fatal != nil {
		// A published version whose schema cannot be enforced is a maintainer
		// problem, not a caller problem — log the detail, fail the deploy.
		slog.Error("deployment values schema is unenforceable", "err", fatal)
		return apierror.New(http.StatusUnprocessableEntity, "schema_unenforceable",
			"this app version's values schema cannot be enforced; the app maintainer must republish a valid values.schema.json")
	}
	if len(errs) > 0 {
		return apierror.NewValidation("values validation failed", errs)
	}
	return nil
}

func mustMarshalRefs(refs []orchestrator.SecretRef) datatypes.JSON {
	if len(refs) == 0 {
		return datatypes.JSON(`[]`)
	}
	raw, err := json.Marshal(refs)
	if err != nil {
		// Marshalling a known-clean slice cannot fail in practice; fall
		// back to empty array rather than failing the request.
		return datatypes.JSON(`[]`)
	}
	return raw
}
