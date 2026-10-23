package models

import (
	"time"

	"github.com/google/uuid"
)

// RevalidationStatus tracks the lifecycle of one re-validation run.
type RevalidationStatus string

const (
	RevalidationUnknown    RevalidationStatus = "" // zero value — uninitialized
	RevalidationPending    RevalidationStatus = "pending"
	RevalidationProcessing RevalidationStatus = "processing"
	RevalidationDone       RevalidationStatus = "done"
	RevalidationFailed     RevalidationStatus = "failed"
)

// RevalidationJob is created by the GitHub webhook push handler whenever a
// push to an app's audited branch is received. The revalidation worker
// polls this table, re-runs Stage-1 validation at the new HEAD commit, and
// creates a new AppVersion + AuditSubmission if validation passes.
type RevalidationJob struct {
	IDModel

	AppID   uuid.UUID          `gorm:"type:uuid;not null;index"                                                json:"app_id"`
	NewHead string             `gorm:"type:varchar(40);not null"                                               json:"new_head"`
	OldHead string             `gorm:"type:varchar(40);not null"                                               json:"old_head"`
	Status  RevalidationStatus `gorm:"type:varchar(20);not null;default:'pending';index;check:status IN ('pending','processing','done','failed')" json:"status"`
	Error   *string            `gorm:"type:text"                                                               json:"error,omitempty"`

	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}
