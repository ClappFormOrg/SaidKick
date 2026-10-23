# sAId-Kick Appstore API

Go-based REST API for the sAId-Kick Appstore platform.

---

## Stack

| Concern        | Choice                                          |
|----------------|-------------------------------------------------|
| Language       | Go 1.25                                         |
| HTTP framework | Echo v4                                         |
| ORM            | GORM v1.25 + Postgres driver                    |
| Database       | PostgreSQL 16                                   |
| Migrations     | `gormigrate` v2 (versioned, ordered)            |
| Logging        | `log/slog` (structured, JSON in production)     |
| Env loading    | `joho/godotenv` for local `.env`                |

> ADR-9 in the design document was updated in Sprint 0 to reflect the GORM
> choice with the explicit ~2-3× throughput trade-off vs Bun + sqlc.

## Layout

```
API/
├── cmd/
│   ├── api/main.go              # boots config → DB → migrate → HTTP server
│   ├── agent/                   # sAId-Kick agent binary (heartbeat + Argo CD)
│   ├── agentinit/               # one-time cluster handshake init container
│   ├── drain-audit-dlq/         # CLI tool to drain the audit dead-letter queue
│   ├── sarif-gate-report/       # read-only CLI: recent submissions SARIF enforce mode would block
│   └── seed/                    # database seed tool for local/test environments
├── internal/
│   ├── apierror/                # shared error envelope (breaks import cycle)
│   ├── audit/                   # canonical AuditLog writer service
│   ├── auth/                    # principal context + RBAC + DevModeAuthenticator
│   ├── config/                  # env-driven Config
│   ├── db/
│   │   ├── db.go                # two-role connection pool (RW + AuditWriter)
│   │   ├── migrate.go           # versioned migrations + genesis row
│   │   └── models/              # 10 GORM entities
│   ├── handlers/                # HTTP transport — one file per domain
│   │   ├── handlers.go          # router + dependency container
│   │   ├── health.go            # liveness + readiness
│   │   ├── me.go                # GET /me
│   │   ├── catalog.go           # GET /apps, GET /apps/:slug
│   │   ├── apps.go              # CreateApp + versions (GitHub stub)
│   │   ├── audit.go             # audit queue + decision pipeline + comments
│   │   ├── clusters.go          # cluster CRUD + handshake + scope + access
│   │   ├── agent.go             # agent heartbeat + deployment state reports
│   │   ├── deployments.go       # full deployment lifecycle
│   │   ├── orgs.go              # org + member management
│   │   ├── advisories.go        # security advisories
│   │   ├── community.go         # community discussions (GitHub GraphQL proxy)
│   │   ├── apikeys.go           # API-key CRUD
│   │   └── webhook.go           # GitHub webhook
│   ├── integrations/
│   │   ├── github/              # GitHub App client + webhook parser
│   │   ├── orchestrator/argocd/ # Argo CD deployer (apply / delete / rollback)
│   │   ├── secrets/             # Vault KV secret store (Vault / InMemory / Null)
│   │   └── vault/               # Vault primitives: KV, PKI, AppRole, token renewer
│   └── server/                  # Echo wrapper, middleware, error handler
├── go.mod / go.sum
├── Makefile
├── .env.example
└── README.md
```

## Quick start

```pwsh
# 1. fetch modules
make tidy

# 2. build + run the custom dev Postgres image
#    (extends postgres:16-alpine with pgcrypto + app_rw + audit_writer roles)
make dev-db

# 3. point the API at it
Copy-Item .env.example .env

# 4. start the API (runs migrations, then listens on :8080)
make run
```

Health check:

```pwsh
curl http://localhost:8080/healthz
curl http://localhost:8080/readyz
curl http://localhost:8080/api/v1/apps
```

Other dev-db targets:

| Target              | Purpose                                                    |
|---------------------|------------------------------------------------------------|
| `make dev-db`       | build the image + run a fresh container                    |
| `make dev-db-build` | rebuild the image only                                     |
| `make dev-db-run`   | recreate the container from the existing image             |
| `make dev-db-down`  | remove the container                                       |
| `make dev-db-logs`  | follow container logs                                      |
| `make dev-db-psql`  | open a psql shell as superuser inside the running container|

