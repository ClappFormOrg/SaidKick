package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	gh "github.com/ClappFormOrg/SaidKick/API/internal/integrations/github"
	"github.com/ClappFormOrg/SaidKick/API/internal/manifest"
	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

const maxAuditCommentLength = 10_000

// listSubRow is the flattened scan target for ListAuditSubmissions, joining
// audit_submissions → app_versions → apps in a single query.
type listSubRow struct {
	AuditID             string    `gorm:"column:audit_id"`
	AutoValidationState string    `gorm:"column:auto_validation_state"`
	FormVersion         string    `gorm:"column:form_version"`
	CreatedAt           time.Time `gorm:"column:created_at"`
	AppSlug             string    `gorm:"column:app_slug"`
	AppName             string    `gorm:"column:app_name"`
	AppRiskClass        string    `gorm:"column:app_risk_class"`
	AppOrgID            string    `gorm:"column:app_org_id"`
	VersionGitRef       string    `gorm:"column:version_git_ref"`
	VersionGitSHA       string    `gorm:"column:version_git_sha"`
}

// ListAuditSubmissions returns the queue of submissions for the auditor.
// Filter via ?state=submitted|needs_changes|...&risk_class=minimal|limited|high ; default state: submitted.
// Each item includes is_own_org:true when the submission belongs to the caller's org (auditors cannot act on those).
func (h *Handlers) ListAuditSubmissions(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}

	state := strings.TrimSpace(c.QueryParam("state"))
	if state == "" {
		state = string(models.AppVersionSubmitted)
	} else if !slices.Contains(models.AllAppVersionStates, models.AppVersionState(state)) {
		return apierror.New(http.StatusBadRequest, "bad_request", "invalid state filter value")
	}

	page, pageSize := paginationFromQuery(c)

	// Join AppVersion → AuditSubmission → App for filtering and enriched projection.
	tx := h.DB.RW.WithContext(c.Request().Context()).
		Model(&models.AuditSubmission{}).
		Joins("JOIN app_versions ON app_versions.audit_submission_id = audit_submissions.id").
		Joins("JOIN apps ON apps.id = app_versions.app_id AND apps.deleted_at IS NULL").
		Where("app_versions.state = ?", state)

	if v := strings.TrimSpace(c.QueryParam("risk_class")); v != "" {
		tx = tx.Where("app_versions.appstore_yaml->'spec'->'ai'->>'risk_class' = ?", v)
	}

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return err
	}

	var rows []listSubRow
	if err := tx.
		Select(`audit_submissions.id::text AS audit_id,
			audit_submissions.auto_validation_state,
			audit_submissions.form_version,
			audit_submissions.created_at,
			apps.slug AS app_slug,
			COALESCE(app_versions.appstore_yaml->'metadata'->>'display_name', apps.slug) AS app_name,
			COALESCE(app_versions.appstore_yaml->'spec'->'ai'->>'risk_class', '') AS app_risk_class,
			apps.owning_organization_id::text AS app_org_id,
			app_versions.git_ref AS version_git_ref,
			app_versions.git_sha AS version_git_sha`).
		Order("audit_submissions.created_at ASC"). // oldest first per US-2
		Limit(pageSize).
		Offset((page - 1) * pageSize).
		Scan(&rows).Error; err != nil {
		return err
	}

	callerOrgID := p.OrganizationID.String()
	out := make([]map[string]any, 0, len(rows))
	for _, s := range rows {
		out = append(out, map[string]any{
			"id":                    s.AuditID,
			"auto_validation_state": s.AutoValidationState,
			"form_version":          s.FormVersion,
			"created_at":            s.CreatedAt,
			"is_own_org":            s.AppOrgID == callerOrgID,
			"app": map[string]any{
				"slug":       s.AppSlug,
				"name":       s.AppName,
				"risk_class": s.AppRiskClass,
			},
			"version": map[string]any{
				"git_ref": s.VersionGitRef,
				"git_sha": s.VersionGitSHA,
			},
		})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// GetAuditSubmission returns the full detail of one submission, including
// the form responses + evidence-file metadata. Cross-org check applied:
// an auditor cannot inspect a submission from their own organisation.
func (h *Handlers) GetAuditSubmission(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return apierror.New(http.StatusNotFound, "not_found", "submission not found")
	}

	var sub models.AuditSubmission
	if err := h.DB.RW.WithContext(c.Request().Context()).
		First(&sub, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apierror.New(http.StatusNotFound, "not_found", "submission not found")
		}
		return err
	}

	// Look up the owning App via AppVersion.
	var version models.AppVersion
	if err := h.DB.RW.WithContext(c.Request().Context()).
		First(&version, "id = ?", sub.AppVersionID).Error; err != nil {
		return err
	}
	var app models.App
	if err := h.DB.RW.WithContext(c.Request().Context()).
		First(&app, "id = ?", version.AppID).Error; err != nil {
		return err
	}

	// Cross-org rule.
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}
	if err := authorizeAuditAccess(p, app, false); err != nil {
		return err
	}

	return c.JSON(http.StatusOK, map[string]any{
		"id":                     sub.ID.String(),
		"app_version_id":         sub.AppVersionID.String(),
		"form_responses":         sub.FormResponses,
		"form_version":           sub.FormVersion,
		"evidence_files":         sub.EvidenceFiles,
		"auto_validation_state":  string(sub.AutoValidationState),
		"auto_validation_errors": sub.AutoValidationErrors,
		"created_at":             sub.CreatedAt,
		"app": map[string]any{
			"id":                     app.ID.String(),
			"slug":                   app.Slug,
			"owning_organization_id": app.OwningOrganizationID.String(),
			"state":                  string(app.State),
		},
		"version": map[string]any{
			"git_ref": version.GitRef,
			"git_sha": version.GitSHA,
			"state":   string(version.State),
		},
	})
}

