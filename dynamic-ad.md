# NFS + Kerberos over FUSE — design & proven client path

Self-contained record of what was proven on EKS, why each piece exists, identity/mapping behavior, seccomp syscalls that matter, and the proposed Go client shape. Lab code lives next to this file; tear down with `./run-aws.sh down`.

**Proven:** 2026-11-17, EKS `nfs-krb5-fuse` **1.33**, us-east-1  
**Constraints:** no privileged client, no `SYS_ADMIN` on the pod, `hostUsers: false`, `allowPrivilegeEscalation: false`, `drop ALL`, Localhost seccomp, `/dev/fuse` via device plugin.

---

## 1. Problem

Coder-like workspaces need NFSv4 + Kerberos home/share mounts inside pods that must stay:

- unprivileged (`CapEff=0`)
- non-root (`runAsUser: 1000`)
- user-namespaced (`hostUsers: false`)
- under PSA **restricted**
- without setuid helpers (`allowPrivilegeEscalation: false` → `no_new_privs`)

**Kernel NFS fails** in that box (EPERM / no idmapped mounts for NFS). The working path is **userspace NFS (libnfs + GSS) behind a FUSE mount**.

---

## 2. End-to-end architecture

```text
┌──────────────────────────── Pod (hostUsers: false) ─────────────────────────────┐
│  uid_map: 0 → <hostBase> 65536   CapEff=0   seccomp=Localhost/overlay-userns    │
│  /dev/fuse ← smarter-devices/fuse                                                │
│                                                                                  │
│  PID1 sleep ──► child: unshare -Urm ──► nested userns (uid 0 + caps in-ns)       │
│                      │                                                           │
│                      ├─ kinit -kt alice.keytab alice@LAB.LOCAL                   │
│                      └─ fuse-nfs / Go-FUSE+libnfs                                 │
│                            │ open /dev/fuse + mount(2)                           │
│                            │ NFS over TCP + RPCSEC_GSS (sec=krb5)                 │
└────────────────────────────┼────────────────────────────────────────────────────┘
                             ▼
                    Ganesha (lab may be privileged)
                    idmap: principal → server uid/gid
                             ▲
                    KDC (alice + nfs/hostname principals)
```

| Layer | Implementation in this spike |
|---|---|
| Cluster | EKS ≥ **1.33** (`eksctl-cluster.yaml`); 1.31 **strips** `hostUsers` |
| FUSE device | `k8s/fuse-device-plugin.yaml` → `smarter-devices/fuse: "1"` |
| Seccomp | `k8s/overlay-userns.json` installed by `seccomp-install` DS |
| Client pod | `k8s/client.yaml` |
| Client binary (proven) | `fuse-nfs` linked to libnfs **main + GSS** (`client/Dockerfile`) |
| Client binary (proposed) | Go: `go-fuse` FS ops → cgo → libnfs(GSS) |
| Auth | `kinit` + keytab; `USER=<principal-shortname>` |
| Server (lab only) | Ganesha + loop ext4 export; `SecType = krb5` |

---

## 3. What was proven (client)

| Check | Result |
|---|---|
| Live `spec.hostUsers` | `false` (honored on 1.33) |
| Pod `uid_map` | remapped, e.g. `0  695861248  65536` (not identity) |
| `CapEff` | `0` |
| Seccomp | mode 2 + Localhost profile |
| `/dev/fuse` | present via device plugin |
| Mount | `unshare -Urm` + `fuse-nfs … -p 0` |
| R/W | `READ:hello-krb5-nfs` / write OK under `sec=krb5` |

```sh
export KRB5_CONFIG=/shared/krb5.conf LD_LIBRARY_PATH=/opt/libnfs/lib USER=alice
unshare -Urm sh -c '
  kinit -kt /shared/alice.keytab alice@LAB.LOCAL
  fuse-nfs -n "nfs://nfs/export?version=4&sec=krb5" -m /home/coder/nfs -p 0
'
```

Reproduce: `./run-aws.sh` (creates/upgrades stack and runs the check). Destroy: `./run-aws.sh down`.