The image source lives at [`docker/postgres/`](../docker/postgres) and:

- enables the `pgcrypto` + `citext` extensions on first boot
- pre-creates the production roles `app_rw` and `audit_writer` (passwords `app_rw_dev` / `audit_writer_dev` in the dev image — change them for non-laptop use)
- pre-creates the database `saidkick_appstore`
- enables 200ms slow-query logging via `postgresql.conf.append`

## Endpoint status

| Endpoint group                                       | Status                                                                      |
|------------------------------------------------------|-----------------------------------------------------------------------------|
| `/healthz`, `/readyz`                                | ✅ implemented                                                              |
| `GET /api/v1/me`                                     | ✅ implemented (returns authenticated principal)                            |
| `GET /api/v1/apps`, `/apps/:slug`                    | ✅ implemented (anonymous OK)                                               |
| `POST /apps`                                         | ✅ implemented (GitHub App: real / NullClient: 503)                         |
| `GET /apps/:slug/versions`                           | ✅ implemented                                                              |
| `POST /apps/:slug/versions`                          | ✅ implemented                                                              |
| `POST /apps/:slug/submit`                            | ✅ implemented                                                              |
| `GET /audit-submissions`, `/:id`                     | ✅ implemented (cross-org check)                                            |
| `GET /audit-submissions/:id/evidence/:name`          | ✅ implemented (proxies to GitHub raw)                                      |
| `GET /audit-submissions/:id/comments`                | ✅ implemented                                                              |
| `POST /audit-submissions/:id/comments`               | ✅ implemented                                                              |
| `POST /audit-submissions/:id/decision`               | ✅ implemented (state transitions + audit log)                              |
| `GET /clusters`, `POST /clusters`                    | ✅ implemented (scope-aware)                                                |
| `GET /clusters/:id`, `PATCH /clusters/:id`           | ✅ implemented                                                              |
| `GET /clusters/:id/access`                           | ✅ implemented (PlatformAdmin)                                              |
| `POST /clusters/:id/access`                          | ✅ implemented (grant access to org)                                        |
| `DELETE /clusters/:id/access/:org_id`                | ✅ implemented (revoke access)                                              |
| `POST /clusters/:id/scope`                           | ✅ implemented                                                              |
| `DELETE /clusters/:id`                               | ✅ implemented                                                              |
| `POST /clusters/:id/handshake`                       | ✅ implemented (join-token JWT + mTLS cert issuance via Vault PKI)          |
| `POST /clusters/:id/heartbeat`                       | ✅ implemented (agent auth; stamps last_heartbeat; returns pending commands)|
| `POST /clusters/:id/deployment-states`               | ✅ implemented (agent reports state transitions; validates FSM)             |
| `POST /clusters/:id/commands/:cmd_id/ack`            | ✅ implemented (agent acknowledges or nacks a command)                      |
| `GET /deployments`, `GET /deployments/:id`           | ✅ implemented (RBAC-filtered; live status from Argo CD if configured)      |
| `POST /deployments`                                  | ✅ implemented (RBAC + cluster scope + duplicate guard + secrets + Argo CD) |
| `DELETE /deployments/:id`                            | ✅ implemented (Argo CD cascade + secret purge + soft-delete)               |
| `GET /deployments/:id/events`                        | ✅ implemented (SSE polling, 2s tick, terminal-phase shutdown)              |
| `POST /deployments/:id/upgrade`                      | ✅ implemented (re-applies with new version + optional values/secrets)      |
| `POST /deployments/:id/rollback`                     | ✅ implemented (Orchestrator.Rollback with target GitSHA)                   |
| `POST /orgs`, `POST /orgs/:slug/oidc`                | ✅ implemented                                                              |
| `GET /orgs`                                          | ✅ implemented (list federated orgs)                                        |
| `GET /orgs/:slug/members`, `POST .../roles`          | ✅ implemented                                                              |
| `GET /api-keys`, `POST /api-keys`, `DELETE /:id`     | ✅ implemented (SHA-256 digest stored; full token returned once on create)  |
| `GET /audit-log`                                     | ✅ implemented (PlatformAdmin)                                              |
| `GET /advisories`, `POST /advisories`                | ✅ implemented (platform security advisories)                               |
| `GET /community/discussions`                         | ✅ implemented (GitHub GraphQL proxy with in-memory cache)                  |
| `POST /webhooks/github`                              | ✅ implemented (HMAC verify + push / installation handlers; queues revalidation jobs) |

