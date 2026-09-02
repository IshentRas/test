# EKS test reference — ADR-001 / ADR-002

Self-contained runbooks, cluster configs, manifests, quest scripts, and **image build sources** for validating Git CSI on EKS.

| Build context | Path |
|---|---|
| fake-git | `fake-git/` |
| reconciler | `reconciler/` |
| fuse-csi | `fuse-csi/` |
| app | `app/` |

Set `QUEST_ROOT` only if build contexts live elsewhere.

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

## Replica lab (kind, no FUSE/CSI)

Validates **upstream → git-replica (Go + Smart HTTP on UBI9) → git-reconciler-go → node backend**:

```bash
cd test
./scripts/run-replica-lab.sh
RECREATE=1 ./scripts/run-replica-lab.sh   # clean cluster
```

See [`docs/REPLICA-LAB.md`](docs/REPLICA-LAB.md). Code in `replica-lab/`.


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
  replica-lab/      # Go git-replica + git-reconciler (UBI9, git-http-backend)
  fuse-csi/         # git-fuse + git-csi + static busybox (Dockerfile)
  reconciler/       # git-reconciler (Dockerfile / Dockerfile.ubi9)
  fake-git/         # lab fake git server
  app/              # lab validation app
  scripts/          # up/down/render/push/probe/quest
  eks/              # eksctl configs, node storage, probes
  k8s/eks/          # workload manifests (rendered with ECR refs)
  docs/             # lab runbooks + findings
```

## Environment

| Variable | Default | Purpose |
|---|---|---|
| `QUEST_ROOT` | `.` (this repo) | Override if sources are elsewhere |
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

## Build and push images (org registry)

You must build and push images from this repo. Bottlerocket does **not** ship `git-fuse` / `git-csi` — that logic lives in `fuse-csi/`.

### All images (recommended)

```bash
cd test
IMAGE_TAG=<your-tag> ./scripts/eks-push-images.sh
```

Writes `eks/images.env` with ECR URLs for render/apply.

### `fuse-csi` only (manual)

One image powers **both** `git-fuse` and `git-csi` DaemonSets:

| Artifact in image | DaemonSet | Role |
|---|---|---|
| `git-fuse` | `git-fuse` | FUSE server (`DirectMountStrict` on Bottlerocket) |
| `git-csi` | `git-csi` | CSI node plugin (bind-mount into pods) |
| `adr001-busybox` | both | static busybox copied to `/mnt/git-storage/bins/busybox` for `nsenter` on host mount ns |

```bash
REGISTRY=<account>.dkr.ecr.<region>.amazonaws.com
TAG=<your-tag>

docker build --platform linux/amd64 \
  -t "${REGISTRY}/adr001-fuse-csi:${TAG}" \
  fuse-csi/
docker push "${REGISTRY}/adr001-fuse-csi:${TAG}"
```

Also push **`adr001-reconciler`** (materializes git backend). Production uses your real git remote instead of `fake-git`.

```bash
docker build --platform linux/amd64 \
  -t "${REGISTRY}/adr001-reconciler:${TAG}" \
  -f reconciler/Dockerfile.ubi9 reconciler/
docker push "${REGISTRY}/adr001-reconciler:${TAG}"
```

### Bottlerocket deploy order

Do **not** apply `eks/install-fuse.yaml` on Bottlerocket (no host `dnf`).

1. `br-node-storage` — format/mount extra EBS → `/mnt/git-storage`
2. `run-eks-br-probe.sh` (or karpenter probe) — gate: `PROBE_OK`
3. `git-reconciler`
4. `git-fuse` + `git-csi` (same `adr001-fuse-csi` image)
5. app / workloads

`br-node-storage` handles disk only; FUSE userspace comes from the `fuse-csi` image (`git-fuse` binary + `/dev/fuse` on the node).

