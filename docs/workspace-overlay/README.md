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

### Why not s3fs ([s3fs-fuse](https://github.com/s3fs-fuse/s3fs-fuse)) as the overlay upperdir

Sharing via a shared S3 bucket looks attractive (“one scratch everyone can see”). It is a **poor live upperdir** for OverlayFS / fuse-overlayfs.

| Overlay need | Block PVC (XFS/ext4) | s3fs-fuse |
|---|---|---|
| Whiteouts | Character devices (or xattrs) on a real POSIX FS | **S3 cannot store chardev whiteouts**; xattr support is incomplete |
| Overlay `upperdir` / `workdir` | Supported | Typically **EINVAL / unsupported** (FUSE cannot be a kernel overlay upper; same class of failure as virtiofs in the kind lab) |
| Atomic rename, `workdir/` internals | Local disk | Eventual consistency, high metadata latency |
| Multi-writer | One RWO attach (safe) | Concurrent writers on one prefix **corrupt** the overlay |
| Latency | Sub-ms | Extra FUSE + S3 round trips per syscall |

Kind already showed **virtiofs cannot host a writable overlay upperdir**. s3fs is FUSE + object storage — strictly worse.

**What S3 is good for:** exporting a *translated* delta (files + whiteouts→deletes) as objects, or a side mount at `/share` for large artifacts. Not the live `/workspace` COW layer.

**How we share instead:** publish service walks the **private PVC upperdir** → Git **tag** (`ws/…`) or **MR**. Colleagues consume via existing RO `tags/` + `CONFIG_PATH`. No shared live mount.

### Privileges: `/dev/fuse` is not enough

The fuse device plugin exposes **`/dev/fuse`**. Creating the mount still needs **`CAP_SYS_ADMIN`** or **privileged** on the **mounter** (or host `fusermount3` — unavailable on Bottlerocket).

Pattern: **privileged fuse-overlay sidecar** mounts `/workspace`; quant container stays unprivileged. FUSE must stay running — use a sidecar, not a one-shot init.

Example manifest: [`example-workspace-pod.yaml`](example-workspace-pod.yaml)

---

## Publish service (share vs promote)

**Implementation:** Go CLI in the workspace image — [`workspace-publish/`](../../workspace-publish/) (`ws-publish share|promote`). Uses the **real `git` binary** (same as replica/reconciler); go-git optional later.

One CLI, two modes. Same delta → commit pipeline; different refs and GitLab actions.

```text
upperdir + .base_commit_sha
            │
            ▼
     secret scan (upper only — see Secret scanning)
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

## Secret scanning (publish gate)

**Status:** designed, not implemented.

### The gap

`promote` opens an MR, so Wiz runs in the pipeline. **`share` never opens an MR** — so today the cheap path, the one we expect people to use most, has *no* secret scanning at all.

A leaked `ws/*` tag is not contained: it flows GitLab → replica → `tags/` and onto every colleague's RO mount. Deleting the tag afterwards does **not** remove the objects.

Quant workspaces are a high-risk population for this. Notebooks are the worst case — `.ipynb` stores cell **outputs**, so a printed env dump or an API response containing a token gets committed as JSON without anyone knowingly writing a secret to a file.

### Two layers, different jobs

| Layer | Mechanism | Job | Bypassable? |
|---|---|---|---|
| **Server-side** | GitLab **Secret Push Protection** (Ultimate, pre-receive) | **Enforcement** — blocks the push | Only via documented skip option |
| **Client-side** | `ws-publish` embedded scan of upperdir | **Fast feedback** — fails in <1s, names file + line | **Yes — trivially** |

Enable the server-side check per project **first**; it costs no code and covers `ws/*` tag pushes over HTTPS.

But it deliberately matches only **high-confidence patterns** to keep the hook fast (GitLab's docs call out that e.g. custom-prefix PATs are missed). Our actual risk is mostly *not* high-confidence: market-data vendor keys, DB connection strings, internal service creds. Necessary, **not sufficient**.

> **The client-side scan is not a security control.** Quants hold their own PAT, so they can always bypass `ws-publish` and `git push` directly. It is a speed bump for **accidents**, which is the real threat model — and it must never become the reason server-side protection stays off.

### Decision: embed the library, do not ship a binary

Import gitleaks' detection engine (`detect` package) rather than shelling out to the CLI. Measured, `linux/amd64`, gitleaks `v8.30.1`:

| Build | Size |
|---|---|
| `ws-publish` today (stdlib only) | 2.98 MiB |
| `ws-publish` + gitleaks `detect`, stripped `-s -w` | **9.16 MiB** |
| Standalone `gitleaks` CLI (for shell-out) | 23.39 MiB |

Shelling out ships **both** binaries (~26 MiB) — roughly **3× larger** than embedding. Importing `detect` alone skips cobra, the report formatters, the SCM integrations, and all git-history scanning, so **`go-git` is never linked in**. We only pay for the engine we use on the delta.

Module path gotcha: the repo moved to `gitleaks/gitleaks` but `go.mod` still declares the old path. Import **`github.com/zricethezav/gitleaks/v8`** — `go get github.com/gitleaks/gitleaks/v8` fails.

### Where it sits

Scan the **upperdir files, before building the commit** — so a detected secret never enters the temp worktree's object store. `walkUpper` already yields the exact changed-file list; whiteouts are deletions and need no scan. Delta is small, so this stays sub-second.

### Rules and posture

- Load rules from an **external TOML** (`config.ViperConfig` → `Translate()` → `detect.NewDetector`), not only the baked-in default set. Ship it **in the repo** so it arrives via the pinned lowerdir — versioned, reviewed, and updatable without rebuilding `ws-publish`.
- **Block by default.** Override via `--allow-secret` requiring a reason, recorded as a **commit trailer** so it is auditable rather than silent.

### Open question

Wiz (MR) and gitleaks (local) are **different rule corpora**. "Local says clean, Wiz says leak" is a confusing experience for a non-technical user. Check whether the Wiz CLI has a fast local directory-scan mode before committing to gitleaks — corpus consistency may be worth more than the packaging convenience.

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
- s3fs (or any FUSE) as overlay upperdir — see [Why not s3fs](#why-not-s3fs-s3fs-fuse-as-the-overlay-upperdir)
- Shared live upperdir across users
- Direct push to git-replica bypassing GitLab as source of truth for shares

---

## Lab status

| Piece | Status |
|---|---|
| Overlay COW / whiteout / revert / durable scratch | Kind lab OK — [`OVERLAY-LAB.md`](../OVERLAY-LAB.md) |
| Wire lower to reconciler `commits/<sha>/` on EKS | Next integration |
| Publish service (`share` / `promote`) | Skeleton CLI — [`workspace-publish/`](../../workspace-publish/) |
| Tag retention job | Designed here — to implement |
| Secret scanning gate | Designed here — to implement; enable GitLab Secret Push Protection first |

---

## Related

- [OVERLAY-LAB.md](../OVERLAY-LAB.md) — kind OverlayFS proof
- [REPLICA-LAB.md](../REPLICA-LAB.md) — git-replica + reconciler
- [KARPENTER-LAB.md](../KARPENTER-LAB.md) — EKS / Bottlerocket git-workers