Every response — including stubs — uses the canonical `{ code, message }` envelope so the webapp can pattern-match consistently.

## Agent binaries

### `cmd/agent`

The sAId-Kick agent runs inside each spoke cluster and bridges between the cluster-local Argo CD and the hub API.

```
SAIDKICK_HUB_URL=https://hub.appstore.gov.nl \
SAIDKICK_CLUSTER_ID=<uuid> \
SAIDKICK_CERT_DIR=/certs \
ARGOCD_SERVER_URL=http://argocd-server.argocd.svc \
ARGOCD_AUTH_TOKEN=<token> \
  ./agent
```

The agent loops on a configurable interval:
1. **Heartbeat** — `POST /clusters/:id/heartbeat` → receives pending deploy/upgrade/rollback/delete commands.
2. **Execute** — translates each command into Argo CD API calls.
3. **Report states** — `POST /clusters/:id/deployment-states` → streams phase transitions back to the hub.

The hub serves these routes only on its agent mTLS listener (`AGENT_TLS_ADDR`, default `:8443`), which requires TLS 1.3 and a client certificate signed by the agent CA, and whose serial must equal the cluster's `agent_cert_serial`. Point the agent at it with `SAIDKICK_HUB_AGENT_URL`; it falls back to `SAIDKICK_HUB_URL`, which the handshake keeps using.

In dev mode (`SAIDKICK_DEV_MODE=true`) the agent sends `X-Agent-Cluster-ID` instead of an mTLS certificate. A hub with `APP_DEV_MODE=true` also serves the agent routes on its main listener for this.

### `cmd/agentinit`

Kubernetes init container that performs the one-time cluster handshake and writes the issued mTLS certificate material to a shared volume. Idempotent — skips the handshake if a valid, non-expiring cert already exists.

```
SAIDKICK_HUB_URL=https://hub.appstore.gov.nl \
SAIDKICK_CLUSTER_ID=<uuid> \
SAIDKICK_JOIN_TOKEN=<one-time JWT from CreateCluster> \
SAIDKICK_CERT_DIR=/certs \
  ./agentinit
```

## GitHub App integration

The platform talks to GitHub via a single GitHub App (one App across all
participating orgs). Developers install that App on their own repos to
grant scoped read access to `appstore.yaml` + audit evidence files.

### Setting up the App

1. At https://github.com/settings/apps/new (or your org's App settings),
   create a new GitHub App with:
   - **Permissions**: `Contents: Read`, `Metadata: Read`,
     `Pull requests: Write`, `Webhooks: Read & write`.
   - **Subscribe to events**: `push`, `installation`,
     `installation_repositories`, `repository`.
   - **Webhook URL**: `https://<your-host>/api/v1/webhooks/github`.
   - **Webhook secret**: generate a strong random string and put it in
     `GITHUB_WEBHOOK_SECRET`.
2. Download the App's private key (a PEM file). Point
   `GITHUB_APP_PRIVATE_KEY_FILE` at the path, or paste the PEM into
   `GITHUB_APP_PRIVATE_KEY`.
