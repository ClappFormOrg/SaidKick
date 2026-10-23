package models

import (
	"time"

	"github.com/google/uuid"
)

// APIKey is a long-lived machine token that authenticates CI/CD pipelines
// and other non-interactive callers. The full token is shown once at creation
// and never stored — only a SHA-256 hex digest (KeyHash) is persisted.
//
// Format: "sak_<base64url-random>" (~47 chars). The Prefix column stores the
// first 12 characters ("sak_" + 8 random chars) for human identification.
type APIKey struct {
	IDModel

	Name    string `gorm:"type:varchar(120);not null" json:"name"`
	Prefix  string `gorm:"type:varchar(20);not null"  json:"prefix"`
	KeyHash string `gorm:"type:varchar(64);not null;uniqueIndex" json:"-"`

	OwnerUserID uuid.UUID `gorm:"type:uuid;not null;index" json:"owner_user_id"`

	// Roles caps what the key may do. The effective role set on each request
	// is the intersection of Roles with the owner's live role assignments, so
	// revoking a role from the owner also revokes it from every key.
	Roles StringArray `gorm:"type:text[];not null;default:'{}'" json:"roles"`

	// ExpiresAt stays nullable so the scoping migration can roll back, but
	// every key created since then has a deadline of at most 90 days.
	CreatedAt  time.Time  `gorm:"autoCreateTime"  json:"created_at"`
	LastUsedAt *time.Time `                        json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `                        json:"expires_at,omitempty"`
	RevokedAt  *time.Time `gorm:"index"            json:"revoked_at,omitempty"`
}
