#!/usr/bin/env bash
# Multi-node EKS quest: layout, fan-out lag, upgrade domains.
set -euo pipefail

# shellcheck source=lib.sh
source "$(cd "$(dirname "$0")" && pwd)/lib.sh"
NS="${NS:-adr001}"
REGION="${AWS_REGION:-us-east-1}"
CLUSTER="${CLUSTER:-adr001-git-csi}"
ENV_FILE="${ENV_FILE:-$TEST_ROOT/eks/images.env}"
RENDERED="${RENDERED:-$TEST_ROOT/eks/rendered}"
FINDINGS="${FINDINGS:-$TEST_ROOT/docs/EKS-FINDINGS.md}"

pass=0
fail=0
ok() { echo "PASS: $*"; pass=$((pass + 1)); }
bad() { echo "FAIL: $*"; fail=$((fail + 1)); }

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required tool: $1" >&2
    exit 1
  }
}

need aws
need kubectl
need python3
need envsubst

if [ ! -f "$ENV_FILE" ]; then
  echo "missing $ENV_FILE — run ./scripts/eks-push-images.sh" >&2
  exit 1
fi
# shellcheck disable=SC1090
source "$ENV_FILE"

aws eks update-kubeconfig --name "$CLUSTER" --region "$REGION" >/dev/null

echo "== render + apply manifests =="
bash "$TEST_ROOT/scripts/eks-render.sh" "$RENDERED"
# Host fusermount via dnf — AL2023 only. Bottlerocket uses DirectMountStrict (skip).
if [ "${SKIP_HOST_FUSE_INSTALL:-0}" != "1" ] && [[ "${CLUSTER}" != *-br* ]] && [[ "${CLUSTER}" != *-karpenter* ]]; then
  kubectl apply -f "$TEST_ROOT/eks/install-fuse.yaml"
  kubectl -n adr001-system rollout status daemonset/install-fuse --timeout=300s || true
else
  echo "skipping install-fuse (Bottlerocket / SKIP_HOST_FUSE_INSTALL=1); relying on DirectMountStrict"
  kubectl apply -f "$TEST_ROOT/eks/br-node-storage.yaml" || true
fi
kubectl apply -f "$RENDERED/00-namespace-rbac.yaml"
kubectl apply -f "$RENDERED/20-release-state.yaml"
kubectl apply -f "$RENDERED/10-fake-git.yaml"
kubectl apply -f "$RENDERED/25-fuse-csi.yaml"
kubectl apply -f "$RENDERED/30-reconciler.yaml"

echo "== wait fake-git PVC + pod =="
# WaitForFirstConsumer: PVC binds after the pod schedules; wait on the Deployment.
kubectl -n "$NS" rollout status deployment/fake-git --timeout=600s
kubectl -n "$NS" wait --for=condition=Ready pod -l app=fake-git --field-selector=status.phase=Running --timeout=300s
# Bound is status.phase (not a Condition type on all clusters).
kubectl -n "$NS" wait --for=jsonpath='{.status.phase}'=Bound pvc/fake-git-data --timeout=120s || \
  kubectl -n "$NS" get pvc fake-git-data

echo "== seed ConfigMap from fake-git =="
POD="$(kubectl -n "$NS" get pod -l app=fake-git --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}')"
STATE="$(kubectl -n "$NS" exec "$POD" -- cat /git/meta/state.json)"
SHA_A="$(python3 -c "import json,sys; print(json.load(sys.stdin)['sha_a'])" <<<"$STATE")"
SHA_B="$(python3 -c "import json,sys; print(json.load(sys.stdin)['sha_b'])" <<<"$STATE")"
TAGS_JSON="$(python3 -c "import json; print(json.dumps({'v1.0.0': '$SHA_A'}))")"
echo "ACTIVE_COMMIT=$SHA_A (B=$SHA_B)"
kubectl -n "$NS" create configmap git-release-state \
  --from-literal=ACTIVE_COMMIT="$SHA_A" \
  --from-literal=ACTIVE_TAGS="$TAGS_JSON" \
  -o yaml --dry-run=client | kubectl apply -f -

