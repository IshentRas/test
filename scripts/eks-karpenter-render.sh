#!/usr/bin/env bash
# Substitute CLUSTER / Karpenter IAM role into eks/karpenter manifests.
set -euo pipefail

# shellcheck source=lib.sh
source "$(cd "$(dirname "$0")" && pwd)/lib.sh"
CLUSTER="${CLUSTER:-adr002-git-csi-karpenter}"
OUT="${1:-$TEST_ROOT/eks/karpenter/rendered}"

export CLUSTER
export KARPENTER_NODE_ROLE="${KARPENTER_NODE_ROLE:-eksctl-KarpenterNodeRole-${CLUSTER}}"

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required tool: $1" >&2
    exit 1
  }
}

need envsubst

mkdir -p "$OUT"
envsubst '${CLUSTER} ${KARPENTER_NODE_ROLE}' \
  < "$TEST_ROOT/eks/karpenter/ec2nodeclass-git-workers.yaml.in" \
  > "$OUT/ec2nodeclass-git-workers.yaml"
cp "$TEST_ROOT/eks/karpenter/nodepool-git-workers.yaml" "$OUT/"
cp "$TEST_ROOT/eks/karpenter/scale-git-workers.yaml" "$OUT/"

echo "rendered Karpenter manifests -> $OUT (cluster=$CLUSTER role=$KARPENTER_NODE_ROLE)"
ls -1 "$OUT"
