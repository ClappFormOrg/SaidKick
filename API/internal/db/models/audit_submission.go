package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// AuditSubmission holds the developer-supplied audit artefacts for one
// AppVersion. Stage 1 (auto) validates structural correctness; Stage 2 is
// the auditor's decision captured separately in AuditDecision.
type AuditSubmission struct {
	IDModel

	AppVersionID uuid.UUID `gorm:"type:uuid;not null;index" json:"app_version_id"`

	// FormResponses holds the structured Q&A from the audit form
	// (DPIA, AI-risk, accessibility, model card, ...). Schema versioned via
	// FormVersion so older submissions remain interpretable.
	FormResponses datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'" json:"form_responses"`
	FormVersion   string         `gorm:"type:varchar(32);not null"        json:"form_version"`

	// EvidenceFiles is an array of {logical_name, repo_path, content_sha256,
	// size_bytes, content_type} records — one per file referenced from
	// appstore.yaml.spec.audit.* and ...demo.* paths.
	EvidenceFiles datatypes.JSON `gorm:"type:jsonb;not null;default:'[]'" json:"evidence_files"`

	AutoValidationState  AutoValidationState `gorm:"type:varchar(16);not null;default:'pending';check:auto_validation_state IN ('pending','passed','failed')" json:"auto_validation_state"`
	AutoValidationErrors datatypes.JSON      `gorm:"type:jsonb"                                                                                                json:"auto_validation_errors,omitempty"`

	CreatedAt time.Time `gorm:"autoCreateTime" json:"created_at"`
}