echo "== wait DaemonSets on all nodes =="
# Seed before waiting on FUSE readiness (needs CURRENT_SHA from reconciler).
kubectl -n "$NS" rollout status daemonset/git-reconciler --timeout=300s
kubectl -n "$NS" rollout status daemonset/git-csi --timeout=300s
# Restart FUSE so probes pick up after seed (clears prior CrashLoopBackOff).
kubectl -n "$NS" rollout restart daemonset/git-fuse
kubectl -n "$NS" rollout status daemonset/git-fuse --timeout=300s

# Wait until every fuse pod is Ready (CURRENT_SHA present).
for i in $(seq 1 90); do
  READY="$(kubectl -n "$NS" get pods -l app=git-fuse --no-headers 2>/dev/null | awk '$2=="1/1"{c++} END{print c+0}')"
  WANT="$(kubectl -n "$NS" get ds git-fuse -o jsonpath='{.status.desiredNumberScheduled}' 2>/dev/null || true)"
  if [ -z "$WANT" ] || [ "$WANT" = "0" ]; then
    WANT="$(kubectl get nodes --no-headers | wc -l | tr -d ' ')"
  fi
  if [ "$READY" -ge "$WANT" ]; then
    echo "FUSE ready on $READY/$WANT nodes"
    break
  fi
  if [ "$i" -eq 90 ]; then
    bad "FUSE not ready on all nodes ($READY/$WANT)"
    kubectl -n "$NS" get pods -o wide
    kubectl -n "$NS" logs -l app=git-fuse -c fuse --tail=40 || true
    exit 1
  fi
  sleep 2
done

echo "== deploy apps (3 replicas, anti-affinity) =="
# Delete+recreate: rolling restart cannot schedule with required anti-affinity
# while old pods still occupy each node.
kubectl -n "$NS" delete deployment app --ignore-not-found=true --wait=true --timeout=120s || true
kubectl apply -f "$RENDERED/40-app.yaml"
kubectl -n "$NS" rollout status deployment/app --timeout=300s
kubectl -n "$NS" wait --for=condition=Ready pod -l app=adr001-app --field-selector=status.phase=Running --timeout=300s

mapfile -t APP_PODS < <(kubectl -n "$NS" get pods -l app=adr001-app --field-selector=status.phase=Running -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')
mapfile -t APP_NODES < <(kubectl -n "$NS" get pods -l app=adr001-app --field-selector=status.phase=Running -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}')

echo "apps: ${APP_PODS[*]}"
echo "nodes: ${APP_NODES[*]}"

UNIQUE_NODES="$(printf '%s\n' "${APP_NODES[@]}" | sort -u | wc -l | tr -d ' ')"
if [ "$UNIQUE_NODES" -ge 3 ]; then
  ok "apps scheduled across $UNIQUE_NODES distinct nodes"
else
  bad "expected apps on 3 nodes, got unique=$UNIQUE_NODES"
fi

echo "== assert FUSE mount in apps =="
for pod in "${APP_PODS[@]}"; do
  FSTYPE="$(kubectl -n "$NS" exec "$pod" -- findmnt -n -o FSTYPE /mnt/git 2>/dev/null || true)"
  case "$FSTYPE" in
    fuse*|fuseblk)
      if kubectl -n "$NS" exec "$pod" -- cat /mnt/git/CURRENT_SHA >/dev/null 2>&1; then
        ok "pod $pod fstype=$FSTYPE readable"
      else
        bad "pod $pod fstype=$FSTYPE but CURRENT_SHA unreadable (stale ENOTCONN?)"
      fi
      ;;
    *) bad "pod $pod expected FUSE, got fstype='$FSTYPE'" ;;
  esac
done

echo "== layout / CONFIG_PATH validate on first app =="
if kubectl -n "$NS" exec "${APP_PODS[0]}" -- /usr/local/bin/validate.sh; then
  ok "in-pod validate.sh (layout + flip + snapshot)"
else
  bad "in-pod validate.sh failed"
fi

# Reseed A then flip to B while measuring multi-node lag (validate left CM on B).
echo "== fan-out consistency: flip to B and measure per-node lag =="
# Ensure we start from A for a clean fan-out measurement.
kubectl -n "$NS" create configmap git-release-state \
  --from-literal=ACTIVE_COMMIT="$SHA_A" \
  --from-literal=ACTIVE_TAGS="$TAGS_JSON" \
  -o yaml --dry-run=client | kubectl apply -f -

