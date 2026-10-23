# saidkick-hub Helm Chart

Deploys the sAId-Kick Appstore **hub** onto the central orchestration cluster
(for example AKS). The hub runs the API, the Webapp, and optional
infrastructure subcharts.

---

## Architecture

```
┌─────────────────────────────────────────────────┐
│  Hub cluster (saidkick-system namespace)         │
│                                                  │
│  ┌──────────────────────┐  ┌──────────┐          │
│  │  API :8080           │  │  Webapp  │          │
│  │  agent mTLS :8443    │  │ :3000    │          │
│  └────┬─────────────────┘  └──────────┘          │
│       │                                          │
│  ┌────▼────┐  ┌──────────┐  ┌────────────────┐  │
│  │Postgres │  │ OpenBao  │  │ VictoriaMetrics│  │
│  │ :5432   │  │  :8200   │  │  :8428         │  │
│  └─────────┘  └──────────┘  └────────────────┘  │
└─────────────────────────────────────────────────┘
         ▲ agents dial in over mTLS (Vault PKI)
         │
  spoke clusters, each with its own agent and
  Argo CD  (see charts/saidkick-agent)
```

The hub runs no Argo CD and holds no spoke credentials (ADR-4). The API
defaults to `api.config.orchestratorKind: agent`: it queues deployment
commands, and each spoke's agent applies them to the Argo CD on its own
cluster.

---

## Prerequisites

| Tool | Version | Notes |
|------|---------|-------|
| Helm | ≥ 3.12 | OCI registry support required |
| kubectl | any recent | context pointing at the hub cluster |
| cert-manager | ≥ 1.14 (optional) | for `ingress.tls.enabled=true` with Let's Encrypt |

---

## Prerequisites — Zalando Postgres Operator

The chart uses the Zalando Postgres Operator to manage the PostgreSQL cluster.
Install it cluster-wide once before the first `helm install`:

```bash
helm repo add postgres-operator-charts \
  https://opensource.zalando.com/postgres-operator/charts/postgres-operator
helm repo update
helm install postgres-operator postgres-operator-charts/postgres-operator \
  --namespace postgres-operator --create-namespace
```

---

## Quick start (local / staging)

```bash
# 1. Add dependency repos
helm repo add openbao https://openbao.github.io/openbao-helm
helm repo add victoriametrics https://victoriametrics.github.io/helm-charts/
helm repo add argo https://argoproj.github.io/argo-helm
helm repo update

# 2. Download subcharts
helm dependency update charts/saidkick-hub

# 3. Generate required secrets
export SESSION_SECRET=$(openssl rand -base64 32)
export JOIN_SECRET=$(openssl rand -base64 32)
export ENCRYPTION_KEY=$(openssl rand -base64 32)

# 4. Install (all subcharts enabled, no TLS for local dev)
#    Use a placeholder databaseUrl — update it after retrieving the
#    Zalando-generated password (see "PostgreSQL" section below).
#    The agent mTLS listener is off, which the chart allows only for a
#    dev-like appEnv (see "Agent mTLS listener" below).
helm upgrade --install saidkick-hub ./charts/saidkick-hub \
  --namespace saidkick-system --create-namespace \
  --set api.config.appEnv=development \
  --set api.agentListener.enabled=false \
  --set api.config.authBaseUrl=http://localhost:8080 \
  --set api.secrets.authSessionSecret="$SESSION_SECRET" \
  --set api.secrets.authSecretEncryptionKey="$ENCRYPTION_KEY" \
  --set api.secrets.clusterJoinSecret="$JOIN_SECRET" \
  --set api.secrets.databaseUrl="postgres://saidkick:PLACEHOLDER@saidkick-hub-postgresql:5432/saidkick_appstore?sslmode=disable" \
  --wait --timeout 8m
```

---

## Production install

1. Copy `values-production.yaml` and fill in every `<REPLACE>` placeholder.
2. Store the file in a secrets manager (Azure Key Vault + ESO, SOPS, or
   Sealed Secrets) — never commit it to source control.

```bash
helm dependency update charts/saidkick-hub

helm upgrade --install saidkick-hub ./charts/saidkick-hub \
  --namespace saidkick-system --create-namespace \
  --values charts/saidkick-hub/values-production.yaml \
  --wait --timeout 10m
```

---

## Configuration

### Required values

| Key | Description |
|-----|-------------|
| `api.config.authBaseUrl` | Public base URL of the hub, e.g. `https://appstore.example.nl` |
| `api.secrets.authSessionSecret` | HMAC key for session cookies; min 32 bytes |
| `api.secrets.clusterJoinSecret` | HMAC key for cluster join-token JWTs; min 32 bytes |
| `api.secrets.databaseUrl` | PostgreSQL connection string |
| `api.agentListener.servingCertSecret` | `kubernetes.io/tls` Secret for the agent mTLS listener (unless `appEnv` is dev-like and the listener is disabled) |
| `api.agentListener.clientCASecret` | Secret holding the agent client CA bundle (same condition) |

