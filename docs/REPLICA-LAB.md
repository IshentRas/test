# Replica lab (kind) — no FUSE/CSI

Validates the **upstream → in-cluster replica → node reconciler** path:

```text
fake-git (upstream)  →  git-replica (Go + git-http-backend)  →  git-release-state
                                                              ↓
                                                    git-reconciler-go (Go + git)
                                                              ↓
                                                    /var/git-backend (hostPath)
```

## Run

```bash
./scripts/run-replica-lab.sh
RECREATE=1 ./scripts/run-replica-lab.sh   # clean cluster
```

## What it proves

1. **git-replica** mirrors dumb HTTP upstream on start and patches `ACTIVE_COMMIT`.
2. **git-reconciler-go** watches the ConfigMap, `git fetch`es from replica, materializes `git archive` layout.
3. ConfigMap flip A→B converges on the node backend within seconds.

## Code

| Path | Role |
|---|---|
| `replica-lab/cmd/replica` | Smart HTTP (`git-http-backend` CGI on UBI9) + post-receive → patch ConfigMap |
| `replica-lab/cmd/reconciler` | ConfigMap watch + `git fetch` + materialize |
| `replica-lab/Dockerfile.replica` | **UBI9-minimal** + `git` + `git-core` (provides `git-http-backend`) |
| `k8s/replica-lab/10-stack.yaml` | replica Deployment/PVC + reconciler Deployment |

Push-to-replica (post-receive) is implemented; the lab script also tests CM flip directly.
