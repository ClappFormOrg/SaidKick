# saidkick-agent Helm Chart

Deploys the sAId-Kick Appstore **spoke agent** onto a target (spoke) cluster.
The agent registers with the hub, then brokers Argo CD syncs and ESO secret
pulls on the hub's behalf.

---

## Architecture

```
  Hub cluster                     Spoke cluster
  ─────────────────               ──────────────────────────────────
  sAId-Kick API                   ┌─────────────────────────────────┐
      │                           │  saidkick-system namespace       │
      │ POST /clusters/:id/        │                                  │
      │ handshake (JWT + CSR)      │  ┌─────────────┐               │
      │◄──────────────────────────►│  │ saidkick-   │               │
      │ ← client cert (90d)        │  │ agent       │               │
      │ ← CA cert                  │  │             │               │
      │ ← hub endpoint             │  └──────┬──────┘               │
      │                           │         │ wss (mTLS)             │
  Argo CD hub◄────────────────────┼─────────┘                        │
                                  │                                  │
  OpenBao/Vault                   │  ┌─────────────┐                │
      ▲                           │  │ ESO         │ pulls secrets  │
      │ token read                │  │ SecretStore │───────────────►│
      └───────────────────────────┼──┤ (namespace- │                │
                                  │  │  scoped)    │                │
                                  │  └─────────────┘                │
                                  └─────────────────────────────────┘
```

---

## Prerequisites

| Tool | Version | Notes |
|------|---------|-------|
| Helm | ≥ 3.12 | |
| kubectl | any recent | context pointing at the **spoke** cluster |
| Access to the hub | — | `POST /api/v1/clusters` must succeed |
| External Secrets Operator | chart `0.20.4` | Serves `external-secrets.io/v1`. Install it before this chart; see below. Not needed with `secretStore.enabled=false`. |

### External Secrets Operator

ESO is a spoke prerequisite, like Argo CD; this chart does not bundle it.
The chart's `SecretStore` is a custom resource of ESO's CRD, and Helm cannot
install a CRD and a resource of that kind in one release. Install ESO first,
with the version the hub prints and the end-to-end run uses:

```bash
helm install external-secrets external-secrets --repo https://charts.external-secrets.io \
  --version 0.20.4 \
  --namespace external-secrets --create-namespace \
  --wait
```

Skip this if the spoke already runs ESO that serves `external-secrets.io/v1`.
Without ESO, installing this chart with `secretStore.enabled=true` (the
default) stops with:

```
External Secrets Operator is not installed: install it first (see README) or set secretStore.enabled=false
```

The spoke cluster must be able to reach:
- The hub HTTPS endpoint (for handshake + WebSocket tunnel)
- The OpenBao / Vault HTTPS endpoint (for ESO secret pulls)

---

## Install

