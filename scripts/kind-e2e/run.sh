#!/usr/bin/env bash
# SC2016: jq programs and JSON literals are single-quoted on purpose.
# SC2015: "check && record PASS || record FAIL" is intended; record never fails.
# shellcheck disable=SC2016,SC2015
#
# Local end-to-end run for the agent-brokered deployment path (#63) over the
# agent mTLS listener (#64), with the hub's VictoriaMetrics scraping (#101 to
# #105), the agent's metrics endpoint (#106) and ESO as a spoke prerequisite
# installed from the hub's printed steps (#140): a hub kind cluster and a
# spoke kind cluster on the shared "kind" docker network. See
# scripts/kind-e2e/README.md.
#
# Re-running is safe: finished steps are detected and skipped. Everything
# generated (secrets, certificates, logs) lives in scripts/kind-e2e/.work/,
# which is gitignored. ./teardown.sh deletes both clusters.
#
# Environment knobs (all optional):
#   KIND_E2E_SKIP_BUILD=1     reuse the images already loaded into the clusters
#   KIND_E2E_STRICT=1         stop at the first known blocker instead of
#                             applying the documented test-only workaround
#   KIND_NODE_IMAGE=<image>   kind node image (default: kind's own default)
#   ARGOCD_VERSION=<tag>      Argo CD release installed on the spoke
set -euo pipefail

# ── Layout ────────────────────────────────────────────────────────────────────
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/../.." && pwd)"
cd "$REPO_ROOT"
# Paths below stay relative to the repo root so native Windows binaries
# (kubectl.exe, helm.exe, docker.exe) and bash agree on them.
WORK="scripts/kind-e2e/.work"
TESTDATA="scripts/kind-e2e/testdata"
mkdir -p "$WORK"
# Optional drop-in tools (helm, jq, ...) that are not on PATH.
PATH="$REPO_ROOT/$WORK/bin:$PATH"
# Git Bash: never rewrite arguments such as /CN=... or /api/v1 into Windows paths.
export MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL='*'

# ── Settings ──────────────────────────────────────────────────────────────────
HUB=saidkick-hub
SPOKE=saidkick-spoke
HUB_CTX="kind-$HUB"
SPOKE_CTX="kind-$SPOKE"
HUB_NS=saidkick
AGENT_NS=saidkick-system
RELEASE=saidkick-hub
API_DEPLOY="$RELEASE-api"
BAO_POD="$RELEASE-openbao-0"
API_NODEPORT=30080
AGENT_NODEPORT=30443
# OpenBao on a NodePort so the spoke ESO SecretStore can reach it.
VAULT_NODEPORT=30820
LOCAL_API_PORT=18080
LOCAL_PG_PORT=15432
LOCAL_ARGO_PORT=18081
LOCAL_VM_PORT=18428
VM_NAME="$RELEASE-victoria-metrics-single-server"
METRICS_PORT=9090
AGENT_HEALTH_PORT=8081
IMAGE_TAG=e2e
API_IMAGE="saidkick-api:$IMAGE_TAG"
AGENT_IMAGE="saidkick-agent:$IMAGE_TAG"
WEBAPP_IMAGE="saidkick-webapp:$IMAGE_TAG"
WEBAPP_DEPLOY="$RELEASE-webapp"
LOCAL_WEBAPP_PORT=18300
# Name fixed by scripts/kind-e2e/smoke-postgres.yaml, which this run applies.
PG_IMAGE="saidkick-postgres:ci"
ARGOCD_VERSION="${ARGOCD_VERSION:-v3.4.9}"
CURL_IMAGE="curlimages/curl:8.11.1"
STRICT="${KIND_E2E_STRICT:-0}"

# Certificate renewal (#139). The agent is installed with a renewal threshold
# just under SHORT_CERT_TTL. Against the hub's default 90-day certificates it
# never fires; once the rotation checks set the hub's test-only
# AGENT_CERT_TTL to SHORT_CERT_TTL, every certificate the agent gets is due
# for renewal two minutes after it was issued.
CERT_RENEW_BEFORE=47h58m
SHORT_CERT_TTL=48h

# Public chart the test app deploys. The hub builds the Argo CD source URL as
# https://github.com/<owner>/<repo> (handlers/deployments.go:282), so the
# chart has to live in a public GitHub repo. Two pinned commits that both
# carry the same helm-guestbook chart (image gb-frontend:v5, which serves on
# port 80). The upgrade is visible as a new targetRevision plus replicaCount=2,
# the rollback as the targetRevision returning to version 1. Older commits of
# the chart use images that exit or no longer pull, so avoid them.
APP_SLUG=kind-e2e-guestbook
APP_REPO_OWNER=argoproj
APP_REPO_NAME=argocd-example-apps
APP_CHART_PATH=helm-guestbook
APP_V1_SHA=f58c7ed8cfe28ad70701c5923fdbd0154388ea9f
APP_V2_SHA=8088f4c0d970abb09e250248cc97e35623447cb5
APP_IMAGE=gb-frontend
APP_NAMESPACE=guestbook

STATE_FILE="$WORK/state.env"
SECRETS_FILE="$WORK/secrets.env"
RESULTS_FILE="$WORK/results.txt"

# ── Output helpers ────────────────────────────────────────────────────────────
START_TS=$SECONDS
log() { printf '\n[%s] == %s\n' "$(date +%H:%M:%S)" "$*"; }
info() { printf '   %s\n' "$*"; }
die() { printf '\nERROR: %s\n' "$*" >&2; summary; exit 1; }

: >"$RESULTS_FILE"
# record STATUS CRITERION DETAIL; STATUS is PASS, FAIL or BLOCKER.
record() {
  printf '%-7s | %s | %s\n' "$1" "$2" "$3" >>"$RESULTS_FILE"
  printf '   [%s] %s: %s\n' "$1" "$2" "$3"
}
summary() {
  printf '\n================ kind e2e summary (%ss) ================\n' "$((SECONDS - START_TS))"
  if [ -s "$RESULTS_FILE" ]; then cat "$RESULTS_FILE"; else echo "(no checks recorded)"; fi
  printf '========================================================\n'
}
# blocker CRITERION DETAIL: a product defect this run cannot get past without
# a test-only workaround. Strict mode stops here.
blocker() {
  record BLOCKER "$1" "$2"
  if [ "$STRICT" = "1" ]; then die "known blocker hit in strict mode: $1"; fi
}

jqr() { jq "$@" | tr -d '\r'; }

# state_set KEY VALUE persists a value across re-runs.
state_set() {
  touch "$STATE_FILE"
  grep -v "^$1=" "$STATE_FILE" >"$STATE_FILE.tmp" || true
  printf '%s=%q\n' "$1" "$2" >>"$STATE_FILE.tmp"
  mv "$STATE_FILE.tmp" "$STATE_FILE"
  printf -v "$1" '%s' "$2"
}
# shellcheck disable=SC1090
[ -f "$STATE_FILE" ] && . "$STATE_FILE"

# secret NAME prints a random hex secret, generated once and kept in .work.
secret() {
  local name=$1 val
  touch "$SECRETS_FILE"
  chmod 600 "$SECRETS_FILE" 2>/dev/null || true
  val=$(sed -n "s/^$name=//p" "$SECRETS_FILE")
  if [ -z "$val" ]; then
    val=$(openssl rand -hex 32)
    printf '%s=%s\n' "$name" "$val" >>"$SECRETS_FILE"
  fi
  printf '%s' "$val"
}

# wait_until TIMEOUT_S INTERVAL_S DESCRIPTION CMD... polls CMD until it succeeds.
wait_until() {
  local timeout=$1 interval=$2 desc=$3
  shift 3
  local deadline=$((SECONDS + timeout))
  until "$@"; do
    if [ "$SECONDS" -ge "$deadline" ]; then
      info "timed out after ${timeout}s waiting for: $desc"
      return 1
    fi
    sleep "$interval"
  done
}

kh() { kubectl --context "$HUB_CTX" "$@"; }
ks() { kubectl --context "$SPOKE_CTX" "$@"; }

psql_hub() {
  kh -n "$HUB_NS" exec -i deploy/smoke-postgres -- \
    psql -U postgres -d saidkick_appstore -v ON_ERROR_STOP=1 -qAt "$@" | tr -d '\r'
}

bao() {
  kh -n "$HUB_NS" exec -i "$BAO_POD" -- \
    env BAO_ADDR=http://127.0.0.1:8200 BAO_TOKEN="$BAO_ROOT_TOKEN" bao "$@"
}

# ── Port-forwards ─────────────────────────────────────────────────────────────
port_open() { (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; }

# start_pf NAME CONTEXT NAMESPACE TARGET LOCAL_PORT REMOTE_PORT
start_pf() {
  local name=$1 ctx=$2 ns=$3 target=$4 lport=$5 rport=$6
  stop_pf "$name"
  kubectl --context "$ctx" -n "$ns" port-forward --address 127.0.0.1 "$target" "$lport:$rport" \
    >"$WORK/pf-$name.log" 2>&1 &
  echo $! >"$WORK/pf-$name.pid"
  wait_until 30 1 "port-forward $name on :$lport" port_open "$lport" ||
    die "port-forward $name failed: $(cat "$WORK/pf-$name.log")"
}
stop_pf() {
  local f="$WORK/pf-$1.pid"
  if [ -f "$f" ]; then
    kill "$(cat "$f")" 2>/dev/null || true
    rm -f "$f"
  fi
}
cleanup() {
  local f
  for f in "$WORK"/pf-*.pid; do
    [ -e "$f" ] || continue
    kill "$(cat "$f")" 2>/dev/null || true
    rm -f "$f"
  done
}
trap cleanup EXIT

pf_api() { start_pf api "$HUB_CTX" "$HUB_NS" "svc/$API_DEPLOY" "$LOCAL_API_PORT" 80; }

# ── Hub API helper (human calls, dev-mode header auth) ───────────────────────
# api METHOD PATH USER_ID [JSON_BODY] sets API_CODE and API_BODY.
api() {
  local method=$1 path=$2 user=$3 body=${4:-} attempt
  local args=(-sS -o "$WORK/api-body.json" -w '%{http_code}' --max-time 30
    -X "$method" "http://127.0.0.1:$LOCAL_API_PORT$path" -H 'Content-Type: application/json')
  [ -n "$user" ] && args+=(-H "X-Dev-User-ID: $user")
  [ -n "$body" ] && args+=(--data-binary "$body")
  for attempt in 1 2; do
    API_CODE=$(curl "${args[@]}" 2>/dev/null) || API_CODE=000
    [ "$API_CODE" != "000" ] && break
    # The port-forward pins one pod and dies with it; reopen once and retry.
    [ "$attempt" = 1 ] && pf_api
  done
  API_BODY=$(tr -d '\r' <"$WORK/api-body.json" 2>/dev/null || true)
}
api_ok() {
  api "$@"
  case "$API_CODE" in
  2??) ;;
  *) die "$1 $2 returned $API_CODE: $API_BODY" ;;
  esac
}

# ── 1. Prerequisites ──────────────────────────────────────────────────────────
check_prereqs() {
  log "Checking prerequisites"
  local missing=0 tool
  declare -A hint=(
    [docker]="Docker Desktop (Windows/macOS) or Docker Engine (Linux): https://docs.docker.com/get-docker/"
    [kind]="go install sigs.k8s.io/kind@latest  (or https://kind.sigs.k8s.io/docs/user/quick-start/#installation)"
    [kubectl]="https://kubernetes.io/docs/tasks/tools/ (Docker Desktop ships one)"
    [helm]="https://helm.sh/docs/intro/install/ (v3.12 or newer)"
    [go]="https://go.dev/dl/ (the version in API/go.mod)"
    [jq]="https://jqlang.org/download/ (winget install jqlang.jq on Windows)"
    [openssl]="ships with Git for Windows, macOS and most Linux distributions"
    [curl]="ships with Git for Windows, macOS and most Linux distributions"
  )
  for tool in docker kind kubectl helm go jq openssl curl; do
    if command -v "$tool" >/dev/null 2>&1; then
      info "ok      $tool"
    else
      info "MISSING $tool -> ${hint[$tool]}"
      missing=1
    fi
  done
  [ "$missing" = 0 ] || die "install the missing tools above and re-run (nothing is installed for you)"
  docker info >/dev/null 2>&1 ||
    die "docker is installed but not running. On Windows, Docker Desktop needs WSL 2: run 'wsl --install', reboot, then start Docker Desktop."
  local arch
  arch=$(docker info --format '{{.Architecture}}' | tr -d '\r')
  case "$arch" in
  x86_64 | amd64) ;;
  *) die "docker reports architecture '$arch'. API/Dockerfile and API/Dockerfile.agent hard-code GOARCH=amd64, so the images cannot run on this kind node. See README, Known blockers." ;;
  esac
}

