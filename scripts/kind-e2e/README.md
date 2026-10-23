# kind end-to-end run: agent-brokered deployments over the agent mTLS listener

`run.sh` builds two local kind clusters, a hub and a spoke, and drives a real
deployment from the hub API through the spoke agent to a running workload. The
agent talks to the hub only over the agent mTLS listener added in #64 (#111).
It is the real-cluster check for #63, which made `orchestratorKind: agent`
the hub chart default; neither values file the run uses sets the kind.

```
 saidkick-hub (kind)                              saidkick-spoke (kind)
 ┌─────────────────────────────────────┐          ┌──────────────────────────────┐
 │ API  :8080  NodePort 30080 (http)   │◄─────────│ agent-init: handshake        │
 │ API  :8443  NodePort 30443 (mTLS)   │◄─────────│ agent: heartbeat/states/acks │
 │ Postgres (smoke-postgres.yaml)      │          │ Argo CD (upstream manifest)  │
 │ OpenBao dev server (KV + PKI)       │          │ helm-guestbook workload      │
 │ VictoriaMetrics -> API :9090        │          │ agent /metrics :9090 (curl)  │
 └─────────────────────────────────────┘          └──────────────────────────────┘
          both nodes on the docker network "kind"; the spoke dials the hub node IP
```

## What it proves

| Step in `run.sh` | Acceptance criterion |
|---|---|
| Hub installed from `charts/saidkick-hub` with `api.agentListener.enabled=true`, serving cert and client CA issued from OpenBao PKI with the commands in `charts/saidkick-hub/README.md` | #64: chart Service and port for the listener, serving cert, client CA from the agent PKI mount |
| Webapp built from `Webapp/Dockerfile` and enabled on the hub; two `GET /catalogus` responses through a port-forward | #137: the HTML carries a `script-src 'self' 'nonce-...' 'strict-dynamic'` policy with a new nonce per response, every `<script>` (inline and `src=`) and modulepreload link carries that nonce, and `Cache-Control` is `no-store` |
| ConfigMap carries `ORCHESTRATOR_KIND=agent` and the API logs `agent-brokered orchestrator wired`, with no orchestrator override in any values file | #63: the hub chart defaults to the agent-brokered deployer |
| `POST /api/v1/clusters` returns the install command with `agent.hubAgentUrl`; the agent is installed with that command | #64: hub prints the agent listener URL |
| The install command starts `helm install saidkick-agent oci://ghcr.io/clappformorg/charts/saidkick-agent --version <hub appVersion>` | #87: the hub prints the published OCI chart at its own version |
| `installer.steps` lists `external-secrets` then `saidkick-agent`, the ESO step pins the chart version the agent README documents, and `helm_command` equals the agent step | #140: the installer response lists the ESO step before the agent step |
| ESO installed with the first step; the agent installed with the second, `secretStore.enabled` at its default (true), in one `helm` run | #140: one-step install on a fresh spoke |
| The `external-secrets.io/v1` SecretStore `saidkick-openbao` is `Ready`, and an ExternalSecret reads a value from `kv/deployments/<cluster-id>/` with the cluster-scoped token | #140: the chart's SecretStore works against the hub OpenBao |
| agent-init handshake: cluster `ready`, `agent_cert_serial` recorded | #63/#64: agent receives a Vault-signed client cert |
| Heartbeats advance with the agent pointed only at `https://<hub>:30443` | #64: valid cert with matching serial authenticates |
| App version submitted (developer) and approved (auditor from another org), deployment created, hub state `ready`, pod Running on the spoke | #63: an end-to-end deployment completes through an agent against a real cluster |
| Upgrade to version 2 with new values; Argo CD targetRevision and replica count change | #63: deployer `Apply` on an existing deployment |
| Rollback to the archived version 1 through the API; the command is queued for the spoke's cluster | #63: deployer `Rollback` |
| Dev-mode auth turned off; heartbeats continue | #64: agent auth works outside dev mode |
| Probe pod on the spoke, no client cert | #64: request without a client certificate is rejected (at the TLS handshake, before HTTP) |
| Probe with a cert from a different CA | #64: unknown CA rejected at the TLS handshake |
| Probe with a second cert from the agent CA for the same cluster | #64: serial mismatch returns 401 |
| Probe with the agent's own cert | #64: matching serial authenticates and resolves the right cluster (404 for an unknown command, past auth) |
| `POST .../heartbeat` with `X-Agent-Cluster-ID` on the main listener | #64: main listener does not accept agent routes (404) |
| `agent_cert_serial` switched to the second cert, then restored | #64: rotating the recorded serial invalidates the previous cert on the next request |
| Agent pod deleted with dev mode off; the replacement pod becomes Ready | #122: a restarted agent pod comes back healthy |
| `agent_cert_serial`, `registered_at` and the cluster's `used_join_tokens` rows unchanged; agent-init logs that it reused the credentials Secret | #122: no new handshake, the join token is not presented again |
| Heartbeats advance from the replacement pod | #122: heartbeats resume over mTLS on the stored certificate |
| Hub restarted with the test-only `AGENT_CERT_TTL=48h`; `POST /clusters/<id>/rotate-credential` as developer/deployer (403) and platform admin (202) | #139: admin-triggered rotation, other roles refused |
| Within 120s the active serial changes, no serial is pending and the rotation request is cleared; from a spoke pod the previous cert gets 401 and the new one authenticates; the credentials Secret holds the new cert | #139: the agent renews on the heartbeat's `renew` flag, the hub activates the new cert on first use, the agent persists it |
| The agent was installed with `agent.certRenewBefore=47h58m`, so the 48h cert is renewed again on its own about two minutes later; the previous cert then gets 401 | #139: automatic renewal before expiry |
| Same agent pod and restart count before and after both renewals, heartbeats still advance | #139: the agent swaps the certificate without a pod restart |
| `<release>-api-metrics` is ClusterIP with port `metrics` 9090; the NodePort API Service carries no metrics port | #101: the metrics port stays in-cluster |
| `GET /metrics` on the API http port returns 404 | #101: `/metrics` moved off the main listener |
| VictoriaMetrics (chart default, own scrape job) has one `saidkick-api` target per API pod, every one `up` and on `:9090/metrics` | #101: the relabel keeps exactly the API metrics port |
| After the deploy flow, VictoriaMetrics holds non-zero `up`, Go and process series, `http_requests_total` (including `path="unmatched",status="404"` from the main-port `/metrics` probe), `deployment_transitions_total`, `orchestrator_calls_total` for apply and rollback, `audit_decisions_total`, `audit_writes_total` and `auth_attempts_total` | #102 to #105: the domain metrics are emitted and collected |
| `agent_heartbeat_age_seconds` is above 0 and at most three heartbeat intervals while the agent runs | #104: the agent gauge tracks a live agent |
| Every label value of every stored `saidkick-api` series, read through the series API at the end of the run, holds no UUID or email | Design doc section 6: closed label sets |
| From a pod on the spoke: the `saidkick-agent-metrics` ClusterIP Service and the pod's port 9090 serve `/metrics` with `agent_up 1` and Go series; the health port answers `/metrics` with 404 | #106: in-cluster `/metrics` on its own port |
| The agent pod carries no `prometheus.io/*` annotations on a default install | #106: scraping is opt-in on a spoke |
| `agent_heartbeats_total{outcome="success"}` rises between two scrapes 12s apart | #106: heartbeats counted from the agent's view |
| `agent_commands_processed_total` counts 2 apply and 1 rollback successes, with matching `agent_command_duration_seconds` counts and hub round trips observed | #106: commands counted and timed |
| No agent label value holds a UUID, an email or a cluster label | #106: no cluster ID label |

