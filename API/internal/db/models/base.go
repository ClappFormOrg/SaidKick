// Package models defines the GORM database entities for the sAId-Kick
// Appstore. Each struct maps to a PostgreSQL table; IDModel is embedded by
// every entity to mint time-ordered UUIDv7 primary keys server-side.
package models

import (
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// IDModel is the embeddable primary-key block. We mint UUIDv7 server-side so
// IDs are time-ordered for B-tree friendliness while still being globally
// unique.
type IDModel struct {
	ID uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
}

// BeforeCreate mints a UUIDv7 if the caller did not already supply one.
// GORM promotes this hook to any struct embedding IDModel.
func (m *IDModel) BeforeCreate(_ *gorm.DB) error {
	if m.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		m.ID = id
	}
	return nil
}