### Agent mTLS listener

Spoke agents authenticate with the client certificate they receive at
handshake. The API terminates that mTLS itself on a second listener
(`AGENT_TLS_ADDR`, default `:8443`), exposed by the `<release>-api-agent`
Service. The agent routes (heartbeat, deployment states, command acks) exist
only on that listener; the main API listener answers them with 404. The
handshake itself stays on the main listener because it authenticates with
the join token.

The listener requires TLS 1.3 and a client certificate signed by the agent
CA. After the handshake the API also compares the certificate serial with
the serial recorded for the cluster, so a rotated certificate stops working
immediately.

| Key | Default | Description |
|-----|---------|-------------|
| `api.agentListener.enabled` | `true` | May be `false` only when `api.config.appEnv` is `dev`, `development`, `local`, `test` or `ci`. |
| `api.agentListener.port` | `8443` | Container port (`AGENT_TLS_ADDR`). |
| `api.agentListener.servingCertSecret` | `""` | `kubernetes.io/tls` Secret with the serving certificate. Issue it from the Vault PKI mount that signs agent certificates, since agents trust only that CA. |
| `api.agentListener.clientCASecret` | `""` | Secret with the CA of the Vault PKI mount that signs agent CSRs (`AGENT_CLIENT_CA_FILE`). |
| `api.agentListener.clientCAKey` | `ca.crt` | Key in `clientCASecret` holding the PEM bundle. |
| `api.agentListener.service.type` | `LoadBalancer` | Agents connect from other clusters. Use TCP/TLS passthrough if it sits behind a proxy; a TLS-terminating ingress breaks client authentication. |
| `api.agentListener.service.port` | `8443` | Service port. |
| `api.config.clusterHubAgentEndpoint` | `""` | Public URL of the listener, printed as `agent.hubAgentUrl` in the agent install command. |

The role that signs agent CSRs (`api.config.vaultPkiRole`, default `agent`)
must allow the common name the handshake requests,
`saidkick-agent-<cluster-uuid>`. `allowed_domains="saidkick-agent"` with
`allow_subdomains=true` does not: it only allows `*.saidkick-agent`.

```bash
bao write pki/roles/agent \
  allowed_domains="saidkick-agent-*" allow_glob_domains=true \
  use_csr_common_name=false use_csr_sans=false \
  server_flag=false client_flag=true max_ttl=2160h
```

`use_csr_common_name=false` makes the hub's common name win over the one in
the CSR, and the certificate is issued for client authentication only.

Create the two Secrets from the Vault PKI mount (`api.config.vaultPkiMount`,
default `pki`):

```bash
# Client CA: the issuer agent-init receives as ca_pem at handshake.
bao read -field=certificate pki/cert/ca > ca.crt
kubectl -n saidkick-system create secret generic saidkick-agent-client-ca --from-file=ca.crt

# Serving certificate from a server-auth role on the same mount.
bao write pki/roles/hub-agent-listener allowed_domains=agents.appstore.example.nl \
  allow_bare_domains=true server_flag=true client_flag=false max_ttl=2160h
bao write -format=json pki/issue/hub-agent-listener common_name=agents.appstore.example.nl > issued.json
jq -r .data.certificate issued.json > tls.crt
jq -r .data.private_key issued.json > tls.key
kubectl -n saidkick-system create secret tls saidkick-agent-listener-tls --cert=tls.crt --key=tls.key
rm issued.json tls.key
```

The API reads both Secrets at startup. Restart the API pods after rotating
either one.

### Agent chart reference

`POST /api/v1/clusters` prints
`helm install saidkick-agent <agentChartRepo>/saidkick-agent --version <appVersion> ...`.

| Key | Default | Description |
|-----|---------|-------------|
| `api.config.agentChartRepo` | `oci://ghcr.io/clappformorg/charts` | OCI repository the charts are published to (`AGENT_CHART_REPO`). Releases use `oci://ghcr.io/<lower-cased repository owner>/charts`; change this value when the owner changes. |

`AGENT_CHART_VERSION` is this chart's `appVersion`. Both charts are stamped
with the same release version, so a hub prints the agent chart released with
it.

### PostgreSQL

When `postgresql.enabled=true` (default), the chart creates a Zalando
`postgresql` cluster CR. The Zalando operator provisions the cluster,
manages streaming replication (set `postgresql.instances: 2` for HA), and
writes per-user credential Secrets automatically.

After install, retrieve the app user password:

```bash
kubectl get secret \
  saidkick.saidkick-hub-postgresql.credentials.postgresql.acid.zalan.do \
  -n saidkick-system \
  -o jsonpath='{.data.password}' | base64 -d
```

Then upgrade with the real connection string (the master service name is
`{release}-postgresql`, matching the old StatefulSet address):

```
postgres://saidkick:<password>@<release>-postgresql:5432/saidkick_appstore?sslmode=disable
```

A post-install/upgrade Helm hook Job runs the extension and role SQL
(`pgcrypto`, `citext`, `app_rw`, `audit_writer`) on every deploy.

