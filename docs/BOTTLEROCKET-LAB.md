# Bottlerocket + EKS 1.35 lab (ADR-002 workplace gate)

## Why this lab

AL2023 quest proved the architecture. Workplace runs **EKS + Bottlerocket**.
Bottlerocket cannot `dnf install fuse` — that is the gate.

| Gate | Pass criteria |
|---|---|
| OS | Nodes report Bottlerocket; kubelet ~1.35.x |
| Disk | `/mnt/git-storage/{backend,fuse}` present (bootstrap DS) |
| FUSE | `PROBE_OK` on all nodes (`./scripts/run-eks-br-probe.sh`) |
| Full quest | `CLUSTER=adr002-git-csi-br ./scripts/run-eks-test.sh` → `EKS QUEST OK` |

## Code change for BR

`git-fuse` mounts with go-fuse **`DirectMountStrict`** (raw `/dev/fuse`, no host `fusermount`).
Rebuild/push images before the probe.

## Runbook

Run from the `test/` directory (see [`README.md`](../README.md)):

```bash
cd test
# 1. Cluster (15–25+ min)
./scripts/eks-br-up.sh

# 2. Images (linux/amd64 → ECR), includes new DirectMountStrict fuse binary
./scripts/eks-push-images.sh
# Ensure eks/images.env exists; probe/test use FUSE_CSI_IMAGE from it.

# 3. FUSE gate only (cheap fail-fast)
./scripts/run-eks-br-probe.sh

# 4. Full multi-node quest (only if probe passes)
CLUSTER=adr002-git-csi-br ./scripts/run-eks-test.sh

# 5. Tear down
./scripts/eks-br-down.sh
```

Do **not** apply `eks/install-fuse.yaml` on Bottlerocket (host `dnf` is not available).

## If the probe fails

1. Inspect: `kubectl -n adr001-system logs -l app=br-fuse-probe --tail=100`
2. Confirm `/dev/fuse` on host via probe logs.
3. Decide: fix DirectMount path, use an **AL2023** labeled node group for Git CSI, or defer until Bottlerocket ships FUSE userspace support.

## Findings

Record results in `docs/BOTTLEROCKET-FINDINGS.md` after the run (create when probe/quest complete).

## Related

- **Karpenter + extra EBS** (workplace-shaped reference): `docs/KARPENTER-LAB.md`
