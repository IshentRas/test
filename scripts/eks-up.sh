#!/usr/bin/env bash
# Create ephemeral EKS cluster and wait for 3 nodes with /mnt/git-storage.
set -euo pipefail

# shellcheck source=lib.sh
source "$(cd "$(dirname "$0")" && pwd)/lib.sh"
CLUSTER="${CLUSTER:-adr001-git-csi}"
REGION="${AWS_REGION:-us-east-1}"
CONFIG="${EKSCTL_CONFIG:-$TEST_ROOT/eks/cluster.yaml}"

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required tool: $1" >&2
    exit 1
  }
}

need aws
need eksctl
need kubectl

echo "account=$(aws sts get-caller-identity --query Account --output text) region=$REGION"

if eksctl get cluster --name "$CLUSTER" --region "$REGION" >/dev/null 2>&1; then
  echo "cluster $CLUSTER already exists; writing kubeconfig"
  aws eks update-kubeconfig --name "$CLUSTER" --region "$REGION"
else
  echo "creating cluster from $CONFIG (15–20+ minutes)..."
  eksctl create cluster -f "$CONFIG"
fi

aws eks update-kubeconfig --name "$CLUSTER" --region "$REGION"

echo "waiting for 3 Ready nodes"
kubectl wait --for=condition=Ready nodes --all --timeout=600s
NODE_COUNT="$(kubectl get nodes --no-headers | wc -l | tr -d ' ')"
if [ "$NODE_COUNT" -lt 3 ]; then
  echo "expected >=3 nodes, got $NODE_COUNT" >&2
  kubectl get nodes -o wide
  exit 1
fi

echo "verifying /mnt/git-storage on each node via debug pods"
kubectl apply -f "$TEST_ROOT/eks/verify-node-storage.yaml"
kubectl -n adr001-system wait --for=condition=Ready pod -l app=git-storage-verify --timeout=180s || true

FAIL=0
for pod in $(kubectl -n adr001-system get pod -l app=git-storage-verify -o jsonpath='{.items[*].metadata.name}'); do
  NODE="$(kubectl -n adr001-system get pod "$pod" -o jsonpath='{.spec.nodeName}')"
  if kubectl -n adr001-system exec "$pod" -- sh -c 'mountpoint -q /mnt/git-storage && test -d /mnt/git-storage/backend && test -d /mnt/git-storage/fuse'; then
    FS="$(kubectl -n adr001-system exec "$pod" -- sh -c 'df -h /mnt/git-storage | tail -1')"
    echo "OK node=$NODE storage ready ($FS)"
  else
    echo "FAIL node=$NODE storage missing" >&2
    kubectl -n adr001-system exec "$pod" -- sh -c 'df -h /mnt/git-storage; ls -la /mnt/git-storage 2>&1' || true
    FAIL=1
  fi
done

kubectl -n adr001-system delete daemonset git-storage-verify --ignore-not-found=true >/dev/null 2>&1 || true

if [ "$FAIL" -ne 0 ]; then
  exit 1
fi

# Ensure host fusermount is present (bootstrap should install; DS covers existing nodes).
kubectl apply -f "$TEST_ROOT/eks/install-fuse.yaml"
echo "EKS up: context=$(kubectl config current-context)"
echo "Next: ./scripts/eks-push-images.sh && ./scripts/run-eks-test.sh"
