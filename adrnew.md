# SPIKE: PostgreSQL bytea → FUSE → fuse-overlayfs (Kind, no host SYS_ADMIN)

**Status:** Proven in Kind  
**Goal:** Present PostgreSQL `bytea` rows as a real Linux filesystem via a Go FUSE daemon, plus a writable union via `fuse-overlayfs`, runnable in a non-privileged pod.

---

## 1. Problem statement

Global workspace files live in PostgreSQL as `bytea` rows. This SPIKE asks:

1. Can a **Go FUSE** daemon present those rows as a **real Linux filesystem**?
2. Can **local edits** stay local (CoW) without writing back to Postgres?
3. Can this run in **Kubernetes (Kind)** with:
   - Postgres in **another pod**
   - Workspace **`privileged: false`**
   - **No host `CAP_SYS_ADMIN`**
4. Can **two containers** independently mount the same lower and see the same tree, with lower **read-only**?

---

## 2. Architecture (what we shipped)

```
┌─────────────────────┐         SQL / pgx          ┌──────────────────────────────────────┐
│  postgres pod       │◄──────────────────────────│  workspace pod (×2 replicas)          │
│  workspace_files     │                            │  privileged: false                    │
│  (path, bytea, …)   │                            │  no host SYS_ADMIN                    │
└─────────────────────┘                            │  resources: devic.es/fuse: 1          │
                                                   │                                      │
                                                   │  fuseboot (Go CLI)                   │
                                                   │    └─ unshare --user --map-root-user │
                                                   │         --mount --propagation private│
                                                   │         ├─ fusemount --direct-mount  │
                                                   │         │    → /mnt/lower  (ro)   │
                                                   │         └─ fuse-overlayfs            │
                                                   │              → /workspace (merged)   │
                                                   │         upper: /mnt/overlay/upper    │
                                                   └──────────────────────────────────────┘
```

| Path | Role |
|------|------|
| `/mnt/lower` | Go FUSE **lower** — Postgres `bytea`, kernel mount `ro` |
| `/mnt/overlay/upper` | Local **upper** — CoW edits / whiteouts (emptyDir) |
| `/mnt/overlay/work` | fuse-overlayfs workdir |
| `/workspace` | **Merged** writable view |

### Why this split

| Concern | Choice | Why |
|---------|--------|-----|
| Postgres → files | Go `fusemount` | Novel part of the SPIKE |
| Writable union | `fuse-overlayfs` | Correct OverlayFS semantics without reimplementing CoW/whiteouts |
| Kernel `mount -t overlay` | Avoided | Needs host `SYS_ADMIN`; upper cannot sit on overlay roots |
| Bidirectional sidecar | Avoided | Privilege-heavy; one process owns both mounts instead |
| Host `SYS_ADMIN` | Avoided | User+mount namespace gives mount capability **inside** the userns only |

---

## 3. Privilege model (lessons learned)

### 3.1 What failed

| Attempt | Result |
|---------|--------|
| `hostPath /dev/fuse` only | `open(/dev/fuse): Operation not permitted` — **devices cgroup** |
| `capabilities.drop: [ALL]` | CapBnd cleared → setuid `fusermount3` cannot gain mount caps |
| `RuntimeDefault` seccomp | **Fails** — `unshare: Operation not permitted` (confirmed Kind) |
| `allowPrivilegeEscalation: false` | **Works** with Localhost seccomp + DirectMount (confirmed Kind) |
| Expecting other containers to see FUSE mounts | Mounts are **per mount-namespace**; no magic sharing |

### 3.2 What worked

1. **Device plugin** (squat `generic-device-plugin`) allocating `devic.es/fuse: "1"`  
   → injects `/dev/fuse` into the pod devices cgroup  
2. **User + mount namespace at startup** via `unshare --user --map-root-user --mount`  
   → `uid_map`: `0 1000 1` (mapped root)  
   → CapEff full **inside** userns; host credential remains uid 1000 with **no** `SYS_ADMIN` in the pod securityContext  
