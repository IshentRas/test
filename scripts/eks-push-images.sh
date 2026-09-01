#!/usr/bin/env bash
# Build linux/amd64 images and push to ECR for the EKS quest.
set -euo pipefail

# shellcheck source=lib.sh
source "$(cd "$(dirname "$0")" && pwd)/lib.sh"
REGION="${AWS_REGION:-us-east-1}"
ACCOUNT="$(aws sts get-caller-identity --query Account --output text)"
REGISTRY="${ACCOUNT}.dkr.ecr.${REGION}.amazonaws.com"
TAG="${IMAGE_TAG:-eks}"
PLATFORM="${PLATFORM:-linux/amd64}"
USE_UBI9="${USE_UBI9:-0}"
RECONCILER_DOCKERFILE="${RECONCILER_DOCKERFILE:-Dockerfile}"
if [ "$USE_UBI9" = "1" ]; then
  RECONCILER_DOCKERFILE="Dockerfile.ubi9"
fi

REPOS=(adr001-fake-git adr001-reconciler adr001-fuse-csi adr001-app)

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required tool: $1" >&2
    exit 1
  }
}

need aws
need docker

echo "logging into ECR $REGISTRY"
aws ecr get-login-password --region "$REGION" | docker login --username AWS --password-stdin "$REGISTRY"

for repo in "${REPOS[@]}"; do
  if ! aws ecr describe-repositories --repository-names "$repo" --region "$REGION" >/dev/null 2>&1; then
    echo "creating ECR repo $repo"
    aws ecr create-repository --repository-name "$repo" --region "$REGION" >/dev/null
  fi
done

build_push() {
  local name="$1" context="$2"
  local image="${REGISTRY}/${name}:${TAG}"
  echo "building $image ($PLATFORM)"
  docker build --platform "$PLATFORM" -t "$image" "$context"
  docker push "$image"
  echo "pushed $image"
}

build_push adr001-fake-git "$QUEST_ROOT/fake-git"
echo "reconciler image: $RECONCILER_DOCKERFILE (USE_UBI9=$USE_UBI9)"
docker build --platform "$PLATFORM" -t "${REGISTRY}/adr001-reconciler:${TAG}" -f "$QUEST_ROOT/reconciler/$RECONCILER_DOCKERFILE" "$QUEST_ROOT/reconciler"
docker push "${REGISTRY}/adr001-reconciler:${TAG}"
echo "pushed ${REGISTRY}/adr001-reconciler:${TAG}"
build_push adr001-fuse-csi "$QUEST_ROOT/fuse-csi"
build_push adr001-app "$QUEST_ROOT/app"

# Write image env file for run-eks-test.sh / kustomize render.
cat > "$TEST_ROOT/eks/images.env" <<EOF
AWS_REGION=$REGION
ACCOUNT=$ACCOUNT
REGISTRY=$REGISTRY
IMAGE_TAG=$TAG
FAKE_GIT_IMAGE=${REGISTRY}/adr001-fake-git:${TAG}
RECONCILER_IMAGE=${REGISTRY}/adr001-reconciler:${TAG}
FUSE_CSI_IMAGE=${REGISTRY}/adr001-fuse-csi:${TAG}
APP_IMAGE=${REGISTRY}/adr001-app:${TAG}
EOF

echo "wrote $TEST_ROOT/eks/images.env"
cat "$TEST_ROOT/eks/images.env"
