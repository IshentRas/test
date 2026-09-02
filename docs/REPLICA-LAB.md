# Replica lab (kind) — no FUSE/CSI

Validates **GitLab-style push → in-cluster replica → node reconciler**:

```text
fake-git (simulates GitLab)  --git push-->  git-replica (empty start, Smart HTTP)
                                                    │
                                                    │ post-receive → ConfigMap
                                                    ▼
                                          git-reconciler-go (fetch + materialize)
                                                    ▼
                                          /var/git-backend (hostPath)
```

Production: replace the lab push job with **GitLab push mirroring** to the replica Ingress.

## Run

```bash
./scripts/run-replica-lab.sh
RECREATE=1 ./scripts/run-replica-lab.sh   # clean cluster
```

## What it proves

1. **git-replica** starts with `git init --bare`; first **push** patches `ACTIVE_COMMIT` via `post-receive`.
2. **git-reconciler-go** watches ConfigMap, `git fetch`es from replica (Smart HTTP), materializes layout.
3. ConfigMap flip B→A converges on the node backend.

## Code

| Path | Role |
|---|---|
| `replica-lab/cmd/replica` | Smart HTTP (`git-http-backend` on UBI9) + post-receive → patch ConfigMap |
| `replica-lab/cmd/reconciler` | ConfigMap watch + `git fetch` + materialize |
| `replica-lab/Dockerfile.replica` | UBI9-minimal + `git` + `git-core` |
| `k8s/replica-lab/10-stack.yaml` | replica Deployment/PVC + reconciler Deployment |

No `UPSTREAM_URL` — replica never pulls; writers push to it.
