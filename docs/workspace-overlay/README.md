# Workspace write-layer (OverlayFS) + publish

Architecture note for editable quant workspaces on top of the ADR-001 read-only Git CSI stack.

**Status:** Overlay COW proven in kind ([OVERLAY-LAB.md](../OVERLAY-LAB.md)). Publish service (share / promote) designed, not implemented.

---

## Objective

Give non-technical users a normal writable filesystem (`/workspace`) while:

1. Keeping the Git-backed tree **strictly read-only** and uncorruptible.
2. Surviving workspace crash / reschedule (durable scratch).
3. Making “what changed?” cheap (no monorepo `git status`).
4. Sharing experiments and landing on `main` through the **same Git → replica → RO stores** path.

---

## Two layers (do not mix)

```text
READ PATH (existing — unchanged)
  GitLab → git-replica → ConfigMap → reconciler → backend
       → git-fuse → git-csi → /mnt/git   (CONFIG_PATH selects view)

WRITE PATH (new — per workspace)
  lowerdir = pinned materialized commit (real disk, NOT FUSE)
  upperdir = private PVC scratch
  workdir  = PVC
  merged   = /workspace
```

| Concern | Where |
|---|---|
| Immutable Git base | Materialized `commits/<sha>/` on node backend (or equivalent bind) |
| Live edits / deletes | Overlay **upperdir** on PVC |
| App / colleague consume | Existing FUSE + CSI read-only mounts |
| Lazy `cat-file` FUSE | Out of scope (later) |

**Do not** use today’s FUSE CSI mount as OverlayFS `lowerdir`. Use the materialized backend tree (XFS/ext4).

---

## Overlay semantics

### 1. Copy-on-write editing

- **Reads** → `lowerdir` (immutable Git snapshot).
- **Creates / edits** → diverted to `upperdir`.
- Lower tree is never written by the user.

### 2. Deletes and revert (whiteouts)

- `rm` of a base file → **whiteout** in upperdir; lower file untouched.
- **Revert one file** → remove that path from upperdir (then refresh overlay view).
- **Undelete** → remove the whiteout; base file shows through again.
- **Revert all** → wipe upper + work; keep `.base_commit_sha`.

### 3. Session durability

On the PVC (or lab stand-in):

| Path | Role |
|---|---|
| `upper/` | COW files + whiteouts |
| `work/` | Overlay workdir |
| `.base_commit_sha` | Pinned lower commit at session start |

Pod restart / reschedule remounts the same upper on the same base. **Do not** silently retarget `current` under a live upper.

### 4. Zero-scan delta

Only changed / new / whiteout entries live in `upper/`. Publishing walks **upper only** — no full-repo `git status`.

---

## Pinning the base

At workspace start:

1. Resolve desired view (`CONFIG_PATH` / commit / tag).
2. Record absolute SHA in `.base_commit_sha` on the PVC.
3. Mount overlay with `lowerdir=…/commits/<sha>/` (or equivalent).

Changing base under an existing upper requires wipe or an explicit rebase of the delta — never an accidental flip.

---

## Storage

| Environment | Scratch (upper + work) | Lower |
|---|---|---|
| Kind lab | Loop-ext4 on node (virtiofs cannot be overlay upper) | Fixture / materialized tree on node disk |
| Production | **RWO PVC** (gp3 / XFS) | Pinned `commits/<sha>/` on git-worker backend |

**Not suitable as overlay upperdir:** s3fs, FUSE, virtiofs, another overlay. Object storage is fine for **export**, not for live COW.

PVC is required if scratch must survive reboot / reschedule. `emptyDir` is only for disposable sessions.

---

## Publish service (share vs promote)

One service, two modes. Same delta → commit pipeline; different refs and GitLab actions.

```text
upperdir + .base_commit_sha
            │
            ▼
     apply delta → git commit (parent = base)
            │
            ├── share   → tag    refs/tags/ws/<user>/<id>
            │              push tag → GitLab
            │              colleagues: CONFIG_PATH=/mnt/git/tags/ws/...
            │
            └── promote → branch refs/heads/ws/<user>/<id>
                           push branch + open MR → main
                           (review / CI / merge = global)
```

Share first, promote later is allowed: reopen an MR from the same commit/branch without rebuilding the upper.

### Why tags for quick share

- Flows through existing **GitLab → replica → `ACTIVE_TAGS` → reconciler → `tags/`**.
- Colleagues stay on RO CSI; they only change `CONFIG_PATH`.
- No live multi-writer on one upperdir.
- Ephemeral naming + retention keeps the tag namespace clean.

### Naming convention

| Kind | Ref | Purpose |
|---|---|---|
| Quick share | `ws/<user>/<utc>-<short>` **tag** | Try my scratch |
| Promote | `ws/<user>/<utc>-<short>` **branch** + MR → `main` | Make global |
| Releases | `v*` / protected tags | Untouched by workspace publish |

### Retention (share tags)

- Delete `ws/**` tags older than N days (e.g. 7–14).
- Optionally prune node `tags/ws/...` and orphaned `commits/<sha>/` later (GC).
- Release tags and `main` are out of scope for that job.

---

## Permissions

Publish needs **write access to GitLab** (separate from replica `PUSH_AUTH_*`, which is GitLab → replica only).

Recommended:

1. **AuthN** — workspace user (OAuth / short-lived token) or bot with encoded identity in the ref name.
2. **AuthZ in publish service**
   - May only create `refs/tags/ws/<that-user>/…` or `refs/heads/ws/<that-user>/…`
   - Must not update `main` or release tags
   - Max upper size / file count
3. **AuthZ on GitLab** — project role + optional **pre-receive** hook enforcing the same prefix rules.

Promote additionally needs permission to **create MRs** into `main` (merge stays with humans / CODEOWNERS).

---

## End-to-end flows

### Edit locally

```text
quant ↔ /workspace (overlay)
         lower = commits/<base>/   (RO)
         upper = PVC               (RW)
```

### Quick share

```text
publish(mode=share)
  → tag ws/alice/…
  → GitLab → mirror → replica → CM tags
  → reconciler materializes tags/ws/alice/…
  → bob: CONFIG_PATH=/mnt/git/tags/ws/alice/…
```

### Make global

```text
publish(mode=promote)
  → branch ws/alice/… + MR → main
  → review / merge
  → main tip flows as today (replica post-receive → ACTIVE_COMMIT)
```

---

## What we are not doing (yet)

- Lazy `git cat-file` FUSE (monorepo read optimization)
- s3fs (or any FUSE) as overlay upperdir
- Shared live upperdir across users
- Direct push to git-replica bypassing GitLab as source of truth for shares

---

## Lab status

| Piece | Status |
|---|---|
| Overlay COW / whiteout / revert / durable scratch | Kind lab OK — [`OVERLAY-LAB.md`](../OVERLAY-LAB.md) |
| Wire lower to reconciler `commits/<sha>/` on EKS | Next integration |
| Publish service (`share` / `promote`) | Designed here — to implement |
| Tag retention job | Designed here — to implement |

---

## Related

- [OVERLAY-LAB.md](../OVERLAY-LAB.md) — kind OverlayFS proof
- [REPLICA-LAB.md](../REPLICA-LAB.md) — git-replica + reconciler
- [KARPENTER-LAB.md](../KARPENTER-LAB.md) — EKS / Bottlerocket git-workers