# ── 2. Clusters ───────────────────────────────────────────────────────────────
create_clusters() {
  log "Creating kind clusters $HUB and $SPOKE on the shared 'kind' docker network"
  local name existing
  existing=$(kind get clusters 2>/dev/null | tr -d '\r')
  for name in "$HUB" "$SPOKE"; do
    if grep -qx "$name" <<<"$existing"; then
      info "cluster $name exists, reusing it"
    else
      local args=(create cluster --name "$name" --wait 180s)
      [ -n "${KIND_NODE_IMAGE:-}" ] && args+=(--image "$KIND_NODE_IMAGE")
      kind "${args[@]}"
    fi
    kind export kubeconfig --name "$name" >/dev/null
  done
  HUB_IP=$(docker inspect -f '{{(index .NetworkSettings.Networks "kind").IPAddress}}' "$HUB-control-plane" | tr -d '\r')
  [ -n "$HUB_IP" ] || die "could not read the hub node IP on the 'kind' network"
  info "hub node IP on the kind network: $HUB_IP"
  info "hub API (handshake):       http://$HUB_IP:$API_NODEPORT"
  info "hub agent mTLS listener:   https://$HUB_IP:$AGENT_NODEPORT"
  HUB_URL="http://$HUB_IP:$API_NODEPORT"
  HUB_AGENT_URL="https://$HUB_IP:$AGENT_NODEPORT"
}

# ── 3. Images ─────────────────────────────────────────────────────────────────
build_images() {
  if [ "${KIND_E2E_SKIP_BUILD:-0}" = "1" ]; then
    log "Skipping image build (KIND_E2E_SKIP_BUILD=1)"
    return
  fi
  log "Building images from the repo Dockerfiles"
  docker build -t "$API_IMAGE" -f API/Dockerfile API
  # One image carries both /agent and /agent-init; the agent chart runs the
  # init container from the same image.
  docker build -t "$AGENT_IMAGE" -f API/Dockerfile.agent API
  docker build -t "$PG_IMAGE" docker/postgres
  docker build -t "$WEBAPP_IMAGE" -f Webapp/Dockerfile Webapp
  log "Loading images into the clusters"
  kind load docker-image "$API_IMAGE" "$PG_IMAGE" "$WEBAPP_IMAGE" --name "$HUB"
  kind load docker-image "$AGENT_IMAGE" --name "$SPOKE"
}

# ── 4. Helm dependencies ──────────────────────────────────────────────────────
helm_deps() {
  log "Fetching chart dependencies"
  local repo
  for repo in \
    "postgres-operator-charts https://opensource.zalando.com/postgres-operator/charts/postgres-operator" \
    "openbao https://openbao.github.io/openbao-helm" \
    "victoriametrics https://victoriametrics.github.io/helm-charts/" \
    "argo https://argoproj.github.io/argo-helm" \
    "ingress-nginx https://kubernetes.github.io/ingress-nginx" \
    "external-dns https://kubernetes-sigs.github.io/external-dns" \
    "jetstack https://charts.jetstack.io"; do
    # shellcheck disable=SC2086
    helm repo add --force-update $repo >/dev/null
  done
  local chart
  for chart in charts/saidkick-hub charts/saidkick-agent; do
    # Helm wants every dependency present even when its condition is false.
    helm dependency build "$chart" >/dev/null 2>&1 || helm dependency update "$chart" >/dev/null
    info "dependencies ready for $chart"
  done
}

# ── 5. Hub: Postgres (scripts/kind-e2e/smoke-postgres.yaml) ──────────────────
hub_postgres() {
  log "Hub: Postgres (scripts/kind-e2e/smoke-postgres.yaml)"
  kh create namespace "$HUB_NS" --dry-run=client -o yaml | kh apply -f - >/dev/null
  # The manifest reads key "password" from this Secret; the value is generated.
  local key=password
  kh -n "$HUB_NS" create secret generic smoke-postgres \
    --from-literal="$key=$(secret DB_PASSWORD)" --dry-run=client -o yaml | kh apply -f - >/dev/null
  kh -n "$HUB_NS" apply -f scripts/kind-e2e/smoke-postgres.yaml >/dev/null
  kh -n "$HUB_NS" rollout status deploy/smoke-postgres --timeout=300s
}

# ── 6. Hub: chart install ─────────────────────────────────────────────────────
write_hub_values() {
  # Generated at runtime; holds secrets, so it lives in the gitignored .work dir.
  cat >"$WORK/hub-values.yaml" <<EOF
# Generated by scripts/kind-e2e/run.sh. Layered over ci/smoke-values.yaml.
api:
  image:
    repository: saidkick-api
    tag: $IMAGE_TAG
    pullPolicy: Never
  service:
    type: NodePort
  config:
    authBaseUrl: $HUB_URL
    clusterHubEndpoint: $HUB_URL
    clusterHubAgentEndpoint: $HUB_AGENT_URL
  # The point of this run: the agent mTLS listener is ON, with its serving
  # certificate and client CA issued from OpenBao PKI.
  agentListener:
    enabled: true
    servingCertSecret: saidkick-agent-listener-tls
    clientCASecret: saidkick-agent-client-ca
    service:
      type: NodePort
  secrets:
    authSessionSecret: "$(secret SESSION_SECRET)"
    clusterJoinSecret: "$(secret JOIN_SECRET)"
    databaseUrl: "postgres://postgres:$(secret DB_PASSWORD)@smoke-postgres:5432/saidkick_appstore?sslmode=disable"
    vaultAddr: "http://$RELEASE-openbao:8200"
    vaultToken: "${VAULT_API_TOKEN:-}"
# ci/smoke-values.yaml turns the webapp off; the run checks its nonce CSP.
webapp:
  enabled: true
  replicaCount: 1
  image:
    repository: saidkick-webapp
    tag: $IMAGE_TAG
    pullPolicy: Never
  config:
    apiBase: $HUB_URL
# The chart default; ci/smoke-values.yaml turns it off. The run checks that
# VictoriaMetrics scrapes the API metrics port with the chart's own scrape job.
victoriametrics:
  enabled: true
openbao:
  enabled: true
  injector:
    enabled: false
  server:
    dev:
      enabled: true
      devRootToken: "$BAO_ROOT_TOKEN"
EOF
}

hub_install() {
  log "Hub: helm upgrade --install $RELEASE (agentListener enabled, default orchestratorKind, OpenBao dev server)"
  BAO_ROOT_TOKEN=$(secret BAO_ROOT_TOKEN)
  VAULT_API_TOKEN=$(sed -n 's/^VAULT_API_TOKEN=//p' "$SECRETS_FILE" 2>/dev/null || true)
  write_hub_values
  # No --wait: the API pod cannot start until the agent listener Secrets
  # exist, and those are issued from the OpenBao this release installs.
  helm --kube-context "$HUB_CTX" upgrade --install "$RELEASE" charts/saidkick-hub \
    --namespace "$HUB_NS" \
    --values charts/saidkick-hub/ci/smoke-values.yaml \
    --values "$WORK/hub-values.yaml" >/dev/null
  # Fixed NodePorts so the hub URLs are known before the API starts.
  kh -n "$HUB_NS" patch svc "$API_DEPLOY" --type=json \
    -p "[{\"op\":\"replace\",\"path\":\"/spec/ports/0/nodePort\",\"value\":$API_NODEPORT}]" >/dev/null
  kh -n "$HUB_NS" patch svc "$API_DEPLOY-agent" --type=json \
    -p "[{\"op\":\"replace\",\"path\":\"/spec/ports/0/nodePort\",\"value\":$AGENT_NODEPORT}]" >/dev/null
  wait_until 180 3 "OpenBao pod to exist" kh -n "$HUB_NS" get pod "$BAO_POD" >/dev/null 2>&1 ||
    die "OpenBao pod $BAO_POD never appeared"
  kh -n "$HUB_NS" wait --for=condition=Ready "pod/$BAO_POD" --timeout=300s >/dev/null
}

# ── 7. Hub: OpenBao PKI, KV and the agent listener certificates ──────────────
bao_has_mount() { bao secrets list -format=json | jqr -e --arg m "$1" 'has($m)' >/dev/null; }

hub_pki() {
  log "Hub: OpenBao KV, PKI and API token (commands from the hub NOTES.txt / RUNBOOK)"
  local fresh=0
  if ! bao_has_mount "kv/"; then
    bao secrets enable -path=kv kv-v2 >/dev/null
  fi
  if ! bao_has_mount "pki/"; then
    fresh=1
    bao secrets enable pki >/dev/null
    bao secrets tune -max-lease-ttl=87600h pki >/dev/null
    bao write -field=certificate pki/root/generate/internal \
      common_name="sAId-Kick Hub CA" ttl=87600h >/dev/null
  fi
  # Role exactly as documented in charts/saidkick-hub/templates/NOTES.txt,
  # API/RUNBOOK.md and charts/saidkick-hub/README.md. Written on every run so
  # a resumed run picks up documentation changes.
  bao write pki/roles/agent \
    allowed_domains="saidkick-agent-*" allow_glob_domains=true \
    use_csr_common_name=false use_csr_sans=false \
    server_flag=false client_flag=true max_ttl=2160h >/dev/null
  # The handshake signs CN=saidkick-agent-<cluster-uuid> (handlers/clusters.go:286).
  if bao write pki/issue/agent common_name="saidkick-agent-00000000-0000-0000-0000-000000000000" ttl=1h \
    >"$WORK/pki-probe.log" 2>&1; then
    record PASS "Documented PKI role signs the agent CN" "pki/roles/agent issues CN saidkick-agent-<uuid>"
  else
    record FAIL "Documented PKI role signs the agent CN" "$(tr -d '\r' <"$WORK/pki-probe.log" | grep -m1 '^\*' || echo 'issue failed')"
    die "the documented PKI role cannot issue agent certificates"
  fi
  bao policy write saidkick-api - >/dev/null <<'EOF'
path "kv/data/deployments/*"   { capabilities = ["create","update","read","delete"] }
path "kv/metadata/deployments/*" { capabilities = ["list","delete"] }
path "pki/sign/agent"          { capabilities = ["update"] }
EOF
  if [ "$fresh" = 1 ] || ! grep -q '^VAULT_API_TOKEN=' "$SECRETS_FILE"; then
    VAULT_API_TOKEN=$(bao token create -policy=saidkick-api -period=720h -field=token | tr -d '\r')
    grep -v '^VAULT_API_TOKEN=' "$SECRETS_FILE" >"$SECRETS_FILE.tmp" || true
    printf 'VAULT_API_TOKEN=%s\n' "$VAULT_API_TOKEN" >>"$SECRETS_FILE.tmp"
    mv "$SECRETS_FILE.tmp" "$SECRETS_FILE"
  fi

  if [ "$fresh" = 1 ] || ! kh -n "$HUB_NS" get secret saidkick-agent-listener-tls >/dev/null 2>&1; then
    log "Hub: agent listener client CA and serving certificate (commands from charts/saidkick-hub/README.md)"
    local domain="agents.$HUB.kind"
    bao read -field=certificate pki/cert/ca | tr -d '\r' >"$WORK/agent-client-ca.crt"
    kh -n "$HUB_NS" create secret generic saidkick-agent-client-ca \
      --from-file=ca.crt="$WORK/agent-client-ca.crt" --dry-run=client -o yaml | kh apply -f - >/dev/null
    bao write pki/roles/hub-agent-listener allowed_domains="$domain" \
      allow_bare_domains=true server_flag=true client_flag=false max_ttl=2160h >/dev/null
    # ip_sans: agents dial the listener by the hub node IP on the kind network.
    bao write -format=json pki/issue/hub-agent-listener common_name="$domain" ip_sans="$HUB_IP" \
      | tr -d '\r' >"$WORK/listener-issued.json"
    jqr -r .data.certificate "$WORK/listener-issued.json" >"$WORK/listener-tls.crt"
    jqr -r .data.private_key "$WORK/listener-issued.json" >"$WORK/listener-tls.key"
    kh -n "$HUB_NS" create secret tls saidkick-agent-listener-tls \
      --cert="$WORK/listener-tls.crt" --key="$WORK/listener-tls.key" --dry-run=client -o yaml | kh apply -f - >/dev/null
    rm -f "$WORK/listener-issued.json" "$WORK/listener-tls.key"
    # The API reads both Secrets only at startup.
    kh -n "$HUB_NS" rollout restart "deploy/$API_DEPLOY" >/dev/null 2>&1 || true
  fi
}

