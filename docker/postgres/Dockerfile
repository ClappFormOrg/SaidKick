# sAId-Kick Appstore — Postgres image for local development.
#
# Extends the official postgres:18-alpine image with:
#   - pgcrypto extension enabled (for gen_random_uuid())
#   - the two production roles (app_rw, audit_writer) pre-created
#   - the saidkick_appstore database pre-created
#   - a small postgresql.conf override that turns on slow-query logging
#
# Usage:
#   docker build -t saidkick-appstore-pg ./docker/postgres
#   docker run -d --name saidkick-appstore-pg \
#       -e POSTGRES_PASSWORD=postgres \
#       -p 5432:5432 \
#       saidkick-appstore-pg
#
# The Makefile `dev-db` target does both of those steps in one shot.

FROM postgres:18-alpine

# Defaults consumed by the upstream entrypoint. Override at runtime when
# you need a non-default superuser password (which you should in any
# environment other than your laptop).
ENV POSTGRES_DB=saidkick_appstore \
    POSTGRES_USER=postgres \
    POSTGRES_PASSWORD=postgres \
    POSTGRES_INITDB_ARGS="--encoding=UTF8 --locale=C"

# Init scripts in /docker-entrypoint-initdb.d/ run once, on first boot,
# AFTER postgres has accepted connections. They run in lexicographic
# order, so the numeric prefixes pin sequencing.
COPY init/ /docker-entrypoint-initdb.d/

# Modest tuning: slow-query log + per-statement timing so we can spot
# pathological queries during development. Production config is owned
# by the AKS/Helm chart, not this dev image.
COPY postgresql.conf.append /tmp/postgresql.conf.append
RUN cat /tmp/postgresql.conf.append >> /usr/local/share/postgresql/postgresql.conf.sample

# Health probe that exits 0 once the server is accepting connections.
HEALTHCHECK --interval=5s --timeout=3s --retries=20 \
    CMD pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB" || exit 1

EXPOSE 5432