3. **`DirectMount`** for go-fuse inside the userns (no setuid fusermount needed once mapped root)  
4. **Localhost seccomp** `deploy/seccomp/fuse-mount.json` — Docker-default based, with ungated allow for `unshare` / `mount` / `umount2`  
   (`RuntimeDefault` is **not** enough — still need this profile)  
5. **`allowPrivilegeEscalation: false`** — works with DirectMount; no_new_privs does not block userns creation here  

### 3.3 Pod securityContext (workspace)

```yaml
securityContext:
  allowPrivilegeEscalation: false
  privileged: false
  runAsNonRoot: true
  runAsUser: 1000
  runAsGroup: 1000
  capabilities:
    drop: ["ALL"]
    add: []          # no host SYS_ADMIN
  seccompProfile:
    type: Localhost
    localhostProfile: fuse-mount.json
resources:
  limits:
    devic.es/fuse: "1"
```

---

## 4. PostgreSQL schema and seed

### Schema (`sql/schema.sql`)

```sql
CREATE TABLE IF NOT EXISTS workspace_files (
    id SERIAL PRIMARY KEY,
    path VARCHAR(1000) NOT NULL,
    content BYTEA,
    is_directory BOOLEAN DEFAULT FALSE,
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE (path)
);
```

### Seed tree (nested paths)

Example rows used in the SPIKE:

```
/
├── README.md
├── LICENSE
├── notebooks/
│   ├── demo.ipynb
│   └── analysis/
│       ├── explore.ipynb
│       └── notes.md
├── data/
│   ├── sample.csv
│   ├── raw/
│   │   ├── events.jsonl
│   │   └── nested/
│   │       └── deep.txt
│   └── processed/
│       └── summary.csv
├── src/
│   ├── app/
│   │   ├── main.py
│   │   └── config.toml
│   └── lib/
│       └── util.py
└── etc/
    └── project.env
```

---

## 5. Go logic

### 5.1 Package layout

```
cmd/fuseboot/          # startup orchestrator (userns + mounts + validate)
cmd/fusemount/         # read-only Postgres FUSE server
internal/store/        # pgx queries (Getattr, ReadDir, Read)
internal/fusefs/       # go-fuse inode ops → store
internal/overlay/      # optional in-process union (not used on Kind path)
```

Libraries: `github.com/hanwen/go-fuse/v2`, `github.com/jackc/pgx/v5`.

---

### 5.2 `fuseboot` — namespace + mount lifecycle

**File:** `cmd/fuseboot/main.go`

**Outer process** (uid 1000, host userns): re-execes itself under `unshare`.

```go
const innerEnv = "FUSEBOOT_INNER"

func main() {
	if os.Getenv(innerEnv) != "1" {
		reexecInUserns()
		return
	}
	runInner()
}

func reexecInUserns() {
	self, _ := os.Executable()
	args := append([]string{
		"--user", "--map-root-user",
		"--mount", "--propagation", "private",
		self,
	}, os.Args[1:]...)
	cmd := exec.Command("unshare", args...)
	cmd.Env = append(os.Environ(),
		innerEnv+"=1",
		"FUSE_DIRECT_MOUNT=1",
	)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil { /* exit */ }
}
```

**Inner process** (mapped root, private mount ns):

1. Refuse to continue if still host-identity userns (`uid_map` `0 0 …`)
2. Wait for Postgres (`psql SELECT 1`)
3. Start `fusemount --direct-mount --mount=/mnt/lower`
4. Wait until `/mnt/lower/README.md` exists
5. Run `fuse-overlayfs -o lowerdir=…,upperdir=…,workdir=… /workspace`
6. Write readiness file `/tmp/fuse-ready`
7. Optionally run `container-validate.sh` → `/tmp/spike-validate.{log,ok}`
8. Block until SIGTERM (cleanup unmounts)

