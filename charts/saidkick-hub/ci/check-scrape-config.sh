#!/usr/bin/env bash
# Checks the VictoriaMetrics scrape config against what the chart renders, so
# the relabel rules and the API pod labels cannot drift apart again.
#
# It renders the chart, builds the targets Kubernetes pod discovery would
# produce (one per pod template and container port), applies the saidkick-api
# job's keep rules, and fails unless exactly the API Deployment's metrics port
# survives. Every other workload in the render, subcharts included, must be
# dropped. It then renders the chart with the ingress enabled and fails if
# any ingress path reaches the metrics port, and with a LoadBalancer API
# Service and fails if a non-ClusterIP Service publishes the metrics port.
#
# Usage: charts/saidkick-hub/ci/check-scrape-config.sh [helm template args...]
# Requires helm (v3) and yq (v4, mikefarah) on PATH.
set -euo pipefail

chart_dir=$(cd "$(dirname "$0")/.." && pwd)
job=saidkick-api
rendered=$(helm template saidkick-hub "$chart_dir" "$@")

scrape=$(yq 'select(.kind == "ConfigMap" and .metadata.name == "*-scrapeconfig") | .data["scrape.yml"]' <<<"$rendered")
if [[ -z "$scrape" ]]; then
  echo "FAIL: no VictoriaMetrics scrape ConfigMap rendered" >&2
  exit 1
fi
rules=$(yq -o=json -I=0 ".scrape_configs[] | select(.job_name == \"$job\") | .relabel_configs[]" <<<"$scrape")
if [[ -z "$rules" ]]; then
  echo "FAIL: scrape job $job has no relabel rules" >&2
  exit 1
fi

# One line per discovered target: workload|port name|k=v,k=v pod labels.
targets=$(yq -N 'select(.spec.template.spec.containers) |
  (.kind + "/" + .metadata.name) as $w |
  (.spec.template.metadata.labels // {} | to_entries | map(.key + "=" + (.value | tostring)) | join(",")) as $l |
  .spec.template.spec.containers[] | .ports[]? |
  $w + "|" + (.name // "") + "|" + $l' <<<"$rendered")

# meta_value prints the discovery meta label value for one target.
meta_value() {
  local name=$1 port=$2 labels=$3 kv key
  if [[ $name == __meta_kubernetes_pod_container_port_name ]]; then
    printf '%s' "$port"
    return
  fi
  IFS=, read -ra kvs <<<"$labels"
  for kv in "${kvs[@]}"; do
    key=${kv%%=*}
    if [[ $name == "__meta_kubernetes_pod_label_${key//[^a-zA-Z0-9_]/_}" ]]; then
      printf '%s' "${kv#*=}"
      return
    fi
  done
}

kept=()
while IFS='|' read -r workload port labels; do
  keep=1
  while read -r rule; do
    action=$(yq -p=json '.action // "replace"' <<<"$rule")
    [[ $action == keep ]] || continue
    regex=$(yq -p=json '.regex // "(.*)"' <<<"$rule")
    sep=$(yq -p=json '.separator // ";"' <<<"$rule")
    value=""
    first=1
    while read -r src; do
      [[ $first == 1 ]] || value+=$sep
      first=0
      value+=$(meta_value "$src" "$port" "$labels")
    done < <(yq -p=json '.source_labels[]' <<<"$rule")
    if ! [[ $value =~ ^(${regex})$ ]]; then
      keep=0
      break
    fi
  done <<<"$rules"
  if [[ $keep == 1 ]]; then
    kept+=("$workload:$port")
  fi
done <<<"$targets"

api=$(yq 'select(.kind == "Deployment" and .spec.template.metadata.labels["app.kubernetes.io/component"] == "api") | .kind + "/" + .metadata.name' <<<"$rendered")
want="$api:metrics"

if [[ ${#kept[@]} -ne 1 || ${kept[0]} != "$want" ]]; then
  echo "FAIL: scrape job $job keeps [${kept[*]}], want exactly [$want]" >&2
  exit 1
fi
echo "OK: scrape job $job keeps exactly $want"

# /metrics has no authentication: no ingress rule may reach the metrics port
# or route the /metrics path.
metrics_port=$(yq 'select(.kind == "Service" and .metadata.labels["app.kubernetes.io/component"] == "api") | .spec.ports[] | select(.name == "metrics") | .port' <<<"$rendered")
exposed=$(helm template saidkick-hub "$chart_dir" "$@" --set ingress.enabled=true |
  yq "select(.kind == \"Ingress\") | .spec.rules[].http.paths[] |
    select(.backend.service.port.name == \"metrics\" or .backend.service.port.number == $metrics_port or .path == \"/metrics*\") | .path")
if [[ -n "$exposed" ]]; then
  echo "FAIL: ingress routes the metrics port or path: $exposed" >&2
  exit 1
fi
echo "OK: ingress does not route the metrics port ($metrics_port) or /metrics"

# A NodePort or LoadBalancer api.service.type must publish the http port
# only: every Service port that reaches the metrics container port stays on
# a ClusterIP Service.
published=$(helm template saidkick-hub "$chart_dir" "$@" --set api.service.type=LoadBalancer |
  yq -N "select(.kind == \"Service\" and .spec.type != \"ClusterIP\") | .metadata.name as \$s | .spec.ports[] |
    select(.name == \"metrics\" or .targetPort == \"metrics\" or .port == $metrics_port) | \$s + \":\" + (.port | tostring)")
if [[ -n "$published" ]]; then
  echo "FAIL: a non-ClusterIP Service publishes the metrics port: $published" >&2
  exit 1
fi
echo "OK: the metrics port stays on a ClusterIP Service with api.service.type=LoadBalancer"
