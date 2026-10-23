#!/usr/bin/env bash
# Checks the agent chart's metrics wiring against what it renders:
#
#   - the agent container exposes a port named "metrics" and passes the same
#     port to the agent as SAIDKICK_METRICS_ADDR
#   - the metrics Service is ClusterIP and targets that port by name
#   - scrape annotations are opt-in: absent by default, present and pointing
#     at the metrics port with agent.metrics.annotations=true
#   - no Ingress in the render, with every subchart that can create one
#     enabled, routes the metrics Service or port
#
# Usage: charts/saidkick-agent/ci/check-metrics.sh [helm template args...]
# Requires helm (v3) and yq (v4, mikefarah) on PATH, and the chart's
# dependencies built (helm dependency build).
set -euo pipefail

chart_dir=$(cd "$(dirname "$0")/.." && pwd)
render() { helm template saidkick-agent "$chart_dir" "$@"; }
fail() {
  echo "FAIL: $*" >&2
  exit 1
}

rendered=$(render "$@")
agent='select(.kind == "Deployment" and .spec.template.metadata.labels["app.kubernetes.io/component"] == "agent")'

port=$(yq "$agent | .spec.template.spec.containers[] | select(.name == \"agent\") | .ports[] | select(.name == \"metrics\") | .containerPort" <<<"$rendered")
[[ -n $port ]] || fail "agent container has no port named metrics"
addr=$(yq "$agent | .spec.template.spec.containers[] | select(.name == \"agent\") | .env[] | select(.name == \"SAIDKICK_METRICS_ADDR\") | .value" <<<"$rendered")
[[ $addr == ":$port" ]] || fail "SAIDKICK_METRICS_ADDR is '$addr', want ':$port'"
echo "OK: agent serves /metrics on container port $port (metrics)"

svc=$(yq 'select(.kind == "Service" and .spec.ports[].name == "metrics") | .spec.type + "|" + (.spec.ports[] | select(.name == "metrics") | .targetPort)' <<<"$rendered")
[[ $svc == "ClusterIP|metrics" ]] || fail "metrics Service is '$svc', want a ClusterIP Service targeting port metrics"
echo "OK: metrics Service is ClusterIP and targets the metrics port"

annotations=$(yq "$agent | .spec.template.metadata.annotations | keys | .[] | select(test(\"^prometheus.io/\"))" <<<"$rendered")
[[ -z $annotations ]] || fail "scrape annotations render by default: $annotations"
opted=$(render "$@" --set agent.metrics.annotations=true |
  yq -N "$agent | .spec.template.metadata.annotations | [.[\"prometheus.io/scrape\"], .[\"prometheus.io/port\"], .[\"prometheus.io/path\"]] | join(\"|\")" |
  sed '/^$/d')
[[ $opted == "true|$port|/metrics" ]] || fail "agent.metrics.annotations=true renders '$opted', want 'true|$port|/metrics'"
echo "OK: scrape annotations are opt-in and point at port $port"

exposed=$(render "$@" --set ingress-nginx.enabled=true --set external-dns.enabled=true --set cert-manager.enabled=true |
  yq "select(.kind == \"Ingress\") | .spec.rules[].http.paths[] |
    select(.backend.service.name == \"*-metrics\" or .backend.service.port.name == \"metrics\" or .backend.service.port.number == $port or .path == \"/metrics*\") | .path")
[[ -z $exposed ]] || fail "an ingress routes the agent metrics port or path: $exposed"
echo "OK: no ingress routes the agent metrics port ($port) or /metrics"