```go
fuseCmd := exec.Command(fusemountBin, "--dsn", dsn, "--mount", lower, "--direct-mount")
fuseCmd.Start()
waitFile(filepath.Join(lower, "README.md"), 15*time.Second)

ovCmd := exec.Command(overlayBin,
	"-o", fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", lower, upper, work),
	merged,
)
ovCmd.Run()
```

**Note on `kubectl exec`:** exec joins the **sandbox** namespaces, not the `unshare`’d ones. Validation therefore runs **inside** fuseboot’s userns and writes artifacts to the shared rootfs for `kind-validate.sh` to read.

---

### 5.3 `fusemount` — FUSE server entrypoint

**File:** `cmd/fusemount/main.go`

```go
root := fusefs.NewRoot(st)
opts := &fs.Options{
	MountOptions: fuse.MountOptions{
		FsName:            "pgfuse",
		Name:              "pgfuse",
		DirectMount:       *direct,       // true inside userns
		DirectMountStrict: *direct,
		DisableXAttrs:     true,
		Options:           []string{"ro"}, // kernel read-only mount
	},
}
server, err := fs.Mount(absMount, root, opts)
```

- **`DirectMount=false`** (default): setuid `fusermount3` (needs CapBnd / device access).  
- **`DirectMount=true`**: `mount(2)` as mapped root inside userns (Kind path).

---

### 5.4 `internal/store` — FUSE ↔ SQL

**File:** `internal/store/store.go`

Path normalization: FUSE `""` / `"."` → DB `"/"`; relative names get a leading `/`.

| FUSE op | SQL |
|---------|-----|
| **Getattr** | `SELECT path, is_directory, COALESCE(length(content),0), updated_at WHERE path=$1` |
| **ReadDir** (root) | `WHERE path LIKE '/%' AND path NOT LIKE '/%/%' AND path <> '/'` |
| **ReadDir** (dir) | `WHERE path LIKE $dir\|\|'/%' AND path NOT LIKE $dir\|\|'/%/%'` |
| **Read** | `SELECT substring(content FROM $offset+1 FOR $size)` — Postgres is **1-based** |

```go
// Read: FUSE offset is 0-based; PostgreSQL substring is 1-based.
err := s.pool.QueryRow(ctx, `
	SELECT substring(content FROM $2 FOR $3)
	FROM workspace_files
	WHERE path = $1 AND is_directory = FALSE
`, dbPath, offset+1, size).Scan(&data)
```

---

### 5.5 `internal/fusefs` — go-fuse nodes

**File:** `internal/fusefs/root.go`

Each inode is a `Node{store, path}` implementing:

| Interface | Behavior |
|-----------|----------|
| `Lookup` | `Getattr(child)` → new child inode |
| `Readdir` | `ReadDir` → `DirEntry` list |
| `Getattr` | size / mode / mtime from DB |
| `Open` | reject write flags with **`EROFS`**; else return `fileHandle` |
| `fileHandle.Read` | `store.Read(path, off, len(dest))` |

Modes reported as `0644` / `0755` so overlay can CoW; the FUSE mount itself is still `ro` and Open rejects writes.

```go
func (n *Node) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if flags&(syscall.O_WRONLY|syscall.O_RDWR|syscall.O_TRUNC|syscall.O_APPEND|syscall.O_CREAT) != 0 {
		return nil, 0, syscall.EROFS
	}
	// …
	return &fileHandle{store: n.store, path: n.path}, fuse.FOPEN_KEEP_CACHE, 0
}
```

Stable inode numbers: FNV-ish hash of path (`hashIno`).

---

## 6. Kind / Kubernetes layout

