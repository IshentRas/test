#!/usr/bin/env bash
# Bottlerocket FUSE gate: host-ns DirectMountStrict without fusermount.
set -euo pipefail

# shellcheck source=lib.sh
source "$(cd "$(dirname "$0")" && pwd)/lib.sh"
CLUSTER="${CLUSTER:-adr002-git-csi-br}"
REGION="${AWS_REGION:-us-east-1}"
ENV_FILE="${ENV_FILE:-$TEST_ROOT/eks/images.env}"
NS=adr001-system

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required tool: $1" >&2
    exit 1
  }
}

need aws
need kubectl

aws eks update-kubeconfig --name "$CLUSTER" --region "$REGION" >/dev/null

if [ -f "$ENV_FILE" ]; then
  # shellcheck disable=SC1090
  source "$ENV_FILE"
fi

PROBE="$TEST_ROOT/eks/probe-br-fuse.yaml"
if [ -n "${FUSE_CSI_IMAGE:-}" ]; then
  TMP="$(mktemp)"
  sed "s|image: .*adr001-fuse-csi:.*|image: ${FUSE_CSI_IMAGE}|" "$PROBE" > "$TMP"
  PROBE="$TMP"
fi

kubectl apply -f "$TEST_ROOT/eks/br-node-storage.yaml"
kubectl apply -f "$PROBE"
kubectl -n "$NS" rollout status daemonset/br-fuse-probe --timeout=300s || true

echo "waiting for probe logs..."
sleep 25

OK=0
FAIL=0
for pod in $(kubectl -n "$NS" get pod -l app=br-fuse-probe -o jsonpath='{.items[*].metadata.name}'); do
  NODE="$(kubectl -n "$NS" get pod "$pod" -o jsonpath='{.spec.nodeName}')"
  LOG="$(kubectl -n "$NS" logs "$pod" --tail=80 2>/dev/null || true)"
  if echo "$LOG" | grep -q 'PROBE_OK'; then
    echo "PASS node=$NODE"
    OK=$((OK + 1))
  else
    echo "FAIL node=$NODE"
    echo "$LOG" | tail -40
    FAIL=$((FAIL + 1))
  fi
done

echo "summary: PROBE_OK=$OK PROBE_FAIL=$FAIL"
if [ "$FAIL" -ne 0 ] || [ "$OK" -eq 0 ]; then
  echo "Bottlerocket FUSE gate FAILED — do not claim workplace readiness."
  echo "Options: DirectMount debug, AL2023 git-worker pool, or wait on Bottlerocket FUSE support."
  exit 1
fi

echo "Bottlerocket FUSE gate PASSED — proceed with CLUSTER=$CLUSTER ./scripts/run-eks-test.sh"