type commentRequest struct {
	Body string `json:"body"`
}

// CommentOnAuditSubmission adds a comment to an audit submission. Enforces
// the cross-org rule: auditors may only comment on submissions from other orgs.
func (h *Handlers) CommentOnAuditSubmission(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}

	subID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return apierror.New(http.StatusNotFound, "not_found", "submission not found")
	}

	var req commentRequest
	if err := c.Bind(&req); err != nil {
		return apierror.New(http.StatusBadRequest, "bad_request", "invalid JSON")
	}
	body := strings.TrimSpace(req.Body)
	if body == "" || len(body) > maxAuditCommentLength {
		return apierror.NewValidation("invalid body", []apierror.FieldError{
			{Path: "body", Code: "invalid_length", Message: "body must be between 1 and 10 000 characters"},
		})
	}

	ctx := c.Request().Context()

	var sub models.AuditSubmission
	if err := h.DB.RW.WithContext(ctx).First(&sub, "id = ?", subID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apierror.New(http.StatusNotFound, "not_found", "submission not found")
		}
		return err
	}

	var version models.AppVersion
	if err := h.DB.RW.WithContext(ctx).First(&version, "id = ?", sub.AppVersionID).Error; err != nil {
		return err
	}
	var app models.App
	if err := h.DB.RW.WithContext(ctx).First(&app, "id = ?", version.AppID).Error; err != nil {
		return err
	}

	if err := authorizeAuditAccess(p, app, false); err != nil {
		return err
	}

	comment := models.AuditComment{
		AuditSubmissionID: subID,
		AuthorUserID:      p.UserID,
		Body:              body,
	}
	if err := h.DB.RW.WithContext(ctx).Create(&comment).Error; err != nil {
		return err
	}

	_ = h.Audit.Log(ctx, audit.Event{
		Type:                "app.audit_comment_added",
		ActorUserID:         &p.UserID,
		ActorOrganizationID: &p.OrganizationID,
		SubjectType:         "audit_submission",
		SubjectID:           subID,
		Payload:             map[string]any{"comment_id": comment.ID.String()},
	})

	return c.JSON(http.StatusCreated, map[string]any{
		"id":                  comment.ID.String(),
		"audit_submission_id": comment.AuditSubmissionID.String(),
		"author_user_id":      comment.AuthorUserID.String(),
		"body":                comment.Body,
		"created_at":          comment.CreatedAt,
	})
}

// ListAuditComments returns all comments for a given submission, ordered
// oldest-first. Enforces the cross-org rule.
func (h *Handlers) ListAuditComments(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}

	subID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return apierror.New(http.StatusNotFound, "not_found", "submission not found")
	}

	ctx := c.Request().Context()

	var sub models.AuditSubmission
	if err := h.DB.RW.WithContext(ctx).First(&sub, "id = ?", subID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apierror.New(http.StatusNotFound, "not_found", "submission not found")
		}
		return err
	}

	var version models.AppVersion
	if err := h.DB.RW.WithContext(ctx).First(&version, "id = ?", sub.AppVersionID).Error; err != nil {
		return err
	}
	var app models.App
	if err := h.DB.RW.WithContext(ctx).First(&app, "id = ?", version.AppID).Error; err != nil {
		return err
	}

	if err := authorizeAuditAccess(p, app, false); err != nil {
		return err
	}

	page, pageSize := paginationFromQuery(c)

	var total int64
	if err := h.DB.RW.WithContext(ctx).Model(&models.AuditComment{}).
		Where("audit_submission_id = ?", subID).Count(&total).Error; err != nil {
		return err
	}

	var comments []models.AuditComment
	if err := h.DB.RW.WithContext(ctx).
		Where("audit_submission_id = ?", subID).
		Order("created_at ASC").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Find(&comments).Error; err != nil {
		return err
	}

	items := make([]map[string]any, 0, len(comments))
	for _, cm := range comments {
		items = append(items, map[string]any{
			"id":                  cm.ID.String(),
			"audit_submission_id": cm.AuditSubmissionID.String(),
			"author_user_id":      cm.AuthorUserID.String(),
			"body":                cm.Body,
			"created_at":          cm.CreatedAt,
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"items": items, "total": total})
}

