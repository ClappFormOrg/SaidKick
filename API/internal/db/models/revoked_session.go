package models

import (
	"time"

	"github.com/google/uuid"
)

// RevokedSession records the jti of a session JWT that was ended before its
// expiry (for example by POST /auth/logout). The session middleware rejects
// any token whose jti appears here. Rows are only needed until the token they
// name would have expired anyway, so a periodic sweep deletes rows once
// ExpiresAt has passed.
type RevokedSession struct {
	JTI       uuid.UUID `gorm:"column:jti;type:uuid;primaryKey"                       json:"jti"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;index"                              json:"user_id"`
	ExpiresAt time.Time `gorm:"type:timestamptz;not null;index"                       json:"expires_at"`
	RevokedAt time.Time `gorm:"type:timestamptz;not null;default:now()"               json:"revoked_at"`
}
