# sAId-Kick Appstore API — Development Runbook

The shortest path from a fresh clone to a request that returns real data.

---

## Prerequisites

| Tool                | Version            | Why                                                       |
|---------------------|--------------------|-----------------------------------------------------------|
| Go                  | 1.23 or later      | builds the API                                            |
| Docker Desktop      | recent             | runs the dev Postgres image (and optionally the Vault dev container) |
| GNU make            | shipped with WSL or `choco install make` on Windows | runs the Makefile targets    |
| `psql` (optional)   | shipped with Postgres client tools | seed users + ad-hoc queries              |
| GitHub App (optional) | —                | only needed if you want `POST /apps` to actually fetch a repo |
| OpenBao (optional) | any recent | only needed if you want to test the real secret-store path; dev mode uses an in-memory fallback |

---

## One-time setup

```pwsh
cd API

# 1. fetch Go modules
make tidy

# 2. build + start the dev Postgres image
#    (extends postgres:18-alpine, pre-creates app_rw + audit_writer roles,
#     enables pgcrypto + citext, configures slow-query logging)
make dev-db

# 3. copy the env template and edit if needed
Copy-Item .env.example .env
```

The default `.env` connects as superuser to the container above. You only
need to edit it if:

- you want `POST /apps` to actually fetch a repo → fill in the
  `GITHUB_APP_*` block (see `README.md` for App setup)
- you want the real Vault secret store → fill in the `VAULT_*` block
  (see below)
- you change the dev DB port → update `DATABASE_URL`

---

## Daily loop

```pwsh
# Apply migrations (idempotent — safe to run every boot)
make migrate

# Or: migrate + start the HTTP server, blocking on SIGINT
make run
```

Server listens on `:8080`. Tear-down between days:

```pwsh
make dev-db-down   # remove the Postgres container
```

