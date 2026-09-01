# EKS test reference — ADR-001 / ADR-002

Self-contained runbooks, cluster configs, manifests, and quest scripts for validating Git CSI on EKS.

Image builds still use the parent quest repo (`QUEST_ROOT`, default: `..`):

| Build context | Path |
|---|---|
| fake-git | `$QUEST_ROOT/fake-git` |
| reconciler | `$QUEST_ROOT/reconciler` |
| fuse-csi | `$QUEST_ROOT/fuse-csi` |
| app | `$QUEST_ROOT/app` |

## Prerequisites

- AWS CLI, `eksctl`, `kubectl`, `docker`, `envsubst`, `python3`
- AWS credentials with permission to create/delete EKS clusters and push to ECR

## Labs

| Lab | Cluster name | Up | Quest |
|---|---|---|---|
| AL2023 MNG | `adr001-git-csi` | `./scripts/eks-up.sh` | `./scripts/run-eks-test.sh` |
| Bottlerocket MNG | `adr002-git-csi-br` | `./scripts/eks-br-up.sh` | `CLUSTER=adr002-git-csi-br ./scripts/run-eks-test.sh` |
| Karpenter + BR | `adr002-git-csi-karpenter` | `./scripts/eks-karpenter-up.sh` | `CLUSTER=adr002-git-csi-karpenter ./scripts/run-eks-test.sh` |

Runbooks: [`docs/BOTTLEROCKET-LAB.md`](docs/BOTTLEROCKET-LAB.md), [`docs/KARPENTER-LAB.md`](docs/KARPENTER-LAB.md).

## Typical flow

```bash
cd test

# 1. Create cluster (pick a lab)
./scripts/eks-karpenter-up.sh

# 2. Build + push images to ECR (from parent repo sources)
IMAGE_TAG=eks-karpenter ./scripts/eks-push-images.sh

# 3. FUSE gate (Bottlerocket / Karpenter labs)
./scripts/run-eks-karpenter-probe.sh   # or run-eks-br-probe.sh

# 4. Full multi-node quest
CLUSTER=adr002-git-csi-karpenter ./scripts/run-eks-test.sh

# 5. Tear down
./scripts/eks-karpenter-down.sh
```

Quest writes findings to `docs/EKS-FINDINGS.md` (overwritten each run).

## Layout

```
test/
  scripts/          # up/down/render/push/probe/quest
  eks/              # eksctl configs, node storage, probes
  k8s/eks/          # workload manifests (rendered with ECR refs)
  docs/             # lab runbooks + findings
```

## Environment

| Variable | Default | Purpose |
|---|---|---|
| `QUEST_ROOT` | `..` | Parent repo with Docker build contexts |
| `CLUSTER` | per script | EKS cluster name |
| `AWS_REGION` | `us-east-1` | AWS region |
| `IMAGE_TAG` | `eks` | ECR image tag |
| `ENV_FILE` | `eks/images.env` | Rendered image refs |
| `SKIP_HOST_FUSE_INSTALL` | `0` | Set `1` on Bottlerocket / Karpenter |

## From the parent repo

The same scripts are available as thin wrappers:

```bash
./scripts/run-eks-test.sh
./scripts/eks-karpenter-up.sh
```

Those delegate to `test/scripts/`.
