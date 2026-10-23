-- sAId-Kick Appstore — production-shape role separation.
--
-- Two roles, mirrored from internal/db/db.go::Handle:
--
--   app_rw        : full DML on every table except audit_logs.
--                   It can SELECT from audit_logs but cannot INSERT,
--                   UPDATE, or DELETE.
--
--   audit_writer  : INSERT-only on audit_logs. No SELECT, no UPDATE,
--                   no DELETE on any table. Used by the canonical
--                   audit.Logger (Handle.AuditWriter pool).
--
-- The application's primary connection (DATABASE_URL) typically still
-- uses the superuser in dev. To exercise the separation locally, set:
--
--   DATABASE_URL=postgres://app_rw:app_rw_dev@localhost:5432/saidkick_appstore?sslmode=disable
--   AUDIT_DATABASE_URL=postgres://audit_writer:audit_writer_dev@localhost:5432/saidkick_appstore?sslmode=disable
--
-- The actual GRANT statements are applied below AFTER the GORM
-- AutoMigrate creates the tables on first API boot. That is why this
-- file only creates the roles — see internal/db/migrate.go for the
-- forthcoming grant logic in Sprint 2.

\connect saidkick_appstore

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'app_rw') THEN
        CREATE ROLE app_rw LOGIN PASSWORD 'app_rw_dev';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'audit_writer') THEN
        CREATE ROLE audit_writer LOGIN PASSWORD 'audit_writer_dev';
    END IF;
END
$$;

-- Allow both roles to USE the schema and SELECT future tables. Specific
-- privilege scoping (INSERT-only for audit_writer; everything-except-
-- audit_logs for app_rw) is applied by the application migrator once
-- the tables exist.
GRANT CONNECT ON DATABASE saidkick_appstore TO app_rw, audit_writer;
GRANT USAGE  ON SCHEMA public                TO app_rw, audit_writer;

ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO app_rw;

ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT ON TABLES TO audit_writer;
