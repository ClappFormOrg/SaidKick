#!/usr/bin/env bash
# Checks what the chart renders with values-production.yaml.
#
# The production session cookie is host-only: the API ConfigMap must render
# AUTH_COOKIE_DOMAIN as an empty string, so the API sets saidkick_session
# without a Domain attribute and no sibling host receives it.
#
# Usage: charts/saidkick-hub/ci/check-production-values.sh [helm template args...]
# Extra args are appended after -f values-production.yaml.
# Requires helm (v3) and yq (v4, mikefarah) on PATH.
set -euo pipefail

chart_dir=$(cd "$(dirname "$0")/.." && pwd)
rendered=$(helm template saidkick-hub "$chart_dir" -f "$chart_dir/values-production.yaml" "$@")

configmaps=$(yq 'select(.kind == "ConfigMap" and has("data") and (.data | has("AUTH_COOKIE_DOMAIN"))) | .metadata.name' <<<"$rendered")
if [[ -z "$configmaps" ]]; then
  echo "FAIL: no API ConfigMap with AUTH_COOKIE_DOMAIN rendered" >&2
  exit 1
fi

domain=$(yq 'select(.kind == "ConfigMap" and has("data") and (.data | has("AUTH_COOKIE_DOMAIN"))) | .data.AUTH_COOKIE_DOMAIN' <<<"$rendered")
if [[ -n "$domain" ]]; then
  echo "FAIL: production AUTH_COOKIE_DOMAIN is \"$domain\", want \"\" (host-only session cookie)" >&2
  exit 1
fi
echo "OK: production AUTH_COOKIE_DOMAIN is empty (host-only session cookie)"