---

## 4. Identity & “uid/gid mapping” (three different maps)

Do not conflate these.

### 4.1 Pod user namespace (`hostUsers: false`)

- **What:** Kubernetes/runtime writes `/proc/self/uid_map` so pod UIDs are high host UIDs.
- **Identity mapping** would be `0 0 4294967295` (pod root = host root). We do **not** have that.
- **Remapped** example: `0 <hostBase> 65536` → pod 1000 = host `<hostBase>+1000`.
- **Purpose:** isolation for Coder. **Not** what authenticates NFS in this design.
- **EKS:** needs ≥ 1.33 or the API field is stripped.

### 4.2 Nested `unshare -Urm` user namespace

- With `CapEff=0`, the process cannot `mount` FUSE.
- `unshare -Urm` creates a **nested** userns where the process is uid 0 **with capabilities inside that ns**, so `mount`/`fusermount` can succeed.
- This nested `uid_map` is **required for the CapEff=0 path**.
- Pod-level `hostUsers: false` alone does **not** replace it when caps are fully dropped.

### 4.3 NFS / Kerberos identity (server-side)

- Wire auth is **`sec=krb5` (RPCSEC_GSS)**, not AUTH_SYS numeric uid/gid from the client.
- Client proves `alice@LAB.LOCAL` via ticket; Ganesha maps principal → **server** uid/gid (idmapper).
- Client Linux uid (1000 vs remapped host uid) does **not** need to equal NFS file owner numbers.
- `-p 0` on `fuse-nfs` disables **FUSE local** permission checks; server still enforces via GSS. `ls -l ~/nfs` works for browsing, but its owner/mode columns are **not** proof of server idmap (FUSE `-p 0` + remapped pod UIDs). Prove access with ticket-authenticated R/W.

**Kernel NFS + `hostUsers: false`:** dead end (EPERM / no idmapped NFS mounts). Irrelevant to the FUSE+GSS path except as a closed alternative.

---

## 5. Why each security control forces the next

```text
restricted + drop ALL + no privilege escalation
        │
        ├─► CapEff=0  ──► need nested unshare for FUSE mount
        │                      │
        │                      └─► RuntimeDefault blocks unshare/mount
        │                                 │
        │                                 └─► Localhost overlay-userns.json
        │
        ├─► allowPrivilegeEscalation:false ──► setuid fusermount cannot elevate
        │
        └─► hostUsers:false ──► pod uid_map remapped; kernel NFS volumes fail
```

| Approach | Under our constraints |
|---|---|
| Kernel `mount -t nfs4` / K8s NFS volume | Fail |
| Add `CAP_SYS_ADMIN` on pod | Blocked by restricted PSA; not the goal |
| setuid `fusermount` | Blocked by `no_new_privs` |
| RuntimeDefault + nested `unshare` | `unshare` → EPERM |
| Localhost seccomp + nested `unshare` + fuse-nfs(GSS) | **Works** |

**Keep the Localhost seccomp.** Dropping it while keeping this mount path fails.

---

## 6. Seccomp — what matters to allow

Profile: `k8s/overlay-userns.json`  
Origin: moby/containerd default, **pre-resolved** for a no-caps container (strip Docker `includes.caps` — containerd Localhost does not honor them), plus overlay/FUSE mount syscalls.

### Must allow beyond RuntimeDefault-with-no-caps

| Syscall | Why |
|---|---|
| `unshare` | Nested userns + mount ns (`unshare -Urm`) |
| `mount` | FUSE mount (and fusermount helper) |
| `umount2` | Unmount |
| `fsopen` | New mount API (util-linux / some FUSE stacks) |
| `fsconfig` | same |
| `fsmount` | same |
| `move_mount` | same |

Optional debug-only: `setns` (helps some `kubectl exec` / nsenter flows; **do not** ship in production profile unless needed).

### Generation sketch (same idea as overlay-restricted lab)

