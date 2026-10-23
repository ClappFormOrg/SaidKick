package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// GenesisSubjectID returns the stable, well-known UUID used as the SubjectID
// of the very first AuditLog row (state: chain genesis). The value is derived
// from the SHA-1 of the literal "saidkick-appstore:audit-log:genesis" under
// the standard URL namespace — i.e. UUIDv5 — and is reproducible offline.
func GenesisSubjectID() uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("saidkick-appstore:audit-log:genesis"))
}

func genesisSubjectID() uuid.UUID { return GenesisSubjectID() }

// Migrate runs versioned schema migrations via gormigrate, then seeds the
// genesis audit-log row and annual partition tables.
//
// On a fresh database (or an existing AutoMigrate-created one), InitSchema
// runs the full DDL idempotently and records the baseline in the migrations
// table. Subsequent boots apply only new versioned migrations.
func Migrate(ctx context.Context, h *Handle) error {
	if h == nil || h.RW == nil {
		return errors.New("db.Migrate: handle not initialised")
	}

	db := h.RW.WithContext(ctx)

	m := gormigrate.New(db, gormigrate.DefaultOptions, migrations)
	m.InitSchema(initSchema)

	if err := m.Migrate(); err != nil {
		return fmt.Errorf("schema migrations: %w", err)
	}

	if err := ensureGenesisAuditLog(ctx, h); err != nil {
		return fmt.Errorf("genesis audit log: %w", err)
	}

	if err := ensureAuditLogPartitions(db); err != nil {
		return fmt.Errorf("audit log partitions: %w", err)
	}

	return nil
}

// migrations is the ordered list of versioned schema changes applied after
// the baseline. Never modify or delete an existing entry — only append.
// IDs are timestamps in YYYYMMDDHHMMSS format.
var migrations = []*gormigrate.Migration{
	// Baseline schema is captured in InitSchema above.

	{
		// Sprint 4 — S1/S2: add per-cluster Vault AppRole fields and Argo CD
		// project name to the clusters table.
		ID: "20260529100000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&models.Cluster{})
		},
		Rollback: func(tx *gorm.DB) error {
			for _, col := range []string{"vault_role_id", "vault_secret_id", "argocd_project_name"} {
				if err := tx.Exec("ALTER TABLE clusters DROP COLUMN IF EXISTS " + col).Error; err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		// Sprint 4 — API keys: machine tokens for CI/CD pipelines.
		ID: "20260529110000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&models.APIKey{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec("DROP TABLE IF EXISTS api_keys").Error
		},
	},
	{
		// Sprint 4 — Audit comments: inline reviewer notes on submissions.
		ID: "20260529120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&models.AuditComment{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec("DROP TABLE IF EXISTS audit_comments").Error
		},
	},
	{
		// Sprint 4 — Cluster scope refactor: replace free-form "org:<uuid>"
		// scope strings with a typed enum ('public'|'org'|'restricted') and
		// introduce cluster_access_grants for the new restricted tier.
		ID: "20260530100000",
		Migrate: func(tx *gorm.DB) error {
			// Backfill: "org:<uuid>" → "org" (owning_organization_id already holds the UUID).
			if err := tx.Exec("UPDATE clusters SET scope = 'org' WHERE scope LIKE 'org:%'").Error; err != nil {
				return err
			}
			if err := tx.AutoMigrate(&models.ClusterAccessGrant{}); err != nil {
				return err
			}
			tx.Exec("ALTER TABLE clusters DROP CONSTRAINT IF EXISTS clusters_scope_owner_invariant")
			return tx.Exec(`ALTER TABLE clusters
			  ADD CONSTRAINT clusters_scope_owner_invariant
			  CHECK (
			    (scope = 'public' AND owning_organization_id IS NULL)
			    OR
			    (scope IN ('org', 'restricted') AND owning_organization_id IS NOT NULL)
			  )`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Exec("DROP TABLE IF EXISTS cluster_access_grants").Error; err != nil {
				return err
			}
			if err := tx.Exec("ALTER TABLE clusters DROP CONSTRAINT IF EXISTS clusters_scope_owner_invariant").Error; err != nil {
				return err
			}
			return tx.Exec(`ALTER TABLE clusters
			  ADD CONSTRAINT clusters_scope_owner_invariant
			  CHECK (
			    (scope = 'public' AND owning_organization_id IS NULL)
			    OR
			    (scope LIKE 'org:%' AND owning_organization_id IS NOT NULL)
			  )`).Error
		},
	},
	{
		// Sprint 5 — Agent command queue: queued instructions from hub to
		// cluster agents (apply, delete, rollback). Claimed on heartbeat.
		ID: "20260530200000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&models.AgentCommand{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec("DROP TABLE IF EXISTS agent_commands").Error
		},
	},
	{
		// Sprint 5 — Re-validation jobs: queued re-validation runs triggered
		// by GitHub push events to an app's audited branch.
		ID: "20260530210000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&models.RevalidationJob{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec("DROP TABLE IF EXISTS revalidation_jobs").Error
		},
	},
	{
		// Sprint 6 — Rename OIDC columns on organizations to use clean
		// snake_case names. GORM's NamingStrategy splits OIDCIssuer as
		// o_id_c_issuer; explicit column: tags now enforce oidc_issuer.
		// Fresh databases already get the correct names via initSchema +
		// AutoMigrate with the updated model; this migration fixes existing ones.
		ID: "20260603100000",
		Migrate: func(tx *gorm.DB) error {
			// RENAME COLUMN does not support IF EXISTS in PostgreSQL; use a
			// DO block to guard against fresh databases that never had the
			// o_id_c_* names.
			return tx.Exec(`DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='organizations' AND column_name='o_id_c_issuer') THEN
    ALTER TABLE organizations RENAME COLUMN o_id_c_issuer TO oidc_issuer;
  END IF;
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='organizations' AND column_name='o_id_c_client_id') THEN
    ALTER TABLE organizations RENAME COLUMN o_id_c_client_id TO oidc_client_id;
  END IF;
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='organizations' AND column_name='o_id_c_client_secret_ref') THEN
    ALTER TABLE organizations RENAME COLUMN o_id_c_client_secret_ref TO oidc_client_secret_ref;
  END IF;
END$$`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='organizations' AND column_name='oidc_issuer') THEN
    ALTER TABLE organizations RENAME COLUMN oidc_issuer TO o_id_c_issuer;
  END IF;
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='organizations' AND column_name='oidc_client_id') THEN
    ALTER TABLE organizations RENAME COLUMN oidc_client_id TO o_id_c_client_id;
  END IF;
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='organizations' AND column_name='oidc_client_secret_ref') THEN
    ALTER TABLE organizations RENAME COLUMN oidc_client_secret_ref TO o_id_c_client_secret_ref;
  END IF;
