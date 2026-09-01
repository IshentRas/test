#!/usr/bin/env bash
# Shared paths for the EKS test reference repo (test/).
TEST_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Docker build contexts (fake-git, fuse-csi, reconciler, app). Default: this repo.
QUEST_ROOT="${QUEST_ROOT:-$TEST_ROOT}"
