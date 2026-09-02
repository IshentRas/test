#!/usr/bin/env bash
# Kind lab: upstream fake-git → Go git-replica → Go git-reconciler (no FUSE/CSI).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CLUSTER="${KIND_CLUSTER:-adr001}"
NS="${NS:-adr001}"

UPSTREAM_IMAGE="${UPSTREAM_IMAGE:-adr001-fake-git:local}"
REPLICA_IMAGE="${REPLICA_IMAGE:-adr001-git-replica:local}"
RECONCILER_IMAGE="${RECONCILER_IMAGE:-adr001-git-reconciler-go:local}"

need() { command -v "$1" >/dev/null 2>&1 || { echo "missing $1" >&2; exit 1; }; }
need docker
need kind
need kubectl
need python3

mkdir -p /tmp/adr001-git-backend
chmod 777 /tmp/adr001-git-backend

if [ "${RECREATE:-0}" = "1" ] && kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  kind delete cluster --name "$CLUSTER"
fi
if ! kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  kind create cluster --config "$ROOT/kind/cluster.yaml"
fi

echo "== build images =="
docker build -t "$UPSTREAM_IMAGE" "$ROOT/fake-git"
docker build -f "$ROOT/replica-lab/Dockerfile.replica" -t "$REPLICA_IMAGE" "$ROOT/replica-lab"
docker build -f "$ROOT/replica-lab/Dockerfile.reconciler" -t "$RECONCILER_IMAGE" "$ROOT/replica-lab"

kind load docker-image "$UPSTREAM_IMAGE" "$REPLICA_IMAGE" "$RECONCILER_IMAGE" --name "$CLUSTER"

echo "== apply stack =="
kubectl apply -f "$ROOT/k8s/00-namespace-rbac.yaml"
kubectl apply -f "$ROOT/k8s/10-fake-git.yaml"
kubectl apply -f "$ROOT/k8s/20-release-state.yaml"
kubectl apply -f "$ROOT/k8s/replica-lab/10-stack.yaml"

echo "== wait upstream + replica + reconciler =="
kubectl -n "$NS" rollout status deployment/fake-git --timeout=180s
kubectl -n "$NS" rollout status deployment/git-replica --timeout=180s
kubectl -n "$NS" rollout status deployment/git-reconciler-go --timeout=180s

echo "== read shas from fake-git =="
POD="$(kubectl -n "$NS" get pod -l app=fake-git -o jsonpath='{.items[0].metadata.name}')"
STATE="$(kubectl -n "$NS" exec "$POD" -- cat /git/meta/state.json)"
SHA_A="$(python3 -c "import json,sys; print(json.load(sys.stdin)['sha_a'])" <<<"$STATE")"
SHA_B="$(python3 -c "import json,sys; print(json.load(sys.stdin)['sha_b'])" <<<"$STATE")"
echo "sha_a=$SHA_A"
echo "sha_b=$SHA_B"

echo "== verify replica patched ConfigMap (HEAD = main tip) =="
HEAD_SHA="$(kubectl -n "$NS" exec "$POD" -- git -C /git/http/repo.git rev-parse main)"
for i in $(seq 1 30); do
  ACTIVE="$(kubectl -n "$NS" get configmap git-release-state -o jsonpath='{.data.ACTIVE_COMMIT}')"
  if [ "$ACTIVE" = "$HEAD_SHA" ]; then
    echo "ConfigMap ACTIVE_COMMIT=$ACTIVE (main HEAD)"
    break
  fi
  sleep 2
done
[ "$(kubectl -n "$NS" get configmap git-release-state -o jsonpath='{.data.ACTIVE_COMMIT}')" = "$HEAD_SHA" ] \
  || { echo "FAIL: replica did not publish main HEAD"; exit 1; }

echo "== verify reconciler materialized HEAD on node backend =="
for i in $(seq 1 60); do
  if docker exec "${CLUSTER}-control-plane" test -f "/var/git-backend/current/config/VERSION" 2>/dev/null; then
    VER="$(docker exec "${CLUSTER}-control-plane" cat /var/git-backend/current/config/VERSION 2>/dev/null || true)"
    if [ "$VER" = "commit-b" ]; then
      echo "backend VERSION=$VER"
      break
    fi
  fi
  sleep 2
done
[ "$(docker exec "${CLUSTER}-control-plane" cat /var/git-backend/current/config/VERSION 2>/dev/null)" = "commit-b" ] \
  || { echo "FAIL: reconciler did not materialize main (commit-b)"; exit 1; }

echo "== flip ConfigMap to A (older commit still in repo) =="
TAGS_JSON="$(python3 -c "import json; print(json.dumps({'v1.0.0': '$SHA_A'}))")"
kubectl -n "$NS" create configmap git-release-state \
  --from-literal=ACTIVE_COMMIT="$SHA_A" \
  --from-literal=ACTIVE_TAGS="$TAGS_JSON" \
  -o yaml --dry-run=client | kubectl apply -f -

echo "== verify reconciler converged to A =="
for i in $(seq 1 30); do
  VER="$(docker exec "${CLUSTER}-control-plane" cat /var/git-backend/current/config/VERSION 2>/dev/null || true)"
  SHA="$(docker exec "${CLUSTER}-control-plane" cat /var/git-backend/CURRENT_SHA 2>/dev/null || true)"
  if [ "$VER" = "commit-a" ] && [ "$SHA" = "$SHA_A" ]; then
    echo "backend VERSION=$VER CURRENT_SHA=$SHA"
    break
  fi
  sleep 2
done
[ "$(docker exec "${CLUSTER}-control-plane" cat /var/git-backend/current/config/VERSION)" = "commit-a" ] \
  || { echo "FAIL: flip to A did not converge"; exit 1; }

echo ""
echo "REPLICA LAB OK"
echo "  upstream fake-git  -> git-replica (mirror + CM patch)"
echo "  git-replica        -> git-reconciler-go (watch + fetch + materialize)"
echo "  node backend       -> /var/git-backend on kind control-plane"
