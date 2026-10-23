package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"strings"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	gh "github.com/ClappFormOrg/SaidKick/API/internal/integrations/github"
	"github.com/ClappFormOrg/SaidKick/API/internal/manifest"
	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
	"github.com/ClappFormOrg/SaidKick/API/internal/sarifgate"
	"github.com/ClappFormOrg/SaidKick/API/internal/valuesschema"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"gopkg.in/yaml.v3"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// manifestFileName is the path the platform looks for at the root of the
// linked branch. Hard-coded for V1; future versions could allow an
// explicit path override.
const manifestFileName = "appstore.yaml"

// CreateAppRequest is the body of POST /api/v1/apps.
type CreateAppRequest struct {
	GitHubInstallationID int64  `json:"github_installation_id"`
	RepoOwner            string `json:"repo_owner"`
	RepoName             string `json:"repo_name"`
	Branch               string `json:"branch"` // optional — defaults to repo default branch
}

// CreateApp registers a new App from a linked GitHub repository.
//
// Flow:
//  1. Validate request fields + Developer role
//  2. Resolve target branch + HEAD SHA via the GitHub App
//  3. Fetch appstore.yaml at that SHA + parse + run schema validation
//  4. Stage-1 validate (fetch every declared evidence file, check size + content-type)
//  5. Fetch Chart.yaml + values.schema.json from the declared chart path
//  6. Persist App + AppVersion + AuditSubmission + EvidenceBlobs in one transaction
//  7. Audit-log the event
func (h *Handlers) CreateApp(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}

	var req CreateAppRequest
	if err := c.Bind(&req); err != nil {
		return apierror.New(http.StatusBadRequest, "bad_request", "invalid JSON")
	}

	// Request-level validation.
	fields := make([]apierror.FieldError, 0)
	if req.GitHubInstallationID == 0 {
		fields = append(fields, apierror.FieldError{
			Path: "github_installation_id", Code: "required",
			Message: "GitHub App installation id is required",
		})
	}
	if strings.TrimSpace(req.RepoOwner) == "" {
		fields = append(fields, apierror.FieldError{
			Path: "repo_owner", Code: "required", Message: "repo_owner is required",
		})
	}
	if strings.TrimSpace(req.RepoName) == "" {
		fields = append(fields, apierror.FieldError{
			Path: "repo_name", Code: "required", Message: "repo_name is required",
		})
	}
	if len(fields) > 0 {
		return apierror.NewValidation("request validation failed", fields)
	}

	ctx := c.Request().Context()

	// 2. Resolve target branch + HEAD SHA.
	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		def, err := h.GitHub.GetDefaultBranch(ctx, req.GitHubInstallationID, req.RepoOwner, req.RepoName)
		if err != nil {
			return mapGitHubErr(err)
		}
		branch = def
	}
	sha, err := h.GitHub.ResolveRef(ctx, req.GitHubInstallationID, req.RepoOwner, req.RepoName, branch)
	if err != nil {
		return mapGitHubErr(err)
	}

	// 3. Fetch + parse appstore.yaml at SHA.
	manifestBytes, _, err := h.GitHub.GetFileAtRef(ctx, req.GitHubInstallationID, req.RepoOwner, req.RepoName, manifestFileName, sha)
	if err != nil {
		if errors.Is(err, gh.ErrFileNotFound) {
			return rejectStage1(apierror.NewValidation("appstore.yaml is missing from the repo root", []apierror.FieldError{
				{Path: "appstore.yaml", Code: "file_not_found",
					Message: "the platform looks for ./appstore.yaml at the linked branch HEAD"},
			}), metrics.GateSchema)
		}
		return fetchErr(err, metrics.GateSchema)
	}

	m, err := manifest.Parse(manifestBytes)
	if err != nil {
		return rejectStage1(apierror.NewValidation("appstore.yaml could not be parsed", []apierror.FieldError{
			{Path: "appstore.yaml", Code: "yaml_parse_failed", Message: err.Error()},
		}), metrics.GateSchema)
	}
	if schemaErrs := m.ValidateSchema(); len(schemaErrs) > 0 {
		return rejectStage1(apierror.NewValidation("appstore.yaml failed schema validation", schemaErrs),
			manifest.RejectedGates(schemaErrs)...)
	}

	// 4. Stage-1 evidence validation.
	evidence, sarifWouldBlock, evErrs := m.ValidateEvidence(ctx, h.GitHub, req.GitHubInstallationID, req.RepoOwner, req.RepoName, sha, h.SARIFGateMode)
	if len(evErrs) > 0 {
		return rejectStage1(apierror.NewValidation("evidence file validation failed", evErrs),
			manifest.RejectedGates(evErrs)...)
	}

	// 5. Fetch Chart.yaml + values.schema.json.
	chartPath := strings.TrimPrefix(m.Spec.Chart.Path, "./")
	chartYamlBytes, _, err := h.GitHub.GetFileAtRef(ctx, req.GitHubInstallationID, req.RepoOwner, req.RepoName, path.Join(chartPath, "Chart.yaml"), sha)
	if err != nil {
		if errors.Is(err, gh.ErrFileNotFound) {
			return rejectStage1(apierror.NewValidation("chart metadata missing", []apierror.FieldError{
				{Path: "spec.chart.path", Code: "chart_yaml_missing",
					Message: fmt.Sprintf("expected %s/Chart.yaml at the audited commit", chartPath)},
			}), metrics.GateChart)
		}
		return fetchErr(err, metrics.GateChart)
	}
	valuesSchemaBytes, _, err := h.GitHub.GetFileAtRef(ctx, req.GitHubInstallationID, req.RepoOwner, req.RepoName, path.Join(chartPath, "values.schema.json"), sha)
	if err != nil {
		if errors.Is(err, gh.ErrFileNotFound) {
			return rejectStage1(apierror.NewValidation("chart values schema missing", []apierror.FieldError{
				{Path: "spec.chart.path", Code: "values_schema_missing",
					Message: fmt.Sprintf("expected %s/values.schema.json at the audited commit (required for the deploy form generator)", chartPath)},
			}), metrics.GateChart)
		}
		return fetchErr(err, metrics.GateChart)
	}

	manifestJSON, err := yamlToJSON(manifestBytes)
	if err != nil {
		return rejectStage1(apierror.NewValidation("appstore.yaml could not be normalised", []apierror.FieldError{
			{Path: "appstore.yaml", Code: "yaml_to_json_failed", Message: err.Error()},
		}), metrics.GateSchema)
	}
	chartMetaJSON, err := yamlToJSON(chartYamlBytes)
	if err != nil {
		return rejectStage1(apierror.NewValidation("Chart.yaml could not be normalised", []apierror.FieldError{
			{Path: "Chart.yaml", Code: "yaml_to_json_failed", Message: err.Error()},
		}), metrics.GateChart)
	}
	if err := valuesschema.Enforceable(valuesSchemaBytes); err != nil {
		return rejectStage1(apierror.NewValidation("values.schema.json cannot be enforced", []apierror.FieldError{
			{Path: "values.schema.json", Code: "unenforceable_schema", Message: "values.schema.json must be an enforceable JSON Schema (draft-07+): " + err.Error()},
		}), metrics.GateChart)
	}

	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		return fmt.Errorf("marshal evidence: %w", err)
	}

	// 6. Persist in one transaction.
	var created models.App
	var versionID string
	var verUUID, subUUID uuid.UUID
	err = h.DB.RW.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Slug uniqueness.
		var clash int64
		if err := tx.Model(&models.App{}).
			Where("slug = ?", m.Metadata.Name).
			Count(&clash).Error; err != nil {
			return err
		}
		if clash > 0 {
			return apierror.New(http.StatusConflict, "slug_taken",
				fmt.Sprintf("an app named %q is already registered", m.Metadata.Name))
		}
		// Repo uniqueness.
		if err := tx.Model(&models.App{}).
			Where("github_repo_owner = ? AND github_repo_name = ?", req.RepoOwner, req.RepoName).
			Count(&clash).Error; err != nil {
			return err
		}
		if clash > 0 {
			return apierror.New(http.StatusConflict, "repo_already_linked",
				fmt.Sprintf("repository %s/%s is already linked to an app", req.RepoOwner, req.RepoName))
		}

		app := models.App{
			Slug:                 m.Metadata.Name,
			OwningOrganizationID: p.OrganizationID,
			OwningUserID:         p.UserID,
			GitHubInstallationID: req.GitHubInstallationID,
			GitHubRepoOwner:      req.RepoOwner,
			GitHubRepoName:       req.RepoName,
			GitHubDefaultBranch:  branch,
			State:                models.AppDraft,
			RepoLinkState:        models.RepoConnected,
		}
		if err := tx.Create(&app).Error; err != nil {
			return err
		}

		// Mint both UUIDv7s up-front so we can wire the (intentionally
		// unenforced) cross-references in a single tx. See
		// internal/db/models/app_version.go for why neither side has a
		// DB-level FK constraint.
		subID, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("uuid for submission: %w", err)
		}
		verID, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("uuid for version: %w", err)
		}

		sub := models.AuditSubmission{
			AppVersionID:        verID,
			FormResponses:       datatypes.JSON(`{}`),
			FormVersion:         "v1.0",
			EvidenceFiles:       datatypes.JSON(evidenceJSON),
			AutoValidationState: models.AutoValPassed,
		}
		sub.ID = subID

		ver := models.AppVersion{
			AppID:             app.ID,
			GitRef:            branch,
			GitSHA:            sha,
			AppstoreYAML:      datatypes.JSON(manifestJSON),
			ValuesSchema:      datatypes.JSON(valuesSchemaBytes),
			ChartMetadata:     datatypes.JSON(chartMetaJSON),
			AuditSubmissionID: subID,
			State:             models.AppVersionDraft,
		}
		ver.ID = verID

		if err := tx.Create(&sub).Error; err != nil {
			return err
		}
		if err := tx.Create(&ver).Error; err != nil {
			return err
		}
		if err := storeEvidenceBlobs(tx, subID, evidence); err != nil {
			return err
		}

		created = app
		versionID = ver.ID.String()
		verUUID, subUUID = ver.ID, subID
		return nil
	})
	if err != nil {
		var apiErr *apierror.Error
		if errors.As(err, &apiErr) {
			return apiErr
		}
		return err
	}
	metrics.RecordSubmission(nil)

	// 7. Audit log (best-effort, after commit).
	_ = h.Audit.Log(ctx, audit.Event{
		Type:                "app.created",
		ActorUserID:         &p.UserID,
		ActorOrganizationID: &p.OrganizationID,
		SubjectType:         "app",
		SubjectID:           created.ID,
		Payload: map[string]any{
			"slug":           created.Slug,
			"github_repo":    req.RepoOwner + "/" + req.RepoName,
			"branch":         branch,
			"git_sha":        sha,
			"app_version_id": versionID,
			"evidence_count": len(evidence),
		},
	})
	h.reportSARIFWouldBlock(ctx, sarifgate.Subject{
		AppID:               created.ID,
		AppSlug:             created.Slug,
		AppVersionID:        verUUID,
		AuditSubmissionID:   subUUID,
		GitSHA:              sha,
		ActorUserID:         &p.UserID,
		ActorOrganizationID: &p.OrganizationID,
	}, sarifWouldBlock)

	return c.JSON(http.StatusCreated, map[string]any{
		"id":                    created.ID.String(),
		"slug":                  created.Slug,
		"state":                 string(created.State),
		"github_repo":           req.RepoOwner + "/" + req.RepoName,
		"github_default_branch": branch,
		"app_version": map[string]any{
			"id":      versionID,
			"git_ref": branch,
			"git_sha": sha,
			"state":   string(models.AppVersionDraft),
		},
	})
}

