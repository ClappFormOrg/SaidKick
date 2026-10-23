// Command seed applies a declarative seed configuration to the sAId-Kick
// database. It is safe to re-run; all operations are idempotent.
//
// Usage:
//
//	go run ./cmd/seed seed.json
//	go run ./cmd/seed            # reads SEED_FILE env var
//
// The seed file (JSON) supports two top-level arrays:
//
//	organizations – orgs to create/update, optionally with OIDC federation
//	users         – users to create/update with role assignments
//
// OIDC client secrets are read from the env var named in
// "oidc_client_secret_env" — they are never stored in the seed file.
// Secrets are encrypted with AUTH_SECRET_ENCRYPTION_KEY when set, or stored
// as v0-inline: refs in dev mode (APP_DEV_MODE=true).
//
// The command is for development and test databases only. It exits non-zero
// unless APP_ENV is dev, development, local, test or ci.
//
// See seed.example.json in the repo root for the full format.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/config"
	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/vault"
	"gorm.io/gorm"
)

// ─── Seed file schema ─────────────────────────────────────────────────────────

type seedFile struct {
	Organizations []seedOrg  `json:"organizations"`
	Users         []seedUser `json:"users"`
}

type seedOrg struct {
	Slug         string `json:"slug"`
	DisplayName  string `json:"display_name"`
	ContactEmail string `json:"contact_email"`
	OIDCIssuer   string `json:"oidc_issuer"`
	OIDCClientID string `json:"oidc_client_id"`
	// OIDCClientSecretEnv is the NAME of the env var that holds the plain
	// client secret. Never put the secret itself in the seed file.
	OIDCClientSecretEnv string `json:"oidc_client_secret_env"`
}

type seedUser struct {
	Email            string   `json:"email"`
	DisplayName      string   `json:"display_name"`
	OrganizationSlug string   `json:"organization_slug"`
	Roles            []string `json:"roles"`
}

// ─── Entry point ──────────────────────────────────────────────────────────────

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if err := run(); err != nil {
		slog.Error("seed failed", "error", err)
		os.Exit(1)
	}
	slog.Info("seed complete")
}

// checkEnv refuses to seed when APP_ENV is production-like. It uses the same
// rule as config's dev-mode guard, so an unset or unknown APP_ENV is refused.
func checkEnv() error {
	if config.IsProdLikeEnv() {
		return fmt.Errorf("refusing to seed a production-like environment (APP_ENV=%s)", os.Getenv("APP_ENV"))
	}
	return nil
}