```python
EXTRA = ["unshare", "mount", "umount2", "fsopen", "fsconfig", "fsmount", "move_mount"]
# Start from moby default.json; drop rules that include caps the pod lacks;
# strip includes/excludes; append EXTRA as SCMP_ACT_ALLOW.
```

Install on nodes: ConfigMap → DaemonSet copies to `/var/lib/kubelet/seccomp/overlay-userns.json`.  
Pod: `seccompProfile.type: Localhost`, `localhostProfile: overlay-userns.json`.

Also needed for FUSE itself (already in default allow lists in this profile): open/read/write/`ioctl` on `/dev/fuse`, `clone` (with usual flags mask), etc. The **delta vs RuntimeDefault for CapEff=0** is the EXTRA list above.

---

## 7. Client binary path (proven): fuse-nfs + libnfs GSS

### 7.1 Debian spike (EKS lab)

`client/Dockerfile`: git-clone **main**, build with `libkrb5-dev`, link `fuse-nfs`, copy into runtime.

### 7.2 Red Hat / no-git path (org-friendly)

`client/Dockerfile.ubi` — multi-stage Rocky 9:

1. **curl** pinned tarball  
   `https://github.com/sahlberg/libnfs/archive/refs/tags/libnfs-8.0.1.tar.gz`  
   (swap `LIBNFS_URL` to an internal mirror when GitHub is blocked)
2. `./bootstrap && ./configure --prefix=/opt/libnfs && make install` with `krb5-devel`
3. `COPY --from=libnfs-build /opt/libnfs /opt/libnfs` into a thin runtime image (`krb5-libs`)
4. Build gate: `ldd … | grep -iE 'gss|krb'` must pass

Verified locally: configure prints `Build with gssapi_krb5 support`; final image links `libgssapi_krb5`.

**EPEL `libnfs-5.0.3` on el9 = no GSS** — do not use for `sec=krb5`.

Hard-learned:

| Issue | Fix |
|---|---|
| libnfs **5.0.2 / EPEL 5.0.3** have no GSS | Build **8.0.1+** (or main) from source tarball |
| Empty / wrong `USER` | `gss_import_name` fails → `export USER=alice` (match principal) |
| FUSE local mode bits vs server ACL | `fuse-nfs … -p 0` |
| Ticket ccache vs nested ns | `kinit` **inside** the same `unshare` as `fuse-nfs` |
| Cannot `git clone` in org | curl release tarball (or vendor it); see `Dockerfile.ubi` |

URL shape:

```text
nfs://<host>/export?version=4&sec=krb5
```

---

## 8. Proposed Go client (go-fuse ↔ libnfs mapping)

Not shipped in this spike yet; hellfuse (`nfs-fuse-poc/hellofuse`) only proved raw FUSE. Production shape:

```text
  VFS ops (open/read/write/…)
        │
        ▼
  go-fuse (hanwen/go-fuse/v2)     ← talks /dev/fuse, issues mount(2)
        │
        ▼
  Go NFS FS inode/file handles
        │  cgo
        ▼
  libnfs (GSS)                    ← NFSv4 + RPCSEC_GSS to Ganesha
```

### Mapping responsibilities

| Concern | Owner |
|---|---|
| Mount `/dev/fuse`, serve kernel upcalls | `go-fuse` `fs.Mount` |
| Translate FUSE → NFS (LOOKUP/READ/WRITE/…) | Go FS nodes calling libnfs |
| RPCSEC_GSS / krb5 context | libnfs (`sec=krb5`) + process ccache + `USER` |
| Local FUSE permission policy | Prefer “no local deny” equivalent of `-p 0` (always allow locally; server authorizes) |
| Nested userns / caps for mount | **Outer** `unshare -Urm` (or entrypoint wrapper) — same as fuse-nfs |
| Seccomp / device / PSA | Unchanged pod spec |

### Suggested Go surface