// storeEvidenceBlobs persists the raw bytes of each validated evidence file
// against submission subID, so auditors can re-download the exact artefacts
// even after the source repo changes or the GitHub token expires. One row
// per evidence file, keyed by logical_name. Run it inside the transaction
// that creates the submission.
func storeEvidenceBlobs(tx *gorm.DB, subID uuid.UUID, evidence []manifest.EvidenceFileInfo) error {
	for _, ev := range evidence {
		blob := models.EvidenceBlob{
			AuditSubmissionID: subID,
			LogicalName:       ev.LogicalName,
			RepoPath:          ev.RepoPath,
			ContentType:       ev.ContentType,
			Content:           ev.Content,
		}
		if err := tx.Create(&blob).Error; err != nil {
			return fmt.Errorf("persist evidence blob %q: %w", ev.LogicalName, err)
		}
	}
	return nil
}

// reportSARIFWouldBlock hands report-mode SARIF findings to the reporter
// once the submission is persisted, so the report carries its IDs. Findings
// on submissions rejected for other reasons are not reported: those would
// fail under enforce too, so they are not part of the rollout's blast radius.
func (h *Handlers) reportSARIFWouldBlock(ctx context.Context, s sarifgate.Subject, findings []manifest.SARIFFinding) {
	if h.SARIFReporter == nil || len(findings) == 0 {
		return
	}
	h.SARIFReporter.ReportWouldBlock(ctx, s, findings)
}