The spoke runs no monitoring stack, which is the case #106 has to allow for,
so the spoke checks scrape the agent endpoint with curl from a pod, the way
an in-cluster scraper would.

Each check prints `[PASS]`, `[FAIL]` or `[BLOCKER]`, and the run ends with a
summary table. Exit code 0 means every check passed without workarounds, 2
means the flow completed only with the test-only workarounds listed as
`BLOCKER`, 1 means something failed.

## Prerequisites

| Tool | Notes |
|---|---|
| Docker | Docker Desktop on Windows/macOS, Docker Engine on Linux. About 6 GB of memory for both clusters. On Windows, Docker Desktop needs WSL 2: run `wsl --install` in an elevated terminal, reboot, then start Docker Desktop. |
| kind | `go install sigs.k8s.io/kind@latest` |
| kubectl | Docker Desktop ships one |
| helm | v3.12 or newer |
| go | the version in `API/go.mod` (runs `cmd/seed`) |
| jq, openssl, curl | Git for Windows ships openssl and curl; `winget install jqlang.jq` for jq |

The script checks for these and prints install hints. It installs nothing. On
Windows run it from Git Bash. Binaries that are not on `PATH` can be dropped
into `scripts/kind-e2e/.work/bin/`, which `run.sh` puts first on `PATH`.

Images are built for amd64 only: `API/Dockerfile` and `API/Dockerfile.agent`
hard-code `GOARCH=amd64`, so the run stops early on an arm64 Docker host
(Apple Silicon).

