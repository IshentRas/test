# Overlay workspace lab (kind)

Proves **copy-on-write editing** over an immutable Git-like lowerdir using OverlayFS.

```text
lowerdir  = /overlay-lab/lower              (immutable fixture on kind node)
upperdir  = /overlay-lab/scratch/upper      (loop-backed ext4)
workdir   = /overlay-lab/scratch/work
merged    = /workspace                      (what the user sees)
```

> **Kind-on-Mac:** node root is already OverlayFS, and virtiofs `extraMounts` cannot host a writable OverlayFS upperdir. The lab uses a **loop-backed ext4** image as the scratch volume (stand-in for a PVC). Production uses an EBS/XFS PVC for upper/work and a pinned materialized commit tree for lower (not FUSE).

## Run

```bash
cd test
./scripts/run-overlay-lab.sh
RECREATE=1 ./scripts/run-overlay-lab.sh   # clean kind cluster
```

From repo root: `./scripts/run-overlay-lab.sh`

## What it proves

1. Reads from lowerdir through `/workspace`
2. Edits/creates go only to upperdir; lower untouched
3. `rm` of a base file → whiteout; lower untouched
4. Revert = delete upper copy; undelete = drop whiteout
5. Scratch survives pod delete/recreate (loop-ext4 stand-in for PVC)
6. Delta enumeration = walk upper only (no full-tree scan)