// appNotOwnedError is the refusal for a caller acting on an app they do not
// own. It matches GetApp's 404 when the caller may not see the app at all,
// so an unpublished slug cannot be confirmed through the developer routes,
// and is 403 otherwise.
func appNotOwnedError(p *auth.Principal, app models.App) error {
	if !appVisibleTo(p, app) {
		return apierror.New(http.StatusNotFound, "not_found", "app not found")
	}
	return apierror.New(http.StatusForbidden, "forbidden", "you do not own this app")
}

// ListAppVersions returns every AppVersion for one App. Developer (own
// org) or Auditor (any) only.
func (h *Handlers) ListAppVersions(c echo.Context) error {
	slug := strings.TrimSpace(c.Param("slug"))
	if slug == "" {
		return apierror.New(http.StatusNotFound, "not_found", "app not found")
	}

	var app models.App
	err := h.DB.RW.WithContext(c.Request().Context()).
		Where("slug = ?", slug).First(&app).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apierror.New(http.StatusNotFound, "not_found", "app not found")
	}
	if err != nil {
		return err
	}

	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}
	if !p.Has(models.RoleAuditor, models.RolePlatformAdmin) &&
		p.OrganizationID != app.OwningOrganizationID {
		return appNotOwnedError(p, app)
	}

	var versions []models.AppVersion
	if err := h.DB.RW.WithContext(c.Request().Context()).
		Where("app_id = ?", app.ID).
		Order("created_at DESC").
		Find(&versions).Error; err != nil {
		return err
	}

	out := make([]map[string]any, 0, len(versions))
	for _, v := range versions {
		out = append(out, map[string]any{
			"id":         v.ID.String(),
			"git_ref":    v.GitRef,
			"git_sha":    v.GitSHA,
			"state":      string(v.State),
			"created_at": v.CreatedAt,
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"items": out})
}

