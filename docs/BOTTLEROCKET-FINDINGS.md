# Bottlerocket validation findings — ADR-002

* **Cluster:** `adr002-git-csi-br` (us-east-1)
* **Date:** 2026-08-31
* **Platform:** EKS **1.35** + **Bottlerocket OS 1.64.0** (`aws-k8s-1.35`), 3× m5.large
* **Result:** FUSE gate `PROBE_OK` ×3; full quest **14 passed, 0 failed** (`EKS QUEST OK`)

## Gate vs AL2023 lab

| Item | AL2023 lab | Bottlerocket lab |
|---|---|---|
| Host `fusermount` via `dnf` | Required | **Not available** (immutable OS) |
| go-fuse mount | fusermount helper | **`DirectMountStrict`** (`/dev/fuse` only) |
| `nsenter` + host `sh` | OK | **Fails** — host shell is restricted (`brush`; blocks `cat`, no `bash`) |
| Host-ns helpers | host binaries | Static **Go** `git-fuse` (+ optional static busybox); never invoke host brush |
| Extra disk | `preBootstrapCommands` | Privileged DS + **Bidirectional** `/mnt` (`br-node-storage`) |
| FUSE visible to CSI | `hostPID` + `nsenter` | **Same** — still required |

## Fan-out lag (ConfigMap flip A→B)

| Node | Converge ms |
|---|---|
| `ip-192-168-20-246.ec2.internal` | 4150 |
| `ip-192-168-51-34.ec2.internal` | 5452 |
| `ip-192-168-24-113.ec2.internal` | 6558 |

| Metric | Value |
|---|---|
| min_ms | 4150 |
| max_ms | 6558 |
| spread_ms | 2408 |

Comparable to AL2023 (~2–5s / ~3s spread).

## Upgrade domains / failure

- CSI restart: FUSE UIDs unchanged; apps kept reading.
- Reconciler restart: FUSE UIDs unchanged.
- FUSE death on one node: local ENOTCONN (~20s); other nodes kept serving.
- Recovery: remount consumers after FUSE Ready (purge helper had a busybox flag glitch; apps still recovered after recreate).

## Workplace takeaway

**Bottlerocket is viable** for this design on EKS 1.35 if you:

1. Build `git-fuse` / `git-csi` with `CGO_ENABLED=0` and use **`DirectMountStrict`**.
2. Keep **`privileged` + `hostPID`** on `git-fuse` and `git-csi`.
3. Run host-ns commands only via **static binaries** on a hostPath (e.g. `/mnt/git-storage/bins`) — never host `sh`/`brush`.
4. Bootstrap `/mnt/git-storage` without AL2023 `dnf`/`preBootstrapCommands`.

Runbook: [`BOTTLEROCKET-LAB.md`](BOTTLEROCKET-LAB.md).