hub_finish() {
  log "Hub: upgrade with the OpenBao API token and enable dev-mode header auth for the human API calls"
  write_hub_values
  helm --kube-context "$HUB_CTX" upgrade --install "$RELEASE" charts/saidkick-hub \
    --namespace "$HUB_NS" \
    --values charts/saidkick-hub/ci/smoke-values.yaml \
    --values "$WORK/hub-values.yaml" >/dev/null
  # The chart hardcodes APP_DEV_MODE=false; an explicit env entry overrides it. Only the human API
  # calls use it; agent traffic never sends the dev header. Turned off again
  # before the mTLS checks.
  kh -n "$HUB_NS" set env "deploy/$API_DEPLOY" APP_DEV_MODE=true >/dev/null
  if ! kh -n "$HUB_NS" rollout status "deploy/$API_DEPLOY" --timeout=300s; then
    kh -n "$HUB_NS" describe pods -l app.kubernetes.io/component=api | tail -40 || true
    kh -n "$HUB_NS" logs "deploy/$API_DEPLOY" --tail=80 || true
    die "hub API did not become ready"
  fi
  pf_api
  wait_until 60 2 "hub /readyz" curl -fsS "http://127.0.0.1:$LOCAL_API_PORT/readyz" >/dev/null ||
    die "hub API /readyz failed"
  # Neither values file sets orchestratorKind: the chart default has to wire
  # the agent-brokered deployer (#63).
  local kind wired
  kind=$(kh -n "$HUB_NS" get configmap "$API_DEPLOY" -o jsonpath='{.data.ORCHESTRATOR_KIND}' | tr -d '\r')
  wired=$(kh -n "$HUB_NS" logs "deploy/$API_DEPLOY" | grep -c 'agent-brokered orchestrator wired' || true)
  if [ "$kind" = "agent" ] && [ "$wired" != "0" ]; then
    record PASS "Hub wires the agent-brokered deployer by default" "ORCHESTRATOR_KIND=$kind from the chart default; API logged 'agent-brokered orchestrator wired'"
  else
    record FAIL "Hub wires the agent-brokered deployer by default" "ORCHESTRATOR_KIND=${kind:-unset}, 'agent-brokered orchestrator wired' lines: $wired"
  fi
  local listener
  listener=$(kh -n "$HUB_NS" logs "deploy/$API_DEPLOY" | grep -c 'agent mTLS listener disabled' || true)
  if [ "$listener" != "0" ]; then
    record FAIL "Hub starts the agent mTLS listener" "API logged 'agent mTLS listener disabled'"
  else
    record PASS "Hub starts the agent mTLS listener" "AGENT_TLS_* set from Vault-issued Secrets, Service $API_DEPLOY-agent NodePort $AGENT_NODEPORT"
  fi
}

# ── 7a. Webapp: nonce CSP (#137) ──────────────────────────────────────────────
# fetch_webapp N saves the headers and body of one /catalogus response.
fetch_webapp() {
  curl -sS --max-time 30 -D "$WORK/webapp-$1.headers" -o "$WORK/webapp-$1.html" \
    "http://127.0.0.1:$LOCAL_WEBAPP_PORT/catalogus"
}
# header_value FILE NAME prints the first value of a response header.
header_value() { tr -d '\r' <"$1" | sed -n "s/^$2: //Ip" | head -1; }

webapp_csp_check() {
  local criterion="Webapp HTML carries a nonce CSP and nonced scripts (#137)"
  log "Webapp: nonce Content-Security-Policy (#137)"
  if ! kh -n "$HUB_NS" rollout status "deploy/$WEBAPP_DEPLOY" --timeout=300s; then
    record FAIL "$criterion" "deploy/$WEBAPP_DEPLOY did not become ready"
    return
  fi
  start_pf webapp "$HUB_CTX" "$HUB_NS" "svc/$WEBAPP_DEPLOY" "$LOCAL_WEBAPP_PORT" 80
  if ! fetch_webapp 1 || ! fetch_webapp 2; then
    record FAIL "$criterion" "GET /catalogus failed"
    stop_pf webapp
    return
  fi
  stop_pf webapp

  local csp nonce nonce2 cache tags scripts nonced inline preloads preloads_nonced
  csp=$(header_value "$WORK/webapp-1.headers" content-security-policy)
  nonce=$(sed -n "s/.*script-src 'self' 'nonce-\([^']*\)' 'strict-dynamic'.*/\1/p" <<<"$csp")
  nonce2=$(header_value "$WORK/webapp-2.headers" content-security-policy | sed -n "s/.*script-src 'self' 'nonce-\([^']*\)'.*/\1/p")
  cache=$(header_value "$WORK/webapp-1.headers" cache-control)
  tags=$(grep -o '<script[^>]*>' "$WORK/webapp-1.html" || true)
  scripts=$(grep -c . <<<"$tags" || true)
  nonced=$(grep -cF "nonce=\"$nonce\"" <<<"$tags" || true)
  inline=$(grep -v ' src=' <<<"$tags" | grep -cF "nonce=\"$nonce\"" || true)
  tags=$(grep -o '<link[^>]*rel="modulepreload"[^>]*>' "$WORK/webapp-1.html" || true)
  preloads=$(grep -c . <<<"$tags" || true)
  preloads_nonced=$(grep -cF "nonce=\"$nonce\"" <<<"$tags" || true)

  if [ -n "$nonce" ] && [ -n "$nonce2" ] && [ "$nonce" != "$nonce2" ] &&
    grep -qF "style-src 'self' 'nonce-$nonce'" <<<"$csp" && [ "$cache" = "no-store" ] &&
    [ "$scripts" -gt 0 ] && [ "$nonced" = "$scripts" ] && [ "$inline" -ge 1 ] &&
    [ "$preloads_nonced" = "$preloads" ]; then
    record PASS "$criterion" "script-src 'nonce-...' 'strict-dynamic'; $nonced/$scripts scripts ($inline inline) and $preloads_nonced/$preloads modulepreload links nonced; a second response got a new nonce; Cache-Control no-store"
  else
    record FAIL "$criterion" "nonce=${nonce:-none} second=${nonce2:-none} cache=${cache:-none} scripts nonced $nonced/$scripts (inline $inline) modulepreload $preloads_nonced/$preloads; CSP: ${csp:-none}"
  fi
}

# ── 8. Seed users (cmd/seed, as in deploy-smoke.yml) ─────────────────────────
seed_users() {
  log "Seeding orgs and users with cmd/seed"
  start_pf pg "$HUB_CTX" "$HUB_NS" svc/smoke-postgres "$LOCAL_PG_PORT" 5432
  local out pg_pass
  pg_pass=$(secret DB_PASSWORD)
  out=$(cd API && DATABASE_URL="postgres://postgres:$pg_pass@127.0.0.1:$LOCAL_PG_PORT/saidkick_appstore?sslmode=disable" \
    APP_ENV=ci APP_DEV_MODE=true go run ./cmd/seed "../$TESTDATA/seed.json" 2>&1) || {
    echo "$out"
    die "seed failed"
  }
  stop_pf pg
  user_id() { grep '"user applied"' <<<"$out" | jqr -r --arg e "$1" 'select(.email == $e) | .id' | head -1; }
  org_id() { grep '"org applied"' <<<"$out" | jqr -r --arg s "$1" 'select(.slug == $s) | .id' | head -1; }
  state_set ADMIN_ID "$(user_id admin@kind-e2e.test)"
  state_set DEV_ID "$(user_id dev@kind-e2e.test)"
  state_set AUDITOR_ID "$(user_id auditor@kind-e2e.test)"
  state_set APPS_ORG_ID "$(org_id kind-e2e-apps)"
  [ -n "$ADMIN_ID" ] && [ -n "$DEV_ID" ] && [ -n "$AUDITOR_ID" ] && [ -n "$APPS_ORG_ID" ] ||
    die "could not read seeded IDs from: $out"
  info "admin=$ADMIN_ID developer/deployer=$DEV_ID auditor=$AUDITOR_ID"
}

# ── 9. Spoke: Argo CD ─────────────────────────────────────────────────────────
spoke_argocd() {
  log "Spoke: Argo CD $ARGOCD_VERSION (upstream install manifest)"
  ks create namespace argocd --dry-run=client -o yaml | ks apply -f - >/dev/null
  ks -n argocd apply --server-side --force-conflicts \
    -f "https://raw.githubusercontent.com/argoproj/argo-cd/$ARGOCD_VERSION/manifests/install.yaml" >/dev/null
  # Serve plain HTTP inside the cluster: the agent's Argo CD client uses the
  # default TLS trust store and cannot verify the self-signed server cert.
  local insecure
  insecure=$(ks -n argocd get cm argocd-cmd-params-cm -o jsonpath='{.data.server\.insecure}' 2>/dev/null | tr -d '\r')
  if [ "$insecure" != "true" ]; then
    ks -n argocd patch cm argocd-cmd-params-cm --type merge -p '{"data":{"server.insecure":"true"}}' >/dev/null
    ks -n argocd rollout restart deploy/argocd-server >/dev/null
  fi
  local d
  for d in argocd-server argocd-repo-server argocd-redis; do
    ks -n argocd rollout status "deploy/$d" --timeout=300s >/dev/null
  done
  ks -n argocd rollout status statefulset/argocd-application-controller --timeout=300s >/dev/null

  local pass
  pass=$(ks -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | tr -d '\r' | base64 -d)
  start_pf argo "$SPOKE_CTX" argocd svc/argocd-server "$LOCAL_ARGO_PORT" 80
  ARGOCD_TOKEN=""
  wait_until 60 3 "Argo CD session token" argo_login || die "could not log in to Argo CD"
  stop_pf argo
  info "Argo CD admin session token obtained"
  unset pass
}
argo_login() {
  ARGOCD_TOKEN=$(curl -fsS --max-time 10 -X POST "http://127.0.0.1:$LOCAL_ARGO_PORT/api/v1/session" \
    -H 'Content-Type: application/json' \
    --data-binary "$(jq -cn --arg p "$pass" '{username:"admin",password:$p}')" 2>/dev/null | jqr -r '.token // empty')
  [ -n "$ARGOCD_TOKEN" ]
}

# ── 10. Register the spoke and install the agent ─────────────────────────────
cluster_state() {
  psql_hub -c "SELECT state FROM clusters WHERE id = '$1'" 2>/dev/null || true
}