| Manifest | Purpose |
|----------|---------|
| `deploy/kind/cluster.yaml` | Kind cluster |
| `deploy/k8s/namespace.yaml` | `fuse-spike` |
| `deploy/k8s/postgres.yaml` | Postgres + init ConfigMap (schema/seed); Service is **headless** (`clusterIP: None`) because nested Kind broke ClusterIP/kube-proxy |
| `deploy/k8s/workspace.yaml` | **replicas: 2**, fuseboot entrypoint, fuse device, seccomp |
| `deploy/k8s/generic-device-plugin.yaml` | Exposes `devic.es/fuse` |
| `deploy/seccomp/fuse-mount.json` | Localhost seccomp profile |

`kind-up.sh` also rewrites the DSN secret to the **Postgres Pod IP** when DNS/ClusterIP is flaky in nested environments.

---

## 7. Validation results

### Per-pod (`container-validate.sh`, inside userns)

- Mapped userns (`uid_map` ≠ `0 0 …`)
- Nested paths present on **lower** and **merged**
- Write/touch on `/mnt/lower` → **`Read-only file system`**
- Edit via `/workspace` → file appears in **upper**; lower seed unchanged
- New files upper-only
- Lower fingerprint (file tree + sha256 of seed files) written to `/tmp/spike-fingerprint.txt`

### Multi-pod (`kind-validate.sh`)

- Both Ready workspace pods: no host `SYS_ADMIN`, `privileged: false`, `devic.es/fuse=1`
- Both report `spike-validate.ok=ok`
- **Lower fingerprints identical** across pods

```
KIND SPIKE OK — 2 containers see the same Postgres lower (R/O);
fuseboot userns; no host SYS_ADMIN.
```

### What this proves about “bytea → real file”

| Claim | Result |
|-------|--------|
| bytea row readable as a normal file path | Yes (in each FUSE consumer) |
| Nested subpaths work | Yes |
| Lower is R/O | Yes |
| Two containers see the same lower content | Yes (each mounts independently) |
| Other containers see mounts **without** running FUSE | **No** — out of scope; would need Bidirectional/CSI/shared mount ns |

---

## 8. How to run

```bash
# Prerequisites: Docker, kind, kubectl, /dev/fuse on the host for Kind nodes
./scripts/kind-up.sh
./scripts/kind-validate.sh
./scripts/kind-down.sh
```

Image entrypoint:

```text
/usr/local/bin/fuseboot
```

---

## 9. Follow-ups (out of Phase 1)

1. Jupyter / workbench sidecar consuming `/workspace` (needs shared mount story or its own fuseboot)
2. Push/sync upper → Postgres
3. Pure-Go userns (`clone` + uid_map) instead of shelling to `unshare`
4. Production device manager (smarter-device-manager / CSI) instead of squat GDP in Kind
5. Auth, multi-tenant path isolation, large-file streaming tuning

---

## 10. Decision log (short)

| Decision | Outcome |
|----------|---------|
| fuse-overlayfs vs Go in-process union | **fuse-overlayfs** — easier, correct whiteouts |
| Bidirectional sidecar vs single process | **Single process** (`fuseboot`) |
| Host SYS_ADMIN vs userns | **Userns** at startup |
| Device plugin needed? | **Yes** for `/dev/fuse` cgroup |
| Headless Postgres Service | **Yes** in this nested Kind (ClusterIP timed out) |

---

## 11. Key source index

| Path | Role |
|------|------|
| `cmd/fuseboot/main.go` | Userns reexec + mount orchestration |
| `cmd/fusemount/main.go` | FUSE mount options / lifecycle |
| `internal/store/store.go` | SQL for getattr/readdir/read |
| `internal/fusefs/root.go` | go-fuse Lookup/Readdir/Open/Read |
| `sql/schema.sql` / `sql/seed.sql` | Table + nested seed |
| `deploy/k8s/workspace.yaml` | Dual-replica non-privileged pod |
| `deploy/seccomp/fuse-mount.json` | Custom seccomp |
| `scripts/kind-up.sh` / `kind-validate.sh` | Cluster bring-up + multi-pod proof |
| `scripts/container-validate.sh` | In-userns functional checks |
