#!/usr/bin/env bash
# Create ephemeral EKS 1.35 + Bottlerocket cluster for ADR-002 workplace gate.
set -euo pipefail

# shellcheck source=lib.sh
source "$(cd "$(dirname "$0")" && pwd)/lib.sh"
CLUSTER="${CLUSTER:-adr002-git-csi-br}"
REGION="${AWS_REGION:-us-east-1}"
CONFIG="${EKSCTL_CONFIG:-$TEST_ROOT/eks/cluster-bottlerocket.yaml}"

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
echo "target: EKS 1.35 + Bottlerocket cluster=$CLUSTER"

if eksctl get cluster --name "$CLUSTER" --region "$REGION" >/dev/null 2>&1; then
  echo "cluster $CLUSTER already exists; writing kubeconfig"
  aws eks update-kubeconfig --name "$CLUSTER" --region "$REGION"
else
  echo "creating cluster from $CONFIG (15–25+ minutes)..."
  eksctl create cluster -f "$CONFIG"
fi

aws eks update-kubeconfig --name "$CLUSTER" --region "$REGION"

echo "waiting for Ready nodes"
kubectl wait --for=condition=Ready nodes --all --timeout=900s
kubectl get nodes -o wide
kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.nodeInfo.osImage}{"\t"}{.status.nodeInfo.kubeletVersion}{"\n"}{end}'

echo "applying Bottlerocket node storage DaemonSet (extra EBS -> /mnt/git-storage)"
kubectl apply -f "$TEST_ROOT/eks/br-node-storage.yaml"
kubectl -n adr001-system rollout status daemonset/br-node-storage --timeout=300s || true
sleep 15

FAIL=0
for pod in $(kubectl -n adr001-system get pod -l app=br-node-storage -o jsonpath='{.items[*].metadata.name}'); do
  NODE="$(kubectl -n adr001-system get pod "$pod" -o jsonpath='{.spec.nodeName}')"
  if kubectl -n adr001-system logs "$pod" --tail=20 2>/dev/null | grep -q 'br-node-storage OK\|using /mnt/git-storage'; then
    echo "OK storage setup logs on node=$NODE"
  else
    echo "WARN storage logs unclear on node=$NODE — check: kubectl -n adr001-system logs $pod" >&2
  fi
done

echo
echo "Bottlerocket EKS up: context=$(kubectl config current-context)"
echo "Next:"
echo "  1) Rebuild/push fuse image (DirectMountStrict): ./scripts/eks-push-images.sh"
echo "  2) FUSE gate probe:                 ./scripts/run-eks-br-probe.sh"
echo "  3) If PROBE_OK:                     CLUSTER=$CLUSTER ./scripts/run-eks-test.sh"
echo "  4) Tear down:                       ./scripts/eks-br-down.sh"