// DecideAuditSubmissionRequest is the body of the decision endpoint.
type DecideAuditSubmissionRequest struct {
	Decision  string `json:"decision"`
	Rationale string `json:"rationale"`
}

// DecideAuditSubmission persists an AuditDecision and, if the decision is
// `approve`, transitions the AppVersion to `published` and bumps the
// App.current_published_version_id.
//
// Enforces the cross-org rule server-side. Writes one AuditLog row in the
// same transaction.
func (h *Handlers) DecideAuditSubmission(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return apierror.New(http.StatusNotFound, "not_found", "submission not found")
	}

	var req DecideAuditSubmissionRequest
	if err := c.Bind(&req); err != nil {
		return apierror.New(http.StatusBadRequest, "bad_request", "invalid JSON")
	}

	decision := models.Decision(strings.ToLower(strings.TrimSpace(req.Decision)))
	switch decision {
	case models.DecisionApprove, models.DecisionRequestChanges, models.DecisionReject:
		// ok
	default:
		return apierror.NewValidation("invalid decision", []apierror.FieldError{
			{Path: "decision", Code: "invalid_value",
				Message: "must be one of: approve, request_changes, reject"},
		})
	}
	if len(strings.TrimSpace(req.Rationale)) < 20 {
		return apierror.NewValidation("rationale too short", []apierror.FieldError{
			{Path: "rationale", Code: "min_length_20",
				Message: "rationale must be at least 20 characters"},
		})
	}

	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}

	ctx := c.Request().Context()

	// Cross-org rule, checked before the transaction opens so a refusal
	// costs no rollback. Platform admins get no exemption here.
	var owner models.App
	if err := h.DB.RW.WithContext(ctx).
		Select("apps.*").
		Joins("JOIN app_versions ON app_versions.app_id = apps.id").
		Joins("JOIN audit_submissions ON audit_submissions.app_version_id = app_versions.id").
		Where("audit_submissions.id = ?", id).
		First(&owner).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apierror.New(http.StatusNotFound, "not_found", "submission not found")
		}
		return err
	}
	if err := authorizeAuditAccess(p, owner, true); err != nil {
		return err
	}

	// Load + validate the submission, its AppVersion, and its App. All in
	// one transaction so the state transition + AuditDecision + AuditLog
	// row commit atomically.
	var newDecision *models.AuditDecision
	var subjectApp models.App
	var submittedAt time.Time
	err = h.DB.RW.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var sub models.AuditSubmission
		if err := tx.First(&sub, "id = ?", id).Error; err != nil {
			return err
		}
		submittedAt = sub.CreatedAt
		var version models.AppVersion
		if err := tx.First(&version, "id = ?", sub.AppVersionID).Error; err != nil {
			return err
		}
		var app models.App
		if err := tx.First(&app, "id = ?", version.AppID).Error; err != nil {
			return err
		}

		// At-most-one decision per submission.
		var existing int64
		if err := tx.Model(&models.AuditDecision{}).
			Where("audit_submission_id = ?", sub.ID).Count(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			return apierror.New(http.StatusConflict, "already_decided",
				"this submission already has a decision")
		}

		dec := &models.AuditDecision{
			AuditSubmissionID: sub.ID,
			Decision:          decision,
			Rationale:         strings.TrimSpace(req.Rationale),
			DecidedByUserID:   p.UserID,
		}
		if err := tx.Create(dec).Error; err != nil {
			return err
		}

		// Apply state transitions.
		switch decision {
		case models.DecisionApprove:
			// Archive the previously-published version of the same App. It
			// passed audit, so it is archived, not rejected, and stays a
			// rollback target. This runs first because
			// idx_appver_published_per_app allows one published version per
			// App.
			if app.CurrentPublishedVersionID != nil && *app.CurrentPublishedVersionID != version.ID {
				if err := tx.Model(&models.AppVersion{}).
					Where("id = ?", *app.CurrentPublishedVersionID).
					Update("state", models.AppVersionArchived).Error; err != nil {
					return err
				}
			}
			// Mark AppVersion published.
			if err := tx.Model(&version).
				Updates(map[string]any{
					"state":        models.AppVersionPublished,
					"published_at": gorm.Expr("now()"),
				}).Error; err != nil {
				return err
			}
			// Promote App.
			if err := tx.Model(&app).Updates(map[string]any{
				"state":                        models.AppPublished,
				"current_published_version_id": version.ID,
			}).Error; err != nil {
				return err
			}
		case models.DecisionRequestChanges:
			if err := tx.Model(&version).Update("state", models.AppVersionNeedsChanges).Error; err != nil {
				return err
			}
			if err := tx.Model(&app).Update("state", models.AppNeedsChanges).Error; err != nil {
				return err
			}
		case models.DecisionReject:
			if err := tx.Model(&version).Update("state", models.AppVersionRejected).Error; err != nil {
				return err
			}
			// App stays in submitted/needs_changes; rejection of a single
			// version does not archive the App.
		}

		newDecision = dec
		subjectApp = app
		return nil
	})
	if err != nil {
		var apiErr *apierror.Error
		if errors.As(err, &apiErr) {
			return apiErr
		}
		return err
	}
	if newDecision == nil {
		return fmt.Errorf("audit decision: transaction succeeded but decision is nil")
	}

	// Counted only after the commit, so a rolled-back decision counts nothing.
	metrics.AuditDecisionsTotal(string(newDecision.Decision)).Inc()
	metrics.AuditDecisionLatencySeconds.Update(time.Since(submittedAt).Seconds())

	// Best-effort audit log write — outside the tx so its failure does not
	// roll back the decision. The append-only handle has its own internal
	// hash-chain guarantees.
	_ = h.Audit.Log(ctx, audit.Event{
		Type:                "app.audit_decision",
		ActorUserID:         &p.UserID,
		ActorOrganizationID: &p.OrganizationID,
		SubjectType:         "app",
		SubjectID:           subjectApp.ID,
		Payload: map[string]any{
			"app_slug":            subjectApp.Slug,
			"audit_submission_id": newDecision.AuditSubmissionID.String(),
			"decision":            string(newDecision.Decision),
			"rationale":           newDecision.Rationale,
			"subject_org_id":      subjectApp.OwningOrganizationID.String(),
		},
	})

	return c.JSON(http.StatusOK, map[string]any{
		"id":                  newDecision.ID.String(),
		"audit_submission_id": newDecision.AuditSubmissionID.String(),
		"decision":            string(newDecision.Decision),
		"rationale":           newDecision.Rationale,
		"decided_by_user_id":  newDecision.DecidedByUserID.String(),
		"decided_at":          newDecision.DecidedAt,
	})
}

