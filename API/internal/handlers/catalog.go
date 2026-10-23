package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
	"github.com/ClappFormOrg/SaidKick/API/internal/appurl"
	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/manifest"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
	maxPage         = 1000
)

// AppCard is the lightweight catalogue entry returned by ListApps.
// Manifest fields and ChartVersion (the Helm chart version) are populated
// when the app has a published AppVersion.
type AppCard struct {
	ID                  string   `json:"id"`
	Slug                string   `json:"slug"`
	Vendor              string   `json:"vendor"`
	State               string   `json:"state"`
	GitHubRepo          string   `json:"github_repo"`
	CurrentSHA          string   `json:"current_sha,omitempty"`
	LastPublished       string   `json:"last_published_at,omitempty"`
	DisplayName         string   `json:"display_name,omitempty"`
	Categories          []string `json:"categories,omitempty"`
	RiskClass           string   `json:"risk_class,omitempty"`
	License             string   `json:"license,omitempty"`
	ChartVersion        string   `json:"chart_version,omitempty"`
	AutoValidationState string   `json:"auto_validation_state,omitempty"`
	AuditApproved       bool     `json:"audit_approved"`
	// DeployedInMyOrg reports whether the caller's own organisation runs a
	// live deployment of this app. Always false for anonymous callers:
	// which organisation runs which app is not public.
	DeployedInMyOrg bool `json:"deployed_in_my_org"`
}

// appScanRow is used to scan the custom SELECT projection with manifest fields.
type appScanRow struct {
	ID                  string `gorm:"column:id"`
	Slug                string `gorm:"column:slug"`
	GitHubRepoOwner     string `gorm:"column:github_repo_owner"`
	GitHubRepoName      string `gorm:"column:github_repo_name"`
	State               string `gorm:"column:state"`
	DisplayName         string `gorm:"column:display_name"`
	CategoriesJSON      string `gorm:"column:categories_json"`
	RiskClass           string `gorm:"column:risk_class"`
	License             string `gorm:"column:license"`
	ChartVersion        string `gorm:"column:chart_version"`
	AutoValidationState string `gorm:"column:auto_validation_state"`
	AuditApproved       bool   `gorm:"column:audit_approved"`
}

