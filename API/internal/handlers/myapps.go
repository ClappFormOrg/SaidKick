package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
	"github.com/ClappFormOrg/SaidKick/API/internal/appurl"
	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

// Availability is the consumer-facing projection of Deployment.State. The
// raw state names operational failures (failed_vault, failed_secret_sync)
// that a consumer cannot act on; four buckets tell them whether to click.
// Operators keep the full state through /deployments.
type Availability string

const (
	// AvailabilityAvailable means the deployment is ready.
	AvailabilityAvailable Availability = "available"
	// AvailabilityDegraded means it serves, but not fully healthy.
	AvailabilityDegraded Availability = "degraded"
	// AvailabilityPreparing means it is still on its way up.
	AvailabilityPreparing Availability = "preparing"
	// AvailabilityUnavailable means it failed, or is in a state this
	// projection does not recognise.
	AvailabilityUnavailable Availability = "unavailable"
)

// Maintainer names who to contact about a deployed app. Every field may be
// empty: the deployer may have left and the manifest may not parse.
type Maintainer struct {
	Name          string `json:"name,omitempty"`
	Email         string `json:"email,omitempty"`
	VendorName    string `json:"vendor_name,omitempty"`
	VendorContact string `json:"vendor_contact,omitempty"`
}

// MyAppCard is one entry in the launcher. It deliberately carries no
// values, secret refs, namespace, cluster ID or raw state; that is why the
// launcher has its own endpoint instead of reusing /deployments.
type MyAppCard struct {
	DeploymentID string       `json:"deployment_id"`
	AppSlug      string       `json:"app_slug"`
	DisplayName  string       `json:"display_name"`
	Categories   []string     `json:"categories"`
	ClusterName  string       `json:"cluster_name,omitempty"`
	Availability Availability `json:"availability"`
	Access       appurl.Kind  `json:"access"`
	URL          string       `json:"url,omitempty"`
	Maintainer   Maintainer   `json:"maintainer"`
}

// myAppRow is the flat projection the launcher query scans into.
type myAppRow struct {
	DeploymentID   string `gorm:"column:deployment_id"`
	State          string `gorm:"column:state"`
	Values         []byte `gorm:"column:deployment_values"`
	AppSlug        string `gorm:"column:app_slug"`
	ClusterName    string `gorm:"column:cluster_name"`
	DeployerName   string `gorm:"column:deployer_name"`
	DeployerEmail  string `gorm:"column:deployer_email"`
	DisplayName    string `gorm:"column:display_name"`
	VendorName     string `gorm:"column:vendor_name"`
	VendorContact  string `gorm:"column:vendor_contact"`
	CategoriesJSON string `gorm:"column:categories_json"`
}

// liveDeploymentsOfOrg is the WHERE shared by the launcher and the
// catalogue's deployed_in_my_org and my_deployments fields: deployments one
// organisation owns that are not being or already removed.
const liveDeploymentsOfOrg = `deployments.owning_organization_id = ?
	AND deployments.deleted_at IS NULL
	AND deployments.state NOT IN ('deleting','deleted')`