Set `postgresql.enabled=false` and provide `api.secrets.databaseUrl` pointing
at an external managed database for production (Azure Database for PostgreSQL,
AWS RDS, CloudNativePG, Zalando operator on a separate cluster, etc.).

### OpenBao (secret store)

When `openbao.enabled=true` (default), an OpenBao instance is deployed.

> **Dev/evaluation only.** The default OpenBao listener sets `tls_disable = 1`,
> so OpenBao serves plaintext HTTP inside the cluster. Do not use it in
> production. `values-production.yaml` sets `openbao.enabled=false` and points
> `api.secrets.vaultAddr` at an external TLS-enabled OpenBao / Vault.

After install you must:

1. **Initialise & unseal** — see `NOTES.txt` post-install output for commands.
2. **Enable KV v2 + PKI** — the NOTES.txt prints the exact commands.
3. **Create a service token** with the `saidkick-api` policy.
4. Set `api.secrets.vaultAddr` and `api.secrets.vaultToken` and run
   `helm upgrade` to inject the credentials.

For production, use an external OpenBao / Vault instance
(`openbao.enabled=false`) with HA storage (Raft or Consul).

### Argo CD

The hub deploys through the spoke agents (`orchestratorKind: agent`), and
`argocd.enabled` defaults to `false`. Argo CD runs on each spoke and the
`saidkick-agent` chart installs it there. The API refuses
`ORCHESTRATOR_KIND=argocd`, and `ARGOCD_*` with no kind, unless `appEnv` is
`dev`, `development`, `local`, `test` or `ci`. The direct Argo CD client is
for local development (see `API/README.md`); this chart does not wire its
credentials.

### Ingress

```yaml
ingress:
  enabled: true
  className: nginx
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod
  host: appstore.example.nl
  tls:
    enabled: true
    secretName: saidkick-hub-tls
```

### Metrics

The API serves `GET /metrics` without authentication on its own listener
(`METRICS_ADDR`), exposed as the `metrics` port of the
`<release>-api-metrics` Service. That Service is always ClusterIP, so
setting `api.service.type` to `NodePort` or `LoadBalancer` publishes the
`http` port only. The ingress routes only the `http` port, and the main
listener answers `/metrics` with 404, so the endpoint is reachable
in-cluster only.

| Key | Default | Description |
|-----|---------|-------------|
| `api.metrics.port` | `9090` | Container port of the metrics listener and port of the `<release>-api-metrics` Service. |

The bundled VictoriaMetrics scrape job keeps pods whose
`app.kubernetes.io/name`/`app.kubernetes.io/component` labels are
`saidkick-hub/api`, then only their `metrics` container port. The name half
is the chart name. If you set `nameOverride`, change the `regex` under
`victoria-metrics-single.server.scrape.config` to match, or nothing is
scraped. `ci/check-scrape-config.sh` renders the chart and fails when the
scrape job keeps anything other than the API metrics port:

```bash
charts/saidkick-hub/ci/check-scrape-config.sh -f my-values.yaml
```

---

## Subcharts

| Component | Source | Condition key |
|-----------|--------|--------------|
| PostgreSQL | Zalando Postgres Operator CR (`acid.zalan.do/v1 postgresql`) | `postgresql.enabled` |
| OpenBao | Subchart `openbao/openbao` | `openbao.enabled` |
| VictoriaMetrics | Subchart `victoria-metrics-single` | `victoriametrics.enabled` |
| Argo CD | Subchart `argo/argo-cd`, off by default (not used by the agent-brokered hub) | `argocd.enabled` |

PostgreSQL is managed via the Zalando operator CRD (cluster-wide prerequisite).
The remaining subcharts accept pass-through configuration under their respective
keys in `values.yaml`; see each chart's own documentation for the full option set.

---

## Upgrading

```bash
helm dependency update charts/saidkick-hub
helm upgrade saidkick-hub ./charts/saidkick-hub \
  --namespace saidkick-system \
  --values charts/saidkick-hub/values-production.yaml \
  --wait
```

The API Secret is annotated `helm.sh/resource-policy: keep` so it survives
`helm uninstall`. Delete it manually if you want a clean slate:

```bash
kubectl delete secret saidkick-hub-api -n saidkick-system
```

> Zalando credential Secrets (`*.credentials.postgresql.acid.zalan.do`) are
> owned by the operator, not Helm. They persist across `helm uninstall` and
> are deleted only when the `postgresql` CR is deleted.

---

## After the hub is running

Register your first spoke cluster:

```bash
# 1. Seed a platform admin user (see API/RUNBOOK.md)
# 2. Create a cluster entry
curl -s -X POST https://appstore.example.nl/api/v1/clusters \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <token>" \
  -d '{"display_name":"Example AKS Prod","scope":"org:provincie-voorbeeld"}' | jq .

# 3. Copy the helm install command from the response and run it on the spoke.
#    See charts/saidkick-agent/README.md.
```