// CreateAppVersionRequest is the body of POST /apps/:slug/versions.
type CreateAppVersionRequest struct {
	GitRef string `json:"git_ref"` // tag or branch; resolved to SHA
}

// CreateAppVersion registers a new AppVersion for an existing App at the
// supplied git ref. Re-runs Stage-1 validation against the new SHA and
// stores the evidence files in the same transaction as the version.
// Developer (own) only.
func (h *Handlers) CreateAppVersion(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}

	slug := strings.TrimSpace(c.Param("slug"))
	var app models.App
	if err := h.DB.RW.WithContext(c.Request().Context()).
		Where("slug = ?", slug).First(&app).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apierror.New(http.StatusNotFound, "not_found", "app not found")
		}
		return err
	}
	if app.OwningOrganizationID != p.OrganizationID {
		return appNotOwnedError(p, app)
	}
	if !p.Has(models.RolePlatformAdmin) && app.OwningUserID != p.UserID {
		return apierror.New(http.StatusForbidden, "forbidden", "you do not own this app")
	}

	var req CreateAppVersionRequest
	if err := c.Bind(&req); err != nil {
		return apierror.New(http.StatusBadRequest, "bad_request", "invalid JSON")
	}
	ref := strings.TrimSpace(req.GitRef)
	if ref == "" {
		ref = app.GitHubDefaultBranch
	}

	ctx := c.Request().Context()
	sha, err := h.GitHub.ResolveRef(ctx, app.GitHubInstallationID, app.GitHubRepoOwner, app.GitHubRepoName, ref)
	if err != nil {
		return mapGitHubErr(err)
	}

	manifestBytes, _, err := h.GitHub.GetFileAtRef(ctx, app.GitHubInstallationID, app.GitHubRepoOwner, app.GitHubRepoName, manifestFileName, sha)
	if err != nil {
		return fetchErr(err, metrics.GateSchema)
	}
	m, err := manifest.Parse(manifestBytes)
	if err != nil {
		return rejectStage1(apierror.NewValidation("appstore.yaml parse failed", []apierror.FieldError{
			{Path: "appstore.yaml", Code: "yaml_parse_failed", Message: err.Error()},
		}), metrics.GateSchema)
	}
	if schemaErrs := m.ValidateSchema(); len(schemaErrs) > 0 {
		return rejectStage1(apierror.NewValidation("appstore.yaml failed schema validation", schemaErrs),
			manifest.RejectedGates(schemaErrs)...)
	}
	// Reject changing slug between versions.
	if m.Metadata.Name != app.Slug {
		return rejectStage1(apierror.NewValidation("manifest slug mismatch", []apierror.FieldError{
			{Path: "metadata.name", Code: "slug_mismatch",
				Message: fmt.Sprintf("manifest declares %q but app slug is %q (slug is immutable)", m.Metadata.Name, app.Slug)},
		}), metrics.GateSchema)
	}

	evidence, sarifWouldBlock, evErrs := m.ValidateEvidence(ctx, h.GitHub, app.GitHubInstallationID, app.GitHubRepoOwner, app.GitHubRepoName, sha, h.SARIFGateMode)
	if len(evErrs) > 0 {
		return rejectStage1(apierror.NewValidation("evidence file validation failed", evErrs),
			manifest.RejectedGates(evErrs)...)
	}

	chartPath := strings.TrimPrefix(m.Spec.Chart.Path, "./")
	chartYamlBytes, _, err := h.GitHub.GetFileAtRef(ctx, app.GitHubInstallationID, app.GitHubRepoOwner, app.GitHubRepoName, path.Join(chartPath, "Chart.yaml"), sha)
	if err != nil {
		return fetchErr(err, metrics.GateChart)
	}
	valuesSchemaBytes, _, err := h.GitHub.GetFileAtRef(ctx, app.GitHubInstallationID, app.GitHubRepoOwner, app.GitHubRepoName, path.Join(chartPath, "values.schema.json"), sha)
	if err != nil {
		return fetchErr(err, metrics.GateChart)
	}
	manifestJSON, err := yamlToJSON(manifestBytes)
	if err != nil {
		return fmt.Errorf("manifest json: %w", err)
	}
	chartMetaJSON, err := yamlToJSON(chartYamlBytes)
	if err != nil {
		return fmt.Errorf("chart json: %w", err)
	}
	if err := valuesschema.Enforceable(valuesSchemaBytes); err != nil {
		return rejectStage1(apierror.NewValidation("values.schema.json cannot be enforced", []apierror.FieldError{
			{Path: "values.schema.json", Code: "unenforceable_schema", Message: "values.schema.json must be an enforceable JSON Schema (draft-07+): " + err.Error()},
		}), metrics.GateChart)
	}
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		return fmt.Errorf("marshal evidence: %w", err)
	}

	var newVersionID string
	var verUUID, subUUID uuid.UUID
	err = h.DB.RW.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// (app_id, git_sha) unique — short-circuit if this SHA was already
		// recorded.
		var clash int64
		if err := tx.Model(&models.AppVersion{}).
			Where("app_id = ? AND git_sha = ?", app.ID, sha).
			Count(&clash).Error; err != nil {
			return err
		}
		if clash > 0 {
			return apierror.New(http.StatusConflict, "version_already_exists",
				"a version at that commit SHA already exists")
		}

		subID, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("uuid for submission: %w", err)
		}
		verID, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("uuid for version: %w", err)
		}

		sub := models.AuditSubmission{
			AppVersionID:        verID,
			FormResponses:       datatypes.JSON(`{}`),
			FormVersion:         "v1.0",
			EvidenceFiles:       datatypes.JSON(evidenceJSON),
			AutoValidationState: models.AutoValPassed,
		}
		sub.ID = subID

		ver := models.AppVersion{
			AppID:             app.ID,
			GitRef:            ref,
			GitSHA:            sha,
			AppstoreYAML:      datatypes.JSON(manifestJSON),
			ValuesSchema:      datatypes.JSON(valuesSchemaBytes),
			ChartMetadata:     datatypes.JSON(chartMetaJSON),
			AuditSubmissionID: subID,
			State:             models.AppVersionDraft,
		}
		ver.ID = verID

		if err := tx.Create(&sub).Error; err != nil {
			return err
		}
		if err := tx.Create(&ver).Error; err != nil {
			return err
		}
		if err := storeEvidenceBlobs(tx, subID, evidence); err != nil {
			return err
		}
		newVersionID = ver.ID.String()
		verUUID, subUUID = ver.ID, subID
		return nil
	})
	if err != nil {
		var apiErr *apierror.Error
		if errors.As(err, &apiErr) {
			return apiErr
		}
		return err
	}
	metrics.RecordSubmission(nil)

	_ = h.Audit.Log(ctx, audit.Event{
		Type:                "app.version_created",
		ActorUserID:         &p.UserID,
		ActorOrganizationID: &p.OrganizationID,
		SubjectType:         "app",
		SubjectID:           app.ID,
		Payload: map[string]any{
			"app_slug":       app.Slug,
			"app_version_id": newVersionID,
			"git_ref":        ref,
			"git_sha":        sha,
		},
	})
	h.reportSARIFWouldBlock(ctx, sarifgate.Subject{
		AppID:               app.ID,
		AppSlug:             app.Slug,
		AppVersionID:        verUUID,
		AuditSubmissionID:   subUUID,
		GitSHA:              sha,
		ActorUserID:         &p.UserID,
		ActorOrganizationID: &p.OrganizationID,
	}, sarifWouldBlock)

	return c.JSON(http.StatusCreated, map[string]any{
		"id":      newVersionID,
		"git_ref": ref,
		"git_sha": sha,
		"state":   string(models.AppVersionDraft),
	})
}