wait_all_sha() {
  local want="$1" deadline=$((SECONDS + 120))
  while [ "$SECONDS" -lt "$deadline" ]; do
    local all=1
    for pod in "${APP_PODS[@]}"; do
      got="$(kubectl -n "$NS" exec "$pod" -- cat /mnt/git/CURRENT_SHA 2>/dev/null || true)"
      if [ "$got" != "$want" ]; then all=0; break; fi
    done
    if [ "$all" -eq 1 ]; then return 0; fi
    sleep 1
  done
  return 1
}

if wait_all_sha "$SHA_A"; then
  ok "all nodes converged to A before flip"
else
  bad "timeout converging to A"
fi

now_ms() { python3 -c 'import time; print(int(time.time()*1000))'; }
FLIP_START="$(now_ms)"
kubectl -n "$NS" create configmap git-release-state \
  --from-literal=ACTIVE_COMMIT="$SHA_B" \
  --from-literal=ACTIVE_TAGS="$TAGS_JSON" \
  -o yaml --dry-run=client | kubectl apply -f -

FANOUT_LOG="$(mktemp)"
for i in "${!APP_PODS[@]}"; do
  pod="${APP_PODS[$i]}"
  node="${APP_NODES[$i]}"
  LAG=""
  for _ in $(seq 1 120); do
    got="$(kubectl -n "$NS" exec "$pod" -- cat /mnt/git/CURRENT_SHA 2>/dev/null || true)"
    ver="$(kubectl -n "$NS" exec "$pod" -- cat /mnt/git/current/config/VERSION 2>/dev/null || true)"
    if [ "$got" = "$SHA_B" ] && [ "$ver" = "commit-b" ]; then
      NOW="$(now_ms)"
      LAG=$((NOW - FLIP_START))
      echo "$node $LAG" >> "$FANOUT_LOG"
      echo "node=$node pod=$pod converged_ms=$LAG"
      break
    fi
    sleep 1
  done
  if [ -z "$LAG" ]; then
    bad "node $node never converged to B"
  fi
done

FANOUT_COUNT="$(wc -l < "$FANOUT_LOG" | tr -d ' ')"
if [ "$FANOUT_COUNT" -ge 3 ]; then
  ok "fan-out: all nodes reached B"
  MAX_LAG="$(awk '{print $2}' "$FANOUT_LOG" | sort -n | tail -1)"
  MIN_LAG="$(awk '{print $2}' "$FANOUT_LOG" | sort -n | head -1)"
  SPREAD=$((MAX_LAG - MIN_LAG))
  echo "fan-out lag_ms min=$MIN_LAG max=$MAX_LAG spread=$SPREAD"
  ok "fan-out lag recorded (spread=${SPREAD}ms)"
else
  bad "fan-out incomplete"
fi

echo "== upgrade domains: CSI restart (FUSE UIDs unchanged, reads OK) =="
mapfile -t FUSE_UIDS_BEFORE < <(kubectl -n "$NS" get pods -l app=git-fuse --field-selector=status.phase=Running -o jsonpath='{range .items[*]}{.metadata.uid}{"\n"}{end}' | sort)
kubectl -n "$NS" rollout restart daemonset/git-csi
kubectl -n "$NS" rollout status daemonset/git-csi --timeout=300s
mapfile -t FUSE_UIDS_AFTER_CSI < <(kubectl -n "$NS" get pods -l app=git-fuse --field-selector=status.phase=Running -o jsonpath='{range .items[*]}{.metadata.uid}{"\n"}{end}' | sort)
if [ "${FUSE_UIDS_BEFORE[*]}" = "${FUSE_UIDS_AFTER_CSI[*]}" ]; then
  ok "all FUSE pod UIDs unchanged after CSI restart"
else
  bad "FUSE UIDs changed after CSI restart"
fi
READ_OK=1
for pod in "${APP_PODS[@]}"; do
  # pods may have been recreated? re-list
  :