## Running

```bash
scripts/kind-e2e/run.sh        # build, deploy, verify; prints a summary
scripts/kind-e2e/teardown.sh   # delete both clusters and the run state
```

Options (environment variables):

| Variable | Effect |
|---|---|
| `KIND_E2E_SKIP_BUILD=1` | Reuse images already loaded into the clusters |
| `KIND_E2E_STRICT=1` | Stop at the first known blocker instead of applying its test-only workaround |
| `KIND_NODE_IMAGE` | kind node image, e.g. `kindest/node:v1.37.0` |
| `ARGOCD_VERSION` | Argo CD release on the spoke (default `v3.4.9`) |

Re-running is safe: existing clusters, releases, PKI and the registered spoke
are reused. For a clean run, call `teardown.sh` first.

Runtime: about 7 minutes on a 16-core machine with warm Docker and Go caches,
15 to 20 minutes on the first run (node image pull, image builds, chart and
Argo CD downloads).

Nothing secret is committed. Passwords, the OpenBao root token, the Argo CD
session token, certificates and the hub's install command (with its one-time
join token) are generated at runtime into `scripts/kind-e2e/.work/`, which is
gitignored. Private keys used by the probes are deleted after the checks.

### How the pieces are wired

- Network: both clusters use kind's default docker network `kind`. The hub
  API Service and the `<release>-api-agent` Service are switched to NodePort
  and pinned to 30080 and 30443. The spoke reaches them on the hub node's IP
  on that network (`docker inspect saidkick-hub-control-plane`). The agent
  listener's serving cert carries that IP as an IP SAN.
