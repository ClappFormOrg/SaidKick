package models

import (
	"time"

	"github.com/google/uuid"
)

// PlatformSettings is the hub's instance-wide branding: six colours and an
// optional PNG logo. The table holds exactly one row, enforced by the
// singleton column (UNIQUE and CHECK (singleton)); the schema is created by
// raw DDL in internal/db (platformSettingsDDL), not AutoMigrate.
//
// The five logo columns are set or cleared together (a CHECK constraint
// enforces it). LogoContent is never serialised.
type PlatformSettings struct {
	IDModel

	Singleton bool `gorm:"not null;default:true" json:"-"`

	// Lowercase #rrggbb.
	CTA          string `gorm:"column:cta;type:varchar(7);not null"  json:"cta"`
	PrimaryColor string `gorm:"type:varchar(7);not null"             json:"primary"`
	Secondary    string `gorm:"type:varchar(7);not null"             json:"secondary"`
	Surface      string `gorm:"type:varchar(7);not null"             json:"surface"`
	BodyText     string `gorm:"type:varchar(7);not null"             json:"body_text"`
	Border       string `gorm:"type:varchar(7);not null"             json:"border"`

	LogoContent     []byte  `gorm:"type:bytea"        json:"-"`
	LogoContentType *string `gorm:"type:varchar(128)" json:"-"`
	LogoWidth       *int    `json:"-"`
	LogoHeight      *int    `json:"-"`
	LogoAlt         *string `gorm:"type:varchar(255)" json:"-"`

	// UpdatedByUserID is NULL on the seeded row.
	UpdatedByUserID *uuid.UUID `gorm:"type:uuid" json:"-"`

	CreatedAt time.Time `gorm:"type:timestamptz;not null;default:now()" json:"created_at"`
	UpdatedAt time.Time `gorm:"type:timestamptz;not null;default:now()" json:"updated_at"`
}

// TableName pins the singular-looking name GORM would otherwise pluralise.
func (PlatformSettings) TableName() string { return "platform_settings" }

// HasLogo reports whether a logo is stored.
func (s PlatformSettings) HasLogo() bool { return s.LogoContentType != nil }