register_spoke() {
  if [ -n "${CLUSTER_ID:-}" ] && [ "$(cluster_state "$CLUSTER_ID")" = "ready" ] &&
    ks -n "$AGENT_NS" get deploy saidkick-agent >/dev/null 2>&1; then
    log "Spoke already registered as cluster $CLUSTER_ID, skipping registration"
    return
  fi
  log "Registering the spoke on the hub (POST /api/v1/clusters as platform admin)"
  api_ok POST /api/v1/clusters "$ADMIN_ID" \
    "{\"name\":\"kind-spoke-$(date +%s)\",\"description\":\"kind e2e spoke cluster\"}"
  printf '%s' "$API_BODY" >"$WORK/cluster-create.json"
  state_set CLUSTER_ID "$(jqr -r .cluster.id "$WORK/cluster-create.json")"
  jqr -r .installer.helm_command "$WORK/cluster-create.json" >"$WORK/agent-install-command.txt"
  jqr -r '.installer.steps[0].command' "$WORK/cluster-create.json" >"$WORK/eso-install-command.txt"
  info "cluster id: $CLUSTER_ID"
  info "hub-issued install commands saved to $WORK/eso-install-command.txt and $WORK/agent-install-command.txt (the agent one contains the one-time join token)"
  grep -q -- "--set agent.hubAgentUrl=$HUB_AGENT_URL" "$WORK/agent-install-command.txt" &&
    record PASS "Install command carries the agent listener URL" "agent.hubAgentUrl=$HUB_AGENT_URL" ||
    record FAIL "Install command carries the agent listener URL" "$(tr '\n' ' ' <"$WORK/agent-install-command.txt" | cut -c1-200)"
  # #87: the published OCI chart at the hub chart's appVersion
  # (AGENT_CHART_REPO default, AGENT_CHART_VERSION from the hub chart).
  local chart_ref want_ref
  chart_ref=$(head -1 "$WORK/agent-install-command.txt" | sed 's/ *\\$//')
  want_ref="helm install saidkick-agent oci://ghcr.io/clappformorg/charts/saidkick-agent --version $(sed -n 's/^appVersion: *"\{0,1\}\([^"]*\)"\{0,1\}$/\1/p' charts/saidkick-hub/Chart.yaml | tr -d '\r')"
  [ "$chart_ref" = "$want_ref" ] &&
    record PASS "Install command pulls the published OCI chart (#87)" "$chart_ref" ||
    record FAIL "Install command pulls the published OCI chart (#87)" "got '$chart_ref', want '$want_ref'"
  local steps eso_version
  steps=$(jqr -r '[.installer.steps[].name] | join(",")' "$WORK/cluster-create.json")
  eso_version=$(sed -n 's/^ *--version \([^ ]*\).*/\1/p' "$WORK/eso-install-command.txt")
  if [ "$steps" = "external-secrets,saidkick-agent" ] && [ -n "$eso_version" ] &&
    jqr -e '.installer.steps[1].command == .installer.helm_command' "$WORK/cluster-create.json" >/dev/null &&
    grep -qF -- "--version $eso_version" charts/saidkick-agent/README.md; then
    record PASS "Installer lists the ESO step before the agent step (#140)" "steps: $steps; ESO chart $eso_version, as in the agent README; helm_command = agent step"
  else
    record FAIL "Installer lists the ESO step before the agent step (#140)" "steps: ${steps:-none}; ESO version '${eso_version:-none}'"
    die "unexpected installer steps"
  fi

  log "Spoke: External Secrets Operator $eso_version with the hub's first step"
  # Exactly the printed command, made re-runnable and pointed at the spoke.
  local eso_cmd
  eso_cmd=$(sed 's#^helm install external-secrets #helm upgrade --install external-secrets #' "$WORK/eso-install-command.txt")
  grep -q '^helm upgrade --install external-secrets external-secrets --repo ' <<<"$eso_cmd" ||
    die "unexpected ESO install command shape: $(head -1 "$WORK/eso-install-command.txt")"
  if eval "$eso_cmd --kube-context $SPOKE_CTX" >"$WORK/eso-install.log" 2>&1; then
    record PASS "ESO installs from the hub's first step" "chart external-secrets $eso_version, --wait returned"
  else
    tail -20 "$WORK/eso-install.log" >&2
    record FAIL "ESO installs from the hub's first step" "helm exited non-zero; see $WORK/eso-install.log"
    die "ESO install failed"
  fi

  # The SecretStore dials vault.addr from the spoke: publish the dev OpenBao
  # on a fixed NodePort of the hub node (test-only; same selector as the
  # chart's own Service).
  kh -n "$HUB_NS" expose svc "$RELEASE-openbao" --name "$RELEASE-openbao-e2e" --type NodePort \
    --port 8200 --target-port 8200 --dry-run=client -o json |
    jqr --argjson p "$VAULT_NODEPORT" '.spec.ports[0].nodePort = $p' | kh -n "$HUB_NS" apply -f - >/dev/null
  # A read token scoped to this cluster's deployment secrets, as the agent
  # README's Vault token scoping section describes.
  bao policy write "saidkick-eso-$CLUSTER_ID" - >/dev/null <<EOF
path "kv/data/deployments/$CLUSTER_ID/*" { capabilities = ["read", "list"] }
EOF
  ESO_VAULT_TOKEN=$(bao token create -policy="saidkick-eso-$CLUSTER_ID" -period=720h -field=token | tr -d '\r')

  log "Spoke: installing the agent with the hub's helm command (SecretStore on)"
  # A previous cluster's credentials Secret is kept by resource-policy; start clean.
  if ks get namespace "$AGENT_NS" >/dev/null 2>&1; then
    helm --kube-context "$SPOKE_CTX" uninstall saidkick-agent -n "$AGENT_NS" >/dev/null 2>&1 || true
    ks delete namespace "$AGENT_NS" --wait=true --timeout=180s >/dev/null
  fi
  # The hub prints a command against the published chart repo. Point it at
  # the chart in this checkout and the locally built image; every other flag
  # is used exactly as the hub returned it.
  local cmd
  cmd=$(sed 's#^helm install saidkick-agent oci://[^ ]*/saidkick-agent\( --version [^ ]*\)\{0,1\}#helm upgrade --install saidkick-agent charts/saidkick-agent#' \
    "$WORK/agent-install-command.txt")
  grep -q '^helm upgrade --install saidkick-agent charts/saidkick-agent' <<<"$cmd" ||
    die "unexpected install command shape: $(head -1 "$WORK/agent-install-command.txt")"
  # Overrides, all test-only:
  #  - local image, never pulled
  #  - Argo CD comes from the upstream manifest instead of the subchart
  #  - short loops so the run finishes in minutes
  #  - a renewal threshold the short test TTL crosses (cert_rotation_checks)
  # The SecretStore stays at the chart default (on), against the ESO above.
  cmd="$cmd --kube-context $SPOKE_CTX \
    --set agent.image.repository=saidkick-agent --set agent.image.tag=$IMAGE_TAG --set agent.image.pullPolicy=Never \
    --set argocd.enabled=false \
    --set agent.heartbeatInterval=10s --set agent.statusInterval=10s \
    --set agent.certRenewBefore=$CERT_RENEW_BEFORE"
  export ARGOCD_SERVER_URL="http://argocd-server.argocd.svc.cluster.local"
  export ARGOCD_AUTH_TOKEN="$ARGOCD_TOKEN"
  export VAULT_ADDR="http://$HUB_IP:$VAULT_NODEPORT"
  export VAULT_TOKEN="$ESO_VAULT_TOKEN"
  if eval "$cmd" >"$WORK/agent-install.log" 2>&1; then
    record PASS "Agent chart installs with its SecretStore in one pass (#140)" "helm upgrade --install succeeded with secretStore.enabled=true after the ESO step"
  else
    tail -20 "$WORK/agent-install.log" >&2
    record FAIL "Agent chart installs with its SecretStore in one pass (#140)" "$(grep -m1 -i error "$WORK/agent-install.log" | cut -c1-200)"
    die "agent install failed"
  fi
  unset VAULT_TOKEN ESO_VAULT_TOKEN
}

# ── 10a. Spoke: ESO SecretStore (#140) ───────────────────────────────────────
secretstore_ready() {
  [ "$(ks -n "$AGENT_NS" get secretstore saidkick-openbao \
    -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null | tr -d '\r')" = "True" ]
}
probe_secret_value() {
  ks -n "$AGENT_NS" get secret kind-e2e-eso-probe -o jsonpath='{.data.value}' 2>/dev/null | tr -d '\r' | base64 -d 2>/dev/null
}
eso_checks() {
  log "Spoke: the agent chart's SecretStore pulls from the hub OpenBao"
  local kind
  kind=$(ks -n "$AGENT_NS" get secretstore saidkick-openbao -o jsonpath='{.apiVersion} {.kind}' 2>/dev/null | tr -d '\r' || true)
  if [ "$kind" = "external-secrets.io/v1 SecretStore" ] && wait_until 120 5 "SecretStore Ready" secretstore_ready; then
    record PASS "SecretStore rendered and Ready" "$kind saidkick-openbao in $AGENT_NS, Ready=True against http://$HUB_IP:$VAULT_NODEPORT"
  else
    ks -n "$AGENT_NS" describe secretstore saidkick-openbao 2>/dev/null | tail -15 || true
    record FAIL "SecretStore rendered and Ready" "${kind:-no SecretStore}; Ready=$(ks -n "$AGENT_NS" get secretstore saidkick-openbao -o jsonpath='{.status.conditions[0].message}' 2>/dev/null)"
    return
  fi
  # One secret under this cluster's path, pulled through the store with the
  # cluster-scoped token.
  local want
  want="probe-$(date +%s)"
  bao kv put "kv/deployments/$CLUSTER_ID/kind-e2e-eso-probe" value="$want" >/dev/null
  ks -n "$AGENT_NS" apply -f - >/dev/null <<EOF
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: kind-e2e-eso-probe
spec:
  refreshInterval: 10s
  secretStoreRef:
    name: saidkick-openbao
    kind: SecretStore
  target:
    name: kind-e2e-eso-probe
  data:
    - secretKey: value
      remoteRef:
        key: deployments/$CLUSTER_ID/kind-e2e-eso-probe
        property: value
EOF
  if wait_until 120 5 "ExternalSecret synced" eval '[ "$(probe_secret_value)" = "$want" ]'; then
    record PASS "ExternalSecret syncs through the SecretStore" "kv/deployments/<cluster-id>/kind-e2e-eso-probe -> Secret kind-e2e-eso-probe"
  else
    ks -n "$AGENT_NS" describe externalsecret kind-e2e-eso-probe 2>/dev/null | tail -15 || true
    record FAIL "ExternalSecret syncs through the SecretStore" "Secret kind-e2e-eso-probe not populated with the stored value"
  fi
  ks -n "$AGENT_NS" delete externalsecret kind-e2e-eso-probe --ignore-not-found >/dev/null
  ks -n "$AGENT_NS" delete secret kind-e2e-eso-probe --ignore-not-found >/dev/null
}

# ── 11. Handshake and heartbeats ──────────────────────────────────────────────
heartbeat_epoch() {
  psql_hub -c "SELECT coalesce(extract(epoch FROM last_heartbeat_at)::bigint, 0) FROM clusters WHERE id = '$CLUSTER_ID'"
}
cluster_ready() { [ "$(cluster_state "$CLUSTER_ID")" = "ready" ]; }
heartbeat_after() { [ "$(heartbeat_epoch)" -gt "$1" ]; }
agent_pod_ready() {
  ks -n "$AGENT_NS" wait --for=condition=Ready pod -l app.kubernetes.io/component=agent --timeout=5s >/dev/null 2>&1
}

wait_handshake() {
  log "Waiting for the agent handshake (cluster state ready)"
  if ! wait_until 300 5 "cluster $CLUSTER_ID ready" cluster_ready; then
    ks -n "$AGENT_NS" logs -l app.kubernetes.io/component=agent -c agent-init --tail=50 || true
    kh -n "$HUB_NS" logs "deploy/$API_DEPLOY" --tail=50 | grep -i handshake || true
    record FAIL "Agent handshake (join token -> Vault-signed client cert)" "cluster state $(cluster_state "$CLUSTER_ID")"
    die "handshake did not complete"
  fi
  local serial
  serial=$(psql_hub -c "SELECT coalesce(agent_cert_serial, '') FROM clusters WHERE id = '$CLUSTER_ID'")
  record PASS "Agent handshake (join token -> Vault-signed client cert)" "cluster ready, agent_cert_serial=$serial"

  log "Waiting for heartbeats over the agent mTLS listener"
  local t0
  t0=$(heartbeat_epoch)
  if wait_until 300 5 "agent pod Ready" agent_pod_ready &&
    wait_until 120 5 "a heartbeat newer than the handshake" heartbeat_after "$t0"; then
    local hub_line
    hub_line=$(ks -n "$AGENT_NS" logs deploy/saidkick-agent -c agent 2>/dev/null | grep -m1 'saidkick-agent starting' | tr -d '\r' || true)
    record PASS "Heartbeats over the agent mTLS listener" "last_heartbeat_at advances; agent: $(jqr -r '.hub' <<<"$hub_line" 2>/dev/null)"
  else
    ks -n "$AGENT_NS" logs deploy/saidkick-agent -c agent --tail=40 || true
    record FAIL "Heartbeats over the agent mTLS listener" "no heartbeat after handshake"
    die "agent is not heartbeating"
  fi
}

# ── 12. App, versions, submission and approval ───────────────────────────────
# CreateApp and CreateAppVersion read the repo through a GitHub App
# (handlers/apps.go:87-99, 402-407), which a local run does not have. The
# rows those handlers would write are inserted directly; submission and
# approval go through the API.
MANIFEST_JSON="{\"apiVersion\":\"appstore.gov.nl/v1\",\"kind\":\"Application\",\"metadata\":{\"name\":\"$APP_SLUG\"},\"spec\":{\"chart\":{\"path\":\"$APP_CHART_PATH\"},\"ai\":{\"risk_class\":\"minimal\"}}}"
SCHEMA_JSON='{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"replicaCount":{"type":"integer","minimum":1,"maximum":3}}}'
CHART_META_JSON="{\"name\":\"$APP_CHART_PATH\",\"version\":\"0.1.0\",\"appVersion\":\"1.0\"}"

ensure_app() {
  APP_ID=$(psql_hub -c "SELECT id FROM apps WHERE slug = '$APP_SLUG' AND deleted_at IS NULL")
  if [ -z "$APP_ID" ]; then
    APP_ID=$(psql_hub -c "INSERT INTO apps (id, slug, owning_organization_id, owning_user_id,
        github_installation_id, github_repo_owner, github_repo_name, github_default_branch,
        state, repo_link_state, created_at, updated_at)
      VALUES (gen_random_uuid(), '$APP_SLUG', '$APPS_ORG_ID', '$DEV_ID', 1,
        '$APP_REPO_OWNER', '$APP_REPO_NAME', 'master', 'draft', 'connected', now(), now())
      RETURNING id" | head -1)
  fi
  state_set APP_ID "$APP_ID"
}

# ensure_version SHA prints the version id, inserting a draft version with its
# audit submission when it does not exist yet.
ensure_version() {
  local sha=$1 id
  id=$(psql_hub -c "SELECT id FROM app_versions WHERE app_id = '$APP_ID' AND git_sha = '$sha'")
  if [ -z "$id" ]; then
    id=$(psql_hub -c "WITH ids AS (SELECT gen_random_uuid() AS vid, gen_random_uuid() AS sid),
      sub AS (INSERT INTO audit_submissions (id, app_version_id, form_responses, form_version,
                evidence_files, auto_validation_state, created_at)
              SELECT sid, vid, '{}', 'v1.0', '[]', 'passed', now() FROM ids RETURNING id),
      ver AS (INSERT INTO app_versions (id, app_id, git_ref, git_sha, appstore_yaml, values_schema,
                chart_metadata, audit_submission_id, state, created_at)
              SELECT vid, '$APP_ID', '$sha', '$sha', '$MANIFEST_JSON'::jsonb, '$SCHEMA_JSON'::jsonb,
                '$CHART_META_JSON'::jsonb, sid, 'draft', now() FROM ids RETURNING id)
      SELECT ver.id FROM ver, sub" | head -1)
  fi
  printf '%s' "$id"
}
version_state() { psql_hub -c "SELECT state FROM app_versions WHERE id = '$1'"; }

# publish_version VERSION_ID: developer submits, auditor from another org approves.
publish_version() {
  local vid=$1 sub
  if [ "$(version_state "$vid")" = "published" ]; then
    info "version $vid already published"
    return
  fi
  sub=$(psql_hub -c "SELECT audit_submission_id FROM app_versions WHERE id = '$vid'")
  if [ -n "$(psql_hub -c "SELECT 1 FROM audit_decisions WHERE audit_submission_id = '$sub'")" ]; then
    # Resuming a run that already approved a later version, which archived
    # this one. Archive the current one the same way the handler does (#116).
    psql_hub -c "UPDATE app_versions SET state = 'archived' WHERE app_id = '$APP_ID' AND state = 'published'" >/dev/null
    psql_hub -c "UPDATE app_versions SET state = 'published' WHERE id = '$vid'" >/dev/null
    info "version $vid was approved in an earlier run; published again to resume"
    return
  fi
  if [ "$(version_state "$vid")" = "draft" ]; then
    api_ok POST "/api/v1/apps/$APP_SLUG/submit" "$DEV_ID" '{}'
  fi
  local decision='{"decision":"approve","rationale":"kind e2e: pinned public chart reviewed for the local run"}'
  api POST "/api/v1/audit-submissions/$sub/decision" "$AUDITOR_ID" "$decision"
  case "$API_CODE" in
  2??) ;;
  *) die "audit decision returned $API_CODE: $API_BODY" ;;
  esac
  [ "$(version_state "$vid")" = "published" ] || die "version $vid not published after approval"
  info "version $vid submitted (developer) and approved (auditor, other org): published"
}

# ── 13. Deployments ───────────────────────────────────────────────────────────
dep_state() { psql_hub -c "SELECT state FROM deployments WHERE id = '$1'"; }
dep_app_name() { psql_hub -c "SELECT coalesce(last_argocd_app_name, '') FROM deployments WHERE id = '$1'"; }
argo_status() {
  ks -n argocd get application "$1" -o jsonpath='{.status.sync.status}/{.status.health.status}' 2>/dev/null | tr -d '\r'
}
argo_revision() {
  ks -n argocd get application "$1" -o jsonpath='{.spec.source.targetRevision}' 2>/dev/null | tr -d '\r'
}
argo_at() { [ "$(argo_revision "$1")" = "$2" ]; }
command_acked() {
  [ -n "$(psql_hub -c "SELECT 1 FROM agent_commands WHERE id = '$1' AND acked_at IS NOT NULL")" ]
}

# wait_ready DEPLOYMENT_ID LABEL waits for state ready, set only by the
# agent's reports. Returns 1 on failure.
wait_ready() {
  local dep=$1 label=$2 deadline=$((SECONDS + 600)) state app last=""
  while :; do
    state=$(dep_state "$dep")
    app=$(dep_app_name "$dep")
    local argo=""
    [ -n "$app" ] && argo=$(argo_status "$app")
    if [ "$state/$argo" != "$last" ]; then
      info "$label: hub state=$state argo=${argo:-n/a} app=${app:-n/a}"
      last="$state/$argo"
    fi
    case "$state" in
    ready) return 0 ;;
    failed_* | degraded) return 1 ;;
    esac
    if [ "$SECONDS" -ge "$deadline" ]; then
      info "$label: timed out in state $state"
      return 1
    fi
    sleep 5
  done
}

