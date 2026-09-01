#!/usr/bin/env bash
# Node-local layout reconciler: git fetch + materialize into backend for FUSE loopback.
set -euo pipefail

EXPORT="${EXPORT_ROOT:-/var/git-backend}"
CACHE="${CACHE_ROOT:-/var/git-cache}"
REMOTE="${GIT_REMOTE:-http://fake-git.adr001.svc.cluster.local:8080/repo.git}"
NS="${NAMESPACE:-adr001}"
CM="${CONFIGMAP:-git-release-state}"

mkdir -p "$EXPORT" "$CACHE"
BARE="$CACHE/repo.git"

log() { echo "[reconciler] $*"; }

ensure_clone() {
  if [ ! -d "$BARE/objects" ]; then
    log "cloning $REMOTE"
    rm -rf "$BARE"
    git clone --bare "$REMOTE" "$BARE"
  fi
}

fetch_all() {
  git -C "$BARE" fetch --tags --force origin '+refs/heads/*:refs/heads/*' '+refs/tags/*:refs/tags/*' 2>/dev/null \
    || git -C "$BARE" fetch --tags --force origin
}

materialize_commit() {
  local sha="$1"
  local dest="$EXPORT/commits/$sha"
  if [ -d "$dest" ] && [ -f "$dest/config/VERSION" ]; then
    return 0
  fi
  log "materializing $sha"
  local tmp
  tmp="$(mktemp -d "$EXPORT/commits/.tmp.$sha.XXXXXX")"
  git -C "$BARE" archive "$sha" | tar -x -C "$tmp"
  rm -rf "$dest"
  mv "$tmp" "$dest"
  chmod_export
}

atomic_symlink() {
  local target="$1"
  local linkpath="$2"
  # ln -sfn replaces the symlink inode itself. Do NOT mv onto an existing
  # dir-symlink — mv follows into the target directory and leaves the link stale.
  ln -sfn "$target" "$linkpath"
}

chmod_export() {
  chmod -R a+rX "$EXPORT" 2>/dev/null || true
}

reconcile_once() {
  ensure_clone
  fetch_all

  local active tags_json
  active="$(kubectl -n "$NS" get configmap "$CM" -o jsonpath='{.data.ACTIVE_COMMIT}')"
  tags_json="$(kubectl -n "$NS" get configmap "$CM" -o jsonpath='{.data.ACTIVE_TAGS}')"

  if [ -z "$active" ] || [ "$active" = "pending" ]; then
    log "ACTIVE_COMMIT unset; skip"
    return 0
  fi

  mkdir -p "$EXPORT/commits" "$EXPORT/tags"
  materialize_commit "$active"
  echo -n "$active" > "$EXPORT/CURRENT_SHA"
  atomic_symlink "commits/$active" "$EXPORT/current"

  if command -v python3 >/dev/null 2>&1; then
    python3 - "$tags_json" "$EXPORT" <<'PY'
import json, sys, pathlib
raw = sys.argv[1] or "{}"
export = pathlib.Path(sys.argv[2])
tags = json.loads(raw)
tags_dir = export / "tags"
tags_dir.mkdir(parents=True, exist_ok=True)
wanted = set()
for name, sha in tags.items():
    wanted.add(name)
    link = tags_dir / name
    target = f"../commits/{sha}"
    tmp = tags_dir / f".{name}.tmp"
    if tmp.exists() or tmp.is_symlink():
        tmp.unlink()
    tmp.symlink_to(target)
    tmp.rename(link)
for p in tags_dir.iterdir():
    if p.name.startswith("."):
        continue
    if p.name not in wanted:
        p.unlink()
PY
  fi

  if [ -n "$tags_json" ] && command -v python3 >/dev/null 2>&1; then
    while IFS= read -r sha; do
      [ -n "$sha" ] && materialize_commit "$sha"
    done < <(python3 -c "import json,sys; print('\\n'.join(json.loads(sys.argv[1] or '{}').values()))" "$tags_json")
  fi

  chmod_export
  log "reconciled current=$active"
}

log "starting; remote=$REMOTE export=$EXPORT (FUSE/CSI consume via separate DaemonSet)"
while true; do
  if reconcile_once; then
    :
  else
    log "reconcile failed (will retry)"
  fi
  sleep 2
done
