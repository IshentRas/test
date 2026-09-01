#!/usr/bin/env bash
# Karpenter Bottlerocket FUSE gate (same probe as BR MNG lab; CLUSTER defaults to karpenter).
set -euo pipefail

# shellcheck source=lib.sh
source "$(cd "$(dirname "$0")" && pwd)/lib.sh"
export CLUSTER="${CLUSTER:-adr002-git-csi-karpenter}"
exec "$TEST_ROOT/scripts/run-eks-br-probe.sh"
