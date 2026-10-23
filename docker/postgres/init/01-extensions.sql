-- sAId-Kick Appstore — required Postgres extensions.
--
-- pgcrypto: server-side gen_random_uuid() used as a belt-and-braces
--           fallback for migrations that don't bind a uuid client-side.
--           The application primarily mints UUIDv7 in Go.
-- citext  : reserved for future case-insensitive email columns.

\connect saidkick_appstore

CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS citext;
