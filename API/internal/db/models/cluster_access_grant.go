package models

import (
	"time"

	"github.com/google/uuid"
)

// ClusterAccessGrant allows a specific organisation to deploy to a
// ClusterScopeRestricted cluster. The grant is created by a PlatformAdmin and
// deleted to revoke access. Both (cluster_id, organization_id) together form
// the primary key, making duplicate grants impossible.
type ClusterAccessGrant struct {
	ClusterID      uuid.UUID `gorm:"type:uuid;not null;primaryKey;index"      json:"cluster_id"`
	OrganizationID uuid.UUID `gorm:"type:uuid;not null;primaryKey;index"      json:"organization_id"`
	GrantedAt      time.Time `gorm:"autoCreateTime"                           json:"granted_at"`
	GrantedBy      uuid.UUID `gorm:"type:uuid;not null"                       json:"granted_by"`
}