func run() error {
	if err := checkEnv(); err != nil {
		return err
	}

	// Resolve seed file path.
	path := ""
	if len(os.Args) > 1 {
		path = os.Args[1]
	} else {
		path = os.Getenv("SEED_FILE")
	}
	if path == "" {
		return errors.New("usage: seed <seed-file.json>  or set SEED_FILE env var")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read seed file: %w", err)
	}
	var sf seedFile
	if err := json.Unmarshal(raw, &sf); err != nil {
		return fmt.Errorf("parse seed file: %w", err)
	}

	// Load config and open DB.
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	handle, err := db.Open(cfg.DB)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer func() {
		if cErr := handle.Close(); cErr != nil {
			slog.Warn("close db", "error", cErr)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	slog.Info("running migrations")
	if err := db.Migrate(ctx, handle); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	// Build the secret store (same logic as the API).
	var kvClient vault.KVClient = vault.NullClient{}
	if cfg.Secrets.Configured() {
		kvClient = vault.New(cfg.Secrets.VaultAddr, cfg.Secrets.VaultToken, cfg.Secrets.VaultMount, nil)
	}
	secretStore := auth.NewSecretRefStore(kvClient, cfg.Auth.OIDC.SecretEncryptionKey, cfg.IsDevMode)

	// Apply seed in order: orgs first (users reference them by slug).
	orgBySlug := make(map[string]models.Organization)
	for _, so := range sf.Organizations {
		org, err := applyOrg(ctx, handle, secretStore, so)
		if err != nil {
			return fmt.Errorf("org %q: %w", so.Slug, err)
		}
		orgBySlug[org.Slug] = org
		slog.Info("org applied", "slug", org.Slug, "id", org.ID)
	}

	for _, su := range sf.Users {
		org, ok := orgBySlug[su.OrganizationSlug]
		if !ok {
			// Org not in this seed file — try to fetch from DB.
			if err := handle.RW.WithContext(ctx).First(&org, "slug = ?", su.OrganizationSlug).Error; err != nil {
				return fmt.Errorf("user %q: org %q not found", su.Email, su.OrganizationSlug)
			}
		}
		user, err := applyUser(ctx, handle, org, su)
		if err != nil {
			return fmt.Errorf("user %q: %w", su.Email, err)
		}
		slog.Info("user applied", "email", user.Email, "id", user.ID, "roles", su.Roles)
	}

	return nil
}

// ─── Org upsert ───────────────────────────────────────────────────────────────

func applyOrg(ctx context.Context, handle *db.Handle, store *auth.SecretRefStore, so seedOrg) (models.Organization, error) {
	if strings.TrimSpace(so.Slug) == "" {
		return models.Organization{}, errors.New("slug is required")
	}
	if strings.TrimSpace(so.DisplayName) == "" {
		return models.Organization{}, errors.New("display_name is required")
	}

	var org models.Organization
	err := handle.RW.WithContext(ctx).First(&org, "slug = ?", so.Slug).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		org = models.Organization{
			Slug:         so.Slug,
			DisplayName:  so.DisplayName,
			ContactEmail: so.ContactEmail,
		}
		if err := handle.RW.WithContext(ctx).Create(&org).Error; err != nil {
			return models.Organization{}, fmt.Errorf("create: %w", err)
		}
	case err != nil:
		return models.Organization{}, fmt.Errorf("lookup: %w", err)
	default:
		updates := map[string]any{
			"display_name":  so.DisplayName,
			"contact_email": so.ContactEmail,
		}
		if err := handle.RW.WithContext(ctx).Model(&org).Updates(updates).Error; err != nil {
			return models.Organization{}, fmt.Errorf("update: %w", err)
		}
	}

	// Wire OIDC if all three fields are present.
	if so.OIDCIssuer != "" && so.OIDCClientID != "" && so.OIDCClientSecretEnv != "" {
		secret := os.Getenv(so.OIDCClientSecretEnv)
		if secret == "" {
			return models.Organization{}, fmt.Errorf(
				"env var %q (oidc_client_secret_env) is not set or empty", so.OIDCClientSecretEnv)
		}
		ref, err := store.Write(ctx, org.ID, secret)
		if err != nil {
			return models.Organization{}, fmt.Errorf("store oidc secret: %w", err)
		}
		oidcUpdates := map[string]any{
			"oidc_issuer":            so.OIDCIssuer,
			"oidc_client_id":         so.OIDCClientID,
			"oidc_client_secret_ref": ref,
		}
		if err := handle.RW.WithContext(ctx).Model(&org).Updates(oidcUpdates).Error; err != nil {
			return models.Organization{}, fmt.Errorf("update oidc: %w", err)
		}
		slog.Info("oidc configured", "slug", org.Slug, "issuer", so.OIDCIssuer, "ref_scheme", ref[:strings.Index(ref, ":")+1])
	}

	// Reload to get updated fields.
	if err := handle.RW.WithContext(ctx).First(&org, "id = ?", org.ID).Error; err != nil {
		return models.Organization{}, fmt.Errorf("reload org: %w", err)
	}
	return org, nil
}

// ─── User upsert ──────────────────────────────────────────────────────────────

func applyUser(ctx context.Context, handle *db.Handle, org models.Organization, su seedUser) (models.User, error) {
	if strings.TrimSpace(su.Email) == "" {
		return models.User{}, errors.New("email is required")
	}

	// Seed users get a stable external_subject derived from their email so the
	// provisionUser email-fallback can link their real OIDC sub on first login.
	externalSubject := "seed:" + strings.ToLower(strings.TrimSpace(su.Email))

	var user models.User
	err := handle.RW.WithContext(ctx).
		Where("organization_id = ? AND email = ?", org.ID, su.Email).
		First(&user).Error

	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		user = models.User{
			OrganizationID:  org.ID,
			ExternalSubject: externalSubject,
			Email:           su.Email,
			DisplayName:     su.DisplayName,
		}
		if err := handle.RW.WithContext(ctx).Create(&user).Error; err != nil {
			return models.User{}, fmt.Errorf("create user: %w", err)
		}
	case err != nil:
		return models.User{}, fmt.Errorf("lookup user: %w", err)
	default:
		updates := map[string]any{"display_name": su.DisplayName}
		if err := handle.RW.WithContext(ctx).Model(&user).Updates(updates).Error; err != nil {
			return models.User{}, fmt.Errorf("update user: %w", err)
		}
	}

	// Apply roles (idempotent — skip if already active).
	for _, roleStr := range su.Roles {
		role := models.Role(roleStr)
		valid := false
		for _, r := range models.AllRoles {
			if r == role {
				valid = true
				break
			}
		}
		if !valid {
			slog.Warn("unknown role — skipping", "role", roleStr, "email", su.Email)
			continue
		}

		var count int64
		if err := handle.RW.WithContext(ctx).Model(&models.RoleAssignment{}).
			Where("user_id = ? AND role = ? AND revoked_at IS NULL", user.ID, role).
			Count(&count).Error; err != nil {
			return models.User{}, fmt.Errorf("count role assignments: %w", err)
		}
		if count > 0 {
			continue // already active
		}

		assignment := models.RoleAssignment{
			UserID:          user.ID,
			Role:            role,
			GrantedByUserID: user.ID, // self-grant for seed users
		}
		if err := handle.RW.WithContext(ctx).Create(&assignment).Error; err != nil {
			return models.User{}, fmt.Errorf("grant role %q: %w", role, err)
		}
	}

	return user, nil
}