Or, when migrations leave the DB in a partially-applied state (typically
because of a model change that GORM can't reconcile in place):

```pwsh
make dev-db-down
make dev-db-run    # recreate the container from the existing image
make run
```

---

## Secret store / Vault

### How the API picks a backend

The API selects a secret store at boot — you do not need Vault running
to develop locally:

| Situation | Store used | What happens |
|---|---|---|
| `APP_DEV_MODE=true` (default `.env`) | **InMemoryStore** | Secrets live in RAM for the lifetime of the process. All deploy flows work; secrets are lost on restart. |
| `VAULT_ADDR` + `VAULT_TOKEN` set | **VaultStore** | Real Vault KV v2 reads/writes. Works in dev and production. |
| Neither | **NullStore** | API boots fine; any request that tries to write secrets gets `503 secrets_not_configured`. Deployments with no `secret_inputs` work normally. |

### Dev option A — in-memory (no setup)

Leave the defaults in `.env`:

```
APP_DEV_MODE=true
```

That's it. The deploy flow, upgrade, rollback, and delete all work.
Secrets disappear when the API process stops, which is fine for a
laptop.

### Dev option B — real OpenBao container

Run this once per dev session (or add it to your startup script):

```sh
docker run --rm -d --name openbao-dev \
  -p 8200:8200 \
  -e BAO_DEV_ROOT_TOKEN_ID=dev-token \
  openbao/openbao server -dev
```

Then add to `.env` (and remove or set `APP_DEV_MODE=false` so the
OpenBao path is taken instead of the in-memory fallback):

```
VAULT_ADDR=http://localhost:8200
VAULT_TOKEN=dev-token
VAULT_MOUNT=kv
APP_DEV_MODE=false
```

OpenBao's dev server pre-creates the `kv` KV v2 mount automatically.
Restart the API and look for `Vault secret store wired` in the boot log.

You can inspect secrets via the OpenBao CLI (`bao`):

```sh
# list all secrets for a deployment
bao kv list kv/deployments/<deployment-uuid>/

# read one field
bao kv get kv/deployments/<deployment-uuid>/<field-name>
```

Or browse the OpenBao UI at `http://localhost:8200` (token: `dev-token`).

### Per-cluster Vault AppRole (Sprint 4 — production hardening)

When `VAULT_APPROLE_MOUNT` is set, the API scopes each cluster's secrets to its own Vault AppRole instead of using the shared `VAULT_TOKEN`. This limits blast radius: a compromised cluster's credentials cannot read secrets for other clusters.

**How to provision (per cluster, operator task):**

```bash
# 1. Create a policy scoped to this cluster's secrets
bao policy write cluster-<cluster-uuid> - <<'EOF'
path "kv/data/deployments/<cluster-uuid>/*"     { capabilities = ["create","update","read","delete"] }
path "kv/metadata/deployments/<cluster-uuid>/*" { capabilities = ["list","delete"] }
EOF

# 2. Enable AppRole auth if not already enabled
bao auth enable approle

# 3. Create the AppRole role
bao write auth/approle/role/cluster-<cluster-uuid> \
  policies=cluster-<cluster-uuid> \
  token_ttl=1h token_max_ttl=4h

# 4. Read the role_id and generate a secret_id
bao read auth/approle/role/cluster-<cluster-uuid>/role-id
bao write -f auth/approle/role/cluster-<cluster-uuid>/secret-id
```

Then set the `vault_role_id` and `vault_secret_id` fields on the cluster row:

```bash
curl -X PATCH https://<hub>/api/v1/clusters/<uuid> \
  -H "Content-Type: application/json" \
  -d '{"vault_role_id":"<role-id>","vault_secret_id":"<secret-id>"}'
```

The API caches the resulting token until 80% of its TTL elapses, then re-logs in automatically.

**Env var:** `VAULT_APPROLE_MOUNT` — the auth mount name (e.g. `approle`). When unset, the static `VAULT_TOKEN` is used for all clusters.

### Production Vault setup

See the **"Production setup"** section in `README.md` for the full steps
(policy creation, token generation, ESO wiring). The short version:

```
VAULT_ADDR=https://vault.your-org.nl
VAULT_TOKEN=<long-lived service token>
VAULT_MOUNT=kv          # or whatever mount you created
```

The token must have `create + update + read` on
`kv/data/deployments/*` and `list + delete` on
`kv/metadata/deployments/*`. A token-renewer goroutine runs inside the
API process — no max-TTL root token required.

---

## Observability (VictoriaMetrics)

The API serves `GET /metrics` in Prometheus text format, including Go
runtime and process series, on a dedicated listener (`METRICS_ADDR`, default
`:9090`). The main listener answers `/metrics` with 404. The endpoint has no
authentication, so the metrics port must never be routed by an ingress: in the
hub chart it is the API Service's `metrics` port, which `templates/ingress.yaml`
does not reference. VictoriaMetrics scrapes it every 15 seconds when started
via `docker compose`.

### Starting the full dev stack

```pwsh
# From the repo root — starts Postgres, OpenBao, and VictoriaMetrics.
docker compose up -d
```

Then start the API as usual (`make run` from the `API/` directory).

### Querying metrics

Open the VictoriaMetrics UI at `http://localhost:8480` and run MetricsQL:

```
# Total requests by route
sum by (path) (http_requests_total)

# p99 latency per route
histogram_quantile(0.99, sum by (path, vmrange) (rate(http_request_duration_seconds_bucket[1m])))

# Deployment state breakdown
deployment_state_total
```

### Available metrics

| Metric | Type | Labels |
|--------|------|--------|
| `http_requests_total` | counter | `method`, `path`, `status` |
| `http_request_duration_seconds` | histogram | `method`, `path` |
| `deployment_state_total` | gauge | `state` |
| `sarif_gate_would_block_total` | counter | `source` |
| `audit_writes_total` | counter | none (rows committed to the audit log) |
| `audit_write_failures_total` | counter | none (audit writes that returned an error; the event is in `audit_dlq`) |
| `audit_submissions_total` | counter | `outcome` (`success`: Stage-1 submission stored; `failure`: rejected by a manifest gate) |
| `manifest_gate_rejections_total` | counter | `gate` (`sarif`, `dpia`, `ai_risk`, `chart`, `schema`; once per rejected submission per gate) |
| `audit_submission_state_total` | gauge | `state` (AppVersion state of each submission; `submitted` is the auditor queue) |
| `audit_decisions_total` | counter | `decision` (`approve`, `request_changes`, `reject`) |
| `audit_decision_latency_seconds` | histogram | none (submission creation to committed decision) |
| `orchestrator_calls_total` | counter | `operation` (`apply`, `status`, `delete`, `rollback`), `outcome` (`Status` answering "application not found" counts as `success`) |
| `orchestrator_call_duration_seconds` | histogram | `operation` |
| `deployment_transitions_total` | counter | `from`, `to` (deployment states; a new deployment comes `from="unknown"`) |
| `agent_heartbeat_age_seconds` | gauge | none (stalest heartbeat across clusters that are not decommissioned) |
| `agent_commands_pending` | gauge | none (agent commands neither acknowledged nor expired) |
| `agent_cert_renewals_total` | counter | `outcome` (`success`: a renewed agent certificate was issued; `failure`: an authenticated renew request was refused or the PKI failed) |
| `agent_cert_min_remaining_seconds` | gauge | none (shortest remaining validity of the active agent certificates across clusters that are not decommissioned; absent while no expiry is known; alert below 14 days, query with `min`) |
| `auth_attempts_total` | counter | `method` (`oidc` callback, `apikey`, `dev`), `outcome` |
| `auth_failures_total` | counter | `method` (also `none` for `no_credential`), `reason` (`invalid_signature`, `expired`, `revoked`, `unknown_key`, `no_credential`, `malformed`, `no_roles`) |
| `authz_denials_total` | counter | `path` (route pattern of a 403 from `RequireAnyRole`) |
| `apikey_active_total` | gauge | none (API keys neither revoked nor expired) |

A non-zero `auth_attempts_total{method="dev"}` outside a dev environment
means the dev-mode authenticator is wired where it must not be.

The gauges come from the reconciler's 30-second collector, and every API
replica reports the same cluster-wide value: aggregate them with `max` or
`max by (state)`, not `sum`.

---

## Authentication (OIDC)

### How the API picks an authenticator

| Condition | Authenticator | Effect |
|---|---|---|
| `APP_DEV_MODE=true` (default `.env`) | **DevModeAuthenticator** | Reads the `X-Dev-User-ID` header. No login flow. Good for local development. |
| `APP_DEV_MODE=false` + `AUTH_SESSION_SECRET` + `AUTH_BASE_URL` set | **OIDCAuthenticator** | OIDC authorization-code flow with PKCE. Per-org issuer. Session cookie. |

### OIDC env vars

| Env var | Required for OIDC | Default | Description |
|---|---|---|---|
| `AUTH_SESSION_SECRET` | Yes | — | HMAC-SHA256 key for session and state cookies. Must be ≥ 32 bytes. Generate with `openssl rand -base64 32`. The API refuses to start if this is set but shorter than 32 bytes. |
| `AUTH_BASE_URL` | Yes | — | Public base URL of this API, no trailing slash (e.g. `https://appstore.gov.nl`). Builds the OIDC callback URI sent to the IdP. |
| `AUTH_COOKIE_DOMAIN` | No | `""` | Restricts the session cookie to a domain. Leave empty for `localhost`. |
| `AUTH_SESSION_TTL` | No | `8h` | Session cookie lifetime. Accepts Go duration strings (e.g. `4h`, `24h`). |
| `AUTH_SECRET_ENCRYPTION_KEY` | No | `""` | Base64-encoded 32-byte AES-256 key for encrypting OIDC client secrets stored in the DB when Vault is not configured. Generate with `openssl rand -base64 32`. |

| `AUTH_CORS_ALLOWED_ORIGINS` | No (Yes in prod) | `""` (→ `*`) | Comma-separated list of allowed CORS origins (e.g. `https://appstore.gov.nl`). **Required when `APP_ENV=production`** — the API refuses to start without it. In dev/test defaults to `*`. |
| `AUTH_OIDC_PROVIDER_CACHE_TTL` | No | `10m` | How long a resolved OIDC provider config is cached per org. Accepts Go duration strings (e.g. `5m`, `1h`). If an org's IdP config changes, the new config takes effect after one TTL. |

### HTTP server env vars

These rarely need changing from defaults, but they are available:

| Env var | Default | Description |
|---|---|---|
| `HTTP_ADDR` | `:8080` | Listen address (host:port). |
| `HTTP_READ_TIMEOUT` | `10s` | Max time to read a full request. |
| `HTTP_WRITE_TIMEOUT` | `30s` | Max time to write a response. |
| `HTTP_IDLE_TIMEOUT` | `120s` | Keep-alive idle timeout. |
| `HTTP_SHUTDOWN_TIMEOUT` | `15s` | Graceful shutdown window after SIGINT/SIGTERM. |
| `HTTP_TRUSTED_PROXIES` | `""` | Comma-separated list of trusted reverse proxy IPs. Sets Echo's trusted proxy list for correct `X-Real-IP` resolution. |
| `METRICS_ADDR` | `:9090` | Listen address of the unauthenticated `GET /metrics` listener. Keep it off any ingress. |

### Configuring an org for OIDC

OIDC credentials are per-org, not global. After booting with OIDC env vars set:

1. Create the org: `POST /api/v1/orgs`
2. Configure the IdP connection: `POST /api/v1/orgs/:slug/oidc` with `issuer`, `client_id`, and `client_secret`.
3. The login flow starts at `GET /auth/login?org=<slug>`.

The client secret is stored using the best available scheme: Vault KV v2 if configured, AES-256-GCM in the DB (requires `AUTH_SECRET_ENCRYPTION_KEY`) otherwise, or `v0-inline:` in dev mode only.

### Dev OIDC gotchas

| Symptom | Cause + fix |
|---|---|
| `AUTH_SESSION_SECRET must be at least 32 bytes` on startup | Secret is too short. Generate a new one with `openssl rand -base64 32`. |
| `state cookie missing; restart the login flow` | The `/auth/callback` request came in without the state cookie. Usually means the cookie domain doesn't match, or you're testing across different origins. |
| `v0-inline: secrets are not permitted outside dev mode` | A dev-mode OIDC config was imported into a non-dev environment. Re-run `POST /orgs/:slug/oidc` on the target environment to re-encrypt the secret. |
| Client secret is stored as `encrypted:<b64>` | `AUTH_SECRET_ENCRYPTION_KEY` is set and Vault is not configured. This is the AES-256-GCM tier — safe for staging. For production, configure Vault so secrets are stored at `vault:<path>` instead. |

---

## Seeding a dev user

Authentication in V0 is the `X-Dev-User-ID` header — read by the
`DevModeAuthenticator` when `APP_DEV_MODE=true`. To exercise role-gated
endpoints you need at least one User with role assignments.

The fastest path is the `make dev-db-psql` shell:

```pwsh
make dev-db-psql
```

then paste:

```sql
-- 1. an Organization (the federated IdP isn't required for the dev user).
INSERT INTO organizations (id, slug, display_name, contact_email, created_at, updated_at)
VALUES (gen_random_uuid(), 'provincie-voorbeeld', 'Provincie Voorbeeld', 'contact@example.nl', now(), now())
ON CONFLICT (slug) DO NOTHING;

-- 2. a User scoped to that org.
INSERT INTO users (id, organization_id, external_subject, email, display_name, created_at)
SELECT gen_random_uuid(), id, 'dev-maya', 'maya@example.nl', 'Maya Developer', now()
FROM organizations WHERE slug = 'provincie-voorbeeld'
ON CONFLICT (organization_id, external_subject) DO NOTHING;

-- 3. all six roles for that user (easiest for local development;
--    in real life you'd grant only the roles you need).
WITH u AS (SELECT id FROM users WHERE email = 'maya@example.nl')
INSERT INTO role_assignments (id, user_id, role, granted_by_user_id, granted_at)
SELECT gen_random_uuid(), u.id, r, u.id, now()
FROM u, unnest(ARRAY[
  'developer','auditor','deployer','cluster_admin','org_admin','platform_admin'
]) AS r
ON CONFLICT DO NOTHING;

-- 4. print the user id so you can copy it into Bruno / curl.
SELECT id AS dev_user_id FROM users WHERE email = 'maya@example.nl';
```

Copy the printed UUID. That's the value for `X-Dev-User-ID` in every
authenticated request.

> The dev user above holds **every role** including PlatformAdmin. Do not
> ship anything resembling this seed to a staging or production
> environment.

---

## Smoke tests

With `make run` in another shell:

```pwsh
$dev = '<paste-the-uuid-from-step-4>'

# Anonymous — should always work
curl http://localhost:8080/healthz
curl http://localhost:8080/readyz
curl http://localhost:8080/api/v1/apps

# Authenticated — admin-only endpoint
curl -H "X-Dev-User-ID: $dev" http://localhost:8080/api/v1/audit-log
```

Or use the **Bruno collection** in `Bruno/saidkick-appstore/`. Open the
collection, select the **Local** environment, set the `devUserId`
variable to the UUID above, and every request will send the right
header.

---

## Common gotchas

| Symptom                                                                         | Cause + fix                                                                                                                                                         |
|---------------------------------------------------------------------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `ERROR: relation "X" does not exist` during migrate                             | A circular relationship between two models. Drop the Go-level struct field on one side (keep the `*ID uuid.UUID` column). See the comment in `app_version.go`.      |
| `ERROR: column "github_X" does not exist` during migrate                        | GORM's snake-caser split a non-initialism word inside CamelCase. Add an explicit `gorm:"column:..."` tag. See the `GitHub*` fields in `app.go` for the pattern.     |
| Migrate fails mid-way, leaves partial schema                                    | `make dev-db-down && make dev-db-run && make run` — fastest way to restart from a clean schema.                                                                     |
| `401 unauthorized` on a request that should work                                | Forgot the `X-Dev-User-ID` header, or `APP_DEV_MODE=false` in `.env`.                                                                                              |
| `403 forbidden` on an endpoint                                                  | The dev user is missing the required role. Re-run the seed SQL above to grant all six.                                                                              |
| `503 github_not_configured` on `POST /apps`                                     | No `GITHUB_APP_ID` / `_PRIVATE_KEY` / `_WEBHOOK_SECRET` in `.env`. Either configure the App, or stick to the read-side endpoints for dev.                           |
| `503 secrets_not_configured` on `POST /deployments`                             | `APP_DEV_MODE` is `false` and `VAULT_ADDR`/`VAULT_TOKEN` are not set. Either set `APP_DEV_MODE=true` for the in-memory fallback, or configure OpenBao per the section above. |
| `503 orchestrator_not_configured` on `POST /deployments`                        | No `ORCHESTRATOR_KIND` / `ARGOCD_*` in `.env`. Set `ORCHESTRATOR_KIND=agent`, configure the dev-only direct Argo CD client per the README, or stick to read-side endpoints. |
| `502 orchestrator_apply_failed` from `POST /deployments`                        | Argo CD rejected the Application spec. Most common: the `cluster_name` on our `Cluster` row doesn't match anything Argo CD knows about. Run `argocd cluster list` and verify the names match. |
| SSE `/events` closes immediately with `reason: no_orchestrator_ref`             | The deployment never got an Argo CD app name (Apply failed before assignment). Re-create the deployment after fixing the orchestrator config.                       |
| `429 rate_limited` from GitHub                                                  | Burned through the App's 5000/hour quota. Wait the documented time or use a different App.                                                                          |
| Hot-reload                                                                      | The Makefile doesn't ship one. Either restart manually or `go install github.com/cosmtrek/air@latest` and add a `.air.toml`.                                        |

---

## When the model changes

Schema migrations use [`gormigrate` v2](https://github.com/go-gormigrate/gormigrate). The `migrations` table tracks which changes have been applied; each migration runs exactly once.

### Adding a new table or column

1. Make the struct change in `internal/db/models/`.
2. Add a new entry to the `migrations` slice in `internal/db/migrate.go`:

```go
{
    ID: "20260601120000",   // YYYYMMDDHHMMSS — must be strictly increasing
    Migrate: func(tx *gorm.DB) error {
        return tx.AutoMigrate(&models.YourNewModel{})
    },
    Rollback: func(tx *gorm.DB) error {
        return tx.Migrator().DropTable(&models.YourNewModel{})
    },
},
```

3. Run `make migrate` (or `make run`) — the migration runs once and is recorded.

### Never modify or delete an existing migration

Existing entries in the `migrations` slice are history. Changing them won't re-run them; it will just make the recorded ID inconsistent with the code. If you need to undo a migration, add a new one that reverses it.

### Dev DB reset

If your local schema is in a broken state:

```pwsh
make dev-db-down
make dev-db-run
make run    # runs InitSchema from scratch on the empty DB
```

---

## Encryption at rest (TDE)

**This is an operational invariant, not a code switch.** The platform does
not use `pgcrypto` column-level encryption. Encryption at rest is delegated
to the managed PostgreSQL provider via Transparent Data Encryption (TDE).

### Production requirement

All production deployments of sAId-Kick **must** run on a PostgreSQL host
with TDE enabled at the disk layer before any personal or audit data is
written. Accepted providers and the setting to verify:

| Provider | How to verify TDE is active |
|---|---|
| Azure Database for PostgreSQL Flexible Server | `Encryption` blade → *Customer-managed key* or *Platform-managed key* (both enable ADE at rest) |
| AWS RDS for PostgreSQL | `Storage encrypted: Yes` in the RDS console; KMS key ARN visible under `Configuration` |
| Google Cloud SQL | `Data encryption` → *Google-managed* or *Customer-managed (CMEK)* |
| Self-hosted (bare-metal / VM) | OS-level full-disk encryption (LUKS on Linux) confirmed by ops, documented in the deployment run-book |

A fresh PostgreSQL server on any of these providers has TDE enabled by
default. **Never disable it.** If a compliance audit asks for evidence,
export the provider's "Security posture" report and attach it to the audit
package.

### Why TDE and not pgcrypto

`pgcrypto` column-level encryption was evaluated and rejected for three
reasons:

1. **Key management complexity** — pgcrypto pushes the encryption key into
   the application layer. Managing rotation, split-key, and HSM integration
   requires significant custom infrastructure.
2. **Query restrictions** — encrypted columns can't be indexed or sorted;
   this would prevent the hash-chain query in `AuditLog.BeforeCreate`.
3. **TDE is equivalent for our threat model** — the primary risk is data
   exfiltration via stolen disk/snapshot. TDE covers this. The secondary
   risk (compromised DB connection) is mitigated by the role-separation
   (`app_rw` / `audit_writer`) and network-level controls, not by
   column-level encryption.

### Dev environment

The local Docker Postgres container (`make dev-db`) does **not** use TDE.
This is acceptable: the dev container holds no real personal or audit data.
Do not run the container in environments where real data lands — use the
managed provider path.

---

## Building container images

Two images ship in this repo. Both use a two-stage build (Go builder → `gcr.io/distroless/static-debian12`) and run as UID 65534 (nobody).

### API image

```bash
# From the API/ directory
docker build -t ghcr.io/clappformorg/saidkick-api:latest -f Dockerfile .

# With a version tag
docker build \
  -t ghcr.io/clappformorg/saidkick-api:0.1.0 \
  -t ghcr.io/clappformorg/saidkick-api:latest \
  -f Dockerfile .
```

### Agent image

```bash
docker build -t ghcr.io/clappformorg/saidkick-agent:latest -f Dockerfile.agent .
```

### Multi-arch

```bash
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --push \
  -t ghcr.io/clappformorg/saidkick-api:0.1.0 \
  -f Dockerfile .
```

### Quick smoke-test

```bash
# Start only Postgres (no OpenBao needed for basic smoke-test)
docker compose up -d postgres

docker run --rm \
  --network host \
  -e APP_DEV_MODE=true \
  -e DATABASE_URL="postgres://postgres:postgres@localhost:5432/saidkick_appstore?sslmode=disable" \
  ghcr.io/clappformorg/saidkick-api:latest -migrate-only
```

---

## Kubernetes / Helm deployment

This section covers deploying the hub onto a real Kubernetes cluster using
the Helm chart at `charts/saidkick-hub/`. For local development see the
sections above.

### Prerequisites

| Tool | Version | Why |
|------|---------|-----|
| Helm | ≥ 3.12 | deploys the chart |
| kubectl | any recent | context pointing at the hub cluster |
| cert-manager | ≥ 1.14 (optional) | only needed for `ingress.tls.enabled=true` with Let's Encrypt |

The Zalando Postgres Operator must be installed **cluster-wide** before
the first `helm install`. This is a one-time operation per cluster:

```bash
helm repo add postgres-operator-charts \
  https://opensource.zalando.com/postgres-operator/charts/postgres-operator
helm repo update
helm install postgres-operator postgres-operator-charts/postgres-operator \
  --namespace postgres-operator --create-namespace
```

---

### Add Helm repos and fetch subcharts

```bash
helm repo add openbao https://openbao.github.io/openbao-helm
helm repo add victoriametrics https://victoriametrics.github.io/helm-charts/
helm repo add argo https://argoproj.github.io/argo-helm
helm repo update

helm dependency update charts/saidkick-hub
```

---

### Staging / quick-start install

Generate the two required secrets, then install with all subcharts enabled:

```bash
export SESSION_SECRET=$(openssl rand -base64 32)
export JOIN_SECRET=$(openssl rand -base64 32)
export ENCRYPTION_KEY=$(openssl rand -base64 32)

helm upgrade --install saidkick-hub ./charts/saidkick-hub \
  --namespace saidkick-system --create-namespace \
  --set api.config.authBaseUrl=http://localhost:8080 \
  --set api.secrets.authSessionSecret="$SESSION_SECRET" \
  --set api.secrets.authSecretEncryptionKey="$ENCRYPTION_KEY" \
  --set api.secrets.clusterJoinSecret="$JOIN_SECRET" \
  --set api.secrets.databaseUrl="postgres://saidkick:PLACEHOLDER@saidkick-hub-postgresql:5432/saidkick_appstore?sslmode=disable" \
  --wait --timeout 8m
```

> `databaseUrl` uses a placeholder password on first install. After Helm
> completes, retrieve the real password and upgrade (see below).

---

### Post-install: retrieve the database password

The Zalando operator generates a credential Secret for the app user:

```bash
kubectl get secret \
  saidkick.saidkick-hub-postgresql.credentials.postgresql.acid.zalan.do \
  -n saidkick-system \
  -o jsonpath='{.data.password}' | base64 -d
```

Plug the printed password into the connection string and upgrade:

```bash
helm upgrade saidkick-hub ./charts/saidkick-hub \
  --namespace saidkick-system --reuse-values \
  --set api.secrets.databaseUrl="postgres://saidkick:<password>@saidkick-hub-postgresql:5432/saidkick_appstore?sslmode=disable"
```

The post-install Job (`saidkick-hub-postgresql-init`) runs automatically on
every upgrade and applies the extension and role SQL. Inspect it:

```bash
kubectl get job saidkick-hub-postgresql-init -n saidkick-system
kubectl logs job/saidkick-hub-postgresql-init -n saidkick-system -c init
```

---

### Post-install: initialise OpenBao

The OpenBao subchart boots sealed. Port-forward and initialise once:

```bash
kubectl port-forward svc/saidkick-hub-openbao 8200:8200 -n saidkick-system
```

In a second terminal:

```bash
export BAO_ADDR=http://localhost:8200

# Initialise (save the unseal keys and root token securely)
bao operator init

# Unseal (repeat with 3 of the 5 printed unseal keys)
bao operator unseal

# Enable mounts and create the API policy
bao secrets enable -path=kv kv-v2
bao secrets enable pki
bao secrets tune -max-lease-ttl=87600h pki
bao write pki/root/generate/internal \
  common_name="sAId-Kick Hub CA" ttl=87600h
# Agent client certificates. The handshake signs CN=saidkick-agent-<cluster-uuid>,
# so the role allows that glob; the hub's common_name overrides whatever the
# CSR asks for, and the certificate is valid for client auth only.
bao write pki/roles/agent \
  allowed_domains="saidkick-agent-*" allow_glob_domains=true \
  use_csr_common_name=false use_csr_sans=false \
  server_flag=false client_flag=true max_ttl=2160h

bao policy write saidkick-api - <<'EOF'
path "kv/data/deployments/*"     { capabilities = ["create","update","read","delete"] }
path "kv/metadata/deployments/*" { capabilities = ["list","delete"] }
path "pki/sign/agent"            { capabilities = ["update"] }
EOF

# Create a service token (renewable, 30-day period)
bao token create -policy=saidkick-api -period=720h
```

Inject the token and upgrade:

```bash
helm upgrade saidkick-hub ./charts/saidkick-hub \
  --namespace saidkick-system --reuse-values \
  --set api.secrets.vaultAddr=http://saidkick-hub-openbao:8200 \
  --set api.secrets.vaultToken=<printed-token>
```

---

### Seed a platform admin user on the deployed cluster

Port-forward to the Postgres master and use the same seed SQL as local dev:

```bash
kubectl port-forward \
  svc/saidkick-hub-postgresql 5432:5432 -n saidkick-system

# In a second terminal — password from the Zalando secret retrieved above
psql "postgres://saidkick:<password>@localhost:5432/saidkick_appstore"
```

Then run the same seed SQL from the [Seeding a dev user](#seeding-a-dev-user)
section. Copy the returned UUID as the `X-Dev-User-ID` value or as the
first actor for the OIDC setup.

`cmd/seed` is for development and test databases only. It exits non-zero
with `refusing to seed a production-like environment (APP_ENV=<value>)`
unless `APP_ENV` is `dev`, `development`, `local`, `test` or `ci`, the same
rule that guards `APP_DEV_MODE`.

---

### Remove demo data from a shared environment

`cmd/cleanup-demo` deletes the demo apps and clusters listed in
`scripts/demo-names.txt` (one `app:<slug>` or `cluster:<name>` per line).
Run it once in each shared environment, with the same port-forward as above.

```bash
cd API
export DATABASE_URL="postgres://saidkick:<password>@localhost:5432/saidkick_appstore"

# 1. Dry run: prints every row it would delete and every name it did not find.
make cleanup-demo

# 2. Review the output, then delete.
make cleanup-demo APPLY=1
```

Use `DEMO_NAMES=<file>` for a different list. What `APPLY=1` does:

- **Apps** are soft-deleted. Their versions and audit submissions stay in
  the database but drop out of every list with the app. Deployment rows
  already in state `deleted` are soft-deleted with the app. An app with any
  other live deployment is skipped: delete those deployments through the
  API first so the orchestrator and secret store are cleaned up, then rerun.
- **Clusters** are deleted the way `DELETE /api/v1/clusters/{id}` deletes
  them (clusters have no soft delete). Every cluster with a listed name
  matches. A cluster that any deployment row still references is skipped.
- Each deleted row gets one `demo_data.deleted` audit event with no actor.

The command is safe to rerun; rows already removed show as `not found`.

---

### Production install

1. Copy `charts/saidkick-hub/values-production.yaml`, fill every `<REPLACE>`
   placeholder, and store the file in a secrets manager (SOPS, Sealed
   Secrets, or Azure Key Vault + ESO). Never commit it.

2. The `databaseUrl` value is built from the Zalando-generated secret — see
   the inline comment in `values-production.yaml` for the exact `kubectl`
   command.

```bash
helm dependency update charts/saidkick-hub

helm upgrade --install saidkick-hub ./charts/saidkick-hub \
  --namespace saidkick-system --create-namespace \
  --values charts/saidkick-hub/values-production.yaml \
  --wait --timeout 10m
```

Production differences vs staging:
- `postgresql.instances: 2` — Zalando HA with streaming replication
- `postgresql.persistence.size: 50Gi`
- `api.replicaCount: 3` with HPA enabled
- `openbao.enabled: false` — point to an external HA OpenBao / Vault
- `argocd.enabled: false` — point to an existing Argo CD installation

---

### Production `audit_logs` range partitioning

PostgreSQL range partitioning requires the partition key (`created_at`) to be
part of every unique index, including the primary key. GORM's `IDModel` uses
a plain UUIDv7 primary key without `created_at`, so `AutoMigrate` cannot
create a partitioned `audit_logs` table automatically.

**Run this DDL once, before the first `make run` / `helm upgrade`, on a fresh
production database.** If the table already exists as a plain table (e.g.
after a dev migration), drop it first (`DROP TABLE audit_logs CASCADE`) — the
hash-chain genesis row is re-seeded on next boot.

```sql
-- 1. Create the partitioned parent table (no GORM — AutoMigrate skips this
--    on subsequent runs because the table already exists).
CREATE TABLE IF NOT EXISTS audit_logs (
    id            uuid        NOT NULL,
    previous_hash text        NOT NULL DEFAULT '',
    event_hash    text        NOT NULL,
    event_type    text        NOT NULL,
    actor_user_id uuid,
    actor_org_id  uuid,
    subject_type  text        NOT NULL DEFAULT '',
    subject_id    uuid,
    payload       jsonb       NOT NULL DEFAULT '{}',
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (id, created_at)   -- partition key must be in PK
) PARTITION BY RANGE (created_at);

-- 2. Create one partition per month for the current and next calendar year.
--    Re-run this script each January to add the coming year's partitions.
DO $$
DECLARE
    y   int;
    m   int;
    s   date;
    e   date;
    tbl text;
BEGIN
    FOR y IN extract(year FROM now())::int .. extract(year FROM now())::int + 1 LOOP
        FOR m IN 1..12 LOOP
            s   := make_date(y, m, 1);
            e   := s + INTERVAL '1 month';
            tbl := format('audit_logs_%s_%s', y, lpad(m::text, 2, '0'));
            EXECUTE format(
                'CREATE TABLE IF NOT EXISTS %I
                 PARTITION OF audit_logs
                 FOR VALUES FROM (%L) TO (%L)',
                tbl, s, e
            );
        END LOOP;
    END LOOP;
END$$;

-- 3. Replicate the indexes GORM would normally create.
CREATE UNIQUE INDEX IF NOT EXISTS audit_logs_pkey_idx
    ON audit_logs (id, created_at);
CREATE INDEX IF NOT EXISTS idx_audit_logs_actor_user
    ON audit_logs (actor_user_id) WHERE actor_user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_audit_logs_subject
    ON audit_logs (subject_type, subject_id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_created_at
    ON audit_logs (created_at DESC);

-- 4. Prevent UPDATE / DELETE (hash-chain integrity trigger).
--    The trigger body is identical to the one in db/migrate.go's addExtraConstraints.
CREATE OR REPLACE FUNCTION audit_log_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_logs rows are immutable';
END$$;

CREATE OR REPLACE TRIGGER audit_logs_no_update
    BEFORE UPDATE OR DELETE ON audit_logs
    FOR EACH ROW EXECUTE FUNCTION audit_log_immutable();
```

**Annual partition maintenance** — add next year's partitions each January:

```sql
-- Run in January of the new year (replace 2027 with the target year).
DO $$
DECLARE m int; s date; e date; tbl text;
BEGIN
    FOR m IN 1..12 LOOP
        s   := make_date(2027, m, 1);
        e   := s + INTERVAL '1 month';
        tbl := format('audit_logs_2027_%s', lpad(m::text, 2, '0'));
        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS %I PARTITION OF audit_logs FOR VALUES FROM (%L) TO (%L)',
            tbl, s, e
        );
    END LOOP;
END$$;
```

Add a reminder to the ops calendar — a missing partition causes `INSERT` to
fail with `no partition of relation "audit_logs" found for row`.

---

### Upgrading an existing release

```bash
helm dependency update charts/saidkick-hub

helm upgrade saidkick-hub ./charts/saidkick-hub \
  --namespace saidkick-system \
  --values charts/saidkick-hub/values-production.yaml \
  --wait --timeout 10m
```

The post-install/upgrade Job re-runs the init SQL on every upgrade — this
is safe because all SQL statements are idempotent.

---

### Publishing the Helm charts

Releases are cut by hand. Stamp both charts with the same version, package
them and push them to `oci://ghcr.io/<lower-cased repository owner>/charts`:

```bash
# A token with write:packages on the repository owner.
helm registry login ghcr.io -u <github-user>

# The hub chart has subcharts; fetch them before packaging.
helm dependency build charts/saidkick-hub

helm package charts/saidkick-hub  --version <version> --app-version <version>
helm package charts/saidkick-agent --version <version> --app-version <version>

helm push saidkick-hub-<version>.tgz   oci://ghcr.io/clappformorg/charts
helm push saidkick-agent-<version>.tgz oci://ghcr.io/clappformorg/charts
```

Keep the two versions identical: the hub serves its own `appVersion` as
`AGENT_CHART_VERSION`, so a mismatch makes it print an agent chart reference
that was never published. That lands as:

```
oci://ghcr.io/clappformorg/charts/saidkick-hub:<version>
oci://ghcr.io/clappformorg/charts/saidkick-agent:<version>
```

GHCR creates a new package as private, and spokes pull the agent chart
without credentials. After the first release push, make both packages
public once:

1. Open the repository owner's packages on GitHub (Your profile or the
   organisation, then **Packages**), and open `charts/saidkick-agent`.
2. **Package settings** > **Danger Zone** > **Change visibility** > **Public**.
3. Repeat for `charts/saidkick-hub`.

Check from a machine with no GHCR login:

```bash
helm show chart oci://ghcr.io/clappformorg/charts/saidkick-agent --version <version>
```

The hub prints `helm install saidkick-agent <api.config.agentChartRepo>/saidkick-agent --version <hub appVersion>`.
`api.config.agentChartRepo` defaults to `oci://ghcr.io/clappformorg/charts`.
When the repository moves to another owner, push to the new owner's namespace,
set `api.config.agentChartRepo` to `oci://ghcr.io/<new owner>/charts` on the
hub, and make the new packages public.

### Review auditors granted by non-platform-admins

Only a `platform_admin` may grant or revoke `auditor` (#135). Assignments
made earlier by an `org_admin` are kept. Before deploying that change, run
this read-only report against production (connect as in
[Seed a platform admin user](#seed-a-platform-admin-user-on-the-deployed-cluster))
and have the platform admin review every row. It lists active auditors whose
grantor does not hold `platform_admin`:

```sql
SELECT ra.user_id, u.email, ra.granted_by_user_id, g.email AS granted_by_email, ra.granted_at
FROM role_assignments ra
JOIN users u ON u.id = ra.user_id
JOIN users g ON g.id = ra.granted_by_user_id
WHERE ra.role = 'auditor' AND ra.revoked_at IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM role_assignments p
    WHERE p.user_id = ra.granted_by_user_id AND p.role = 'platform_admin' AND p.revoked_at IS NULL
  );
```

To revoke an auditor the review rejects, a platform admin saves the member's
roles without `auditor` in the webapp's member editor.

---

### Register a spoke cluster

After the hub is healthy:

```bash
# Use the UUID from the seeded platform admin user
curl -s -X POST https://<hub-host>/api/v1/clusters \
  -H "Content-Type: application/json" \
  -H "X-Dev-User-ID: <platform-admin-uuid>" \
  -d '{"name":"Example AKS Prod","scope":"org","owning_org_id":"<org-uuid>"}' | jq .
```

The response returns a `joinToken` (JWT, 1 h TTL) and `clusterID`. Use these to install the agent chart on the spoke cluster.

---

### Install the agent on a spoke cluster

> **Context**: kubectl must be pointed at the spoke cluster for all commands below.

```bash
# 1. External Secrets Operator, a spoke prerequisite (installer.steps[0] in
#    the POST /clusters response; skip if the spoke already runs ESO that
#    serves external-secrets.io/v1)
helm install external-secrets external-secrets --repo https://charts.external-secrets.io \
  --version 0.20.4 \
  --namespace external-secrets --create-namespace \
  --wait

# 2. Agent. installer.steps[1] (= installer.helm_command) installs the
#    published chart: helm install saidkick-agent \
#      oci://ghcr.io/clappformorg/charts/saidkick-agent --version <hub-version> ...
#    The steps below install from a checkout instead.
#    Add repos and fetch agent subcharts
helm repo add argo https://argoproj.github.io/argo-helm
helm repo update
helm dependency update charts/saidkick-agent

# 3. Install (values from the POST /clusters response above)
helm upgrade --install saidkick-agent ./charts/saidkick-agent \
  --namespace saidkick-system --create-namespace \
  --set agent.hubUrl=https://<hub-host> \
  --set agent.clusterID=<uuid-from-POST-response> \
  --set agent.joinToken=<jwt-from-POST-response> \
  --set agent.argocd.serverUrl=https://argocd-server.argocd.svc.cluster.local \
  --set agent.argocd.authToken=<argocd-bearer-token> \
  --set vault.addr=https://<vault-host> \
  --set vault.token=<vault-read-token> \
  --wait --timeout 5m
```

#### Populate the mTLS cert (post-handshake)

After install the agent starts without a cert. The operator must call the hub handshake endpoint and write the returned PEM material into the agent's credential Secret:

```bash
# 1. Call the handshake endpoint (replace UUIDs and secrets):
JOIN_RESPONSE=$(curl -s -X POST https://<hub-host>/api/v1/clusters/<uuid>/handshake \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <api-key-or-dev-header>" \
  -d '{"join_token":"<jwt>"}')

CLIENT_CERT=$(echo "$JOIN_RESPONSE" | jq -r '.client_cert_pem')
CLIENT_KEY=$(echo  "$JOIN_RESPONSE" | jq -r '.client_key_pem')
CA_CERT=$(echo     "$JOIN_RESPONSE" | jq -r '.ca_pem')

# 2. Write cert material into the agent Secret on the spoke cluster
#    (kubectl context must be the spoke)
RELEASE=saidkick-agent   # your helm release name
NS=saidkick-system

kubectl patch secret ${RELEASE}-saidkick-agent-credentials \
  -n $NS --type=merge \
  -p "{\"stringData\":{\"tls.crt\":$(echo "$CLIENT_CERT" | jq -Rs .),\"tls.key\":$(echo "$CLIENT_KEY" | jq -Rs .),\"ca.crt\":$(echo "$CA_CERT" | jq -Rs .)}}"

# 3. Restart the agent pod to pick up the new cert
kubectl rollout restart deployment/${RELEASE}-saidkick-agent -n $NS
```

The hub marks the cluster `ready` once the agent's next heartbeat succeeds over mTLS.

#### Dev-mode agent (no mTLS, local testing)

```bash
helm upgrade --install saidkick-agent ./charts/saidkick-agent \
  --namespace saidkick-system --create-namespace \
  --set agent.hubUrl=http://host.docker.internal:8080 \
  --set agent.clusterID=<uuid> \
  --set agent.joinToken=placeholder \
  --set agent.argocd.serverUrl=http://argocd-server.argocd.svc.cluster.local \
  --set agent.argocd.authToken=<token> \
  --set agent.devMode=true \
  --set vault.addr=http://openbao:8200 \
  --set vault.token=dev-token
```

Install ESO first (step 1 above), or add `--set secretStore.enabled=false` on a cluster without it.

In dev mode the agent sends `X-Agent-Cluster-ID: <clusterID>` instead of a cert. The hub must be started with `APP_DEV_MODE=true`; only then does it serve the agent routes on its main listener.

---

### Agent environment variables

| Variable | Required | Default | Notes |
|---|---|---|---|
| `SAIDKICK_HUB_URL` | Yes | — | HTTPS base URL of the hub (handshake) |
| `SAIDKICK_HUB_AGENT_URL` | Production | `SAIDKICK_HUB_URL` | Hub agent mTLS listener (heartbeat, states, acks). Only a dev-mode hub serves agent routes on its main URL |
| `SAIDKICK_CLUSTER_ID` | Yes | — | UUID from `POST /clusters` |
| `ARGOCD_SERVER_URL` | Yes | — | In-cluster Argo CD URL |
| `ARGOCD_AUTH_TOKEN` | Yes | — | Argo CD bearer token |
| `ARGOCD_NAMESPACE` | No | `argocd` | Namespace for Application CRs |
| `SAIDKICK_AGENT_CERT` | No | — | Path to mTLS client cert PEM |
| `SAIDKICK_AGENT_KEY` | No | — | Path to mTLS client key PEM |
| `SAIDKICK_HUB_CA` | No | — | CA PEM for the hub agent listener's serving cert (the chart mounts `ca.crt` from the handshake) |
| `SAIDKICK_DEV_MODE` | No | `false` | `true` = skip mTLS, use header |
| `SAIDKICK_HEARTBEAT_INTERVAL` | No | `30s` | Hub heartbeat cadence |
| `SAIDKICK_STATUS_INTERVAL` | No | `15s` | Argo CD status poll cadence |
| `SAIDKICK_AGENT_VERSION` | No | chart appVersion | Reported in heartbeats |

---

### Helm deployment gotchas

| Symptom | Cause + fix |
|---------|-------------|
| `unable to create new content in namespace saidkick-system` | Namespace doesn't exist; add `--create-namespace`. |
| Init job stuck in `CreateContainerConfigError` | Zalando hasn't created the superuser Secret yet; the job retries automatically (backoffLimit: 10). Wait 1–2 min and check again. |
| Init job fails with `psql: error: connection refused` | Zalando cluster not yet ready. Check `kubectl get postgresql -n saidkick-system` for `Running` state. |
| Init job fails with `FATAL: password authentication failed` | Secret exists but Zalando rotated the password. Restart the job: `kubectl delete job saidkick-hub-postgresql-init -n saidkick-system && helm upgrade ... --reuse-values`. |
| `context deadline exceeded` during `--wait` | A subchart pod is not becoming ready. Check `kubectl get pods -n saidkick-system` for `CrashLoopBackOff` or pending PVCs. |
| `saidkick-hub-openbao` pod sealed after node restart | OpenBao requires manual unseal after every pod restart unless auto-unseal (Azure Key Vault, AWS KMS) is configured. |
| API pod `readyz` returns 503 | DB not reachable — wrong password in `databaseUrl` or Zalando cluster not yet accepting connections. |
| Agent pod crash-loops with `hub heartbeat: auth rejected (401)` | mTLS cert not yet populated, or its serial matches neither the cluster's `agent_cert_serial` nor `agent_cert_pending_serial` (a newer handshake rotated it, or the agent restarted on a cert that a renewal already replaced). Run the handshake patch above, or set `agent.devMode=true` for local testing. |
| `agent_cert_min_remaining_seconds` falls towards 0 | An agent is not renewing. Check its logs for `certificate renewal failed` and `agent_cert_renewals_total{outcome="failure"}` on both sides; a hub-side `pki_unavailable` points at Vault PKI. Renewal only works while the current cert is valid: once it expires, the spoke needs a new join token. |
| Agent heartbeat fails with a TLS handshake error or `404` | `agent.hubAgentUrl` is unset or points at the main API instead of the hub's `<release>-api-agent` Service, or a TLS-terminating proxy sits in front of it. The serving cert must chain to the Vault PKI CA in the agent's `ca.crt`. |
| Agent pod crash-loops with `argo apply: argocd request: ...` | `ARGOCD_SERVER_URL` or `ARGOCD_AUTH_TOKEN` wrong. Check Argo CD is reachable from the agent namespace. |
| Hub cluster stays `unhealthy` after agent restarts | Heartbeat didn't reach the hub. Confirm `SAIDKICK_HUB_URL` is correct and the hub's TLS cert is trusted (set `SAIDKICK_HUB_CA` if self-signed). |
| `helm install` fails with `agent.argocd.serverUrl is required` | Must pass `--set agent.argocd.serverUrl=...` — there is no default. |

---

## Ingress & ExternalDNS setup

After the agent is registered and healthy, enable ingress-nginx and ExternalDNS
so deployed applications automatically get a public URL and DNS record.

The `saidkick-agent` chart ships ingress-nginx, ExternalDNS, and cert-manager as
optional subcharts — all disabled by default. Enable them with `helm upgrade`.

**Prerequisites for all clouds:**
1. A DNS zone you control (e.g. `apps.gemeente.nl`) — point its NS records at
   your cloud's DNS service.
2. Set `base_domain` and `ingress_class_name` on the cluster in the sAId-Kick
   platform UI so the deploy form pre-fills them automatically.

---

### AWS — Route 53 + IRSA

```bash
# 1. Create an IAM policy with Route 53 permissions (full JSON in the
#    ExternalDNS docs: https://github.com/kubernetes-sigs/external-dns/blob/master/docs/tutorials/aws.md)
aws iam create-policy --policy-name ExternalDNS --policy-document file://externaldns-policy.json

# 2. Create an IRSA role and associate it with the ExternalDNS ServiceAccount
eksctl create iamserviceaccount \
  --cluster <cluster-name> \
  --namespace saidkick-system \
  --name external-dns \
  --attach-policy-arn arn:aws:iam::<ACCOUNT>:policy/ExternalDNS \
  --approve

# 3. Upgrade the agent chart
helm upgrade saidkick-agent oci://ghcr.io/clappformorg/charts/saidkick-agent \
  --namespace saidkick-system \
  --set ingress-nginx.enabled=true \
  --set external-dns.enabled=true \
  --set external-dns.provider=aws \
  --set "external-dns.domainFilters[0]=apps.gemeente.nl" \
  --set "external-dns.txtOwnerId=<cluster-slug>" \
  --set "external-dns.serviceAccount.annotations.eks\.amazonaws\.com/role-arn=arn:aws:iam::<ACCOUNT>:role/ExternalDNS"

# Platform cluster settings:
#   ingress_class_name = nginx
#   base_domain        = apps.gemeente.nl
```

---

### Azure — Azure DNS + Workload Identity

Requires `--enable-oidc-issuer` and `--enable-workload-identity` on the AKS
cluster (already in the recommended `az aks create` command).

```bash
# 1. Create a managed identity and grant DNS Contributor on the DNS zone
az identity create --name externaldns --resource-group rg-example
MI_CLIENT_ID=$(az identity show --name externaldns -g rg-example --query clientId -o tsv)
DNS_ZONE_ID=$(az network dns zone show -g rg-example -n apps.gemeente.nl --query id -o tsv)
az role assignment create --role "DNS Zone Contributor" \
  --assignee $MI_CLIENT_ID --scope $DNS_ZONE_ID

# 2. Federate the identity with the ExternalDNS Kubernetes ServiceAccount
AKS_OIDC=$(az aks show -g rg-example -n aks-example --query oidcIssuerProfile.issuerUrl -o tsv)
az identity federated-credential create \
  --name externaldns-federated \
  --identity-name externaldns \
  --resource-group rg-example \
  --issuer $AKS_OIDC \
  --subject system:serviceaccount:saidkick-system:external-dns

# 3. Create the azure.json config secret
kubectl create secret generic azure-config-file \
  --namespace saidkick-system \
  --from-literal=azure.json='{
    "subscriptionId": "<subscription-id>",
    "resourceGroup": "rg-example",
    "tenantId": "<tenant-id>",
    "useManagedIdentityExtension": true,
    "userAssignedIdentityID": "'$MI_CLIENT_ID'"
  }'

# 4. Upgrade the agent chart
helm upgrade saidkick-agent oci://ghcr.io/clappformorg/charts/saidkick-agent \
  --namespace saidkick-system \
  --set ingress-nginx.enabled=true \
  --set external-dns.enabled=true \
  --set external-dns.provider=azure \
  --set "external-dns.domainFilters[0]=apps.gemeente.nl" \
  --set "external-dns.txtOwnerId=<cluster-slug>" \
  --set "external-dns.serviceAccount.annotations.azure\.workload\.identity/client-id=$MI_CLIENT_ID" \
  --set "external-dns.podLabels.azure\.workload\.identity/use=true" \
  --set "external-dns.extraVolumes[0].name=azure-config" \
  --set "external-dns.extraVolumes[0].secret.secretName=azure-config-file" \
  --set "external-dns.extraVolumeMounts[0].name=azure-config" \
  --set "external-dns.extraVolumeMounts[0].mountPath=/etc/kubernetes" \
  --set "external-dns.extraVolumeMounts[0].readOnly=true"

# Platform cluster settings:
#   ingress_class_name = nginx
#   base_domain        = apps.gemeente.nl
```

---

### Scaleway — Scaleway DNS + API token

```bash
# 1. Create a Scaleway API key with DNS read/write permissions
#    (Scaleway console → IAM → API Keys → Create)

# 2. Upgrade the agent chart — credentials are passed inline via values
#    (use a values file or Sealed Secret in production, not --set)
helm upgrade saidkick-agent oci://ghcr.io/clappformorg/charts/saidkick-agent \
  --namespace saidkick-system \
  --set ingress-nginx.enabled=true \
  --set external-dns.enabled=true \
  --set external-dns.provider=scaleway \
  --set "external-dns.domainFilters[0]=apps.gemeente.nl" \
  --set "external-dns.txtOwnerId=<cluster-slug>" \
  --set "externalDnsCredentials.SCW_ACCESS_KEY=<access-key>" \
  --set "externalDnsCredentials.SCW_SECRET_KEY=<secret-key>" \
  --set "externalDnsCredentials.SCW_DEFAULT_ORGANIZATION_ID=<org-id>" \
  --set "external-dns.envFrom[0].secretRef.name=externaldns-credentials"

# Platform cluster settings:
#   ingress_class_name = nginx
#   base_domain        = apps.gemeente.nl
```

---

### Cloudflare — cloud-agnostic DNS (any cluster)

Works with any Kubernetes cluster regardless of cloud.

```bash
# 1. Create a Cloudflare API token with Zone:DNS:Edit permission

helm upgrade saidkick-agent oci://ghcr.io/clappformorg/charts/saidkick-agent \
  --namespace saidkick-system \
  --set ingress-nginx.enabled=true \
  --set external-dns.enabled=true \
  --set external-dns.provider=cloudflare \
  --set "external-dns.domainFilters[0]=apps.gemeente.nl" \
  --set "external-dns.txtOwnerId=<cluster-slug>" \
  --set "externalDnsCredentials.CF_API_TOKEN=<api-token>" \
  --set "external-dns.envFrom[0].secretRef.name=externaldns-credentials"

# Platform cluster settings:
#   ingress_class_name = nginx
#   base_domain        = apps.gemeente.nl
```

---

### TLS with cert-manager (optional, any cloud)

```bash
# Enable cert-manager alongside ingress-nginx
helm upgrade saidkick-agent oci://ghcr.io/clappformorg/charts/saidkick-agent \
  --namespace saidkick-system \
  --set cert-manager.enabled=true \
  # ... other flags ...

# After install, create a Let's Encrypt ClusterIssuer:
kubectl apply -f - <<EOF
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-prod
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: platform@gemeente.nl
    privateKeySecretRef:
      name: letsencrypt-prod
    solvers:
      - http01:
          ingress:
            ingressClassName: nginx
EOF
```

App Helm charts that set `ingress.tls: true` will automatically get certificates
via the `cert-manager.io/cluster-issuer: letsencrypt-prod` annotation.

---

## Where to look next

| Goal                                | Start here                                                              |
|-------------------------------------|-------------------------------------------------------------------------|
| Browse endpoint status              | [`README.md`](README.md)                                                |
| Configure OpenBao                   | "Secret store / Vault" section above + "Secrets" in [`README.md`](README.md) |
| Add a new HTTP handler              | `internal/handlers/<domain>.go`, then register it in `handlers.go`      |
| Add a new model                     | `internal/db/models/<entity>.go`, then add a migration entry to `migrate.go` (see "When the model changes" above) |
| Add a new external integration      | `internal/integrations/<name>/`, with `Client` interface + `NullClient` |
| Add a new env var                   | Read it in `internal/config/config.go::Load()`                          |
