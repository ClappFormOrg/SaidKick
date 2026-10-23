package models

import (
	"time"

	"github.com/google/uuid"
)

// AuditComment is a note left by an auditor on an AuditSubmission during
// the Stage-2 human review. Comments are append-only; deletion is not
// supported (immutable audit trail).
type AuditComment struct {
	IDModel

	AuditSubmissionID uuid.UUID `gorm:"type:uuid;not null;index" json:"audit_submission_id"`
	AuthorUserID      uuid.UUID `gorm:"type:uuid;not null"       json:"author_user_id"`
	Body              string    `gorm:"type:text;not null"       json:"body"`

	CreatedAt time.Time `gorm:"autoCreateTime" json:"created_at"`
}
