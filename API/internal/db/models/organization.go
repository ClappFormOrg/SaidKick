package models

import (
	"time"

	"gorm.io/gorm"
)

// Organization is one participating government body (a ministry, province or municipality).
// One row per federated tenant; soft-deleted via DeletedAt so historical
// audit-log entries remain joinable.
type Organization struct {
	IDModel

	// Slug is the immutable, DNS-label-safe identifier used in URLs and
	// audit log subject_ids ("provincie-voorbeeld", "gemeente-voorbeeld", ...). Enforced regex applied at
	// application layer; uniqueness enforced via partial unique index that
	// excludes soft-deleted rows.
	Slug string `gorm:"type:varchar(40);not null;uniqueIndex:idx_org_slug_active,where:deleted_at IS NULL" json:"slug"`

	DisplayName string `gorm:"type:varchar(120);not null" json:"display_name"`

	// OIDC federation. Nullable until PlatformAdmin configures the IdP for
	// this org. OIDCClientSecretRef stores the Vault path, never the secret.
	// Explicit column: tags required — GORM splits OIDCIssuer as o_id_c_issuer
	// without them (same issue as GitHubRepoOwner → git_hub_repo_owner).
	OIDCIssuer          *string `gorm:"column:oidc_issuer;type:text"           json:"oidc_issuer,omitempty"`
	OIDCClientID        *string `gorm:"column:oidc_client_id;type:text"        json:"oidc_client_id,omitempty"`
	OIDCClientSecretRef *string `gorm:"column:oidc_client_secret_ref;type:text" json:"oidc_client_secret_ref,omitempty"`

	ContactEmail string `gorm:"type:varchar(254);not null" json:"contact_email"`

	CreatedAt time.Time      `gorm:"autoCreateTime"            json:"created_at"`
	UpdatedAt time.Time      `gorm:"autoUpdateTime"            json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index"                     json:"-"`
}
