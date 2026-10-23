package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// AuditDLQ holds audit events that could not be written to audit_logs.
// Written via the RW pool as a last-resort fallback when the audit_writer
// connection fails. Ops should drain and re-ingest these rows once the
// underlying write path is restored.
type AuditDLQ struct {
	ID          uuid.UUID      `gorm:"type:uuid;primaryKey"    json:"id"`
	FailedAt    time.Time      `gorm:"autoCreateTime"          json:"failed_at"`
	EventType   string         `gorm:"type:varchar(128)"       json:"event_type"`
	SubjectType string         `gorm:"type:varchar(64)"        json:"subject_type"`
	SubjectID   uuid.UUID      `gorm:"type:uuid"               json:"subject_id"`
	Payload     datatypes.JSON `gorm:"type:jsonb;default:'{}'" json:"payload"`
	WriteError  string         `gorm:"type:text"               json:"write_error"`
}

// BeforeCreate mints a UUIDv7 primary key.
func (a *AuditDLQ) BeforeCreate(_ *gorm.DB) error {
	if a.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		a.ID = id
	}
	return nil
}