END$$`).Error
		},
	},
	{
		// Sprint 7 — Advisory feed: platform security and operational notices.
		ID: "20260604110000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&models.Advisory{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec("DROP TABLE IF EXISTS advisories").Error
		},
	},
	{
		// Sprint 7 — Cluster ingress defaults: add ingress_class_name and
		// base_domain to the clusters table so the deploy form can pre-fill
		// ingress values without the deployer needing to look them up.
		ID: "20260604100000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&models.Cluster{})
		},
		Rollback: func(tx *gorm.DB) error {
			for _, col := range []string{"ingress_class_name", "base_domain"} {
				if err := tx.Exec("ALTER TABLE clusters DROP COLUMN IF EXISTS " + col).Error; err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		// Fix audit_log.previous_hash column type: char(64) pads the genesis
		// empty-string value with spaces; varchar(64) preserves the empty string
		// so the hash-chain verifier can detect the genesis row correctly.
		// USING trim() repairs any already-padded rows on existing installs.
		ID: "20260608100000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE audit_logs
				ALTER COLUMN previous_hash TYPE varchar(64)
				USING trim(previous_hash)`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE audit_logs
				ALTER COLUMN previous_hash TYPE char(64)
				USING previous_hash`).Error
		},
	},
	{
		// Issue #69: scoped, expiring API keys. Adds api_keys.roles, the
		// ceiling intersected with the owner's live roles on every request.
		// Existing keys are backfilled to the owner's current roles so they
		// keep their effective permissions, and open-ended unrevoked keys get
		// a 90-day deadline so no key stays valid forever.
		ID: "20261005100000",
		Migrate: func(tx *gorm.DB) error {
			stmts := []string{
				`ALTER TABLE api_keys
				  ADD COLUMN IF NOT EXISTS roles text[] NOT NULL DEFAULT '{}'`,
				`UPDATE api_keys k SET roles = COALESCE((
				   SELECT array_agg(DISTINCT ra.role::text ORDER BY ra.role::text)
				   FROM role_assignments ra
				   WHERE ra.user_id = k.owner_user_id AND ra.revoked_at IS NULL
				 ), '{}')`,
				`UPDATE api_keys SET expires_at = now() + interval '90 days'
				 WHERE expires_at IS NULL AND revoked_at IS NULL`,
			}
			for _, sql := range stmts {
				if err := tx.Exec(sql).Error; err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			// The expiry backfill is not reverted: once written, a deadline
			// cannot be told apart from one the owner chose.
			return tx.Exec("ALTER TABLE api_keys DROP COLUMN IF EXISTS roles").Error
		},
	},
	{
		// Session revocation (#86): jti of every session JWT ended by logout
		// before its expiry. Rows are swept once expires_at has passed.
		ID: "20261005110000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&models.RevokedSession{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec("DROP TABLE IF EXISTS revoked_sessions").Error
		},
	},
	{
		// Foreign key from cluster_access_grants.organization_id to
		// organizations.id, so the table cannot hold grants for organisations
		// that do not exist. See grantOrgForeignKeyDDL for the orphan guard.
		ID: "20261006100000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(grantOrgForeignKeyDDL).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE cluster_access_grants
				DROP CONSTRAINT IF EXISTS fk_cluster_access_grants_organization`).Error
		},
	},
	{
		// Issue #97: approving a new version used to demote the previously
		// published one to 'rejected'. Those versions passed audit, so they
		// become 'archived'. A version an auditor turned down has no
		// 'approve' decision and stays 'rejected'.
		ID: "20261006110000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`UPDATE app_versions av SET state = 'archived'
				WHERE av.state = 'rejected'
				  AND EXISTS (
				    SELECT 1 FROM audit_submissions s
				    JOIN audit_decisions d ON d.audit_submission_id = s.id
				    WHERE s.app_version_id = av.id AND d.decision = 'approve'
				  )`).Error
		},
		Rollback: func(*gorm.DB) error {
			// Not reverted: once written, a version archived by this step
			// cannot be told apart from one archived by a later approval.
			return nil
		},
	},
	{
		// Issue #139: agent certificate renewal. A renewed certificate is
		// held as pending until the agent first presents it; not_after
		// tracks the active certificate's expiry; rotation_requested_at
		// records an admin-requested rotation. All nullable, no backfill:
		// existing clusters get not_after on their next renewal or handshake.
		ID: "20261011100000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE clusters
				ADD COLUMN IF NOT EXISTS agent_cert_pending_serial text,
				ADD COLUMN IF NOT EXISTS agent_cert_not_after timestamptz,
				ADD COLUMN IF NOT EXISTS agent_cert_rotation_requested_at timestamptz`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE clusters
				DROP COLUMN IF EXISTS agent_cert_pending_serial,
				DROP COLUMN IF EXISTS agent_cert_not_after,
				DROP COLUMN IF EXISTS agent_cert_rotation_requested_at`).Error
		},
	},
	{
		// Issue #157: the app_user role. AutoMigrate never alters an existing
		// CHECK, so the widened tag on RoleAssignment.Role only reaches fresh
		// databases; this rewrites the constraint on existing ones.
		ID: "20261011110000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(roleCheckSQL(models.AllRoles...)).Error
		},
		Rollback: func(tx *gorm.DB) error {
			// Every app_user row, revoked or not, violates the narrower
			// constraint, so those assignments are deleted first. Their
			// grant history survives only in the audit log.
			if err := tx.Exec("DELETE FROM role_assignments WHERE role = ?", models.RoleAppUser).Error; err != nil {
				return err
			}
			return tx.Exec(roleCheckSQL(
				models.RoleDeveloper, models.RoleAuditor, models.RoleDeployer,
				models.RoleClusterAdmin, models.RoleOrgAdmin, models.RolePlatformAdmin,
			)).Error
		},
	},
	{
		// Issue #158: instance-wide branding. One platform_settings row,
		// seeded with the default GovNL palette. initSchema runs the same
		// DDL for a fresh install.
		ID:      "20261011120000",
		Migrate: ensurePlatformSettings,
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec("DROP TABLE IF EXISTS platform_settings").Error
		},
	},
}

// roleCheckSQL replaces the role CHECK on role_assignments with one that
// admits exactly roles. The existing constraint is found by definition (every
// version lists 'platform_admin'), not by name, because the name comes from
// GORM's naming strategy. Roles are compile-time constants, never user input,
// and a DO body cannot take bind parameters, so they are interpolated.
func roleCheckSQL(roles ...models.Role) string {
	quoted := make([]string, 0, len(roles))
	for _, r := range roles {
		quoted = append(quoted, "'"+string(r)+"'")
	}
	return `DO $$
DECLARE c record;
BEGIN
  FOR c IN
    SELECT conname FROM pg_constraint
     WHERE conrelid = 'role_assignments'::regclass
       AND contype  = 'c'
       AND pg_get_constraintdef(oid) LIKE '%platform_admin%'
  LOOP
    EXECUTE format('ALTER TABLE role_assignments DROP CONSTRAINT %I', c.conname);
  END LOOP;

  ALTER TABLE role_assignments
    ADD CONSTRAINT chk_role_assignments_role
    CHECK (role IN (` + strings.Join(quoted, ",") + `));
END$$`
}

// grantOrgForeignKeyDDL adds the cluster_access_grants -> organizations
// foreign key. It is added NOT VALID, so Postgres enforces it on new and
// updated rows without checking existing ones, and then validated only when
// no existing grant points at a missing organisation. An install with orphan
// grants keeps an enforced but unvalidated constraint and logs a warning
// instead of failing the boot; deleting the orphans and running
// VALIDATE CONSTRAINT completes it. Organisations are soft-deleted, so the
// cascade only fires on a hard delete. Safe to re-run.
const grantOrgForeignKeyDDL = `DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'fk_cluster_access_grants_organization'
  ) THEN
    ALTER TABLE cluster_access_grants
      ADD CONSTRAINT fk_cluster_access_grants_organization
      FOREIGN KEY (organization_id) REFERENCES organizations (id)
      ON DELETE CASCADE NOT VALID;
  END IF;
  IF EXISTS (
    SELECT 1 FROM cluster_access_grants g
    WHERE NOT EXISTS (SELECT 1 FROM organizations o WHERE o.id = g.organization_id)
  ) THEN
    RAISE WARNING 'cluster_access_grants has rows for missing organisations; fk_cluster_access_grants_organization left NOT VALID';
  ELSE
    ALTER TABLE cluster_access_grants VALIDATE CONSTRAINT fk_cluster_access_grants_organization;
  END IF;