```go
// Pseudocode — proposed API
type Config struct {
    Server   string // nfs://host/export?version=4&sec=krb5
    MountAt  string
    User     string // short name for GSS, e.g. "alice"
    // Krb5 via env: KRB5_CONFIG, KRB5CCNAME (ticket already present)
}

// Run inside unshare -Urm after kinit.
func Mount(ctx context.Context, cfg Config) error {
    // cgo: nfs_init_context; nfs_set_username(cfg.User); nfs_mount(...)
    // go-fuse: fs.Mount(cfg.MountAt, root, &fs.Options{...})
    // root methods → nfs_open/nfs_read/nfs_write/nfs_lookup/...
}
```

### cgo / libnfs notes

- Link the **same** `/opt/libnfs` GSS build as fuse-nfs (`LD_LIBRARY_PATH`, rpath).
- Set username for GSS before mount (mirrors `USER=alice`).
- Reuse one `nfs_context` per mount; map FUSE handles ↔ `nfsfh`.
- Errors: surface NFS/GSS status clearly (WRONGSEC, expired TGT).
- Do **not** rely on AUTH_SYS uid/gid from the Go process for authorization when `sec=krb5`.

### Entrypoint pattern (Coder)

Keep PID 1 outside nested ns (so `kubectl exec` works), same lesson as overlay lab:

```sh
# PID1 waits; child owns unshare + mount + agent
unshare -Urm sh -c '
  export USER=alice KRB5_CONFIG=... LD_LIBRARY_PATH=/opt/libnfs/lib
  kinit -kt ... alice@LAB.LOCAL
  nfs-fuse-go -m /home/coder/nfs &   # or fuse-nfs
  exec unshare -U --map-user=1000 --map-group=1000 "$CODER_INIT_SCRIPT"
' &
wait $!
```

Second `unshare` maps back to uid 1000 while keeping the FUSE mount visible.

---

## 9. Lab server notes (not for workspace image)

- KDC mints `alice` + `nfs/<host>` keytabs → Secret into client + ganesha.
- Ganesha: `NFS_KRB5 { Active_krb5 = true; }`, export `Sectype = krb5`, `Squash = None`.
- Ganesha pod may be privileged + loopback ext4 — **infra only**. Workspace/client stays restricted.

---

## 10. Checklist for a production Coder template

- [ ] EKS (or k8s) ≥ 1.33 with `hostUsers: false` actually present on the live Pod
- [ ] Remapped `uid_map` verified once per node class
- [ ] Localhost seccomp with EXTRA syscalls installed on every node
- [ ] FUSE device plugin / CDI grant (`smarter-devices/fuse` or equivalent)
- [ ] Image: libnfs **with GSS**, fuse-nfs and/or Go binary, krb5-user, util-linux
- [ ] Entrypoint: PID1 wait + `unshare -Urm` child; kinit; mount; map back to 1000 for agent
- [ ] `USER` / libnfs username = ticket principal short name
- [ ] FUSE local perms disabled (`-p 0` or Go equivalent)
- [ ] No dependency on setuid fusermount or pod `SYS_ADMIN`
- [ ] Document that NFS auth is GSS principal → server idmap, not pod uid

---

## 11. Repo map

| Path | Role |
|---|---|
| `DESIGN.md` | This document |
| `README.md` | Short verdict + run/down |
| `run-aws.sh` | Deploy + prove + `down` |
| `eksctl-cluster.yaml` | EKS 1.33 lab cluster |
| `client/Dockerfile` | Debian: libnfs main+GSS + fuse-nfs |
| `client/Dockerfile.ubi` | Rocky9: curl `libnfs-8.0.1.tar.gz` → `/opt/libnfs` (GSS) |
| `k8s/client.yaml` | Restricted client Pod |
| `k8s/overlay-userns.json` | Seccomp allowlist |
| `k8s/seccomp-install-ds.yaml` | Install profile on nodes |
| `k8s/fuse-device-plugin.yaml` | `/dev/fuse` |
| `k8s/ganesha.yaml` / `ganesha/` | Lab NFS server |
| `krb5/` | Lab KDC |
| `../nfs-fuse-poc/hellofuse` | Minimal go-fuse smoke (no NFS) |
