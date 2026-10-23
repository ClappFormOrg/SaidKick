package models

import (
	"time"

	"github.com/google/uuid"
)

// UsedJoinToken records the JTI of each consumed cluster join-token,
// enforcing one-time use via a unique index on jti.
type UsedJoinToken struct {
	IDModel
	JTI       string    `gorm:"type:text;not null;uniqueIndex" json:"jti"`
	ClusterID uuid.UUID `gorm:"type:uuid;not null;index"       json:"cluster_id"`
	UsedAt    time.Time `gorm:"autoCreateTime"                 json:"used_at"`
}