3. Put the App ID (visible on the App's settings page) in
   `GITHUB_APP_ID`.

If any of those three vars is set, all three must be set — the boot
refuses to start in a half-configured state. If none is set, the API
boots with `NullClient` and the four endpoints above return 503.

### What `POST /apps` does end-to-end

1. Resolves the request's `branch` to a commit SHA via the GitHub API.
2. Fetches `appstore.yaml` at that SHA.
3. Runs `manifest.ValidateSchema()` — apiVersion + kind + metadata + spec
   completeness, license SPDX allow-list, AI-risk class enum, EU
   data-residency invariant.
4. Runs `manifest.ValidateEvidence()` — fetches every declared evidence
   file (≥3 screenshots, DPIA PDF, SARIF security scan, AI-risk PDF,
   accessibility statement, optional video / model card) and checks
   each against the content-type allow-list.
5. Fetches `Chart.yaml` and `values.schema.json` from the declared chart
   path.
6. Persists `App` + `AppVersion` + `AuditSubmission` in one transaction
   with both `AuditSubmissionID` and `AppVersionID` pre-minted UUIDv7s
   so the (intentionally unenforced at DB level) circular reference
   can be wired atomically.
7. Writes one `audit_logs` row (`app.created`) via the dedicated
   audit-writer pool.

### Webhook handling

The HMAC signature on every incoming webhook is verified against
`GITHUB_WEBHOOK_SECRET` with constant-time comparison.

- **`push`** to the audited branch of any linked App creates a
  `RevalidationJob` row and writes an `app.revalidation_queued` audit
  log entry. The actual re-validation is picked up by the
  `gh-webhook-worker` process (deferred).
- **`installation`** events log the action.
- **`installation_repositories` removed** marks any Apps whose repo is
  no longer accessible as `repo_link_state = disconnected`.
- Anything else: 200 OK + ignored.

## Authentication

The API ships two authenticators, selected at boot:

| Condition | Authenticator | How to identify the caller |
|---|---|---|
| `APP_DEV_MODE=true` | `DevModeAuthenticator` | `X-Dev-User-ID: <uuid>` header |
| `APP_DEV_MODE=false` + OIDC env vars set | `OIDCAuthenticator` | session cookie from OIDC login flow |

### Dev mode (local development)

```pwsh
# 1. seed a user manually (use psql or a Go script)
psql "$env:DATABASE_URL" -c "INSERT INTO organizations (id, slug, display_name, contact_email, created_at, updated_at) VALUES (gen_random_uuid(), 'provincie-voorbeeld', 'Provincie Voorbeeld', 'contact@example.nl', now(), now());"
psql "$env:DATABASE_URL" -c "INSERT INTO users (id, organization_id, external_subject, email, display_name, created_at) VALUES (gen_random_uuid(), (SELECT id FROM organizations WHERE slug='provincie-voorbeeld'), 'dev-1', 'maya@example.nl', 'Maya Developer', now());"
psql "$env:DATABASE_URL" -c "INSERT INTO role_assignments (id, user_id, role, granted_by_user_id, granted_at) SELECT gen_random_uuid(), id, 'developer', id, now() FROM users WHERE email='maya@example.nl';"

# 2. make a request with the user id header
$uid = (psql "$env:DATABASE_URL" -t -c "SELECT id FROM users WHERE email='maya@example.nl';").Trim()
curl -H "X-Dev-User-ID: $uid" http://localhost:8080/api/v1/apps
```

### Production OIDC

Set these env vars to enable the OIDC RP middleware:

| Env var | Required | Description |
|---|---|---|
| `AUTH_SESSION_SECRET` | Yes | HMAC-SHA256 key for session cookies — minimum 32 bytes. Generate with `openssl rand -base64 32`. |
| `AUTH_BASE_URL` | Yes | Public base URL of this API, no trailing slash (e.g. `https://appstore.gov.nl`). Used to build the callback URI. |
| `AUTH_COOKIE_DOMAIN` | No | Cookie domain restriction (e.g. `appstore.gov.nl`). Leave empty for localhost. |
| `AUTH_SESSION_TTL` | No | Session cookie lifetime. Default `8h`. Accepts Go duration strings. |
| `AUTH_SECRET_ENCRYPTION_KEY` | No | Base64-encoded 32-byte AES-256 key for encrypting OIDC client secrets in the DB when Vault is not configured. Generate with `openssl rand -base64 32`. |

Per-org OIDC credentials (issuer URL, client ID, client secret) are configured at runtime via `POST /api/v1/orgs/:slug/oidc`. The OIDC login flow starts at `GET /auth/login?org=<slug>`.

## Architecture notes

### Why `internal/apierror`

`internal/handlers` and `internal/server` both need the error envelope
type. Putting it in either creates a cycle. The tiny `apierror` package
holds the types + constructors; `server.ErrorHandler` consumes them.

### Why two GORM handles

`db.Handle` exposes `RW` (full DML on every table except `audit_logs`) and
`AuditWriter` (INSERT-only on `audit_logs`). Production assigns two
distinct PG roles; V0 dev reuses the same DSN. The `audit.Logger` always
writes via `AuditWriter` so the separation pattern is exercised end-to-end.

### Why hooks for hash chain + UUIDv7

`models.IDModel.BeforeCreate` mints UUIDv7s so primary-key indexes stay
write-friendly. `models.AuditLog.BeforeCreate` acquires
`pg_advisory_xact_lock` so concurrent inserts serialise on the chain
advance; without it two concurrent writers could fork the chain.

## Deployment orchestrator (Argo CD)

The platform delegates the actual Helm install to a configured Argo CD
endpoint via the `internal/integrations/orchestrator` package. The
abstraction (the `Deployer` interface) lets us swap Argo CD for Flux
or an agent-brokered impl later without touching handlers.

### Agent-brokered (production) vs direct Argo CD (dev only)

**Production**: `ORCHESTRATOR_KIND=agent` wires the agent-brokered
deployer (`integrations/orchestrator/agentbrokered`). The hub queues
commands for a cluster; that cluster's agent claims them over the agent
mTLS listener and applies them to its own local Argo CD. The hub holds
no kubeconfigs and no Argo CD credentials (ADR-4). The hub chart defaults
to this kind.

**Dev only**: `ORCHESTRATOR_KIND=argocd` wires `ArgoCDDeployer`, which
talks directly to one Argo CD with a stored token; every spoke must be
registered with it via `argocd cluster add`, which ADR-4 rules out in
production. The API refuses to boot with this kind, or with `ARGOCD_*`
set and no kind, unless `APP_ENV` is `dev`, `development`, `local`,
`test` or `ci`.

### Configuring the direct Argo CD client (dev)

1. Install Argo CD on the hub cluster (or accessible to the API process):
   ```
   helm install argocd argo/argo-cd -n argocd --create-namespace
   ```
2. Add each target cluster:
   ```
   argocd cluster add <kubeconfig-context-name>
   ```
3. Generate a service-account token:
   ```
   argocd account generate-token --account <name>
   ```
4. Set `.env`:
   ```
   ORCHESTRATOR_KIND=argocd
   ARGOCD_SERVER_URL=http://localhost:8081       # or in-cluster URL
   ARGOCD_AUTH_TOKEN=eyJhbGciOiJI...
   ARGOCD_NAMESPACE=argocd
   ARGOCD_INSECURE_SKIP_VERIFY=true              # dev only
   ```

### Status mapping (Argo CD → platform Phase)

| Argo CD sync.status | Argo CD health.status   | Platform Phase   |
|---------------------|-------------------------|------------------|
| (any)               | operationState=Running  | applying         |
| Synced              | Healthy                 | ready            |
| Synced              | Progressing             | reconciling      |
| OutOfSync           | (any)                   | reconciling      |
| (any)               | Degraded                | degraded         |
| (any)               | Missing                 | failed           |
| (empty)             | (empty)                 | pending          |

## Secrets

The platform stores deployment secrets in HashiCorp Vault (KV v2). At
deploy time the caller supplies `secret_inputs` (field → plaintext value)
in the request body. The API writes each value to Vault and hands the
orchestrator only the **vault paths** — the values themselves never sit
in the database. An in-cluster External Secrets Operator (ESO) reads
from Vault and materialises Kubernetes `Secret` objects inside the target
namespace.

### Boot-time store selection

Three env vars control which backend is wired:

| Env var      | Default | Purpose                            |
|--------------|---------|------------------------------------|
| `VAULT_ADDR` | —       | Full URL of the Vault server (no trailing slash), e.g. `https://vault.example.com` |
| `VAULT_TOKEN`| —       | Static token (see note on renewal below) |
| `VAULT_MOUNT`| `kv`    | KV v2 mount path                   |

The API picks the store at startup in this priority order:

| Scenario                          | Store wired      | Boot log message |
|-----------------------------------|------------------|------------------|
| `VAULT_ADDR` + `VAULT_TOKEN` set  | **VaultStore**   | `Vault secret store wired (addr=…)` |
| `APP_DEV_MODE=true`, no Vault     | **InMemoryStore** | `WARNING: secret store is in-memory` |
| Neither                           | **NullStore**     | `Secret store not configured` — any deployment that supplies `secret_inputs` returns `503 secrets_not_configured` |

### Vault path structure

Every secret is stored at:

```
kv/data/deployments/<deployment-uuid>/<field-name>
```

`field-name` is validated against `^[a-zA-Z][a-zA-Z0-9_]{0,62}$` before
any write so user-supplied keys cannot escape the deployment-scoped
namespace. The path is what gets written into `Deployment.secret_refs`
in the database and what ESO references to build the in-cluster `Secret`.

### Local dev — no Vault needed

`APP_DEV_MODE=true` (the `.env.example` default) activates the
in-memory store. Secrets survive for the lifetime of the API process and
the full deploy flow works end-to-end — the only missing piece is ESO
reading them back, which is a cluster-side concern.

### Local dev — testing with a real Vault

If you want to exercise the `VaultStore` path locally, spin up a Vault
dev server in Docker:

```sh
docker run --rm -d --name vault-dev \
  -p 8200:8200 \
  -e VAULT_DEV_ROOT_TOKEN_ID=dev-token \
  hashicorp/vault server -dev
```

Then add to `.env`:

```
VAULT_ADDR=http://localhost:8200
VAULT_TOKEN=dev-token
VAULT_MOUNT=kv
```

Vault's dev mode pre-creates the `kv` KV v2 mount automatically. Remove
`APP_DEV_MODE=true` (or set it to `false`) so the Vault path is taken
instead of the in-memory fallback.

### Production setup

1. Deploy HashiCorp Vault (or HCP Vault) and enable a KV v2 mount:
   ```
   vault secrets enable -path=kv kv-v2
   ```
2. Create a policy that allows the API to read/write/list/delete under
   `kv/data/deployments/*` and `kv/metadata/deployments/*`:
   ```hcl
   path "kv/data/deployments/*"     { capabilities = ["create","update","read"] }
   path "kv/metadata/deployments/*" { capabilities = ["list","delete"] }
   ```
3. Generate a token with that policy and set `VAULT_TOKEN` in your
   deployment environment. The token-renewer goroutine (`internal/integrations/vault/renewer.go`)
   will keep it alive automatically as long as the API is running.
4. Configure ESO in each spoke cluster to read from the same Vault
   address and mount. ESO uses its own service-account credential (not
   the API token) to read secrets after initial write.

### Secret schema enforcement

`POST /deployments` and `POST /deployments/:id/upgrade` validate the
request body against the `AppVersion.values_schema` (the chart's
`values.schema.json`):

- Any field with `"format": "secret"` in the schema **must** be supplied
  in `secret_inputs`, not `values`. Supplying it in `values` returns
  `422 values_validation_failed`.
- Required non-secret fields must be present in `values`.

## Roadmap

- **Done**: deployment orchestrator (Argo CD adapter) + secret-store abstraction + full deployment lifecycle (`POST`, `DELETE`, `upgrade`, `rollback`, SSE `/events`).
- **Done**: real HashiCorp Vault `secrets.Store` with InMemory/Null fallbacks + token-renewer goroutine; secret-field schema enforcement at deploy/upgrade time.
- **Done**: `gormigrate` versioned migrations (replaces `AutoMigrate`).
- **Done**: OIDC RP middleware with PKCE and per-org federation (replaces `DevModeAuthenticator` in production).
- **Done**: hash-chained append-only audit log with dead-letter queue + `drain-audit-dlq` CLI.
- **Done**: Vault PKI client → `POST /clusters/:id/handshake` fully implemented.
- **Done**: API-key store (`GET`/`POST`/`DELETE /api-keys`).
- **Done**: audit submission comments (`GET`/`POST /audit-submissions/:id/comments`).
- **Done**: sAId-Kick agent binary (`cmd/agent`) + agent-init binary (`cmd/agentinit`).
- **Done**: `testcontainers-go` integration tests.
- **Next**: `AgentBrokeredDeployer` — hub serialises `ApplicationSpec` via wss to the agent; no kubeconfigs on hub. Replaces V1 direct Argo CD calls.
- **Sprint 4+**: async revalidation worker (`gh-webhook-worker`) — picks up `RevalidationJob` rows, re-fetches + re-validates, transitions `AppVersion` state.

## Contributing

See [CONTRIBUTING.md](../CONTRIBUTING.md) for prerequisites, dev setup, commit style, and the DCO sign-off requirement.

## License

EUPL-1.2.