# check_workload NAMESPACE IMAGE_SUBSTRING REPLICAS
workload_running() {
  local ns=$1 image=$2 want=$3 got
  got=$(ks -n "$ns" get pods -o jsonpath='{range .items[*]}{.status.phase} {.spec.containers[0].image}{"\n"}{end}' 2>/dev/null |
    tr -d '\r' | grep '^Running ' | grep -c "$image" || true)
  [ "$got" -ge "$want" ]
}

deploy_flow() {
  log "App: register, submit and approve version 1 ($APP_V1_SHA)"
  ensure_app
  V1_ID=$(ensure_version "$APP_V1_SHA")
  state_set V1_ID "$V1_ID"
  publish_version "$V1_ID"

  local prefix="${APPS_ORG_ID: -8}"
  APP_NS_EFFECTIVE="$prefix-$APP_NAMESPACE"

  if [ -z "${DEP_ID:-}" ] || [ -z "$(dep_state "$DEP_ID")" ]; then
    log "Deploying version 1 to the spoke (POST /api/v1/deployments as deployer)"
    api_ok POST /api/v1/deployments "$DEV_ID" \
      "{\"app_id\":\"$APP_ID\",\"app_version_id\":\"$V1_ID\",\"cluster_id\":\"$CLUSTER_ID\",\"namespace\":\"$APP_NAMESPACE\",\"values\":{\"replicaCount\":1}}"
    state_set DEP_ID "$(jqr -r .id <<<"$API_BODY")"
    info "deployment $DEP_ID accepted, state $(jqr -r .state <<<"$API_BODY")"
  fi
  if wait_ready "$DEP_ID" "deploy v1" &&
    wait_until 180 5 "v1 workload Running" workload_running "$APP_NS_EFFECTIVE" "$APP_IMAGE" 1; then
    record PASS "Deployment completes through the agent (#63)" "deployment $DEP_ID ready; pod Running in $APP_NS_EFFECTIVE with $APP_IMAGE"
  else
    record FAIL "Deployment completes through the agent (#63)" "state $(dep_state "$DEP_ID")"
    ks -n "$AGENT_NS" logs deploy/saidkick-agent -c agent --tail=40 || true
    return 1
  fi
  local app
  app=$(dep_app_name "$DEP_ID")

  log "Upgrade: publish version 2 ($APP_V2_SHA) and upgrade with replicaCount=2"
  V2_ID=$(ensure_version "$APP_V2_SHA")
  state_set V2_ID "$V2_ID"
  publish_version "$V2_ID"
  if [ "$(argo_revision "$app")" != "$APP_V2_SHA" ]; then
    api_ok POST "/api/v1/deployments/$DEP_ID/upgrade" "$DEV_ID" \
      "{\"to_version_id\":\"$V2_ID\",\"new_values\":{\"replicaCount\":2}}"
  fi
  if wait_until 120 5 "Argo CD targetRevision v2" argo_at "$app" "$APP_V2_SHA" &&
    wait_ready "$DEP_ID" "upgrade v2" &&
    wait_until 240 5 "v2 workload Running x2" workload_running "$APP_NS_EFFECTIVE" "$APP_IMAGE" 2; then
    record PASS "Upgrade through the agent" "targetRevision=$APP_V2_SHA, 2 pods Running with $APP_IMAGE"
  else
    record FAIL "Upgrade through the agent" "state $(dep_state "$DEP_ID"), targetRevision $(argo_revision "$app")"
    return 1
  fi

  # Approving version 2 archived version 1; archived and approved versions
  # are valid rollback targets (#116).
  log "Rollback to version 1 (archived) through the API"
  info "version 1 state: $(version_state "$V1_ID")"
  api POST "/api/v1/deployments/$DEP_ID/rollback" "$DEV_ID" "{\"to_revision\":\"$V1_ID\"}"
  [ "$API_CODE" = "202" ] || { record FAIL "Rollback to the previous version" "POST rollback -> $API_CODE $API_BODY"; return 1; }

  local cmd_row cmd_id cmd_cluster
  cmd_row=$(psql_hub -F ' ' -c "SELECT id, cluster_id FROM agent_commands WHERE command_type = 'rollback' ORDER BY id DESC LIMIT 1")
  cmd_id=${cmd_row%% *}
  cmd_cluster=${cmd_row##* }
  if [ "$cmd_cluster" = "$CLUSTER_ID" ]; then
    record PASS "Rollback command is queued for the spoke" "command $cmd_id for cluster $CLUSTER_ID"
  else
    record FAIL "Rollback command is queued for the spoke" "command $cmd_id queued for cluster $cmd_cluster, not $CLUSTER_ID"
    return 1
  fi
  if wait_until 90 5 "rollback command acked" command_acked "$cmd_id" &&
    wait_until 120 5 "Argo CD targetRevision back to v1" argo_at "$app" "$APP_V1_SHA" &&
    wait_ready "$DEP_ID" "rollback v1" &&
    wait_until 240 5 "v1 workload Running" workload_running "$APP_NS_EFFECTIVE" "$APP_IMAGE" 1; then
    record PASS "Rollback through the agent" "targetRevision=$APP_V1_SHA, pods Running with $APP_IMAGE"
  else
    local why
    why=$(ks -n "$AGENT_NS" logs deploy/saidkick-agent -c agent --since=10m | grep '"type":"rollback"' | tail -1 |
      tr -d '\r' | jqr -r '.error // empty' 2>/dev/null || true)
    record FAIL "Rollback through the agent" "targetRevision still $(argo_revision "$app"), state $(dep_state "$DEP_ID"); ${why:-no rollback log line}"
  fi
}

# ── 14. mTLS listener checks (#64), with dev mode off ────────────────────────
disable_dev_mode() {
  log "Hub: turning dev-mode header auth off (production auth path for agents)"
  kh -n "$HUB_NS" set env "deploy/$API_DEPLOY" APP_DEV_MODE- >/dev/null
  kh -n "$HUB_NS" rollout status "deploy/$API_DEPLOY" --timeout=300s
  pf_api
  local t0
  t0=$(date +%s)
  # Heartbeats stamp the hub's clock; use the DB clock for the comparison.
  t0=$(psql_hub -c "SELECT extract(epoch FROM now())::bigint")
  if wait_until 120 5 "heartbeat with dev mode off" heartbeat_after "$t0"; then
    record PASS "Agent authenticates with dev mode off" "heartbeat accepted on the mTLS listener after APP_DEV_MODE was removed"
  else
    record FAIL "Agent authenticates with dev mode off" "no heartbeat within 120s"
  fi
}

PROBE_POD=kind-e2e-probe
probe() { # probe EXPECTED_DESC CURL_ARGS... prints the HTTP status (000 = no HTTP response)
  ks -n "$AGENT_NS" exec "$PROBE_POD" -- curl -sS -o /dev/null -w '%{http_code}' --max-time 10 \
    -X POST -H 'Content-Type: application/json' -d '{}' "$@" 2>/dev/null | tr -d '\r' || true
}

mtls_checks() {
  log "mTLS listener checks from a pod on the spoke"
  local rand ack_path ack_url main_url
  rand=$(psql_hub -c "SELECT gen_random_uuid()")
  ack_path="/api/v1/clusters/$CLUSTER_ID/commands/$rand/ack"
  ack_url="$HUB_AGENT_URL$ack_path"
  main_url="$HUB_URL/api/v1/clusters/$CLUSTER_ID/heartbeat"

  # Rogue CA and a client cert with the right CN, signed by it.
  local d="$WORK/probe"
  mkdir -p "$d"
  openssl req -x509 -newkey rsa:2048 -nodes -days 1 -keyout "$d/rogue-ca.key" -out "$d/rogue-ca.crt" \
    -subj "/CN=kind-e2e rogue CA" >/dev/null 2>&1
  openssl req -newkey rsa:2048 -nodes -keyout "$d/rogue.key" -out "$d/rogue.csr" \
    -subj "/CN=saidkick-agent-$CLUSTER_ID" >/dev/null 2>&1
  openssl x509 -req -days 1 -in "$d/rogue.csr" -CA "$d/rogue-ca.crt" -CAkey "$d/rogue-ca.key" \
    -CAcreateserial -out "$d/rogue.crt" >/dev/null 2>&1
  # A second certificate from the real agent CA for the same cluster: valid
  # chain and CN, different serial (what a rotated-out or stolen cert looks like).
  bao write -format=json pki/issue/agent common_name="saidkick-agent-$CLUSTER_ID" ttl=1h | tr -d '\r' >"$d/other.json"
  jqr -r .data.certificate "$d/other.json" >"$d/other.crt"
  jqr -r .data.private_key "$d/other.json" >"$d/other.key"
  local other_hex other_dec
  other_hex=$(jqr -r .data.serial_number "$d/other.json" | tr -d ':' | tr 'A-F' 'a-f')
  rm -f "$d/other.json"
  other_dec=$(psql_hub -c "SELECT sum((position(substr('$other_hex', i, 1) IN '0123456789abcdef') - 1)::numeric
      * power(16::numeric, length('$other_hex') - i))::numeric(80,0)
    FROM generate_series(1, length('$other_hex')) AS i")

  ks -n "$AGENT_NS" create secret generic kind-e2e-probe-certs \
    --from-file=rogue.crt="$d/rogue.crt" --from-file=rogue.key="$d/rogue.key" \
    --from-file=other.crt="$d/other.crt" --from-file=other.key="$d/other.key" \
    --dry-run=client -o yaml | ks apply -f - >/dev/null
  ks -n "$AGENT_NS" delete pod "$PROBE_POD" --ignore-not-found --wait=true >/dev/null
  ks apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: $PROBE_POD
  namespace: $AGENT_NS
spec:
  restartPolicy: Never
  securityContext:
    runAsNonRoot: true
    runAsUser: 100
  containers:
    - name: curl
      image: $CURL_IMAGE
      command: ["sleep", "3600"]
      volumeMounts:
        - { name: agent, mountPath: /agent, readOnly: true }
        - { name: probe, mountPath: /probe, readOnly: true }
  volumes:
    - name: agent
      secret: { secretName: saidkick-agent-credentials }
    - name: probe
      secret: { secretName: kind-e2e-probe-certs }
EOF
  ks -n "$AGENT_NS" wait --for=condition=Ready "pod/$PROBE_POD" --timeout=180s >/dev/null

  local ca=(--cacert /agent/ca.crt) code
  code=$(probe "${ca[@]}" "$ack_url")
  case "$code" in
  000 | 401) record PASS "No client certificate is rejected" "agent listener -> $code (000: refused during the TLS handshake)" ;;
  *) record FAIL "No client certificate is rejected" "agent listener -> $code" ;;
  esac

  code=$(probe "${ca[@]}" --cert /probe/rogue.crt --key /probe/rogue.key "$ack_url")
  [ "$code" = "000" ] &&
    record PASS "Unknown-CA certificate is rejected at the TLS handshake" "agent listener -> no HTTP response" ||
    record FAIL "Unknown-CA certificate is rejected at the TLS handshake" "agent listener -> $code"

  code=$(probe "${ca[@]}" --cert /probe/other.crt --key /probe/other.key "$ack_url")
  [ "$code" = "401" ] &&
    record PASS "Serial mismatch returns 401" "CA-signed cert for the same cluster, serial $other_dec -> 401" ||
    record FAIL "Serial mismatch returns 401" "-> $code"

  code=$(probe "${ca[@]}" --cert /agent/tls.crt --key /agent/tls.key "$ack_url")
  [ "$code" = "404" ] &&
    record PASS "Matching serial authenticates and resolves the cluster" "agent's own cert -> 404 command-not-found for cluster $CLUSTER_ID (past auth)" ||
    record FAIL "Matching serial authenticates and resolves the cluster" "-> $code"

  code=$(probe -H "X-Agent-Cluster-ID: $CLUSTER_ID" "$main_url")
  [ "$code" = "404" ] &&
    record PASS "Main API listener does not serve agent routes" "POST heartbeat on :$API_NODEPORT with the dev header -> 404" ||
    record FAIL "Main API listener does not serve agent routes" "-> $code"

  # Rotation: the hub trusts only the serial on record. Point it at the other
  # certificate (what a re-handshake does) and the agent's cert must stop
  # working on the very next request.
  local original
  original=$(psql_hub -c "SELECT agent_cert_serial FROM clusters WHERE id = '$CLUSTER_ID'")
  psql_hub -c "UPDATE clusters SET agent_cert_serial = '$other_dec' WHERE id = '$CLUSTER_ID'" >/dev/null
  local old_code new_code
  old_code=$(probe "${ca[@]}" --cert /agent/tls.crt --key /agent/tls.key "$ack_url")
  new_code=$(probe "${ca[@]}" --cert /probe/other.crt --key /probe/other.key "$ack_url")
  psql_hub -c "UPDATE clusters SET agent_cert_serial = '$original' WHERE id = '$CLUSTER_ID'" >/dev/null
  if [ "$old_code" = "401" ] && [ "$new_code" = "404" ]; then
    record PASS "Rotating the serial invalidates the previous cert immediately" "previous cert -> 401, new cert -> 404 (authenticated); serial restored"
  else
    record FAIL "Rotating the serial invalidates the previous cert immediately" "previous -> $old_code, new -> $new_code"
  fi
  info "the serial swap stands in for a re-handshake; renewal and admin rotation are checked in cert_rotation_checks"

  local t0
  t0=$(psql_hub -c "SELECT extract(epoch FROM now())::bigint")
  if wait_until 60 5 "heartbeat after serial restore" heartbeat_after "$t0"; then
    record PASS "Agent recovers after the serial is restored" "heartbeat accepted again"
  else
    record FAIL "Agent recovers after the serial is restored" "no heartbeat within 60s"
  fi
  ks -n "$AGENT_NS" delete pod "$PROBE_POD" --ignore-not-found --wait=false >/dev/null
  rm -f "$d"/*.key
}

# ── 15. Agent pod restart (#122), with dev mode off ──────────────────────────
# agent_replaced OLD_POD: exactly one agent pod exists, it is not OLD_POD and it is Ready.
agent_replaced() {
  local names
  names=$(ks -n "$AGENT_NS" get pods -l app.kubernetes.io/component=agent \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null | tr -d '\r' | grep -v '^$' || true)
  [ "$(wc -l <<<"$names")" = 1 ] && [ -n "$names" ] && [ "$names" != "$1" ] && agent_pod_ready
}
cluster_identity() {
  psql_hub -F ' ' -c "SELECT coalesce(agent_cert_serial, ''), coalesce(extract(epoch FROM registered_at)::bigint, 0),
      (SELECT count(*) FROM used_join_tokens WHERE cluster_id = '$CLUSTER_ID')
    FROM clusters WHERE id = '$CLUSTER_ID'"
}

agent_restart_check() {
  log "Agent pod restart: delete the pod and check it comes back on its stored certificate (#122)"
  local old_pod before after t0 init_log
  old_pod=$(ks -n "$AGENT_NS" get pods -l app.kubernetes.io/component=agent -o jsonpath='{.items[0].metadata.name}' | tr -d '\r')
  before=$(cluster_identity)
  info "deleting agent pod $old_pod (serial, registered_at, used join tokens: $before)"
  ks -n "$AGENT_NS" delete pod "$old_pod" --wait=true --timeout=120s >/dev/null
  if ! wait_until 300 5 "replacement agent pod Ready" agent_replaced "$old_pod"; then
    ks -n "$AGENT_NS" get pods -l app.kubernetes.io/component=agent || true
    ks -n "$AGENT_NS" logs -l app.kubernetes.io/component=agent -c agent-init --tail=20 || true
    record FAIL "Restarted agent pod becomes Ready" "no Ready replacement for $old_pod within 300s"
    return
  fi
  local new_pod
  new_pod=$(ks -n "$AGENT_NS" get pods -l app.kubernetes.io/component=agent -o jsonpath='{.items[0].metadata.name}' | tr -d '\r')
  record PASS "Restarted agent pod becomes Ready" "$old_pod deleted, $new_pod Ready"

  # A handshake would record a new serial and registered_at; a second use of
  # the join token would be refused with 410 and fail agent-init.
  init_log=$(ks -n "$AGENT_NS" logs "$new_pod" -c agent-init 2>&1 | tr -d '\r')
  after=$(cluster_identity)
  if [ "$after" = "$before" ] &&
    grep -q 'credentials Secret holds a valid cert, skipping handshake' <<<"$init_log" &&
    ! grep -q 'starting handshake' <<<"$init_log"; then
    record PASS "Restarted agent does not handshake again" "serial, registered_at and used join tokens unchanged ($after); agent-init reused the credentials Secret"
  else
    record FAIL "Restarted agent does not handshake again" "before=($before) after=($after); agent-init: $(tail -3 <<<"$init_log" | tr '\n' ' ' | cut -c1-300)"
  fi

  t0=$(psql_hub -c "SELECT extract(epoch FROM now())::bigint")
  if wait_until 120 5 "heartbeat from the restarted agent" heartbeat_after "$t0"; then
    local hub_line
    hub_line=$(ks -n "$AGENT_NS" logs "$new_pod" -c agent 2>/dev/null | grep -m1 'saidkick-agent starting' | tr -d '\r' || true)
    record PASS "Heartbeats resume over mTLS after the restart" "last_heartbeat_at advances, dev mode off; agent: $(jqr -r '.hub' <<<"$hub_line" 2>/dev/null)"
  else
    ks -n "$AGENT_NS" logs "$new_pod" -c agent --tail=40 || true
    record FAIL "Heartbeats resume over mTLS after the restart" "no heartbeat within 120s"
  fi
}

# ── 15a. Agent certificate rotation (#139), after the restart check ──────────
# The hub's AGENT_CERT_TTL drops to SHORT_CERT_TTL (test-only, refused outside
# dev-like APP_ENVs) and dev-mode header auth comes back for the admin call.
# An admin rotation makes the agent renew on its next heartbeat; the 48h
# certificate it gets is then renewed again on its own two minutes later,
# because less than CERT_RENEW_BEFORE remains. Both renewals must happen
# without a pod restart, and the previous certificate must get 401 once the
# new one is in use. Removing AGENT_CERT_TTL at the end lets the next renewal
# return to 90 days, which ends the cycle.
RENEW_DIR="$WORK/renew"

# cert_state prints active serial|pending serial|rotation requested (t/f).
cert_state() {
  psql_hub -F '|' -c "SELECT coalesce(agent_cert_serial, ''), coalesce(agent_cert_pending_serial, ''),
      agent_cert_rotation_requested_at IS NOT NULL FROM clusters WHERE id = '$CLUSTER_ID'"
}
active_serial() { psql_hub -c "SELECT coalesce(agent_cert_serial, '') FROM clusters WHERE id = '$CLUSTER_ID'"; }
# renewed_from SERIAL: another serial is active, none pending, no rotation requested.
renewed_from() {
  local active pending requested
  IFS='|' read -r active pending requested <<<"$(cert_state)"
  [ -n "$active" ] && [ "$active" != "$1" ] && [ -z "$pending" ] && [ "$requested" = "f" ]
}
# hex_to_dec HEX prints the decimal form the hub stores for a hex serial.
hex_to_dec() {
  local hex
  hex=$(tr -d ':\r' <<<"$1" | tr 'A-F' 'a-f')
  psql_hub -c "SELECT sum((position(substr('$hex', i, 1) IN '0123456789abcdef') - 1)::numeric
      * power(16::numeric, length('$hex') - i))::numeric(80,0)
    FROM generate_series(1, length('$hex')) AS i"
}
# cert_serial FILE prints the decimal serial of a PEM certificate.
cert_serial() { hex_to_dec "$(openssl x509 -noout -serial -in "$1" | sed 's/^serial=//')"; }
# save_agent_cert NAME copies the certificate and key in the agent's
# credentials Secret to $RENEW_DIR/NAME.crt and NAME.key.
save_agent_cert() {
  local key
  for key in crt key; do
    ks -n "$AGENT_NS" get secret saidkick-agent-credentials -o jsonpath="{.data.tls\\.$key}" | tr -d '\r' |
      base64 -d >"$RENEW_DIR/$1.$key"
  done
}
# agent_pod_identity prints the agent pod name and its agent container's restart count.
agent_pod_identity() {
  ks -n "$AGENT_NS" get pods -l app.kubernetes.io/component=agent \
    -o jsonpath='{.items[0].metadata.name}/{.items[0].status.containerStatuses[?(@.name=="agent")].restartCount}' | tr -d '\r'
}
# push_probe_cert NAME copies $RENEW_DIR/NAME.crt and NAME.key into the probe pod.
push_probe_cert() {
  local f
  for f in crt key; do
    ks -n "$AGENT_NS" exec -i "$PROBE_POD" -- sh -c "cat > /tmp/$1.$f" <"$RENEW_DIR/$1.$f"
  done
}
# probe_with NAME prints the status of an ack for an unknown command, sent
# with the certificate NAME: 404 is past auth, 401 a rejected certificate.
probe_with() {
  probe --cacert /agent/ca.crt --cert "/tmp/$1.crt" --key "/tmp/$1.key" "$RENEW_ACK_URL"
}

cert_rotation_checks() {
  log "Agent certificate rotation (#139): admin rotation and renewal before expiry, no pod restart"
  mkdir -p "$RENEW_DIR"
  kh -n "$HUB_NS" set env "deploy/$API_DEPLOY" APP_DEV_MODE=true AGENT_CERT_TTL="$SHORT_CERT_TTL" >/dev/null
  if ! kh -n "$HUB_NS" rollout status "deploy/$API_DEPLOY" --timeout=300s; then
    record FAIL "Hub accepts the test-only AGENT_CERT_TTL" "API did not roll out with AGENT_CERT_TTL=$SHORT_CERT_TTL"
    return
  fi
  pf_api
  RENEW_ACK_URL="$HUB_AGENT_URL/api/v1/clusters/$CLUSTER_ID/commands/$(psql_hub -c "SELECT gen_random_uuid()")/ack"

  # A probe pod that can reach the agent listener; certificates are copied in
  # as they are issued, so no pod start sits between issue and probe.
  ks -n "$AGENT_NS" delete pod "$PROBE_POD" --ignore-not-found --wait=true >/dev/null
  ks apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: $PROBE_POD
  namespace: $AGENT_NS
spec:
  restartPolicy: Never
  securityContext:
    runAsNonRoot: true
    runAsUser: 100
  containers:
    - name: curl
      image: $CURL_IMAGE
      command: ["sleep", "3600"]
      volumeMounts:
        - { name: agent, mountPath: /agent, readOnly: true }
  volumes:
    - name: agent
      secret: { secretName: saidkick-agent-credentials, items: [{ key: ca.crt, path: ca.crt }] }
EOF
  ks -n "$AGENT_NS" wait --for=condition=Ready "pod/$PROBE_POD" --timeout=180s >/dev/null

  local pod0 s0 s1 s2 saved
  pod0=$(agent_pod_identity)
  s0=$(active_serial)
  save_agent_cert rot0
  saved=$(cert_serial "$RENEW_DIR/rot0.crt")
  [ "$saved" = "$s0" ] || info "credentials Secret holds serial $saved, hub has $s0"

  # Admin rotation: other roles are refused, the platform admin is accepted.
  api POST "/api/v1/clusters/$CLUSTER_ID/rotate-credential" "$DEV_ID"
  [ "$API_CODE" = 403 ] &&
    record PASS "Rotate-credential refuses other roles" "developer/deployer -> 403" ||
    record FAIL "Rotate-credential refuses other roles" "developer/deployer -> $API_CODE: $API_BODY"
  api POST "/api/v1/clusters/$CLUSTER_ID/rotate-credential" "$ADMIN_ID"
  if [ "$API_CODE" = 202 ] && jqr -e '.rotation_requested_at' <<<"$API_BODY" >/dev/null; then
    record PASS "Admin rotation request is accepted" "platform admin -> 202, rotation_requested_at $(jqr -r .rotation_requested_at <<<"$API_BODY")"
  else
    record FAIL "Admin rotation request is accepted" "-> $API_CODE: $API_BODY"
    return
  fi

  if wait_until 120 5 "agent renews after the rotation request" renewed_from "$s0"; then
    s1=$(active_serial)
    record PASS "Agent renews on the heartbeat's renew flag" "active serial $s0 -> $s1, no pending serial, rotation request cleared"
  else
    ks -n "$AGENT_NS" logs deploy/saidkick-agent -c agent --tail=30 || true
    record FAIL "Agent renews on the heartbeat's renew flag" "state (active|pending|requested): $(cert_state)"
    return
  fi
  save_agent_cert rot1
  push_probe_cert rot0
  push_probe_cert rot1
  local old_code new_code
  old_code=$(probe_with rot0)
  new_code=$(probe_with rot1)
  if [ "$old_code" = 401 ] && [ "$new_code" = 404 ]; then
    record PASS "Previous certificate gets 401 once the renewed one is in use" "serial $s0 -> 401, serial $s1 -> 404 (authenticated)"
  else
    record FAIL "Previous certificate gets 401 once the renewed one is in use" "previous -> $old_code, renewed -> $new_code"
  fi
  saved=$(cert_serial "$RENEW_DIR/rot1.crt")
  [ "$saved" = "$s1" ] &&
    record PASS "Renewed certificate is stored in the credentials Secret" "tls.crt serial $saved matches the active serial" ||
    record FAIL "Renewed certificate is stored in the credentials Secret" "tls.crt serial $saved, active $s1"

  # The 48h certificate is due once less than CERT_RENEW_BEFORE remains.
  if wait_until 300 10 "agent renews before expiry" renewed_from "$s1"; then
    s2=$(active_serial)
    push_probe_cert rot1
    old_code=$(probe_with rot1)
    [ "$old_code" = 401 ] &&
      record PASS "Agent renews before expiry" "AGENT_CERT_TTL=$SHORT_CERT_TTL, agent.certRenewBefore=$CERT_RENEW_BEFORE: serial $s1 -> $s2; previous -> 401" ||
      record FAIL "Agent renews before expiry" "serial $s1 -> $s2, but the previous certificate -> $old_code"
  else
    ks -n "$AGENT_NS" logs deploy/saidkick-agent -c agent --tail=30 || true
    record FAIL "Agent renews before expiry" "no renewal from $s1 within 300s; state: $(cert_state)"
  fi

  local t0 pod1 renewals
  t0=$(psql_hub -c "SELECT extract(epoch FROM now())::bigint")
  pod1=$(agent_pod_identity)
  renewals=$(ks -n "$AGENT_NS" logs deploy/saidkick-agent -c agent 2>/dev/null | grep -c '"certificate renewed"' || true)
  if wait_until 60 5 "heartbeat after the renewals" heartbeat_after "$t0" && [ "$pod1" = "$pod0" ]; then
    record PASS "Heartbeats continue across renewals without a pod restart" "pod/restarts $pod1 unchanged; $renewals renewals logged"
  else
    record FAIL "Heartbeats continue across renewals without a pod restart" "pod/restarts $pod0 -> $pod1; heartbeat after renewal: $(heartbeat_epoch) vs $t0"
  fi

  log "Hub: removing AGENT_CERT_TTL and dev-mode header auth again"
  kh -n "$HUB_NS" set env "deploy/$API_DEPLOY" APP_DEV_MODE- AGENT_CERT_TTL- >/dev/null
  kh -n "$HUB_NS" rollout status "deploy/$API_DEPLOY" --timeout=300s >/dev/null || true
  pf_api
  ks -n "$AGENT_NS" delete pod "$PROBE_POD" --ignore-not-found --wait=false >/dev/null
  rm -f "$RENEW_DIR"/*.key
}

# ── 16. Monitoring: hub scraping and agent metrics (#101-#106) ───────────────
# Label values must never hold a UUID or an email address (design doc,
# section 6; testutil.RequireNoIdentifyingLabels is the unit-level twin).
IDENT_RE='[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}|[^@[:space:]"]+@[^@[:space:]"]+\.[A-Za-z]{2,}'

# identifying_labels reads a Prometheus exposition on stdin and prints every
# label value that holds a UUID or an email address.
identifying_labels() {
  grep -v '^#' | sed -n 's/^[^{ ]*{\(.*\)}[^}]*$/\1/p' | grep -oE '="([^"\\]|\\.)*"' | grep -E "$IDENT_RE" || true
}

pf_vm() { start_pf vm "$HUB_CTX" "$HUB_NS" "svc/$VM_NAME" "$LOCAL_VM_PORT" 8428; }
# vm_get PATH [CURL_ARGS...] prints the body of a VictoriaMetrics API call.
vm_get() {
  local path=$1
  shift
  curl -fsS --max-time 15 -G "http://127.0.0.1:$LOCAL_VM_PORT$path" "$@" 2>/dev/null | tr -d '\r'
}
# vm_value EXPR prints the value of the first sample EXPR returns, or nothing.
vm_value() { vm_get /api/v1/query --data-urlencode "query=$1" | jqr -r '.data.result[0].value[1] // empty' 2>/dev/null; }
# vm_at_least EXPR MIN: EXPR returns a sample >= MIN.
vm_at_least() {
  local v
  v=$(vm_value "$1")
  [ -n "$v" ] && awk -v v="$v" -v m="$2" 'BEGIN { exit !(v + 0 >= m + 0) }'
}
# api_targets prints one line per active saidkick-api target: health scrapeUrl.
api_targets() {
  vm_get /api/v1/targets --data-urlencode state=active |
    jqr -r '.data.activeTargets[] | select(.labels.job == "saidkick-api") | "\(.health) \(.scrapeUrl)"' 2>/dev/null
}
api_pod_count() {
  kh -n "$HUB_NS" get pods -l app.kubernetes.io/component=api --field-selector=status.phase=Running \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | tr -d '\r' | grep -c . || true
}
# targets_settled: one target per running API pod, every one up.
targets_settled() {
  local t
  t=$(api_targets)
  [ -n "$t" ] && [ "$(grep -c . <<<"$t")" = "$(api_pod_count)" ] && ! grep -qv '^up ' <<<"$t"
}

# Run while the API pod that served the deploy flow is still up: its
# counters reset when disable_dev_mode rolls the Deployment.
hub_monitoring_checks() {
  log "Monitoring (hub): metrics Service, main-port 404, VictoriaMetrics targets and series"
  local svc_type svc_port main_ports
  svc_type=$(kh -n "$HUB_NS" get svc "$API_DEPLOY-metrics" -o jsonpath='{.spec.type}' 2>/dev/null | tr -d '\r')
  svc_port=$(kh -n "$HUB_NS" get svc "$API_DEPLOY-metrics" -o jsonpath='{.spec.ports[?(@.name=="metrics")].port}' 2>/dev/null | tr -d '\r')
  main_ports=$(kh -n "$HUB_NS" get svc "$API_DEPLOY" -o jsonpath='{range .spec.ports[*]}{.name}:{.port}:{.nodePort} {end}' | tr -d '\r')
  if [ "$svc_type" = "ClusterIP" ] && [ "$svc_port" = "$METRICS_PORT" ] && ! grep -qE "metrics|:$METRICS_PORT:" <<<"$main_ports"; then
    record PASS "Hub metrics port is a ClusterIP Service port" "$API_DEPLOY-metrics ClusterIP metrics:$svc_port; NodePort $API_DEPLOY carries only $main_ports"
  else
    record FAIL "Hub metrics port is a ClusterIP Service port" "$API_DEPLOY-metrics type=${svc_type:-missing} port=${svc_port:-none}; $API_DEPLOY ports: $main_ports"
  fi

  local code
  # -o into .work: with path conversion off, a native curl cannot open /dev/null.
  code=$(curl -sS -o "$WORK/main-metrics.out" -w '%{http_code}' --max-time 10 "http://127.0.0.1:$LOCAL_API_PORT/metrics" 2>/dev/null) || code=000
  [ "$code" = "404" ] &&
    record PASS "Main API listener answers /metrics with 404" "GET /metrics on the http port -> 404" ||
    record FAIL "Main API listener answers /metrics with 404" "GET /metrics on the http port -> $code"

  if ! kh -n "$HUB_NS" rollout status "statefulset/$VM_NAME" --timeout=300s >/dev/null; then
    record FAIL "VictoriaMetrics scrapes only the API metrics port" "statefulset $VM_NAME not ready"
    return
  fi
  pf_vm
  local targets
  if wait_until 180 5 "VictoriaMetrics saidkick-api targets up" targets_settled; then
    targets=$(api_targets)
    if ! grep -qvE "^up http://[0-9.]+:$METRICS_PORT/metrics\$" <<<"$targets"; then
      record PASS "VictoriaMetrics scrapes only the API metrics port" "job saidkick-api: $(tr '\n' ' ' <<<"$targets" | sed 's/[0-9]*\.[0-9]*\.[0-9]*\.[0-9]*/<pod-ip>/g')"
    else
      record FAIL "VictoriaMetrics scrapes only the API metrics port" "job saidkick-api: $(tr '\n' ' ' <<<"$targets")"
    fi
  else
    record FAIL "VictoriaMetrics scrapes only the API metrics port" "targets: $(api_targets | tr '\n' ' ') (want one up target per API pod, $(api_pod_count) pods)"
  fi

  # Series the e2e flow must have produced on the API pod that served it.
  # The unmatched 404 series comes from the GET /metrics above: one fixed
  # path label, and the status the client got.
  # The scrape interval is VictoriaMetrics' default (1m), so poll.
  local expr missing=() found=()
  for expr in \
    'up{job="saidkick-api"}' \
    'go_goroutines{job="saidkick-api"}' \
    'process_resident_memory_bytes{job="saidkick-api"}' \
    'sum(http_requests_total{job="saidkick-api"})' \
    'sum(http_requests_total{job="saidkick-api",path="unmatched",status="404"})' \
    'sum(deployment_transitions_total{job="saidkick-api",from="unknown",to="pending"})' \
    'sum(deployment_transitions_total{job="saidkick-api",to="ready"})' \
    'sum(orchestrator_calls_total{job="saidkick-api",operation="apply",outcome="success"})' \
    'sum(orchestrator_calls_total{job="saidkick-api",operation="rollback",outcome="success"})' \
    'sum(audit_decisions_total{job="saidkick-api",decision="approve"})' \
    'sum(audit_writes_total{job="saidkick-api"})' \
    'sum(auth_attempts_total{job="saidkick-api",method="dev",outcome="success"})'; do
    if wait_until 150 10 "$expr > 0" vm_at_least "$expr" 1; then
      local name=${expr#sum(}
      found+=("${name%%\{*}")
    else
      missing+=("$expr=$(vm_value "$expr")")
    fi
  done
  if [ "${#missing[@]}" = 0 ]; then
    record PASS "Key API series reach VictoriaMetrics after the e2e flow" "non-zero: ${found[*]} (incl. deployment_transitions_total and orchestrator_calls_total for apply and rollback)"
  else
    record FAIL "Key API series reach VictoriaMetrics after the e2e flow" "missing or zero: ${missing[*]}"
  fi

  # The agent heartbeats every 10s and the reconciler collects every 30s,
  # so the stalest-heartbeat age stays under three heartbeat intervals.
  local age
  wait_until 90 10 "agent_heartbeat_age_seconds sample" vm_at_least 'max(agent_heartbeat_age_seconds{job="saidkick-api"})' 0 || true
  age=$(vm_value 'max(agent_heartbeat_age_seconds{job="saidkick-api"})')
  if [ -n "$age" ] && awk -v a="$age" 'BEGIN { exit !(a > 0 && a <= 30) }'; then
    record PASS "agent_heartbeat_age_seconds is sane while the agent runs" "max = ${age}s (0 < age <= 30s, heartbeat 10s)"
  else
    record FAIL "agent_heartbeat_age_seconds is sane while the agent runs" "max = ${age:-no sample}"
  fi
  stop_pf vm
}