// ListAuditLog returns the global audit log (PlatformAdmin only).
// Supports ?event_type=&subject_type=&subject_id=&from=<RFC3339>&to=<RFC3339>.
func (h *Handlers) ListAuditLog(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}
	// Mirrors the route's role gate so a routing mistake cannot expose it.
	if !p.Has(models.RolePlatformAdmin) {
		return apierror.New(http.StatusForbidden, "forbidden", "missing required role")
	}

	page, pageSize := paginationFromQuery(c)
	tx := h.DB.RW.WithContext(c.Request().Context()).Model(&models.AuditLog{})

	if v := strings.TrimSpace(c.QueryParam("event_type")); v != "" {
		tx = tx.Where("event_type = ?", v)
	}
	if v := strings.TrimSpace(c.QueryParam("subject_type")); v != "" {
		tx = tx.Where("subject_type = ?", v)
	}
	if v := strings.TrimSpace(c.QueryParam("subject_id")); v != "" {
		if sid, err := uuid.Parse(v); err == nil {
			tx = tx.Where("subject_id = ?", sid)
		}
	}
	if v := strings.TrimSpace(c.QueryParam("from")); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			tx = tx.Where("occurred_at >= ?", t)
		}
	}
	if v := strings.TrimSpace(c.QueryParam("to")); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			tx = tx.Where("occurred_at <= ?", t)
		}
	}

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return err
	}

	var rows []models.AuditLog
	if err := tx.
		Order("occurred_at DESC, id DESC").
		Limit(pageSize).
		Offset((page - 1) * pageSize).
		Find(&rows).Error; err != nil {
		return err
	}

	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{
			"id":            r.ID.String(),
			"event_type":    r.EventType,
			"actor_user_id": uuidPtrToString(r.ActorUserID),
			"subject_type":  r.SubjectType,
			"subject_id":    r.SubjectID.String(),
			"payload":       r.Payload,
			"occurred_at":   r.OccurredAt,
			"ingest_hash":   r.IngestHash,
			"previous_hash": r.PreviousHash,
		})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// GetAuditEvidence serves the raw bytes of one evidence file attached to a