// SubmitApp transitions the App and its latest draft AppVersion to
// `submitted`, eligible for auditor review.
func (h *Handlers) SubmitApp(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}

	slug := strings.TrimSpace(c.Param("slug"))
	ctx := c.Request().Context()

	var app models.App
	if err := h.DB.RW.WithContext(ctx).Where("slug = ?", slug).First(&app).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apierror.New(http.StatusNotFound, "not_found", "app not found")
		}
		return err
	}
	if app.OwningOrganizationID != p.OrganizationID {
		return appNotOwnedError(p, app)
	}
	if !p.Has(models.RolePlatformAdmin) && app.OwningUserID != p.UserID {
		return apierror.New(http.StatusForbidden, "forbidden", "you do not own this app")
	}

	if app.State == models.AppSubmitted {
		return apierror.New(http.StatusConflict, "already_submitted", "this app is already awaiting audit review")
	}

	// Find the latest version eligible for submission.
	var ver models.AppVersion
	err := h.DB.RW.WithContext(ctx).
		Where("app_id = ? AND state IN ?", app.ID, []models.AppVersionState{
			models.AppVersionDraft, models.AppVersionNeedsChanges,
		}).
		Order("created_at DESC").
		First(&ver).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apierror.New(http.StatusConflict, "no_submittable_version",
			"no draft or needs_changes version to submit; create a new version first")
	}
	if err != nil {
		return err
	}

	err = h.DB.RW.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&ver).Update("state", models.AppVersionSubmitted).Error; err != nil {
			return err
		}
		if app.State == models.AppDraft || app.State == models.AppNeedsChanges {
			if err := tx.Model(&app).Update("state", models.AppSubmitted).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	_ = h.Audit.Log(ctx, audit.Event{
		Type:                "app.submitted",
		ActorUserID:         &p.UserID,
		ActorOrganizationID: &p.OrganizationID,
		SubjectType:         "app",
		SubjectID:           app.ID,
		Payload: map[string]any{
			"app_slug":       app.Slug,
			"app_version_id": ver.ID.String(),
			"git_sha":        ver.GitSHA,
		},
	})

	return c.JSON(http.StatusOK, map[string]any{
		"state":          string(models.AppSubmitted),
		"app_version_id": ver.ID.String(),
	})
}

