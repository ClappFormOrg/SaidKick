package db

import (
	"fmt"

	"github.com/ClappFormOrg/SaidKick/API/internal/branding"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// platformSettingsDDL creates the single-row branding table (#158). The
// singleton column is always true and unique, so a second row cannot exist.
// Colours must be lowercase #rrggbb, and the five logo columns are all set
// or all NULL. Safe to re-run.
const platformSettingsDDL = `CREATE TABLE IF NOT EXISTS platform_settings (
  id uuid PRIMARY KEY,
  singleton boolean NOT NULL DEFAULT true,
  cta varchar(7) NOT NULL,
  primary_color varchar(7) NOT NULL,
  secondary varchar(7) NOT NULL,
  surface varchar(7) NOT NULL,
  body_text varchar(7) NOT NULL,
  border varchar(7) NOT NULL,
  logo_content bytea,
  logo_content_type varchar(128),
  logo_width integer,
  logo_height integer,
  logo_alt varchar(255),
  updated_by_user_id uuid REFERENCES users (id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT platform_settings_singleton CHECK (singleton),
  CONSTRAINT platform_settings_colours CHECK (
    cta ~ '^#[0-9a-f]{6}$' AND primary_color ~ '^#[0-9a-f]{6}$' AND
    secondary ~ '^#[0-9a-f]{6}$' AND surface ~ '^#[0-9a-f]{6}$' AND
    body_text ~ '^#[0-9a-f]{6}$' AND border ~ '^#[0-9a-f]{6}$'
  ),
  CONSTRAINT platform_settings_logo_together CHECK (
    (logo_content IS NULL AND logo_content_type IS NULL AND logo_width IS NULL
      AND logo_height IS NULL AND logo_alt IS NULL)
    OR
    (logo_content IS NOT NULL AND logo_content_type IS NOT NULL AND logo_width IS NOT NULL
      AND logo_height IS NOT NULL AND logo_alt IS NOT NULL)
  )
)`

// ensurePlatformSettings creates the platform_settings table and seeds the
// default GovNL palette when the table is empty. Both the versioned
// migration and initSchema call it, so a fresh install and a migrated one
// end up with the same table.
func ensurePlatformSettings(tx *gorm.DB) error {
	if err := tx.Exec(platformSettingsDDL).Error; err != nil {
		return fmt.Errorf("create platform_settings: %w", err)
	}
	if err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_platform_settings_singleton
       ON platform_settings (singleton)`).Error; err != nil {
		return fmt.Errorf("platform_settings singleton index: %w", err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("platform_settings id: %w", err)
	}
	d := branding.Default()
	if err := tx.Exec(`INSERT INTO platform_settings
       (id, cta, primary_color, secondary, surface, body_text, border)
       VALUES (?, ?, ?, ?, ?, ?, ?)
       ON CONFLICT (singleton) DO NOTHING`,
		id, d.CTA.Hex(), d.Primary.Hex(), d.Secondary.Hex(), d.Surface.Hex(), d.BodyText.Hex(), d.Border.Hex(),
	).Error; err != nil {
		return fmt.Errorf("seed platform_settings: %w", err)
	}
	return nil
}