- Hub values: `charts/saidkick-hub/ci/smoke-values.yaml` plus a generated
  `.work/hub-values.yaml` that turns the agent listener on, sets
  `clusterHubEndpoint` and `clusterHubAgentEndpoint`, enables the OpenBao
  subchart in dev mode and turns VictoriaMetrics back on (the chart default,
  with the chart's own scrape job; smoke values disable it). Neither file sets `orchestratorKind`, so the chart
  default (`agent`) applies. `appEnv` stays `ci`: the human API calls need
  dev-mode header auth (`X-Dev-User-ID`, no IdP in kind), and `APP_DEV_MODE`
  is refused in prod-like environments. Dev mode is switched off again before
  the mTLS checks, so those run against the production auth path.
- OpenBao: KV and PKI mounts, the `agent` role and the `saidkick-api` policy
  and token follow `charts/saidkick-hub/templates/NOTES.txt` and
  `API/RUNBOOK.md`; the listener cert and client CA follow
  `charts/saidkick-hub/README.md`.
- Users: `cmd/seed` with `testdata/seed.json`. The developer and the auditor
  sit in different orgs because auditors may not decide on their own org's
  submissions.
- App: CreateApp needs a GitHub App, so the app, version and audit
  submission rows are inserted with SQL. Submit and approve go through the
  API. The chart is `helm-guestbook` from the public
  `argoproj/argocd-example-apps` repo at two pinned commits, because the hub
  builds the Argo CD source as `https://github.com/<owner>/<repo>`.
- Spoke: Argo CD from the pinned upstream `install.yaml`, with
  `server.insecure` so the agent can reach it over in-cluster HTTP. ESO is
  installed with the first command in `installer.steps` (chart
  `external-secrets` 0.20.4, the version the agent README documents), run as
  `helm upgrade --install` so a re-run is safe. The agent is installed with
  the exact `helm_command` the hub returns, with the OCI chart reference and
  its `--version` swapped for `charts/saidkick-agent` (the run tests this
  checkout, not the last release) and these overrides appended: local image,
  `argocd.enabled=false`, 10s heartbeat and status intervals. The
  SecretStore stays on (the chart default); `vault.addr` points at the hub's
  dev OpenBao, published on NodePort 30820 for the run, and `vault.token` is
  a token whose policy reads only `kv/data/deployments/<cluster-id>/*`.

## Debugging

```bash
kubectl config get-contexts                    # kind-saidkick-hub, kind-saidkick-spoke
k9s --context kind-saidkick-hub                # or kind-saidkick-spoke

# Hub
kubectl --context kind-saidkick-hub -n saidkick get pods,svc
kubectl --context kind-saidkick-hub -n saidkick logs deploy/saidkick-hub-api -f
kubectl --context kind-saidkick-hub -n saidkick exec -it deploy/smoke-postgres -- \
  psql -U postgres -d saidkick_appstore -c 'select id, state, agent_cert_serial, last_heartbeat_at from clusters'
kubectl --context kind-saidkick-hub -n saidkick exec -it deploy/smoke-postgres -- \
  psql -U postgres -d saidkick_appstore -c 'select id, state, last_argocd_app_name from deployments'
kubectl --context kind-saidkick-hub -n saidkick exec -it deploy/smoke-postgres -- \
  psql -U postgres -d saidkick_appstore -c 'select command_type, cluster_id, claimed_at, acked_at from agent_commands'

# Spoke
kubectl --context kind-saidkick-spoke -n saidkick-system logs deploy/saidkick-agent -c agent-init
kubectl --context kind-saidkick-spoke -n saidkick-system logs deploy/saidkick-agent -c agent -f
kubectl --context kind-saidkick-spoke -n argocd get applications
kubectl --context kind-saidkick-spoke get pods -A | grep guestbook
```

Run state (cluster ID, user IDs, deployment ID) is in `.work/state.env` and
the check results in `.work/results.txt`.

## Known blockers

Gaps the real-cluster flow still has. The run records a product defect it
can only get past with a test-only workaround as `BLOCKER`;
`KIND_E2E_STRICT=1` stops at the first one instead. None of the items below
has a workaround in `run.sh`.

The blockers this run found are fixed and pass in a strict run: the
documented PKI role (#118), agent-reported states reaching `ready` (#119),
delete and rollback commands addressed to the nil cluster (#120), the
agent's Argo CD rollback request (#121), second-version approval and
rollback to an archived version (#116), and agent-init re-handshaking on a
pod restart (#122). The agent chart's SecretStore now uses
`external-secrets.io/v1`, the only version ESO 0.20.4 serves. The default
install no longer fails on a fresh spoke (#140): ESO is a prerequisite the
hub prints as the first install step, and the chart fails with an
actionable message when the SecretStore API is missing. Agent certificates
are renewed in place and can be rotated by an admin (#139).

1. **amd64-only images.** `API/Dockerfile:15` and `API/Dockerfile.agent:13`
   set `GOARCH=amd64`, so the images do not run on arm64 kind nodes.

## Verified run

`KIND_E2E_STRICT=1 scripts/kind-e2e/run.sh` on 2026-10-11: Windows 11
(Git Bash), Docker Desktop 29.8.2 (WSL 2, 16 CPUs, 16 GB), kind v0.33.0,
node image `kindest/node:v1.37.0`, helm v3.19.0, jq 1.7.1, Argo CD v3.4.9,
External Secrets Operator chart 0.20.4, OpenBao 2.5.4, VictoriaMetrics
v1.144.0 (chart `victoria-metrics-single` 0.39.0). Fresh clusters, warm
build caches: 585 seconds, exit code 0, 38 checks passed, no `BLOCKER` and
no workaround. The hub ran on the chart's default `orchestratorKind` (#63)
and its default VictoriaMetrics scrape job; the agent ran with the chart's
default `secretStore.enabled=true` against ESO installed from the hub's
first install step (#140).

The first run with the monitoring checks failed one of them: the series
API returned `http_requests_total` labelled
`path="/api/v1/clusters/<cluster-uuid>/heartbeat"`. The metrics middleware
used the raw URL for requests no route matched; it now labels them
`path="unmatched"`. The test for that fix showed the middleware also
recorded `status="200"` for every error a handler returned, before the
error handler wrote the real status; it now records the committed status,
and the run checks for `http_requests_total{path="unmatched",status="404"}`.
Writing the checks also showed that the hub's metrics
port sat on the API Service, so `api.service.type` NodePort (as in this
run) or LoadBalancer published it; it now has its own ClusterIP Service.

```
PASS    | Documented PKI role signs the agent CN | pki/roles/agent issues CN saidkick-agent-<uuid>
PASS    | Hub wires the agent-brokered deployer by default | ORCHESTRATOR_KIND=agent from the chart default; API logged 'agent-brokered orchestrator wired'
PASS    | Hub starts the agent mTLS listener | AGENT_TLS_* set from Vault-issued Secrets, Service saidkick-hub-api-agent NodePort 30443
PASS    | Webapp HTML carries a nonce CSP and nonced scripts (#137) | script-src 'nonce-...' 'strict-dynamic'; 4/4 scripts (3 inline) and 17/17 modulepreload links nonced; a second response got a new nonce; Cache-Control no-store
PASS    | Install command carries the agent listener URL | agent.hubAgentUrl=https://172.18.0.2:30443
PASS    | Installer lists the ESO step before the agent step (#140) | steps: external-secrets,saidkick-agent; ESO chart 0.20.4, as in the agent README; helm_command = agent step
PASS    | ESO installs from the hub's first step | chart external-secrets 0.20.4, --wait returned
PASS    | Agent chart installs with its SecretStore in one pass (#140) | helm upgrade --install succeeded with secretStore.enabled=true after the ESO step
PASS    | Agent handshake (join token -> Vault-signed client cert) | cluster ready, agent_cert_serial=9644867066...
PASS    | Heartbeats over the agent mTLS listener | last_heartbeat_at advances; agent: https://172.18.0.2:30443
PASS    | SecretStore rendered and Ready | external-secrets.io/v1 SecretStore saidkick-openbao in saidkick-system, Ready=True against http://172.18.0.2:30820
PASS    | ExternalSecret syncs through the SecretStore | kv/deployments/<cluster-id>/kind-e2e-eso-probe -> Secret kind-e2e-eso-probe
PASS    | Deployment completes through the agent (#63) | deployment ready; pod Running in <org>-guestbook with gb-frontend
PASS    | Upgrade through the agent | targetRevision=8088f4c0..., 2 pods Running with gb-frontend
PASS    | Rollback command is queued for the spoke | command for cluster <cluster-id>
PASS    | Rollback through the agent | targetRevision=f58c7ed8..., pods Running with gb-frontend
PASS    | Hub metrics port is a ClusterIP Service port | saidkick-hub-api-metrics ClusterIP metrics:9090; NodePort saidkick-hub-api carries only http:80:30080
PASS    | Main API listener answers /metrics with 404 | GET /metrics on the http port -> 404
PASS    | VictoriaMetrics scrapes only the API metrics port | job saidkick-api: up http://<pod-ip>:9090/metrics
PASS    | Key API series reach VictoriaMetrics after the e2e flow | non-zero: up go_goroutines process_resident_memory_bytes http_requests_total http_requests_total deployment_transitions_total deployment_transitions_total orchestrator_calls_total orchestrator_calls_total audit_decisions_total audit_writes_total auth_attempts_total (incl. deployment_transitions_total and orchestrator_calls_total for apply and rollback)
PASS    | agent_heartbeat_age_seconds is sane while the agent runs | max = 8.171055s (0 < age <= 30s, heartbeat 10s)
PASS    | Agent serves /metrics in-cluster on its metrics port | Service saidkick-agent-metrics ClusterIP:9090 and pod :9090 -> agent_up 1, Go series; health port :8081/metrics -> 404
PASS    | Agent scrape annotations are opt-in | default install: no prometheus.io/* pod annotations
PASS    | Agent heartbeat counter rises | agent_heartbeats_total{outcome="success"} 15 -> 16 over 12s (failure: 0)
PASS    | Agent counts and times the deploy, upgrade and rollback commands | processed apply/success=2 rollback/success=1; duration counts apply=2 rollback=1; hub round trips=35
PASS    | Agent metrics carry no identifying labels | 101 labelled series scanned; no UUID, email or cluster label
PASS    | Agent authenticates with dev mode off | heartbeat accepted on the mTLS listener after APP_DEV_MODE was removed
PASS    | No client certificate is rejected | agent listener -> 000 (000: refused during the TLS handshake)
PASS    | Unknown-CA certificate is rejected at the TLS handshake | agent listener -> no HTTP response
PASS    | Serial mismatch returns 401 | CA-signed cert for the same cluster -> 401
PASS    | Matching serial authenticates and resolves the cluster | agent's own cert -> 404 command-not-found (past auth)
PASS    | Main API listener does not serve agent routes | POST heartbeat on :30080 with the dev header -> 404
PASS    | Rotating the serial invalidates the previous cert immediately | previous cert -> 401, new cert -> 404 (authenticated); serial restored
PASS    | Agent recovers after the serial is restored | heartbeat accepted again
PASS    | Restarted agent pod becomes Ready | saidkick-agent-...-l66w5 deleted, saidkick-agent-...-pr7px Ready
PASS    | Restarted agent does not handshake again | serial, registered_at and used join tokens unchanged; agent-init reused the credentials Secret
PASS    | Heartbeats resume over mTLS after the restart | last_heartbeat_at advances, dev mode off; agent: https://172.18.0.2:30443
PASS    | Hub series carry no identifying labels | 543 series for job saidkick-api scanned through the series API
```

The #63 criterion "an end-to-end deployment completes through an agent
against a real cluster" passes, including upgrade and rollback.

The SecretStore fix was checked by hand on the same spoke: a release of
the agent chart with ESO enabled, then a second `helm upgrade` with
`secretStore.enabled=true`; ESO's webhook accepted the
`external-secrets.io/v1` SecretStore (Known blocker 2 covers the first
install).