// --- helpers --------------------------------------------------------------

// rejectStage1 counts a submission rejected by the given Stage-1 gates and
// returns err. With no gates (only infrastructure errors) nothing is
// counted.
func rejectStage1(err error, gates ...string) error {
	if len(gates) > 0 {
		metrics.RecordSubmission(gates)
	}
	return err
}

// fetchErr maps a GitHub error for a required Stage-1 file. A missing or
// oversized file is a rejection by gate; any other error says nothing about
// the submission and is not counted.
func fetchErr(err error, gate string) error {
	if errors.Is(err, gh.ErrFileNotFound) || errors.Is(err, gh.ErrFileTooLarge) {
		return rejectStage1(mapGitHubErr(err), gate)
	}
	return mapGitHubErr(err)
}

// mapGitHubErr translates internal/integrations/github errors into the
// platform's HTTP envelope. NullClient errors -> 503; not-found -> 422.
func mapGitHubErr(err error) error {
	switch {
	case errors.Is(err, gh.ErrNotConfigured):
		return apierror.New(http.StatusServiceUnavailable, "github_not_configured",
			"GitHub App is not configured on this environment; ask your platform admin")
	case errors.Is(err, gh.ErrFileNotFound):
		return apierror.NewValidation("required file missing", []apierror.FieldError{
			{Path: "github", Code: "file_not_found", Message: err.Error()},
		})
	case errors.Is(err, gh.ErrFileTooLarge):
		return apierror.NewValidation("file exceeds platform size limit", []apierror.FieldError{
			{Path: "github", Code: "file_too_large", Message: err.Error()},
		})
	default:
		slog.Error("github call failed", "err", err)
		return apierror.New(http.StatusBadGateway, "github_unavailable",
			"GitHub call failed; check app configuration")
	}
}

