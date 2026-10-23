package models

import (
	"time"

	"github.com/google/uuid"
)

// RoleAssignment associates a User with one Role granted by another User.
// A single user may hold multiple roles concurrently (e.g. Developer +
// Deployer). Revocation is soft: RevokedAt becomes non-null, the row stays
// for audit traceability.
type RoleAssignment struct {
	IDModel

	UserID uuid.UUID `gorm:"type:uuid;not null;index" json:"user_id"`
	User   User      `json:"-"`

	Role Role `gorm:"type:varchar(32);not null;index;check:role IN ('developer','auditor','deployer','cluster_admin','org_admin','platform_admin','app_user')" json:"role"`

	// GrantedByUserID is the user who granted the role. The app_user role
	// granted automatically at first OIDC sign-in records the user's own ID.
	GrantedByUserID uuid.UUID `gorm:"type:uuid;not null" json:"granted_by_user_id"`
	GrantedBy       User      `gorm:"foreignKey:GrantedByUserID" json:"-"`

	GrantedAt time.Time  `gorm:"autoCreateTime" json:"granted_at"`
	RevokedAt *time.Time `                      json:"revoked_at,omitempty"`
}

// The partial unique constraint on (user_id, role) WHERE revoked_at IS NULL
// is declared via raw SQL in migrate.go to ensure a user cannot hold two
// active assignments of the same role.
