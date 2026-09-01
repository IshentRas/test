# EKS validation findings — Karpenter lab (ADR-002)

Bottlerocket static MNG: [BOTTLEROCKET-FINDINGS.md](BOTTLEROCKET-FINDINGS.md). Karpenter details: [KARPENTER-FINDINGS.md](KARPENTER-FINDINGS.md).

* **Cluster:** `adr002-git-csi-karpenter` (us-east-1)
* **Date:** 2026-09-01T00:56:49Z
* **Nodes:** 8× m5.large + 40Gi gp3 `/mnt/git-storage`
* **Result:** 14 passed, 0 failed

## Fan-out lag (ConfigMap flip A→B)

| Node | Converge ms |
|---|---|
| `ip-192-168-123-3.ec2.internal` | 4079 |
| `ip-192-168-90-147.ec2.internal` | 5266 |
| `ip-192-168-57-158.ec2.internal` | 6449 |

| Metric | Value |
|---|---|
| min_ms | 4079 |
| max_ms | 6449 |
| spread_ms | 2370 |

## Upgrade domains

- CSI restart: FUSE UIDs unchanged; apps kept reading.
- Reconciler restart: FUSE UIDs unchanged.
- FUSE death on one node: local ENOTCONN; other nodes continued serving.
- Recovery: purge CSI mounts on victim + remount consumers.

## Storage

- fake-git: RWO gp3 PVC via `ebs.csi.aws.com` (`adr001-gp3`).
- Node cache/FUSE: dedicated additional gp3 at `/mnt/git-storage`.
- FUSE/CSI: `hostPID` + `nsenter` into host mount namespace (same pattern as kind).

## Notes

- Images: ECR `209561498705.dkr.ecr.us-east-1.amazonaws.com` tag `eks-karpenter`.
- Quest FUSE remains loopback over materialized backend (not lazy cat-file).