// yamlToJSON normalises YAML bytes into a canonical JSON encoding so we
// can store them in jsonb columns and query them with Postgres operators.
func yamlToJSON(in []byte) ([]byte, error) {
	var root any
	if err := yaml.Unmarshal(in, &root); err != nil {
		return nil, err
	}
	// yaml.v3 produces map[interface{}]interface{} for nested maps; that
	// breaks encoding/json. Convert all such maps to map[string]any.
	clean, err := normaliseForJSON(root)
	if err != nil {
		return nil, err
	}
	return json.Marshal(clean)
}

func normaliseForJSON(v any) (any, error) {
	switch typed := v.(type) {
	case map[any]any:
		out := make(map[string]any, len(typed))
		for k, val := range typed {
			ks, ok := k.(string)
			if !ok {
				return nil, fmt.Errorf("non-string key %v in YAML map", k)
			}
			cleaned, err := normaliseForJSON(val)
			if err != nil {
				return nil, err
			}
			out[ks] = cleaned
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, val := range typed {
			cleaned, err := normaliseForJSON(val)
			if err != nil {
				return nil, err
			}
			out[k] = cleaned
		}
		return out, nil
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			cleaned, err := normaliseForJSON(item)
			if err != nil {
				return nil, err
			}
			out[i] = cleaned
		}
		return out, nil
	default:
		return v, nil
	}
}