END$$`

// initSchema creates the full schema on a fresh database. It is also safe
// to run against an existing AutoMigrate-built database — all DDL uses
// IF NOT EXISTS / IF EXISTS guards and AutoMigrate is idempotent.
func initSchema(tx *gorm.DB) error {
	if err := tx.AutoMigrate(
		&models.Organization{},
		&models.User{},
		&models.RoleAssignment{},
		&models.App{},
		&models.AppVersion{},
		&models.AuditSubmission{},
		&models.EvidenceBlob{},
		&models.AuditDecision{},
		&models.AuditComment{},
		&models.Cluster{},
		&models.ClusterAccessGrant{},
		&models.Deployment{},
		&models.AuditLog{},
		&models.AuditDLQ{},
		&models.UsedJoinToken{},
		&models.APIKey{},
		&models.AgentCommand{},
		&models.RevalidationJob{},
		&models.Advisory{},
		&models.RevokedSession{},
	); err != nil {
		return fmt.Errorf("auto-migrate: %w", err)
	}

	if err := addExtraConstraints(tx); err != nil {
		return err
	}
	// InitSchema marks every versioned migration as applied, so a fresh
	// database gets the branding table here rather than from 20261011120000.
	return ensurePlatformSettings(tx)
}

// addExtraConstraints holds every DDL statement that GORM tags can't
// express. Each statement uses IF NOT EXISTS or DO $$ ... $$ patterns so
// re-running Migrate is a no-op once converged.
func addExtraConstraints(db *gorm.DB) error {
	stmts := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_user_org_subj_active
       ON users (organization_id, external_subject)
       WHERE deleted_at IS NULL`,

		`CREATE UNIQUE INDEX IF NOT EXISTS idx_role_user_role_active
       ON role_assignments (user_id, role)
       WHERE revoked_at IS NULL`,

		`ALTER TABLE apps
       ADD COLUMN IF NOT EXISTS github_installation_id bigint,
       ADD COLUMN IF NOT EXISTS github_repo_owner varchar(120),
       ADD COLUMN IF NOT EXISTS github_repo_name varchar(120),
       ADD COLUMN IF NOT EXISTS github_default_branch varchar(255)`,

		`CREATE UNIQUE INDEX IF NOT EXISTS idx_app_repo_active
       ON apps (github_repo_owner, github_repo_name)
       WHERE deleted_at IS NULL`,

		`CREATE UNIQUE INDEX IF NOT EXISTS idx_appver_published_per_app
       ON app_versions (app_id)
       WHERE state = 'published'`,

		`ALTER TABLE clusters
       DROP CONSTRAINT IF EXISTS clusters_scope_owner_invariant`,
		`ALTER TABLE clusters
       ADD CONSTRAINT clusters_scope_owner_invariant
       CHECK (
         (scope = 'public' AND owning_organization_id IS NULL)
         OR
         (scope IN ('org', 'restricted') AND owning_organization_id IS NOT NULL)
       )`,

		`ALTER TABLE clusters
       ADD COLUMN IF NOT EXISTS orchestrator_cluster_name varchar(80) NOT NULL DEFAULT ''`,

		// InitSchema marks every versioned migration as applied, so a fresh
		// database gets this constraint here rather than from 20261006100000.
		grantOrgForeignKeyDDL,

		`CREATE UNIQUE INDEX IF NOT EXISTS idx_dep_cluster_ns_app_active
       ON deployments (cluster_id, namespace, app_id)
       WHERE deleted_at IS NULL`,

		`CREATE UNIQUE INDEX IF NOT EXISTS idx_evblob_sub_name
       ON evidence_blobs (audit_submission_id, logical_name)`,

		`CREATE UNIQUE INDEX IF NOT EXISTS idx_auditlog_ingest_hash
       ON audit_logs (ingest_hash)`,

		`CREATE OR REPLACE FUNCTION audit_logs_forbid_mutation()
       RETURNS trigger LANGUAGE plpgsql AS $$
     BEGIN
       RAISE EXCEPTION 'audit_logs is append-only (TG_OP=%)', TG_OP
         USING ERRCODE = 'insufficient_privilege';
     END;
     $$`,
		`DROP TRIGGER IF EXISTS trg_audit_logs_no_update ON audit_logs`,
		`CREATE TRIGGER trg_audit_logs_no_update
       BEFORE UPDATE OR DELETE ON audit_logs
       FOR EACH ROW EXECUTE FUNCTION audit_logs_forbid_mutation()`,
	}

	for _, sql := range stmts {
		if err := db.Exec(sql).Error; err != nil {
			return fmt.Errorf("ddl %q: %w", firstLine(sql), err)
		}
	}
	return nil
}