// ListMyApps serves GET /my-apps: the live deployments the caller's own
// organisation owns, with a URL to open each one or a maintainer to ask.
// Every signed-in principal may call it, and it is scoped to the caller's
// organisation for every role, platform_admin included.
func (h *Handlers) ListMyApps(c echo.Context) error {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}
	page, pageSize := paginationFromQuery(c)

	// Two chains rather than one shared *gorm.DB: Count mutates the
	// statement it runs on.
	scoped := func() *gorm.DB {
		return h.DB.RW.WithContext(c.Request().Context()).
			Model(&models.Deployment{}).
			Where(liveDeploymentsOfOrg, p.OrganizationID)
	}

	var total int64
	if err := scoped().Count(&total).Error; err != nil {
		return err
	}

	var rows []myAppRow
	if err := scoped().
		Joins("JOIN apps ON apps.id = deployments.app_id").
		Joins("LEFT JOIN app_versions av ON av.id = deployments.app_version_id").
		Joins("LEFT JOIN clusters ON clusters.id = deployments.cluster_id").
		Joins("LEFT JOIN users ON users.id = deployments.deployer_user_id").
		Select(`deployments.id AS deployment_id,
			deployments.state AS state,
			deployments."values" AS deployment_values,
			apps.slug AS app_slug,
			COALESCE(clusters.name, '') AS cluster_name,
			COALESCE(users.display_name, '') AS deployer_name,
			COALESCE(users.email, '') AS deployer_email,
			COALESCE(av.appstore_yaml->'metadata'->>'display_name', '') AS display_name,
			COALESCE(av.appstore_yaml->'metadata'->>'vendor', '') AS vendor_name,
			COALESCE(av.appstore_yaml->'metadata'->>'contact', '') AS vendor_contact,
			COALESCE((av.appstore_yaml->'spec'->'categories')::text, '[]') AS categories_json`).
		Order("deployments.created_at DESC, deployments.id").
		Limit(pageSize).
		Offset((page - 1) * pageSize).
		Scan(&rows).Error; err != nil {
		return err
	}

	items := make([]MyAppCard, 0, len(rows))
	for _, r := range rows {
		items = append(items, myAppCardFromRow(r))
	}
	return c.JSON(http.StatusOK, map[string]any{
		"items":     items,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// myAppCardFromRow projects one joined row into the launcher DTO.
func myAppCardFromRow(r myAppRow) MyAppCard {
	availability := availabilityFor(models.DeploymentState(r.State))
	access := appurl.Derive(r.Values)
	card := MyAppCard{
		DeploymentID: r.DeploymentID,
		AppSlug:      r.AppSlug,
		DisplayName:  r.DisplayName,
		Categories:   parseCategories(r.CategoriesJSON),
		ClusterName:  r.ClusterName,
		Availability: availability,
		Access:       access.Kind,
		URL:          openableURL(access, availability),
		Maintainer: Maintainer{
			Name:          r.DeployerName,
			Email:         r.DeployerEmail,
			VendorName:    r.VendorName,
			VendorContact: r.VendorContact,
		},
	}
	// The slug is immutable and readable, a better label than a blank one
	// when the manifest did not project.
	if card.DisplayName == "" {
		card.DisplayName = r.AppSlug
	}
	return card
}

// openableURL returns the derived URL only when the deployment is up.
// Linking to an app that is still applying or has failed invites a click
// that cannot succeed.
func openableURL(access appurl.Access, a Availability) string {
	if access.Kind != appurl.KindWeb {
		return ""
	}
	if a != AvailabilityAvailable && a != AvailabilityDegraded {
		return ""
	}
	return access.URL
}

// availabilityFor maps a deployment state onto the consumer projection. An
// unknown state, including one added later, reports unavailable, so no new
// state can claim an app is ready by accident.
func availabilityFor(s models.DeploymentState) Availability {
	switch s {
	case models.DeploymentReady:
		return AvailabilityAvailable
	case models.DeploymentDegraded:
		return AvailabilityDegraded
	case models.DeploymentPending,
		models.DeploymentWaitingForCluster,
		models.DeploymentApplying,
		models.DeploymentSecretSync,
		models.DeploymentReconciling,
		models.DeploymentPendingOrchestrator:
		return AvailabilityPreparing
	default:
		return AvailabilityUnavailable
	}
}

// parseCategories decodes the manifest's categories array. It returns a
// non-nil slice so the JSON field is [] rather than null.
func parseCategories(raw string) []string {
	cats := []string{}
	if raw == "" || raw == "null" {
		return cats
	}
	if err := json.Unmarshal([]byte(raw), &cats); err != nil || cats == nil {
		return []string{}
	}
	return cats
}