`POST /api/v1/clusters` returns the install commands in order in
`installer.steps`: the ESO command above, then the agent command below
(also in `installer.helm_command`). The agent command fills in `agent.hubUrl`, `agent.hubAgentUrl` (from the hub's
`CLUSTER_HUB_AGENT_ENDPOINT`, when set), `agent.clusterID`, `agent.joinToken`
and `vault.mount` (from the hub's `VAULT_MOUNT`). Export the values the hub
cannot know first:

```bash
export ARGOCD_SERVER_URL=https://argocd-server.argocd.svc
export ARGOCD_AUTH_TOKEN=<argocd-token>
export VAULT_ADDR=https://openbao.example.nl
export VAULT_TOKEN=<scoped-read-token>

helm install saidkick-agent oci://ghcr.io/clappformorg/charts/saidkick-agent --version <hub-version> \
  --namespace saidkick-system --create-namespace \
  --set agent.hubUrl=https://appstore.example.nl \
  --set agent.hubAgentUrl=https://agents.appstore.example.nl:8443 \
  --set agent.clusterID=<cluster-uuid> \
  --set agent.joinToken=<jwt-from-hub> \
  --set agent.argocd.serverUrl="$ARGOCD_SERVER_URL" \
  --set agent.argocd.authToken="$ARGOCD_AUTH_TOKEN" \
  --set vault.addr="$VAULT_ADDR" \
  --set vault.mount=kv \
  --set vault.token="$VAULT_TOKEN"
```

The chart is published as an OCI artifact to
`oci://ghcr.io/<lower-cased repository owner>/charts`, today
`oci://ghcr.io/clappformorg/charts`. The hub prints that reference from its
`AGENT_CHART_REPO` setting (`api.config.agentChartRepo` in the hub chart) and
`--version` from its own version (`AGENT_CHART_VERSION`), so the agent chart
matches the hub release. Helm 3.8 or newer pulls OCI charts without extra
setup.

Or from a local checkout for development:

```bash
# Add subchart repos first (ESO is installed separately, see Prerequisites)
helm repo add argo https://argoproj.github.io/argo-helm
helm repo update
helm dependency update charts/saidkick-agent

helm upgrade --install saidkick-agent ./charts/saidkick-agent \
  --namespace saidkick-system --create-namespace \
  --set agent.hubUrl="https://appstore.example.nl" \
  --set agent.hubAgentUrl="https://agents.appstore.example.nl:8443" \
  --set agent.clusterID="$CLUSTER_ID" \
  --set agent.joinToken="$JOIN_TOKEN" \
  --set agent.argocd.serverUrl="$ARGOCD_SERVER_URL" \
  --set agent.argocd.authToken="$ARGOCD_AUTH_TOKEN" \
  --set vault.addr="$VAULT_ADDR" \
  --set vault.mount=kv \
  --set vault.token="$VAULT_TOKEN" \
  --wait
```

---

## Configuration

### Required values

| Key | Description |
|-----|-------------|
| `agent.hubUrl` | HTTPS base URL of the hub, no trailing slash, e.g. `https://appstore.example.nl`. |
| `agent.clusterID` | UUID of the cluster row on the hub. |
| `agent.argocd.serverUrl` | Argo CD API URL reachable from the agent. |
| `agent.argocd.authToken` | Argo CD bearer token. |
| `vault.addr` | HTTPS URL of OpenBao / Vault, e.g. `https://openbao.example.nl`. |

Also set on first install:

| Key | Description |
|-----|-------------|
| `agent.hubAgentUrl` | HTTPS base URL of the hub's agent mTLS listener, e.g. `https://agents.appstore.example.nl:8443`. Required against a production hub; see [Hub endpoints](#hub-endpoints). |
| `agent.joinToken` | One-time JWT from `POST /api/v1/clusters`. Single-use, 1-hour TTL. |
| `vault.mount` | KV v2 mount. Must match the hub's `VAULT_MOUNT` (default `kv`). |
| `vault.token` | Read token for ESO. Without it the SecretStore has no credentials. |

### Hub endpoints

The agent talks to two hub endpoints:

| Value | Used by | Listener |
|-------|---------|----------|
| `agent.hubUrl` | `agent-init` (one-time handshake, join-token auth) | Main API listener. |
| `agent.hubAgentUrl` | `agent` (heartbeat, deployment states, command acks) | Dedicated agent listener. TLS 1.3, client certificate required. |

The hub serves the agent routes only on its agent listener (the
`<release>-api-agent` Service of the hub chart). The listener terminates TLS
itself, so it must be reached directly or through TCP/TLS passthrough, never
through an ingress that terminates TLS.

The agent verifies the listener's serving certificate against `ca.crt` from
the handshake, which is the hub's Vault PKI CA. Issue the listener's serving
certificate from that same PKI mount, or the handshake to the listener fails.

When `agent.hubAgentUrl` is empty the agent sends agent traffic to
`agent.hubUrl`. That only works against a dev-mode hub.

### Health probes

The agent serves `/healthz` and `/readyz` on the pod network. No Service
exposes the port. `/healthz` returns `ok` while the process runs. `/readyz`
returns `ok` once a hub heartbeat has succeeded within the last three
heartbeat intervals, and `not ready` before the first heartbeat or after
heartbeats stop.

| Key | Default | Description |
|-----|---------|-------------|
| `agent.health.port` | `8081` | Container port for the health endpoints. Passed to the agent as `SAIDKICK_HEALTH_ADDR`. |
| `agent.livenessProbe` | `initialDelaySeconds: 10`, `periodSeconds: 15`, `timeoutSeconds: 3`, `failureThreshold: 3` | Timings for the `/healthz` liveness probe. |
| `agent.readinessProbe` | `initialDelaySeconds: 5`, `periodSeconds: 10`, `timeoutSeconds: 3`, `failureThreshold: 3` | Timings for the `/readyz` readiness probe. |
| `agent.heartbeatInterval` | `30s` | Heartbeat cadence. The readiness threshold is three times this value. |

### Metrics

The agent serves Prometheus metrics at `/metrics` on its own port, separate
from the health probes, and nothing else on that port.

> **The metrics port is for in-cluster scrapers only.** `/metrics` has no
> authentication. Never route the port or the `<release>-metrics` Service
> through an ingress, a LoadBalancer or a NodePort. The chart creates no
> ingress for it, and `ci/check-metrics.sh` fails if one appears.

| Metric | Type | Labels |
|--------|------|--------|
| `agent_up` | gauge | none. Constant 1; alert on its absence or on the scraper's `up`. |
| `agent_heartbeats_total` | counter | `outcome`: `success` or `failure`, as the agent saw it. |
| `agent_commands_processed_total` | counter | `command`: `apply`, `delete`, `rollback` or `unknown`; `outcome`. |
| `agent_command_duration_seconds` | histogram | `command`. Time to execute the command against Argo CD. |
| `agent_hub_request_duration_seconds` | histogram | none. Round trip of every request to the hub. |
| `agent_cert_renewals_total` | counter | `outcome`: `success` or `failure`. Client certificate renewal attempts. |
| `agent_cert_remaining_seconds` | gauge | none. Remaining validity of the client certificate in use. |

Go runtime (`go_*`) and process (`process_*`) series are included. No series
carries a cluster ID: the agent serves one cluster, so add the identity as a
target label in the scrape config.

A spoke may run no monitoring stack, so scraping is opt-in:

| Key | Default | Description |
|-----|---------|-------------|
| `agent.metrics.port` | `9090` | Container port named `metrics`. Passed to the agent as `SAIDKICK_METRICS_ADDR`. |
| `agent.metrics.service.enabled` | `true` | ClusterIP Service `<release>-metrics` with a port named `metrics`, for endpoint or service discovery. |
| `agent.metrics.annotations` | `false` | Adds `prometheus.io/scrape: "true"`, `prometheus.io/port` and `prometheus.io/path: /metrics` to the agent pod, for annotation-based scrape configs. |

With an annotation-based Prometheus or VictoriaMetrics scrape config, set
`agent.metrics.annotations=true`. Otherwise point a pod or endpoints scrape
job at the `metrics` port of pods labelled
`app.kubernetes.io/component=agent`. To check the endpoint by hand:

```bash
kubectl -n saidkick-system port-forward svc/saidkick-agent-metrics 9090:9090
curl -s localhost:9090/metrics | grep '^agent_'
```

### Vault token scoping

The `vault.token` must have **read-only** access restricted to this cluster's
deployments. On the hub, create a policy:

```hcl
# spoke-cluster-<cluster-id>.hcl
path "kv/data/deployments/{{identity.entity.aliases.ACCESSOR.metadata.cluster_id}}/*" {
  capabilities = ["read", "list"]
}
```

Or, for initial setup with a simpler policy:

```hcl
path "kv/data/deployments/*" {
  capabilities = ["read", "list"]
}
```

> **Security note**: The second policy grants read access to ALL deployments,
> not just this cluster's. Use it only for testing. A per-cluster scoped
> policy is required for production (see OpenBao ACL templating docs).

### ESO SecretStore

A namespace-scoped `SecretStore` named `saidkick-openbao` is created in
`.Values.namespace`. This follows the ESO security guidance in
[external-secrets/external-secrets#6141](https://github.com/external-secrets/external-secrets/pull/6141):
- `SecretStore` (not `ClusterSecretStore`) — credentials are namespace-local
- Token scoped to `kv/data/deployments/*` only

The template renders only when the cluster serves
`external-secrets.io/v1` `SecretStore`, so install ESO first (see
[Prerequisites](#prerequisites)). To render the chart without a cluster, pass
the API explicitly:

```bash
helm template saidkick-agent ./charts/saidkick-agent -f my-values.yaml \
  --api-versions external-secrets.io/v1/SecretStore
```

Disable with `secretStore.enabled=false` if you manage the SecretStore
outside this chart or the spoke runs no ESO.

### Argo CD (spoke)

When `argocd.enabled=true` (default), a lightweight Argo CD spoke is deployed:
application controller + repo server only, no UI, no Dex. The hub Argo CD
manages spoke Applications by registering the spoke cluster via the agent's
mTLS cert.

Set `argocd.enabled=false` if Argo CD is already installed on this cluster.

---

## Subcharts

| Subchart | Chart | Condition key |
|----------|-------|--------------|
| Argo CD (spoke) | `argo/argo-cd` | `argocd.enabled` |

ESO is not a subchart; it is a prerequisite (see above).

---

## Handshake lifecycle

```
helm install
  └─► agent pod starts
       └─► reads JOIN_TOKEN from Secret
            └─► POST /api/v1/clusters/<id>/handshake
                 ├─ sends: agent_version, CSR (PKCS#10)
                 └─ receives: client_cert_pem, ca_pem, hub_endpoint
                      └─► agent writes cert to Secret (client_cert_pem, ca_pem)
                           └─► establishes wss mTLS tunnel to hub
                                └─► hub marks cluster state = "ready"
```

After the handshake, the `agent.joinToken` in the Secret is no longer valid
(JTI has been burned). The agent uses the issued mTLS cert for all future
communication. The cert is valid for 90 days. The agent renews it on its
own, without a pod restart:

- Each heartbeat cycle it checks the remaining validity. Below
  `agent.certRenewBefore` (default `720h`, 30 days), or when the hub's
  heartbeat response asks for a rotation, it generates a new RSA 2048 key
  and calls `POST /api/v1/clusters/<id>/agent/renew` with its current cert.
- It writes the new `tls.crt`, `tls.key` and `ca.crt` to the credentials
  Secret, then switches to the new cert in memory.
- The hub keeps accepting the previous cert until the agent first presents
  the new one; from then on the previous cert gets 401.
- A failed renewal keeps the current cert and retries with exponential
  backoff from 1 minute to 1 hour.

A cluster admin of the owning organisation, or a platform admin, can force a
rotation with "Inloggegevens vernieuwen" on the cluster page
(`POST /api/v1/clusters/<id>/rotate-credential`); the agent renews on its
next heartbeat.

| Key | Default | Description |
|-----|---------|-------------|
| `agent.certRenewBefore` | `720h` | Renew the client certificate once less than this much validity remains. Passed to the agent as `SAIDKICK_CERT_RENEW_BEFORE`. |

---

## Upgrading

```bash
helm upgrade saidkick-agent oci://ghcr.io/clappformorg/charts/saidkick-agent \
  --version <new-version> \
  --namespace saidkick-system \
  --reuse-values
```

> **Do not** pass `agent.joinToken` on upgrades — the token is single-use.
> The chart Secret is annotated `helm.sh/resource-policy: keep` so the cert
> material is preserved across upgrades.

---

## Troubleshooting

| Symptom | Cause + fix |
|---------|-------------|
| `helm install` stops with `External Secrets Operator is not installed` | ESO is missing or does not serve `external-secrets.io/v1`. Run the ESO step first (see [Prerequisites](#prerequisites)), or install with `secretStore.enabled=false`. |
| Agent pod in `CrashLoopBackOff` immediately | `agent.joinToken` expired (1h TTL). Re-register: `POST /api/v1/clusters` on hub, then `helm upgrade --set agent.joinToken=<new-jwt>`. |
| `connection refused` to hub | `agent.hubUrl` is wrong or the hub ingress doesn't forward WebSocket (`Upgrade: websocket` header). Check nginx annotation `nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"`. |
| ESO `SecretStore` status `Invalid` | `vault.token` lacks read access to `kv/data/deployments/*`, or `vault.addr` is unreachable from the spoke cluster. |
| Cluster remains `pending_registration` on hub | Handshake failed. Check agent logs: `kubectl logs -n saidkick-system -l app.kubernetes.io/name=saidkick-agent`. |
| Argo CD not syncing | Hub Argo CD hasn't registered this spoke cluster yet. The agent handles registration automatically; check agent logs for `"registering cluster with Argo CD hub"`. |