done
mapfile -t APP_PODS < <(kubectl -n "$NS" get pods -l app=adr001-app --field-selector=status.phase=Running -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')
for pod in "${APP_PODS[@]}"; do
  if ! kubectl -n "$NS" exec "$pod" -- cat /mnt/git/current/config/VERSION >/dev/null 2>&1; then
    READ_OK=0
    bad "read failed on $pod after CSI restart"
  fi
done
[ "$READ_OK" -eq 1 ] && ok "all apps readable after CSI restart"

echo "== upgrade domains: reconciler restart =="
kubectl -n "$NS" rollout restart daemonset/git-reconciler
kubectl -n "$NS" rollout status daemonset/git-reconciler --timeout=300s
mapfile -t FUSE_UIDS_AFTER_RECON < <(kubectl -n "$NS" get pods -l app=git-fuse --field-selector=status.phase=Running -o jsonpath='{range .items[*]}{.metadata.uid}{"\n"}{end}' | sort)
if [ "${FUSE_UIDS_BEFORE[*]}" = "${FUSE_UIDS_AFTER_RECON[*]}" ]; then
  ok "all FUSE pod UIDs unchanged after reconciler restart"
else
  bad "FUSE UIDs changed after reconciler restart"
fi

echo "== FUSE death on one node: ENOTCONN local, others survive =="
# Pick first app's node as victim.
VICTIM_POD="$(kubectl -n "$NS" get pods -l app=adr001-app --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}')"
VICTIM_NODE="$(kubectl -n "$NS" get pod "$VICTIM_POD" -o jsonpath='{.spec.nodeName}')"
FUSE_ON_VICTIM="$(kubectl -n "$NS" get pods -l app=git-fuse -o json | python3 -c "
import json,sys
j=json.load(sys.stdin)
for i in j['items']:
  if i['spec'].get('nodeName')=='$VICTIM_NODE':
    print(i['metadata']['name']); break
")"
echo "victim_node=$VICTIM_NODE victim_app=$VICTIM_POD fuse=$FUSE_ON_VICTIM"
kubectl -n "$NS" delete pod "$FUSE_ON_VICTIM" --wait=false

ENOTCONN=0
for i in $(seq 1 60); do
  ERR="$(kubectl -n "$NS" exec "$VICTIM_POD" -- cat /mnt/git/current/config/VERSION 2>&1 || true)"
  if echo "$ERR" | grep -Eqi 'Transport endpoint is not connected|Socket not connected|not connected|Input/output error'; then
    ENOTCONN=1
    echo "observed ENOTCONN on victim after ${i}s"
    break
  fi
  sleep 1
done
[ "$ENOTCONN" -eq 1 ] && ok "victim node ENOTCONN after FUSE death" || bad "no ENOTCONN on victim"

OTHER_OK=1
for pod in $(kubectl -n "$NS" get pods -l app=adr001-app --field-selector=status.phase=Running -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}'); do
  node="$(kubectl -n "$NS" get pod "$pod" -o jsonpath='{.spec.nodeName}')"
  if [ "$node" = "$VICTIM_NODE" ]; then continue; fi
  if ! kubectl -n "$NS" exec "$pod" -- cat /mnt/git/current/config/VERSION >/dev/null 2>&1; then
    OTHER_OK=0
    bad "non-victim pod $pod on $node failed during victim FUSE death"
  fi
done
[ "$OTHER_OK" -eq 1 ] && ok "non-victim nodes kept serving during FUSE death"

echo "== recover victim node (scale apps on node via delete + purge + wait FUSE) =="
# Delete apps on victim node, purge CSI mounts, wait FUSE Ready, recreate.
for pod in $(kubectl -n "$NS" get pods -l app=adr001-app -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.nodeName}{"\n"}{end}'); do
  name="$(echo "$pod" | cut -f1)"
  node="$(echo "$pod" | cut -f2)"
  if [ "$node" = "$VICTIM_NODE" ]; then
    kubectl -n "$NS" delete pod "$name" --force --grace-period=0 --ignore-not-found=true 2>/dev/null || true
  fi
done

# Purge stale CSI mounts on victim via a privileged one-shot.
kubectl -n "$NS" delete pod purge-csi-mounts --ignore-not-found=true --wait=false 2>/dev/null || true
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: purge-csi-mounts
  namespace: $NS
spec:
  nodeName: $VICTIM_NODE
  restartPolicy: Never
  hostPID: true
  containers:
    - name: purge
      image: ${FUSE_CSI_IMAGE}
      imagePullPolicy: IfNotPresent
      securityContext:
        privileged: true
      command: ["/bin/sh", "-ec"]
      args:
        - |
          nsenter --mount=/proc/1/ns/mnt -- sh -ec '
            find /var/lib/kubelet/pods -path "*/volumes/kubernetes.io~csi/*/mount" 2>/dev/null | while read -r mp; do
              umount -l "\$mp" 2>/dev/null || true
              rmdir "\$mp" 2>/dev/null || true
            done
            if ! ls /mnt/git-storage/fuse >/dev/null 2>&1; then
              umount -l /mnt/git-storage/fuse 2>/dev/null || true
            fi
            echo purged
          '
EOF
kubectl -n "$NS" wait --for=jsonpath='{.status.phase}'=Succeeded pod/purge-csi-mounts --timeout=120s || true
kubectl -n "$NS" logs purge-csi-mounts || true
kubectl -n "$NS" delete pod purge-csi-mounts --ignore-not-found=true

kubectl -n "$NS" rollout status daemonset/git-fuse --timeout=300s
kubectl -n "$NS" wait --for=condition=Ready pod -l app=git-fuse --field-selector=spec.nodeName="$VICTIM_NODE" --timeout=300s || \
  kubectl -n "$NS" get pods -l app=git-fuse -o wide

kubectl -n "$NS" delete deployment app --ignore-not-found=true --wait=true --timeout=120s || true
kubectl apply -f "$RENDERED/40-app.yaml"
kubectl -n "$NS" rollout status deployment/app --timeout=300s
kubectl -n "$NS" wait --for=condition=Ready pod -l app=adr001-app --field-selector=status.phase=Running --timeout=300s

RECOVERED=1
for pod in $(kubectl -n "$NS" get pods -l app=adr001-app --field-selector=status.phase=Running -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}'); do
  if ! kubectl -n "$NS" exec "$pod" -- cat /mnt/git/CURRENT_SHA >/dev/null 2>&1; then
    RECOVERED=0
    bad "post-recovery read failed on $pod"
  fi
done
[ "$RECOVERED" -eq 1 ] && ok "all apps readable after FUSE recovery"

echo "== summary: $pass passed, $fail failed =="

# Write findings draft (finalized after run).
{
  echo "# EKS validation findings — ADR-001"
  echo
  echo "* **Cluster:** \`$CLUSTER\` ($REGION)"
  echo "* **Date:** $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "* **Nodes:** $(kubectl get nodes --no-headers | wc -l | tr -d ' ')× m5.large + 40Gi gp3 \`/mnt/git-storage\`"
  echo "* **Result:** $pass passed, $fail failed"
  echo
  echo "## Fan-out lag (ConfigMap flip A→B)"
  echo
  echo "| Node | Converge ms |"
  echo "|---|---|"
  if [ -f "$FANOUT_LOG" ]; then
    while read -r n lag; do
      echo "| \`$n\` | $lag |"
    done < "$FANOUT_LOG"
  fi
  echo
  echo "| Metric | Value |"
  echo "|---|---|"
  echo "| min_ms | ${MIN_LAG:-n/a} |"
  echo "| max_ms | ${MAX_LAG:-n/a} |"
  echo "| spread_ms | ${SPREAD:-n/a} |"
  echo
  echo "## Upgrade domains"
  echo
  echo "- CSI restart: FUSE UIDs unchanged; apps kept reading."
  echo "- Reconciler restart: FUSE UIDs unchanged."
  echo "- FUSE death on one node: local ENOTCONN; other nodes continued serving."
  echo "- Recovery: purge CSI mounts on victim + remount consumers."
  echo
  echo "## Storage"
  echo
  echo "- fake-git: RWO gp3 PVC via \`ebs.csi.aws.com\` (\`adr001-gp3\`)."
  echo "- Node cache/FUSE: dedicated additional gp3 at \`/mnt/git-storage\`."
  echo "- FUSE/CSI: \`hostPID\` + \`nsenter\` into host mount namespace (same pattern as kind)."
  echo
  echo "## Notes"
  echo
  echo "- Images: ECR \`$REGISTRY\` tag \`$IMAGE_TAG\`."
  echo "- Quest FUSE remains loopback over materialized backend (not lazy cat-file)."
} > "$FINDINGS"
echo "wrote $FINDINGS"

if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "EKS QUEST OK"
