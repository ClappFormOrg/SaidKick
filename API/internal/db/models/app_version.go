package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// AppVersion is a specific immutable commit SHA of an App's linked repo.
// Each version has its own audit submission + decision. Existing
// Deployments are pinned to a specific AppVersion so a chart update only
// affects new or upgraded deployments.
type AppVersion struct {
	IDModel

	AppID uuid.UUID `gorm:"type:uuid;not null;index;uniqueIndex:idx_appver_app_sha,priority:1" json:"app_id"`
	App   App       `json:"-"`

	// GitRef is the human-friendly target ref (a tag or branch); GitSHA is
	// the resolved 40-char commit hash. Audits and deployments pin GitSHA.
	GitRef string `gorm:"type:varchar(255);not null" json:"git_ref"`
	GitSHA string `gorm:"type:char(40);not null;uniqueIndex:idx_appver_app_sha,priority:2" json:"git_sha"`

	// AppstoreYAML is the validated, parsed manifest at this SHA.
	AppstoreYAML datatypes.JSON `gorm:"type:jsonb;not null"          json:"appstore_yaml"`

	// ValuesSchema is the chart's values.schema.json — used to generate the
	// deploy-form on the webapp.
	ValuesSchema datatypes.JSON `gorm:"type:jsonb;not null"          json:"values_schema"`

	// ChartMetadata is parsed Chart.yaml (name, version, appVersion, ...).
	ChartMetadata datatypes.JSON `gorm:"type:jsonb;not null"          json:"chart_metadata"`

	// AuditSubmissionID is the back-pointer to the AuditSubmission for this
	// version. We deliberately do NOT declare a Go-level relationship struct
	// here so GORM does not create a DB-level FK constraint — the link
	// between AppVersion and AuditSubmission is bidirectional NOT NULL, which
	// would otherwise create an undeferrable insert cycle. The application
	// is solely responsible for keeping the two columns in sync (within the
	// same transaction).
	AuditSubmissionID uuid.UUID `gorm:"type:uuid;not null" json:"audit_submission_id"`

	State AppVersionState `gorm:"type:varchar(32);not null;default:'draft';index;check:state IN ('draft','submitted','needs_changes','published','rejected','archived')" json:"state"`

	PublishedAt *time.Time `                          json:"published_at,omitempty"`
	CreatedAt   time.Time  `gorm:"autoCreateTime"     json:"created_at"`
}
