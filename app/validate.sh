#!/usr/bin/env bash
# In-pod quest assertions for CONFIG_PATH selection + mid-flight flip.
set -euo pipefail

GIT_ROOT="${GIT_ROOT:-/mnt/git}"
NS="${NAMESPACE:-adr001}"
CM="${CONFIGMAP:-git-release-state}"
META_URL="${META_URL:-http://fake-git.adr001.svc.cluster.local:8080/state.json}"

pass=0
fail=0

ok() { echo "PASS: $*"; pass=$((pass + 1)); }
bad() { echo "FAIL: $*"; fail=$((fail + 1)); }

need_file() {
  local p="$1"
  if [ -e "$p" ]; then ok "exists $p"; else bad "missing $p"; fi
}

expect_eq() {
  local got="$1" want="$2" msg="$3"
  if [ "$got" = "$want" ]; then ok "$msg"; else bad "$msg (got='$got' want='$want')"; fi
}

wait_for() {
  local desc="$1"
  shift
  local i
  for i in $(seq 1 60); do
    if "$@"; then return 0; fi
    sleep 1
  done
  bad "timeout waiting for $desc"
  return 1
}

echo "== fetch fixture SHAs =="
STATE="$(curl -fsSL "$META_URL")"
SHA_A="$(python3 -c "import json,sys; print(json.load(sys.stdin)['sha_a'])" <<<"$STATE")"
SHA_B="$(python3 -c "import json,sys; print(json.load(sys.stdin)['sha_b'])" <<<"$STATE")"
echo "sha_a=$SHA_A"
echo "sha_b=$SHA_B"

echo "== assert mount is FUSE via CSI =="
if command -v findmnt >/dev/null 2>&1; then
  FSTYPE="$(findmnt -n -o FSTYPE "$GIT_ROOT" 2>/dev/null || true)"
  SOURCE="$(findmnt -n -o SOURCE "$GIT_ROOT" 2>/dev/null || true)"
  echo "findmnt: fstype=$FSTYPE source=$SOURCE"
  case "$FSTYPE" in
    fuse*|fuseblk)
      ok "mount fstype is FUSE ($FSTYPE)"
      ;;
    *)
      # Bind of a FUSE export often still reports fuse.* ; if not, check /proc/mounts.
      if grep -E "[[:space:]]$GIT_ROOT[[:space:]]+fuse" /proc/mounts >/dev/null 2>&1 \
        || grep -E "adr001-git|fuse" /proc/mounts | grep -q "$GIT_ROOT"; then
        ok "FUSE visible in /proc/mounts for $GIT_ROOT"
      else
        bad "expected FUSE mount at $GIT_ROOT (fstype='$FSTYPE')"
      fi
      ;;
  esac
else
  bad "findmnt missing"
fi

echo "== wait for initial layout (commit A) =="
wait_for "current -> A" bash -c "[ \"\$(cat $GIT_ROOT/CURRENT_SHA 2>/dev/null)\" = \"$SHA_A\" ]"
wait_for "current VERSION=A" bash -c "[ \"\$(cat $GIT_ROOT/current/config/VERSION 2>/dev/null)\" = commit-a ]"

need_file "$GIT_ROOT/CURRENT_SHA"
need_file "$GIT_ROOT/current"
need_file "$GIT_ROOT/tags/v1.0.0"
need_file "$GIT_ROOT/commits/$SHA_A"

echo "== live trunk via CONFIG_PATH =="
CONFIG_PATH="$GIT_ROOT/current"
expect_eq "$(cat "$CONFIG_PATH/config/VERSION")" "commit-a" "CONFIG_PATH=current sees A"

echo "== tag pin =="
CONFIG_PATH="$GIT_ROOT/tags/v1.0.0"
expect_eq "$(cat "$CONFIG_PATH/config/VERSION")" "commit-a" "CONFIG_PATH=tags/v1.0.0 sees A"

echo "== SHA pin =="
CONFIG_PATH="$GIT_ROOT/commits/$SHA_A"
expect_eq "$(cat "$CONFIG_PATH/config/VERSION")" "commit-a" "CONFIG_PATH=commits/A sees A"

echo "== boot-time resolve snapshot =="
SNAPSHOT="$(readlink -f "$GIT_ROOT/current")"
expect_eq "$(basename "$SNAPSHOT")" "$SHA_A" "resolved current is A"
expect_eq "$(cat "$SNAPSHOT/config/VERSION")" "commit-a" "snapshot content is A"

echo "== flip ACTIVE_COMMIT to B =="
TAGS_JSON="$(python3 -c "import json; print(json.dumps({'v1.0.0': '$SHA_A'}))")"
kubectl -n "$NS" create configmap "$CM" \
  --from-literal=ACTIVE_COMMIT="$SHA_B" \
  --from-literal=ACTIVE_TAGS="$TAGS_JSON" \
  -o yaml --dry-run=client | kubectl apply -f -

wait_for "current -> B" bash -c "[ \"\$(cat $GIT_ROOT/CURRENT_SHA 2>/dev/null)\" = \"$SHA_B\" ]"
wait_for "current VERSION=B" bash -c "[ \"\$(cat $GIT_ROOT/current/config/VERSION 2>/dev/null)\" = commit-b ]"

expect_eq "$(cat "$GIT_ROOT/current/config/VERSION")" "commit-b" "live current sees B after flip"
expect_eq "$(cat "$SNAPSHOT/config/VERSION")" "commit-a" "boot snapshot still sees A after flip"
expect_eq "$(cat "$GIT_ROOT/tags/v1.0.0/config/VERSION")" "commit-a" "tag pin unchanged after flip"
expect_eq "$(cat "$GIT_ROOT/commits/$SHA_A/config/VERSION")" "commit-a" "SHA pin unchanged after flip"

echo "== summary: $pass passed, $fail failed =="
if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "QUEST OK"
