#!/usr/bin/env bash
# Kind lab: OverlayFS COW workspace over an immutable lowerdir (materialized Git stand-in).
# Proves: edit divert, whiteout delete, revert, scratch durability across pod recreate.
#
# Kind-on-Mac note: node root is overlay and virtiofs cannot be an OverlayFS upperdir.
# Scratch is a loop-backed ext4 volume on the node (stand-in for a real PVC/XFS/ext4).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CLUSTER="${KIND_CLUSTER:-adr001}"
NS="${NS:-adr001}"
NODE="${CLUSTER}-control-plane"
NODE_LAB="/overlay-lab"
# Persist the scratch image on the kind extraMount so it survives node remounts in-lab.
SCRATCH_IMG="/var/git-backend/overlay-lab-scratch.img"

need() { command -v "$1" >/dev/null 2>&1 || { echo "missing $1" >&2; exit 1; }; }
need docker
need kind
need kubectl

mkdir -p /tmp/adr001-git-backend
chmod 777 /tmp/adr001-git-backend

if [ "${RECREATE:-0}" = "1" ] && kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  kind delete cluster --name "$CLUSTER"
fi
if ! kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  kind create cluster --config "$ROOT/kind/cluster.yaml"
fi

node() { docker exec "$NODE" "$@"; }

echo "== prepare lowerdir + loop-ext4 scratch on kind node =="
kubectl -n "$NS" delete pod overlay-workspace --ignore-not-found=true --wait=true 2>/dev/null || true

# Tear down previous overlay/scratch mounts (ignore errors).
node sh -c "umount /workspace 2>/dev/null || true; umount $NODE_LAB/merged 2>/dev/null || true"
node sh -c "umount $NODE_LAB/scratch 2>/dev/null || true"
node rm -rf "$NODE_LAB"
node mkdir -p "$NODE_LAB/lower/config"

# Create/reset ext4 scratch image (PVC stand-in).
node sh -c "
  mkdir -p /var/git-backend
  rm -f '$SCRATCH_IMG'
  dd if=/dev/zero of='$SCRATCH_IMG' bs=1M count=64 status=none
  mkfs.ext4 -F -q '$SCRATCH_IMG'
  mkdir -p '$NODE_LAB/scratch'
  mount -o loop '$SCRATCH_IMG' '$NODE_LAB/scratch'
  mkdir -p '$NODE_LAB/scratch/upper' '$NODE_LAB/scratch/work'
  printf 'sha-fixture-a\n' >'$NODE_LAB/scratch/.base_commit_sha'
  chmod -R a+rwX '$NODE_LAB/scratch'
"

node sh -c "printf 'commit-a\n' >$NODE_LAB/lower/config/VERSION"
node sh -c "printf 'base-model-v1\n' >$NODE_LAB/lower/model.py"
node sh -c "printf 'keep-me\n' >$NODE_LAB/lower/README.md"
node chmod -R a+rX "$NODE_LAB/lower"

echo "== ensure namespace =="
kubectl apply -f "$ROOT/k8s/00-namespace-rbac.yaml" >/dev/null
kubectl -n "$NS" wait --for=jsonpath='{.metadata.name}'=default serviceaccount/default --timeout=60s

echo "== deploy overlay workspace pod =="
kubectl apply -f "$ROOT/k8s/overlay-lab/10-workspace.yaml"
kubectl -n "$NS" wait --for=condition=Ready pod/overlay-workspace --timeout=120s

exec_ws() {
  kubectl -n "$NS" exec overlay-workspace -- /bin/sh -ec "$1"
}

echo "== 1) reads come from lowerdir =="
VER="$(exec_ws 'cat /workspace/config/VERSION')"
MODEL="$(exec_ws 'cat /workspace/model.py')"
[ "$VER" = "commit-a" ] || { echo "FAIL: VERSION=$VER"; exit 1; }
[ "$MODEL" = "base-model-v1" ] || { echo "FAIL: model=$MODEL"; exit 1; }
echo "ok: lowerdir visible through /workspace"

echo "== 2) COW edit lands only in upperdir =="
exec_ws 'echo edited-model > /workspace/model.py'
exec_ws 'echo brand-new > /workspace/notes.txt'
UPPER_MODEL="$(node cat "$NODE_LAB/scratch/upper/model.py")"
LOWER_MODEL="$(node cat "$NODE_LAB/lower/model.py")"
[ "$UPPER_MODEL" = "edited-model" ] || { echo "FAIL: upper model=$UPPER_MODEL"; exit 1; }
[ "$LOWER_MODEL" = "base-model-v1" ] || { echo "FAIL: lower mutated: $LOWER_MODEL"; exit 1; }
[ "$(exec_ws 'cat /workspace/model.py')" = "edited-model" ] || { echo "FAIL: merged view"; exit 1; }
[ "$(exec_ws 'cat /workspace/notes.txt')" = "brand-new" ] || { echo "FAIL: new file"; exit 1; }
echo "ok: edits diverted to upper; lower untouched"