// ListApps returns the paginated catalogue. Anonymous-friendly: when no
// principal is attached, only state=published apps are returned regardless
// of the `status` query parameter. Non-published states are scoped to the
// caller's organisation unless the caller is an auditor or platform admin.
//
// Query params:
//   - q          : free-text search across slug, vendor, repo (LIKE)
//   - status     : filter by App.state (default: "published")
//   - risk_class : filter by AI risk class — minimal, limited, or high
//   - category   : filter by one category tag (array containment)
//   - page       : 1-based page number (default: 1, max: 1000)
//   - page_size  : page size (default: 20, max: 100)
func (h *Handlers) ListApps(c echo.Context) error {
	q := strings.TrimSpace(c.QueryParam("q"))
	status := strings.TrimSpace(c.QueryParam("status"))
	if status == "" {
		status = string(models.AppPublished)
	}
	// Anonymous callers may not enumerate non-published apps regardless of the
	// status query param — force published for any unauthenticated request.
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		status = string(models.AppPublished)
	} else if !slices.Contains(models.AllAppStates, models.AppState(status)) {
		return apierror.New(http.StatusBadRequest, "bad_request", "invalid status value")
	}
	riskClass := strings.TrimSpace(c.QueryParam("risk_class"))
	category := strings.TrimSpace(c.QueryParam("category"))

	page, pageSize := paginationFromQuery(c)

	// Always join the published AppVersion for manifest projection + filters.
	// Also left-join the latest audit submission + approval decision for score display.
	baseTx := h.DB.RW.WithContext(c.Request().Context()).
		Model(&models.App{}).
		Joins("LEFT JOIN app_versions av ON av.id = apps.current_published_version_id").
		Joins("LEFT JOIN audit_submissions asu ON asu.app_version_id = av.id").
		Joins("LEFT JOIN audit_decisions ad ON ad.audit_submission_id = asu.id AND ad.decision = 'approve'").
		Where("apps.state = ?", status)

	if status != string(models.AppPublished) && !p.Has(models.RoleAuditor, models.RolePlatformAdmin) {
		baseTx = baseTx.Where("apps.owning_organization_id = ?", p.OrganizationID)
	}

	if q != "" {
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(q))
		pattern := "%" + escaped + "%"
		baseTx = baseTx.Where(
			`LOWER(apps.slug) LIKE ? ESCAPE '\' OR LOWER(apps.github_repo_owner) LIKE ? ESCAPE '\' OR LOWER(apps.github_repo_name) LIKE ? ESCAPE '\'`,
			pattern, pattern, pattern,
		)
	}
	if riskClass != "" {
		baseTx = baseTx.Where("av.appstore_yaml->'spec'->'ai'->>'risk_class' = ?", riskClass)
	}
	if category != "" {
		// ?? is the pq-escaped form of the PostgreSQL ? array-element operator.
		baseTx = baseTx.Where("av.appstore_yaml->'spec'->'categories' ?? ?", category)
	}

	var total int64
	if err := baseTx.Count(&total).Error; err != nil {
		return err
	}

	var rows []appScanRow
	if err := baseTx.
		Select(`apps.id, apps.slug, apps.github_repo_owner, apps.github_repo_name, apps.state,
			COALESCE(av.appstore_yaml->'metadata'->>'display_name', '') AS display_name,
			COALESCE((av.appstore_yaml->'spec'->'categories')::text, '[]') AS categories_json,
			COALESCE(av.appstore_yaml->'spec'->'ai'->>'risk_class', '') AS risk_class,
			COALESCE(av.appstore_yaml->'metadata'->>'license', '') AS license,
			COALESCE(av.chart_metadata->>'version', '') AS chart_version,
			COALESCE(asu.auto_validation_state, '') AS auto_validation_state,
			(ad.id IS NOT NULL) AS audit_approved`).
		Order("apps.updated_at DESC").
		Limit(pageSize).
		Offset((page - 1) * pageSize).
		Scan(&rows).Error; err != nil {
		return err
	}

	cards := make([]AppCard, 0, len(rows))
	for _, r := range rows {
		cats := []string{}
		if r.CategoriesJSON != "" && r.CategoriesJSON != "null" && r.CategoriesJSON != "[]" {
			_ = json.Unmarshal([]byte(r.CategoriesJSON), &cats)
		}
		cards = append(cards, AppCard{
			ID:                  r.ID,
			Slug:                r.Slug,
			Vendor:              r.GitHubRepoOwner,
			State:               r.State,
			GitHubRepo:          r.GitHubRepoOwner + "/" + r.GitHubRepoName,
			DisplayName:         r.DisplayName,
			Categories:          cats,
			RiskClass:           r.RiskClass,
			License:             r.License,
			ChartVersion:        r.ChartVersion,
			AutoValidationState: r.AutoValidationState,
			AuditApproved:       r.AuditApproved,
		})
	}

	if err := h.markDeployedInMyOrg(c, cards); err != nil {
		return err
	}

	return c.JSON(http.StatusOK, map[string]any{
		"items":     cards,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// AppDetail is the full detail response for GET /apps/:slug. ChartVersion
// comes from the embedded AppCard; redeclaring it here would shadow that
// field and let the two drift apart.
type AppDetail struct {
	AppCard
	OwningOrganizationID      string                `json:"owning_organization_id"`
	OwningUserID              string                `json:"owning_user_id"`
	GitHubInstallationID      int64                 `json:"github_installation_id"`
	GitHubDefaultBranch       string                `json:"github_default_branch"`
	RepoLinkState             string                `json:"repo_link_state"`
	CreatedAt                 string                `json:"created_at"`
	UpdatedAt                 string                `json:"updated_at"`
	Manifest                  *manifest.AppManifest `json:"manifest,omitempty"`
	Installs                  int64                 `json:"installs"`
	AutoValidationState       string                `json:"auto_validation_state,omitempty"`
	AuditApproved             bool                  `json:"audit_approved"`
	CurrentPublishedVersionID string                `json:"current_published_version_id,omitempty"`
	ValuesSchema              json.RawMessage       `json:"values_schema,omitempty"`
	// MyDeployments lists this app's live deployments in the caller's own
	// organisation, so the detail page can offer to open one. Always [] for
	// anonymous callers.
	MyDeployments []MyDeploymentRef `json:"my_deployments"`
}

// MyDeploymentRef points at one live deployment: enough to open it or to
// say why it cannot be opened, and nothing operational.
type MyDeploymentRef struct {
	DeploymentID string       `json:"deployment_id"`
	Availability Availability `json:"availability"`
	URL          string       `json:"url,omitempty"`
}

// markDeployedInMyOrg sets DeployedInMyOrg on cards with one query for the
// whole page. Anonymous callers keep the zero value.
func (h *Handlers) markDeployedInMyOrg(c echo.Context, cards []AppCard) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil || len(cards) == 0 {
		return nil
	}
	ids := make([]string, 0, len(cards))
	for _, card := range cards {
		ids = append(ids, card.ID)
	}

	var deployed []string
	if err := h.DB.RW.WithContext(c.Request().Context()).
		Model(&models.Deployment{}).
		Distinct("app_id").
		Where(liveDeploymentsOfOrg, p.OrganizationID).
		Where("deployments.app_id IN ?", ids).
		Pluck("app_id", &deployed).Error; err != nil {
		return err
	}
	for i := range cards {
		cards[i].DeployedInMyOrg = slices.Contains(deployed, cards[i].ID)
	}
	return nil
}

// myDeploymentsOfApp returns the caller's own organisation's live
// deployments of one app, newest first. It returns a non-nil slice so the
// JSON field is [] rather than null.
func (h *Handlers) myDeploymentsOfApp(c echo.Context, appID uuid.UUID) ([]MyDeploymentRef, error) {
	refs := []MyDeploymentRef{}
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return refs, nil
	}

	var rows []struct {
		ID     string `gorm:"column:id"`
		State  string `gorm:"column:state"`
		Values []byte `gorm:"column:deployment_values"`
	}
	if err := h.DB.RW.WithContext(c.Request().Context()).
		Model(&models.Deployment{}).
		Select(`deployments.id AS id, deployments.state AS state,
			deployments."values" AS deployment_values`).
		Where(liveDeploymentsOfOrg, p.OrganizationID).
		Where("deployments.app_id = ?", appID).
		Order("deployments.created_at DESC, deployments.id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		availability := availabilityFor(models.DeploymentState(r.State))
		refs = append(refs, MyDeploymentRef{
			DeploymentID: r.ID,
			Availability: availability,
			URL:          openableURL(appurl.Derive(r.Values), availability),
		})
	}
	return refs, nil
}

