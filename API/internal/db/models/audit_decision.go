package models

import (
	"time"

	"github.com/google/uuid"
)

// AuditDecision is one auditor's verdict on an AuditSubmission. There is at
// most one decision per submission (enforced by the unique index on
// AuditSubmissionID).
//
// Cross-org rule: the platform layer rejects a decision attempt when the
// Decider's User.OrganizationID equals the App's OwningOrganizationID.
// Enforced server-side in apps/api/internal/audit/decision.go (V1 ships
// with at least two orgs federated so the rule is always satisfiable).
type AuditDecision struct {
	IDModel

	AuditSubmissionID uuid.UUID       `gorm:"type:uuid;not null;uniqueIndex" json:"audit_submission_id"`
	AuditSubmission   AuditSubmission `json:"-"`

	Decision Decision `gorm:"type:varchar(16);not null;check:decision IN ('approve','request_changes','reject')" json:"decision"`

	// Rationale is mandatory and must be substantive; CHECK constraint
	// enforces the minimum length. Free-form text — auditors typically
	// reference findings IDs from the SARIF or page numbers from the DPIA.
	Rationale string `gorm:"type:text;not null;check:length(rationale) >= 20" json:"rationale"`

	DecidedByUserID uuid.UUID `gorm:"type:uuid;not null" json:"decided_by_user_id"`
	DecidedBy       User      `gorm:"foreignKey:DecidedByUserID" json:"-"`

	DecidedAt time.Time `gorm:"autoCreateTime" json:"decided_at"`
}