# Run after every other check, so the scan covers the series from both API
# pods and from the auth failures the mTLS probes caused.
hub_series_label_scan() {
  log "Monitoring (hub): no UUID or email in any stored label value"
  pf_vm
  local start body count bad
  start=$(( $(date +%s) - 7200 ))
  body=$(vm_get /api/v1/series --data-urlencode 'match[]={job="saidkick-api"}' --data-urlencode "start=$start")
  count=$(jqr -r '.data | length' <<<"$body" 2>/dev/null || echo 0)
  bad=$(jqr -r '.data[] | to_entries[] | "\(.key)=\(.value)"' <<<"$body" 2>/dev/null | grep -E "$IDENT_RE" | sort -u | head -5 || true)
  if [ "${count:-0}" -gt 0 ] && [ -z "$bad" ]; then
    record PASS "Hub series carry no identifying labels" "$count series for job saidkick-api scanned through the series API"
  else
    record FAIL "Hub series carry no identifying labels" "series=${count:-0}; offending: $(tr '\n' ' ' <<<"$bad")"
  fi
  stop_pf vm
}

SCRAPE_POD=kind-e2e-scrape
# agent_scrape URL prints the body of GET URL from the scrape pod on the spoke.
agent_scrape() { ks -n "$AGENT_NS" exec "$SCRAPE_POD" -- curl -sS --max-time 10 "$1" 2>/dev/null | tr -d '\r' || true; }
agent_scrape_code() {
  ks -n "$AGENT_NS" exec "$SCRAPE_POD" -- curl -sS -o /dev/null -w '%{http_code}' --max-time 10 "$1" 2>/dev/null | tr -d '\r' || true
}
# series_value EXPOSITION SERIES prints the value of one exact series.
series_value() { awk -v s="$2" 'index($0, s " ") == 1 { print $2; exit }' <<<"$1"; }
ge() { awk -v a="${1:-0}" -v b="$2" 'BEGIN { exit !(a + 0 >= b + 0) }'; }

