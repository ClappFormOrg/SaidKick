package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// AgentCommand is a queued instruction from the hub to a cluster agent.
// The agent claims pending commands on each heartbeat and processes them
// locally (creating or deleting Argo CD Application CRs). State changes
// flow back via POST /deployment-states.
//
// Commands expire after 24 hours; the reconciler marks timed-out deployments
// failed so stale commands are never replayed after an agent reconnects.
type AgentCommand struct {
	IDModel

	ClusterID   uuid.UUID      `gorm:"type:uuid;not null;index"           json:"cluster_id"`
	CommandType string         `gorm:"type:varchar(32);not null"          json:"command_type"`
	Payload     datatypes.JSON `gorm:"type:jsonb;not null"                json:"payload"`
	ClaimedAt   *time.Time     `gorm:"index"                              json:"claimed_at,omitempty"`
	AckedAt     *time.Time     `gorm:"index"                              json:"acked_at,omitempty"`
	ExpiresAt   time.Time      `gorm:"not null;index"                     json:"expires_at"`
}