echo "== 3) rm base file creates whiteout; lower untouched =="
exec_ws 'rm -f /workspace/README.md'
exec_ws 'test ! -e /workspace/README.md'
node test -f "$NODE_LAB/lower/README.md" || { echo "FAIL: lower README missing"; exit 1; }
if node sh -c "test -e $NODE_LAB/scratch/upper/README.md || test -e $NODE_LAB/scratch/upper/.wh.README.md"; then
  echo "ok: whiteout present in upper"
else
  node ls -la "$NODE_LAB/scratch/upper" >&2 || true
  echo "FAIL: no whiteout node in upper"
  exit 1
fi

remount_ws() {
  exec_ws '
    umount /workspace
    mount -t overlay overlay \
      -o lowerdir=/lower,upperdir=/scratch/upper,workdir=/scratch/work \
      /workspace
  '
}

echo "== 4) revert edit = drop upper copy; base shows through =="
exec_ws 'rm -f /scratch/upper/model.py'
remount_ws
[ "$(exec_ws 'cat /workspace/model.py')" = "base-model-v1" ] || {
  echo "FAIL: revert did not restore base model"
  exec_ws 'ls -la /scratch/upper; cat /workspace/model.py' >&2 || true
  exit 1
}
echo "ok: revert by deleting upper file"

echo "== 5) undelete = drop whiteout; base shows through =="
exec_ws 'rm -f /scratch/upper/README.md /scratch/upper/.wh.README.md'
remount_ws
[ "$(exec_ws 'cat /workspace/README.md')" = "keep-me" ] || {
  echo "FAIL: undelete did not restore README"
  exec_ws 'ls -la /scratch/upper' >&2 || true
  exit 1
}
echo "ok: undelete by removing whiteout"

echo "== 6) re-apply COW state then prove scratch durability across pod recreate =="
exec_ws 'echo durable-edit > /workspace/model.py'
exec_ws 'echo draft > /workspace/notes.txt'
exec_ws 'rm -f /workspace/README.md'
BASE_SHA="$(node cat "$NODE_LAB/scratch/.base_commit_sha")"
[ "$BASE_SHA" = "sha-fixture-a" ] || { echo "FAIL: base sha lost"; exit 1; }

kubectl -n "$NS" delete pod overlay-workspace --wait=true
kubectl apply -f "$ROOT/k8s/overlay-lab/10-workspace.yaml"
kubectl -n "$NS" wait --for=condition=Ready pod/overlay-workspace --timeout=120s

[ "$(exec_ws 'cat /workspace/model.py')" = "durable-edit" ] || { echo "FAIL: model not durable"; exit 1; }
[ "$(exec_ws 'cat /workspace/notes.txt')" = "draft" ] || { echo "FAIL: notes not durable"; exit 1; }
exec_ws 'test ! -e /workspace/README.md'
[ "$(exec_ws 'cat /scratch/.base_commit_sha')" = "sha-fixture-a" ] || { echo "FAIL: base sha after recreate"; exit 1; }
echo "ok: scratch survived pod recreate (loop-ext4 stand-in for PVC)"

echo "== 7) zero-scan delta = only upper entries =="
DELTA="$(node find "$NODE_LAB/scratch/upper" -mindepth 1 | sort)"
echo "$DELTA"
echo "$DELTA" | grep -q 'model.py' || { echo "FAIL: delta missing model.py"; exit 1; }
echo "$DELTA" | grep -q 'notes.txt' || { echo "FAIL: delta missing notes.txt"; exit 1; }
echo "$DELTA" | grep -Eq 'README|\.wh\.' || {
  echo "FAIL: delta missing whiteout for README"
  exit 1
}
UPPER_COUNT="$(node find "$NODE_LAB/scratch/upper" -mindepth 1 | wc -l | tr -d ' ')"
[ "$UPPER_COUNT" -le 10 ] || { echo "FAIL: upper unexpectedly large ($UPPER_COUNT)"; exit 1; }
echo "ok: delta is upper-only ($UPPER_COUNT entries)"

echo ""
echo "OVERLAY LAB OK"
echo "  lowerdir  -> $NODE_LAB/lower (immutable fixture)"
echo "  upperdir  -> $NODE_LAB/scratch/upper (COW / whiteouts on loop-ext4)"
echo "  merged    -> /workspace"
echo "  durability: loop-ext4 scratch (production: PVC on XFS/ext4)"
