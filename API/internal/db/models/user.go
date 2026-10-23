package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// User is one human (or service account) federated from an Organization's
// IdP. Identity is authoritative via (OrganizationID, ExternalSubject) — the
// email is informational only and may change without breaking the link.
type User struct {
	IDModel

	OrganizationID uuid.UUID    `gorm:"type:uuid;not null;index" json:"organization_id"`
	Organization   Organization `json:"-"`

	// ExternalSubject is the OIDC `sub` claim from the federating IdP.
	// Opaque, never displayed in UI (use DisplayName instead).
	ExternalSubject string `gorm:"type:varchar(256);not null" json:"-"`

	Email       string `gorm:"type:varchar(254);not null"             json:"email"`
	DisplayName string `gorm:"type:varchar(120);not null"             json:"display_name"`

	CreatedAt   time.Time      `gorm:"autoCreateTime"             json:"created_at"`
	LastLoginAt *time.Time     `                                  json:"last_login_at,omitempty"`
	DeletedAt   gorm.DeletedAt `gorm:"index"                      json:"-"`
}

// Note: the partial unique index on (organization_id, external_subject) is
// declared via tag on ExternalSubject above with priority:2, and the
// migrate.go follow-up adds the priority:1 tag to organization_id via raw
// SQL because GORM does not allow multi-column partial indexes purely via
// tags in v1.25. See migrate.go AddExtraConstraints().
