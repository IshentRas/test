#!/usr/bin/env bash
# Tear down Bottlerocket EKS lab cluster.
set -euo pipefail

CLUSTER="${CLUSTER:-adr002-git-csi-br}"
REGION="${AWS_REGION:-us-east-1}"
# shellcheck source=lib.sh
source "$(cd "$(dirname "$0")" && pwd)/lib.sh"
CONFIG="${EKSCTL_CONFIG:-$TEST_ROOT/eks/cluster-bottlerocket.yaml}"

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required tool: $1" >&2
    exit 1
  }
}

need aws
need eksctl

if ! eksctl get cluster --name "$CLUSTER" --region "$REGION" >/dev/null 2>&1; then
  echo "cluster $CLUSTER not found in $REGION; nothing to delete"
  exit 0
fi

echo "deleting cluster $CLUSTER in $REGION..."
eksctl delete cluster -f "$CONFIG" --wait
echo "deleted $CLUSTER"
