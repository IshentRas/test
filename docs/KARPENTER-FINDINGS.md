# Karpenter + Bottlerocket + extra EBS findings — ADR-002

* **Cluster:** `adr002-git-csi-karpenter` (EKS 1.35, us-east-1)
* **Date:** 2026-09-01
* **Git workers:** 3× Karpenter-provisioned Bottlerocket `m5.large` + 40 Gi gp3 `/dev/xvdb`
* **Bootstrap:** 2× Bottlerocket `t3.medium` (Karpenter controller)
* **Result:** FUSE probe 3/3; full quest **14/14** (`EKS QUEST OK`)

## Gate vs static Bottlerocket MNG lab

| Item | Static MNG (`cluster-bottlerocket.yaml`) | Karpenter lab |
|---|---|---|
| Extra disk | eksctl `additionalVolumes: /dev/xvdb` | `EC2NodeClass.blockDeviceMappings` (must set **both** xvda + xvdb) |
| Node provisioning | Fixed 3 nodes at create | `NodePool` + scale hold pods |
| IAM role | eksctl MNG role | `eksctl-KarpenterNodeRole-<cluster>` in EC2NodeClass |
| Bootstrap userData | eksctl `bottlerocket.settings` | EC2NodeClass `userData` — **no** `[settings.motd] message =` (invalid TOML) |

## Pitfalls hit in this run

1. **eksctl Karpenter Helm install** — public ECR token expiry; installed via `helm` + `aws ecr-public get-login-password` manually.
2. **EC2NodeClass role** — render must use `eksctl-KarpenterNodeRole-*`, not `KarpenterNodeRole-*`.
3. **Bottlerocket userData** — invalid `[settings.motd] message = ...` blocks kubelet join; use admin container only or `[settings] motd = "..."`.
4. **br-node-storage** — UBI9 public repos lack `xfsprogs`/`e2fsprogs`; use AL2023 for disk bootstrap container.
5. **Reconciler UBI9** — missing `tar`; use Alpine reconciler (`tar` in Dockerfile).
6. **Cached image** — same ECR tag after rebuild needs `imagePullPolicy: Always` on reconciler.
7. **nodeSelector** — fuse/csi/reconciler/app must target `adr002.io/role=git-worker` (bootstrap nodes lack `/mnt/git-storage`).
8. **Quest script** — FUSE readiness must compare against DS `desiredNumberScheduled`, not total cluster nodes.

## Fan-out (ConfigMap A→B)

| Node | Converge ms |
|---|---|
| `ip-192-168-123-3` | ~4079 |
| `ip-192-168-90-147` | ~5266 |
| `ip-192-168-57-158` | ~6449 |

Spread ~2.4s — comparable to static BR lab.

## Workplace mapping

- Extra disk: `eks/karpenter/ec2nodeclass-git-workers.yaml.in` → `blockDeviceMappings` xvdb
- Mount: `eks/br-node-storage.yaml` on git-worker nodes only
- Labels: `adr002.io/role=git-worker` on NodePool template + app/DS nodeSelectors

See `docs/KARPENTER-LAB.md` for runbook.
