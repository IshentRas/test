#!/usr/bin/env bash
# Render k8s/eks manifests with ECR image refs from eks/images.env.
set -euo pipefail

# shellcheck source=lib.sh
source "$(cd "$(dirname "$0")" && pwd)/lib.sh"
OUT="${1:-$TEST_ROOT/eks/rendered}"
ENV_FILE="${ENV_FILE:-$TEST_ROOT/eks/images.env}"

if [ ! -f "$ENV_FILE" ]; then
  echo "missing $ENV_FILE — run ./scripts/eks-push-images.sh first" >&2
  exit 1
fi

# shellcheck disable=SC1090
source "$ENV_FILE"

mkdir -p "$OUT"
rm -f "$OUT"/*.yaml

export FAKE_GIT_IMAGE RECONCILER_IMAGE FUSE_CSI_IMAGE APP_IMAGE

for f in "$TEST_ROOT"/k8s/eks/*.yaml; do
  base="$(basename "$f")"
  # Substitute ${VAR} placeholders.
  envsubst '${FAKE_GIT_IMAGE} ${RECONCILER_IMAGE} ${FUSE_CSI_IMAGE} ${APP_IMAGE}' < "$f" > "$OUT/$base"
done

echo "rendered manifests -> $OUT"
ls -1 "$OUT"
