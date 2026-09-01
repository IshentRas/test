#!/usr/bin/env bash
# Shared paths for the EKS test reference repo (test/).
TEST_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Quest repo with Docker build contexts (fake-git, fuse-csi, reconciler, app).
QUEST_ROOT="${QUEST_ROOT:-$(cd "$TEST_ROOT/.." && pwd)}"