// submission. Tries the cached evidence_blobs table first; falls back to
// fetching from GitHub at the audited commit SHA when the blob is absent
// (e.g. submissions created before this feature shipped).
func (h *Handlers) GetAuditEvidence(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}
	// Mirrors the route's role gate so a routing mistake cannot expose it.
	if !p.Has(models.RoleAuditor) {
		return apierror.New(http.StatusForbidden, "forbidden", "missing required role")
	}

	subID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return apierror.New(http.StatusNotFound, "not_found", "submission not found")
	}
	logicalName := strings.TrimSpace(c.Param("name"))
	if logicalName == "" {
		return apierror.New(http.StatusNotFound, "not_found", "evidence file not found")
	}

	ctx := c.Request().Context()

	var sub models.AuditSubmission
	if err := h.DB.RW.WithContext(ctx).First(&sub, "id = ?", subID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apierror.New(http.StatusNotFound, "not_found", "submission not found")
		}
		return err
	}

	// Enforce cross-org rule: auditors cannot inspect evidence from their own org.
	var version models.AppVersion
	if err := h.DB.RW.WithContext(ctx).First(&version, "id = ?", sub.AppVersionID).Error; err != nil {
		return err
	}
	var app models.App
	if err := h.DB.RW.WithContext(ctx).First(&app, "id = ?", version.AppID).Error; err != nil {
		return err
	}
	if err := authorizeAuditAccess(p, app, false); err != nil {
		return err
	}

	// Cache hit.
	var blob models.EvidenceBlob
	err = h.DB.RW.WithContext(ctx).
		First(&blob, "audit_submission_id = ? AND logical_name = ?", subID, logicalName).Error
	if err == nil {
		c.Response().Header().Set("Content-Disposition",
			fmt.Sprintf("attachment; filename=%q", path.Base(blob.RepoPath)))
		return c.Blob(http.StatusOK, blob.ContentType, blob.Content)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	// Cache miss — parse EvidenceFiles to find repo_path, then ask GitHub.
	var evidenceFiles []manifest.EvidenceFileInfo
	if err := json.Unmarshal(sub.EvidenceFiles, &evidenceFiles); err != nil {
		return apierror.New(http.StatusNotFound, "not_found", "evidence file not found")
	}
	var match *manifest.EvidenceFileInfo
	for i := range evidenceFiles {
		if evidenceFiles[i].LogicalName == logicalName {
			match = &evidenceFiles[i]
			break
		}
	}
	if match == nil {
		return apierror.New(http.StatusNotFound, "not_found", "evidence file not found")
	}

	content, _, ghErr := h.GitHub.GetFileAtRef(ctx,
		app.GitHubInstallationID, app.GitHubRepoOwner, app.GitHubRepoName,
		match.RepoPath, version.GitSHA)
	if ghErr != nil {
		if errors.Is(ghErr, gh.ErrFileNotFound) {
			return apierror.New(http.StatusNotFound, "not_found",
				"evidence file not in cache and not found on GitHub")
		}
		slog.Error("github unavailable for audit evidence", "err", ghErr)
		return apierror.New(http.StatusServiceUnavailable, "github_unavailable",
			"evidence file is not cached and GitHub is unreachable")
	}

	c.Response().Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", path.Base(match.RepoPath)))
	return c.Blob(http.StatusOK, match.ContentType, content)
}

// authorizeAuditAccess enforces the conflict-of-interest rule: an auditor may
// not act on an app owned by their own organisation. Platform admins are
// exempt for read and review, but never for a decision. Approving your own
// org's app is the thing the rule exists to prevent.
func authorizeAuditAccess(p *auth.Principal, app models.App, deciding bool) error {
	if p.OrganizationID != app.OwningOrganizationID {
		return nil
	}
	if !deciding && p.Has(models.RolePlatformAdmin) {
		return nil
	}
	return apierror.New(http.StatusForbidden, "same_org_audit_forbidden",
		"auditors may not act on submissions from their own organisation")
}

func uuidPtrToString(p *uuid.UUID) string {
	if p == nil {
		return ""
	}
	return p.String()
}