// ensureGenesisAuditLog inserts the first AuditLog row if and only if the
// table is empty. The genesis row has PreviousHash = "" by construction
// and acts as the anchor of the hash chain.
func ensureGenesisAuditLog(ctx context.Context, h *Handle) error {
	var count int64
	if err := h.RW.WithContext(ctx).Model(&models.AuditLog{}).Count(&count).Error; err != nil {
		return fmt.Errorf("count: %w", err)
	}
	if count > 0 {
		return nil
	}

	// Use the audit-writer connection so the production role-separation
	// pattern is exercised even in dev.
	genesis := &models.AuditLog{
		EventType:   "platform.genesis",
		SubjectType: "platform",
		// SubjectID gets a zero-ish UUID for the genesis row; the hook
		// will accept it because we set it explicitly. Using a nil UUID
		// is rejected by the hook, so we use a constant well-known id.
		SubjectID: genesisSubjectID(),
	}

	if err := h.AuditWriter.WithContext(ctx).Create(genesis).Error; err != nil {
		return fmt.Errorf("create genesis: %w", err)
	}
	return nil
}

// ensureAuditLogPartitions creates annual child partitions for audit_logs
// when the table is already range-partitioned by occurred_at (production
// installs only). On a standard AutoMigrate install the table is NOT
// partitioned, so the DO block detects that and skips creation — this
// makes the call idempotent on both partitioned and non-partitioned schemas.
//
// Fresh production installs that want partitioning must create the
// partitioned parent table before running the API for the first time; see
// the "Encryption at rest" section of RUNBOOK.md for the procedure.
func ensureAuditLogPartitions(db *gorm.DB) error {
	// Create the current year plus the next two years so there is always
	// headroom. Additional years are added on each migrate run as time
	// advances. The DO block is a no-op on non-partitioned tables.
	sql := `
DO $$
DECLARE
  yr int;
BEGIN
  IF EXISTS (
    SELECT 1
    FROM   pg_partitioned_table pt
    JOIN   pg_class c ON c.oid = pt.partrelid
    WHERE  c.relname = 'audit_logs'
  ) THEN
    FOR yr IN SELECT extract(year FROM now())::int + n FROM generate_series(0,2) AS gs(n) LOOP
      EXECUTE format(
        'CREATE TABLE IF NOT EXISTS audit_logs_%s
         PARTITION OF audit_logs
         FOR VALUES FROM (''%s-01-01'') TO (''%s-01-01'')',
        yr, yr, yr + 1
      );
    END LOOP;
  END IF;
END;
$$`
	if err := db.Exec(sql).Error; err != nil {
		return fmt.Errorf("ddl audit_logs partitions: %w", err)
	}
	return nil
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}