# The spoke has no monitoring stack: the checks scrape the agent's endpoint
# from a pod, the way an in-cluster scraper would. Run before the agent pod
# restart, which resets its counters.
spoke_monitoring_checks() {
  log "Monitoring (spoke): agent /metrics scraped from a pod on the spoke"
  ks -n "$AGENT_NS" delete pod "$SCRAPE_POD" --ignore-not-found --wait=true >/dev/null
  ks apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: $SCRAPE_POD
  namespace: $AGENT_NS
spec:
  restartPolicy: Never
  securityContext:
    runAsNonRoot: true
    runAsUser: 100
  containers:
    - name: curl
      image: $CURL_IMAGE
      command: ["sleep", "3600"]
EOF
  ks -n "$AGENT_NS" wait --for=condition=Ready "pod/$SCRAPE_POD" --timeout=180s >/dev/null

  local pod pod_ip svc_type svc_url body code health_code annotations
  pod=$(ks -n "$AGENT_NS" get pods -l app.kubernetes.io/component=agent -o jsonpath='{.items[0].metadata.name}' | tr -d '\r')
  pod_ip=$(ks -n "$AGENT_NS" get pod "$pod" -o jsonpath='{.status.podIP}' | tr -d '\r')
  svc_type=$(ks -n "$AGENT_NS" get svc saidkick-agent-metrics -o jsonpath='{.spec.type}:{.spec.ports[?(@.name=="metrics")].port}' 2>/dev/null | tr -d '\r')
  svc_url="http://saidkick-agent-metrics.$AGENT_NS.svc:$METRICS_PORT/metrics"
  body=$(agent_scrape "$svc_url")
  code=$(agent_scrape_code "http://$pod_ip:$METRICS_PORT/metrics")
  health_code=$(agent_scrape_code "http://$pod_ip:$AGENT_HEALTH_PORT/metrics")
  if [ "$svc_type" = "ClusterIP:$METRICS_PORT" ] && [ "$code" = "200" ] && [ "$health_code" = "404" ] &&
    [ "$(series_value "$body" agent_up)" = "1" ] && grep -q '^go_goroutines ' <<<"$body"; then
    record PASS "Agent serves /metrics in-cluster on its metrics port" "Service saidkick-agent-metrics ClusterIP:$METRICS_PORT and pod :$METRICS_PORT -> agent_up 1, Go series; health port :$AGENT_HEALTH_PORT/metrics -> 404"
  else
    record FAIL "Agent serves /metrics in-cluster on its metrics port" "svc=${svc_type:-missing} pod:$METRICS_PORT -> $code, health:$AGENT_HEALTH_PORT/metrics -> $health_code, agent_up=$(series_value "$body" agent_up)"
  fi

  annotations=$(ks -n "$AGENT_NS" get pod "$pod" -o jsonpath='{.metadata.annotations}' | tr -d '\r')
  grep -q 'prometheus.io/' <<<"$annotations" &&
    record FAIL "Agent scrape annotations are opt-in" "default install annotates the pod: $annotations" ||
    record PASS "Agent scrape annotations are opt-in" "default install: no prometheus.io/* pod annotations"

  local hb1 hb2 fail1
  hb1=$(series_value "$body" 'agent_heartbeats_total{outcome="success"}')
  fail1=$(series_value "$body" 'agent_heartbeats_total{outcome="failure"}')
  sleep 12
  hb2=$(series_value "$(agent_scrape "$svc_url")" 'agent_heartbeats_total{outcome="success"}')
  if ge "$hb1" 1 && [ -n "$hb2" ] && [ "$hb2" -gt "$hb1" ]; then
    record PASS "Agent heartbeat counter rises" "agent_heartbeats_total{outcome=\"success\"} $hb1 -> $hb2 over 12s (failure: ${fail1:-0})"
  else
    record FAIL "Agent heartbeat counter rises" "success $hb1 -> ${hb2:-none}"
  fi

  # One apply for version 1, one for the upgrade, one rollback, all on this pod.
  body=$(agent_scrape "$svc_url")
  local apply rollback apply_n rollback_n hub_n
  apply=$(series_value "$body" 'agent_commands_processed_total{command="apply",outcome="success"}')
  rollback=$(series_value "$body" 'agent_commands_processed_total{command="rollback",outcome="success"}')
  apply_n=$(series_value "$body" 'agent_command_duration_seconds_count{command="apply"}')
  rollback_n=$(series_value "$body" 'agent_command_duration_seconds_count{command="rollback"}')
  hub_n=$(series_value "$body" 'agent_hub_request_duration_seconds_count')
  if ge "$apply" 2 && ge "$rollback" 1 && ge "$apply_n" 2 && ge "$rollback_n" 1 && ge "$hub_n" 1; then
    record PASS "Agent counts and times the deploy, upgrade and rollback commands" "processed apply/success=$apply rollback/success=$rollback; duration counts apply=$apply_n rollback=$rollback_n; hub round trips=$hub_n"
  else
    record FAIL "Agent counts and times the deploy, upgrade and rollback commands" "apply/success=${apply:-none} rollback/success=${rollback:-none} duration apply=${apply_n:-none} rollback=${rollback_n:-none} hub=${hub_n:-none}"
  fi

  local bad lines
  bad=$(identifying_labels <<<"$body" | sort -u | head -5)
  lines=$(grep -c '{' <<<"$body" || true)
  if [ -n "$body" ] && [ -z "$bad" ] && ! grep -qi 'cluster' <<<"$(grep -v '^#' <<<"$body")"; then
    record PASS "Agent metrics carry no identifying labels" "$lines labelled series scanned; no UUID, email or cluster label"
  else
    record FAIL "Agent metrics carry no identifying labels" "offending: $(tr '\n' ' ' <<<"$bad")"
  fi
  ks -n "$AGENT_NS" delete pod "$SCRAPE_POD" --ignore-not-found --wait=false >/dev/null
}

# ── Main ──────────────────────────────────────────────────────────────────────
main() {
  check_prereqs
  create_clusters
  build_images
  helm_deps
  hub_postgres
  hub_install
  hub_pki
  hub_finish
  webapp_csp_check
  seed_users
  spoke_argocd
  register_spoke
  wait_handshake
  eso_checks
  local deploy_rc=0
  deploy_flow || deploy_rc=$?
  hub_monitoring_checks
  spoke_monitoring_checks
  disable_dev_mode
  mtls_checks
  agent_restart_check
  cert_rotation_checks
  hub_series_label_scan
  summary
  if [ "$deploy_rc" != 0 ] || grep -q '^FAIL' "$RESULTS_FILE"; then
    echo "Result: FAILED"
    exit 1
  fi
  if grep -q '^BLOCKER' "$RESULTS_FILE"; then
    echo "Result: flow completed, but only with the test-only workarounds listed as BLOCKER above"
    exit 2
  fi
  echo "Result: PASSED"
}
main "$@"
