#!/usr/bin/env bash
# Create ephemeral EKS 1.35 + Karpenter + Bottlerocket git-worker pool (ADR-002 reference lab).
set -euo pipefail

# shellcheck source=lib.sh
source "$(cd "$(dirname "$0")" && pwd)/lib.sh"
CLUSTER="${CLUSTER:-adr002-git-csi-karpenter}"
REGION="${AWS_REGION:-us-east-1}"
CONFIG="${EKSCTL_CONFIG:-$TEST_ROOT/eks/cluster-karpenter.yaml}"
GIT_WORKERS="${GIT_WORKERS:-3}"

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required tool: $1" >&2
    exit 1
  }
}

need aws
need eksctl
need kubectl
need envsubst

echo "account=$(aws sts get-caller-identity --query Account --output text) region=$REGION"
echo "target: EKS 1.35 + Karpenter + Bottlerocket git-workers cluster=$CLUSTER"

if eksctl get cluster --name "$CLUSTER" --region "$REGION" >/dev/null 2>&1; then
  echo "cluster $CLUSTER already exists; writing kubeconfig"
  aws eks update-kubeconfig --name "$CLUSTER" --region "$REGION"
else
  echo "creating cluster from $CONFIG (20–30+ minutes; installs Karpenter via eksctl)..."
  eksctl create cluster -f "$CONFIG"
fi

aws eks update-kubeconfig --name "$CLUSTER" --region "$REGION"

echo "waiting for bootstrap nodes"
kubectl wait --for=condition=Ready nodes --all --timeout=900s
kubectl get nodes -o wide

echo "waiting for Karpenter controller"
kubectl -n kube-system rollout status deployment/karpenter --timeout=600s 2>/dev/null \
  || kubectl -n karpenter rollout status deployment/karpenter --timeout=600s

RENDERED="$TEST_ROOT/eks/karpenter/rendered"
bash "$TEST_ROOT/scripts/eks-karpenter-render.sh" "$RENDERED"

echo "applying EC2NodeClass + NodePool"
kubectl apply -f "$RENDERED/ec2nodeclass-git-workers.yaml"
kubectl apply -f "$RENDERED/nodepool-git-workers.yaml"

echo "scaling git-worker pool to $GIT_WORKERS nodes (Karpenter provision)"
kubectl apply -f "$RENDERED/scale-git-workers.yaml"
kubectl -n adr001-system scale deployment/git-worker-scale --replicas="$GIT_WORKERS" 2>/dev/null || true

echo "waiting for $GIT_WORKERS git-worker nodes (label adr002.io/role=git-worker)"
deadline=$((SECONDS + 900))
while [ "$SECONDS" -lt "$deadline" ]; do
  n="$(kubectl get nodes -l adr002.io/role=git-worker --no-headers 2>/dev/null | wc -l | tr -d ' ')"
  echo "git-worker nodes: $n / $GIT_WORKERS"
  if [ "$n" -ge "$GIT_WORKERS" ]; then
    kubectl wait --for=condition=Ready nodes -l adr002.io/role=git-worker --timeout=300s
    break
  fi
  sleep 15
done

n="$(kubectl get nodes -l adr002.io/role=git-worker --no-headers 2>/dev/null | wc -l | tr -d ' ')"
if [ "$n" -lt "$GIT_WORKERS" ]; then
  echo "WARN: only $n git-worker nodes after timeout — check: kubectl get nodeclaims,nodepools,ec2nodeclasses" >&2
fi

kubectl get nodes -l adr002.io/role=git-worker -o custom-columns=NAME:.metadata.name,INSTANCE:.metadata.labels.node\\.kubernetes\\.io/instance-type,OS:.status.nodeInfo.osImage

echo "applying Bottlerocket node storage (UBI9 bootstrap -> /mnt/git-storage on extra EBS)"
kubectl apply -f "$TEST_ROOT/eks/br-node-storage.yaml"
kubectl -n adr001-system rollout status daemonset/br-node-storage --timeout=600s || true
sleep 10

for pod in $(kubectl -n adr001-system get pod -l app=br-node-storage -o jsonpath='{.items[*].metadata.name}' 2>/dev/null); do
  NODE="$(kubectl -n adr001-system get pod "$pod" -o jsonpath='{.spec.nodeName}')"
  if kubectl -n adr001-system logs "$pod" --tail=20 2>/dev/null | grep -q 'br-node-storage OK\|using /mnt/git-storage'; then
    echo "OK storage setup on node=$NODE"
  else
    echo "WARN storage logs unclear on node=$NODE — kubectl -n adr001-system logs $pod" >&2
  fi
done

echo
echo "Karpenter EKS up: context=$(kubectl config current-context)"
echo "Next:"
echo "  1) UBI9 reconciler + images:  USE_UBI9=1 IMAGE_TAG=eks-karpenter ./scripts/eks-push-images.sh"
echo "  2) FUSE gate probe:             ./scripts/run-eks-karpenter-probe.sh"
echo "  3) Full quest:                  CLUSTER=$CLUSTER ./scripts/run-eks-test.sh"
echo "  4) Tear down:                   ./scripts/eks-karpenter-down.sh"
