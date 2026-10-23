package models

import (
	"time"

	"github.com/google/uuid"
)

// EvidenceBlob persists the raw bytes of one evidence file fetched during
// Stage-1 validation. Keyed by (audit_submission_id, logical_name) so
// auditors can re-download the exact artefact even if the source GitHub
// repo is later modified or the GitHub App token expires.
type EvidenceBlob struct {
	IDModel

	AuditSubmissionID uuid.UUID `gorm:"type:uuid;not null;index:idx_evblob_sub_name,priority:1" json:"audit_submission_id"`
	LogicalName       string    `gorm:"type:varchar(128);not null;index:idx_evblob_sub_name,priority:2" json:"logical_name"`
	RepoPath          string    `gorm:"type:varchar(1024);not null" json:"repo_path"`
	ContentType       string    `gorm:"type:varchar(128);not null" json:"content_type"`
	Content           []byte    `gorm:"type:bytea;not null" json:"-"`

	CreatedAt time.Time `gorm:"autoCreateTime" json:"created_at"`
}