// appVisibleTo reports whether p may learn that app exists. Published apps
// are public; any other state is visible only to the owning organisation,
// auditors and platform admins. p may be nil for an anonymous caller.
func appVisibleTo(p *auth.Principal, app models.App) bool {
	if app.State == models.AppPublished {
		return true
	}
	if p == nil {
		return false
	}
	return p.Has(models.RoleAuditor, models.RolePlatformAdmin) || p.OrganizationID == app.OwningOrganizationID
}

// GetApp serves the app detail page. Anonymous-friendly for published
// apps; non-published apps require an authenticated session.
func (h *Handlers) GetApp(c echo.Context) error {
	slug := strings.TrimSpace(c.Param("slug"))
	if slug == "" {
		return apierror.New(http.StatusNotFound, "not_found", "app not found")
	}

	var app models.App
	err := h.DB.RW.WithContext(c.Request().Context()).
		Where("slug = ?", slug).
		First(&app).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apierror.New(http.StatusNotFound, "not_found", "app not found")
	}
	if err != nil {
		return err
	}

	// Non-published apps are visible only to the owning organisation, auditors
	// and platform admins. Anyone else gets a 404 so the slug is not confirmed.
	p := auth.PrincipalFromContext(c.Request().Context())
	if !appVisibleTo(p, app) {
		return apierror.New(http.StatusNotFound, "not_found", "app not found")
	}

	detail := AppDetail{
		AppCard: AppCard{
			ID:         app.ID.String(),
			Slug:       app.Slug,
			Vendor:     app.GitHubRepoOwner,
			State:      string(app.State),
			GitHubRepo: app.GitHubRepoOwner + "/" + app.GitHubRepoName,
		},
		OwningOrganizationID: app.OwningOrganizationID.String(),
		GitHubDefaultBranch:  app.GitHubDefaultBranch,
		RepoLinkState:        string(app.RepoLinkState),
		CreatedAt:            app.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:            app.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	if p != nil {
		detail.OwningUserID = app.OwningUserID.String()
		detail.GitHubInstallationID = app.GitHubInstallationID
	}

	// Enrich with manifest + chart version when a published AppVersion exists.
	if app.CurrentPublishedVersionID != nil {
		var ver models.AppVersion
		if err := h.DB.RW.WithContext(c.Request().Context()).
			Where("id = ?", *app.CurrentPublishedVersionID).
			First(&ver).Error; err == nil {
			var m manifest.AppManifest
			if jsonErr := json.Unmarshal([]byte(ver.AppstoreYAML), &m); jsonErr == nil {
				detail.DisplayName = m.Metadata.DisplayName
				detail.Categories = m.Spec.Categories
				detail.RiskClass = m.Spec.AI.RiskClass
				detail.License = m.Metadata.License
				detail.Manifest = &m
			}
			// Extract Helm chart version from opaque ChartMetadata JSON.
			var chartMeta map[string]any
			if jsonErr := json.Unmarshal([]byte(ver.ChartMetadata), &chartMeta); jsonErr == nil {
				if v, ok := chartMeta["version"].(string); ok {
					detail.ChartVersion = v
				}
			}
			// Expose the published version UUID so clients can initiate deployments.
			detail.CurrentPublishedVersionID = ver.ID.String()
			// Expose the Helm values schema so the deploy form can render dynamic fields.
			if len(ver.ValuesSchema) > 0 && string(ver.ValuesSchema) != "null" {
				detail.ValuesSchema = json.RawMessage(ver.ValuesSchema)
			}
			// Populate audit validation state and approval flag.
			var auditSub models.AuditSubmission
			if auditErr := h.DB.RW.WithContext(c.Request().Context()).
				Where("app_version_id = ?", ver.ID).
				First(&auditSub).Error; auditErr == nil {
				detail.AutoValidationState = string(auditSub.AutoValidationState)
				var auditDec models.AuditDecision
				if h.DB.RW.WithContext(c.Request().Context()).
					Where("audit_submission_id = ? AND decision = ?", auditSub.ID, models.DecisionApprove).
					First(&auditDec).Error == nil {
					detail.AuditApproved = true
				}
			}
		}
	}

	// Count non-deleted deployments for this app.
	var installCount int64
	if err := h.DB.RW.WithContext(c.Request().Context()).
		Model(&models.Deployment{}).
		Where("app_id = ?", app.ID).
		Count(&installCount).Error; err != nil {
		slog.Warn("catalog: count installs", "error", err, "app_id", app.ID)
	}
	detail.Installs = installCount

	myDeployments, err := h.myDeploymentsOfApp(c, app.ID)
	if err != nil {
		return err
	}
	detail.MyDeployments = myDeployments
	detail.DeployedInMyOrg = len(myDeployments) > 0

	return c.JSON(http.StatusOK, detail)
}

// paginationFromQuery parses page + page_size with defaults and bounds.
func paginationFromQuery(c echo.Context) (page, pageSize int) {
	page = 1
	pageSize = defaultPageSize

	if v := c.QueryParam("page"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p >= 1 && p <= maxPage {
			page = p
		}
	}
	if v := c.QueryParam("page_size"); v != "" {
		if ps, err := strconv.Atoi(v); err == nil && ps >= 1 && ps <= maxPageSize {
			pageSize = ps
		}
	}
	return page, pageSize
}
