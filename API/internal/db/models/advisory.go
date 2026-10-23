package models

import "time"

// Advisory is a platform-level security or operational notice surfaced on the
// dashboard. PlatformAdmin creates advisories; they are readable by anyone
// with a valid session. An advisory expires when ExpiresAt passes; nil means
// it stays active indefinitely.
type Advisory struct {
	IDModel

	// Title is the short one-line summary shown in the dashboard feed.
	Title string `gorm:"type:varchar(200);not null" json:"title"`

	// Body is optional Markdown detail text shown when the advisory is expanded.
	Body string `gorm:"type:text;not null;default:''" json:"body"`

	// Severity is one of "info", "warning", "critical".
	Severity string `gorm:"type:varchar(20);not null;default:'info'" json:"severity"`

	// AppSlug optionally scopes the advisory to a specific application.
	// Nil = platform-wide.
	AppSlug *string `gorm:"type:varchar(40);index" json:"app_slug,omitempty"`

	PublishedAt time.Time  `gorm:"not null"    json:"published_at"`
	ExpiresAt   *time.Time `                   json:"expires_at,omitempty"`
}
