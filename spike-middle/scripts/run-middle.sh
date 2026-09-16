#!/usr/bin/env bash
# Run the spike middle outside short-lived agent shells.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT/go-middle"

: "${CODER_URL:=http://localhost:3000}"
: "${LISTEN:=127.0.0.1:8081}"
: "${MIDDLE_PUBLIC_URL:=http://localhost:8081}"
: "${DEFAULT_TEMPLATE:=spike-docker}"
: "${OIDC_ISSUER_URL:=http://localhost:8080/realms/coder-lab}"
: "${CODER_OWNER_TOKEN:=$(cat /tmp/coder-spike-owner-token 2>/dev/null || true)}"

# Prefer CONFIG_FILE, else config.yaml next to spike-middle/, else example.
if [[ -z "${CONFIG_FILE:-}" ]]; then
  if [[ -f "$ROOT/config.yaml" ]]; then
    export CONFIG_FILE="$ROOT/config.yaml"
  elif [[ -f "$ROOT/config.example.yaml" ]]; then
    export CONFIG_FILE="$ROOT/config.example.yaml"
  fi
fi

if [[ -z "${CODER_OWNER_TOKEN}" ]]; then
  echo "CODER_OWNER_TOKEN missing (set env or write /tmp/coder-spike-owner-token)" >&2
  exit 1
fi

export CODER_URL CODER_OWNER_TOKEN LISTEN MIDDLE_PUBLIC_URL DEFAULT_TEMPLATE OIDC_ISSUER_URL

echo "CONFIG_FILE=${CONFIG_FILE:-"(defaults + env)"}"
go build -o middle .
exec ./middle
