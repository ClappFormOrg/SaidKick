package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// App is one community-submitted application registered to the platform via
// a linked GitHub repository. The Helm chart, audit evidence and
// appstore.yaml manifest all live in that repo — the platform stores only
// indexed metadata + audit decisions.
type App struct {
	IDModel

	// Slug is derived from appstore.yaml metadata.name at first submission
	// and is immutable thereafter. Used as the path segment in URLs.
	Slug string `gorm:"type:varchar(60);not null;uniqueIndex:idx_app_slug_active,where:deleted_at IS NULL" json:"slug"`

	OwningOrganizationID uuid.UUID    `gorm:"type:uuid;not null;index" json:"owning_organization_id"`
	OwningOrganization   Organization `json:"-"`

	OwningUserID uuid.UUID `gorm:"type:uuid;not null" json:"owning_user_id"`
	OwningUser   User      `gorm:"foreignKey:OwningUserID" json:"-"`

	// GitHub linkage. InstallationID identifies the GitHub App installation
	// granting us read access to this repo.
	//
	// NB: explicit `column:` tags override GORM's default naming strategy,
	// which would otherwise snake-case "GitHubRepoOwner" as
	// `git_hub_repo_owner` (it treats "Hub" as a separate word because
	// "GitHub" is not in GORM's commonInitialisms list). The design doc
	// uses `github_*` so we pin the names explicitly here.
	GitHubInstallationID int64  `gorm:"column:github_installation_id;not null"               json:"github_installation_id"`
	GitHubRepoOwner      string `gorm:"column:github_repo_owner;type:varchar(120);not null"  json:"github_repo_owner"`
	GitHubRepoName       string `gorm:"column:github_repo_name;type:varchar(120);not null"   json:"github_repo_name"`
	GitHubDefaultBranch  string `gorm:"column:github_default_branch;type:varchar(255);not null" json:"github_default_branch"`

	State AppState `gorm:"type:varchar(32);not null;default:'draft';index;check:state IN ('draft','submitted','needs_changes','published','rejected','archived')" json:"state"`

	// CurrentPublishedVersionID points at the AppVersion that is currently
	// visible in the catalogue. Nullable until first publish; updated on
	// every AuditDecision = approve transition.
	//
	// We deliberately do NOT declare a Go-level *AppVersion field here
	// because AppVersion already has an FK to App via AppVersion.AppID.
	// Together those would form a cyclic AutoMigrate dependency: GORM
	// processes App's relationships first, dives into AppVersion, tries
	// to create app_versions before apps exists, and crashes. The
	// application maintains the invariant that
	// current_published_version_id always points at a row in
	// app_versions whose app_id equals this app's id.
	CurrentPublishedVersionID *uuid.UUID `gorm:"type:uuid" json:"current_published_version_id,omitempty"`

	RepoLinkState RepoLinkState `gorm:"type:varchar(16);not null;default:'connected';check:repo_link_state IN ('connected','disconnected')" json:"repo_link_state"`

	CreatedAt time.Time      `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt time.Time      `gorm:"autoUpdateTime" json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index"          json:"-"`
}

// The partial unique index on (github_repo_owner, github_repo_name) WHERE
// deleted_at IS NULL is added via raw SQL in migrate.go — one org may not
// register the same repo twice.
