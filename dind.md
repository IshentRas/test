# Nested Podman on OpenShift — organization request, test plan, and security posture

**Self-contained.** Share this file alone. No other documents required.

**Goal:** Run rootless **nested Podman** inside workspace pods that use Kubernetes user namespaces (`hostUsers: false`), for Coder-style developer environments.

**Lab basis:** Validated on OKD 4.22 (SNO). Applies to OpenShift / OKD.

| Cluster version | Path |
| --- | --- |
| **&lt; 4.21** | Custom SCC + Podman-default Localhost seccomp (primary ask below) |
| **≥ 4.21** | Prefer built-in SCC **`nested-container`**; use §3 and still run §7 tests |

---

## 1. What to request (checklist)

### Platform

- [ ] Dedicated **pilot namespace** (PSA allow for userns / Unmasked proc / added caps, or controlled exception).
- [ ] **CRI-O** with **crun** as default runtime (confirm).
- [ ] Device injection via annotation `io.kubernetes.cri-o.Devices`:
  - Required: `/dev/fuse`
  - If pasta: also `/dev/net/tun`
- [ ] Pod user namespaces: `hostUsers: false` (and SCC `userNamespaceLevel: RequirePodLevel` when supported).

### SCC / RBAC (&lt; 4.21)

- [ ] Create custom SCC (YAML in §5).
- [ ] ClusterRole `use` on that SCC only; RoleBinding to **workspace ServiceAccount only**.
- [ ] SCC `users: []` / `groups: []` — do **not** grant to `system:authenticated`.
- [ ] Pods use `openshift.io/required-scc: nested-podman`.

### Seccomp

- [ ] Install **unmodified** Podman default seccomp on nodes as Localhost:  
  `operator/podman-default.json`  
  (= `/usr/share/containers/seccomp.json` from Podman — **we did not add syscalls**).
- [ ] Do **not** use RuntimeDefault alone (pushes toward `SYS_ADMIN`).
- [ ] Optional: SPO ProfileBinding with `image: "*"` in the **dedicated** pilot namespace only (§6).

### Workload

- [ ] Template matches §5 Deployment (caps, priv-esc, Unmasked proc, UID 1000, Localhost seccomp, fuse/tun).

---

## 2. Security posture

### Capsule statement

> Residual risk is **engine capabilities + privilege escalation + a broader-than-RuntimeDefault seccomp filter inside a user namespace**, not host root. Capabilities do not apply on the node outside the pod userns; host UIDs are remapped to unprivileged ranges. The SCC is not privileged and is not granted cluster-wide.

### What this is / is not

| Is | Is not |
| --- | --- |
| SA-scoped custom SCC (or RH `nested-container` on 4.21+) | Privileged pods |
| Mandatory pod userns | hostPath / hostNetwork / hostPID / hostIPC |
| drop ALL + minimal adds | Granted to all authenticated users |
| Filtered Podman Localhost seccomp (&lt;4.21 path) | Invented custom syscall allowlist |

### Capabilities

| Cap | Why |
| --- | --- |
| `SETUID` / `SETGID` | Nested `newuidmap` / `newgidmap` (same as RH `nested-container`) |
| `SYS_CHROOT` | Podman-default seccomp **gates** `chroot` on this cap; without it nested run fails under the filter |
| **Not** `SYS_ADMIN` | Avoided by using Podman seccomp instead of RuntimeDefault |

Lab: SETUID+SETGID only → `chroot: operation not permitted`; +SYS_CHROOT → nested run OK (incl. pasta with tun).

### UID range 0–65534 and “root”

`uidRangeMin: 0` allows UID 0 **inside the userns** — same as Red Hat’s `nested-container`. With `hostUsers: false` that is **not host root**: the kubelet maps to an unprivileged host UID. Caps are namespaced.

Still **pin `runAsUser` / `runAsGroup: 1000`** for `quay.io/podman/stable`. Optional harden: SCC `MustRunAs` uid `1000` or range `1000–1000` (stricter than RH).

SCC has no `runAsNonRoot` field. Non-root is `MustRunAsNonRoot` **or** `MustRunAsRange` with min ≥ 1 — you cannot combine `MustRunAsNonRoot` with a 0–65534 range.

### Seccomp — nothing invented

Use stock Podman `/usr/share/containers/seccomp.json`. RuntimeDefault cap-gates `clone`/`unshare`/`mount` on `CAP_SYS_ADMIN`; Podman default allows those without SYS_ADMIN; `chroot` still needs `SYS_CHROOT`.

### Comparison

| Control | restricted-v2 (default) | Pre-4.21 ask | nested-container (4.21+) |
| --- | --- | --- | --- |
| Userms required | No | Yes | Yes |
| Caps | drop ALL; may add NET_BIND_SERVICE | SETUID, SETGID, **SYS_CHROOT** | SETUID, SETGID |
| Priv esc | false | true | true |
| Seccomp | RuntimeDefault | Podman Localhost (`Seccomp: 2`) | often `*` (open) |
| UID model | Project high UID (e.g. 1000730000+) | In-userns 0–65534; pin 1000 | In-userns 0–65534 |

Interim path is often **stricter on seccomp** than stock nested-container; it asks for **one extra cap** (`SYS_CHROOT`) to keep the filter.

### Consultant Q&A

| Objection | Response |
| --- | --- |
| Min UID 0 = root | In-userns only; host remapped. Pin 1000; can lock SCC to 1000. |
| SETUID/SETGID dangerous | Required for nested maps; RH nested-container allows the same. |
| Why SYS_CHROOT | Seccomp gate for chroot under Podman profile; not host escape. RuntimeDefault alternative ≈ SYS_ADMIN. |
| Priv esc true | Required for engine / NoNewPrivs; same as nested-container. |
| Custom SCC | Temporary; sunset on 4.21+ → nested-container. SA-only binding. |
| Force seccomp | SPO ProfileBinding `image: "*"` in dedicated ns (§6). |

### Optional tightenings

1. Lock SCC UID to 1000 only.  
2. `seccompProfiles: [localhost/*]` only.  
3. ProfileBinding in pilot ns.  
4. Written sunset after 4.21+.  
5. Namespace allowlist only.

---

## 3. ≥ 4.21 — built-in `nested-container`

When available, prefer RH’s SCC instead of the custom one.

| Field | Value |
| --- | --- |
| Caps | SETUID, SETGID |
| Userms | RequirePodLevel |
| SELinux | container_engine_t |
| Priv esc | true |
| UID / fsGroup / supplementalGroups | 0–65534 (in-userns) |
| Seccomp | `*` (often Unconfined — discuss with Security) |
| Privileged / host mounts | false |

**Ask:** RoleBinding for workspace SA → `system:openshift:scc:nested-container`; still require fuse/(tun), `hostUsers: false`, pin UID 1000.

If Security rejects Unconfined, keep Podman Localhost and expect possible `SYS_CHROOT` again.

Upstream SCC shape (for reference): `runAsUser.MustRunAsRange` uidRangeMin 0 / uidRangeMax 65534; fsGroup and supplementalGroups ranges 0–65534; `userNamespaceLevel: RequirePodLevel`; `seccompProfiles: ["*"]`; `allowedCapabilities: [SETUID, SETGID]`.

---

## 4. Obtain Podman-default seccomp JSON

Do **not** invent a profile. Extract stock Podman:

```bash
# From a machine with podman or a one-shot container:
podman run --rm quay.io/podman/stable:latest \
  cat /usr/share/containers/seccomp.json > podman-default.json
```

Install on every node at:

```text
/var/lib/kubelet/seccomp/operator/podman-default.json
```

Pod Localhost reference:

```yaml
seccompProfile:
  type: Localhost
  localhostProfile: operator/podman-default.json
```

---

## 5. Manifests to apply (&lt; 4.21)

Save as separate YAML files or one multi-doc file. Apply in order: SCC → seccomp install → workload.

### 5.1 Namespace for seccomp installer

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: openshift-seccomp
  labels:
    pod-security.kubernetes.io/enforce: privileged
    pod-security.kubernetes.io/audit: privileged
    pod-security.kubernetes.io/warn: privileged
```

### 5.2 ConfigMap (after creating `podman-default.json`)

```bash
oc -n openshift-seccomp create configmap podman-default-seccomp \
  --from-file=podman-default.json=./podman-default.json
```

### 5.3 DaemonSet — copy profile to nodes

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: seccomp-installer
  namespace: openshift-seccomp
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: seccomp-installer-privileged
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: system:openshift:scc:privileged
subjects:
  - kind: ServiceAccount
    name: seccomp-installer
    namespace: openshift-seccomp
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: install-podman-seccomp
  namespace: openshift-seccomp
  labels:
    app: install-podman-seccomp
spec:
  selector:
    matchLabels:
      app: install-podman-seccomp
  template:
    metadata:
      labels:
        app: install-podman-seccomp
    spec:
      serviceAccountName: seccomp-installer
      tolerations:
        - operator: Exists
      containers:
        - name: install
          image: registry.access.redhat.com/ubi9/ubi-minimal:latest
          securityContext:
            privileged: true
          command:
            - /bin/bash
            - -ec
            - |
              DST=/host/var/lib/kubelet/seccomp/operator
              mkdir -p "$DST"
              cp -f /seccomp/podman-default.json "$DST/podman-default.json"
              chmod 0644 "$DST/podman-default.json"
              while true; do
                cp -f /seccomp/podman-default.json "$DST/podman-default.json"
                sleep 3600
              done
          volumeMounts:
            - name: host
              mountPath: /host
            - name: seccomp
              mountPath: /seccomp
              readOnly: true
      volumes:
        - name: host
          hostPath:
            path: /
        - name: seccomp
          configMap:
            name: podman-default-seccomp
            items:
              - key: podman-default.json
                path: podman-default.json
```

### 5.4 Custom SCC + ClusterRole

```yaml
apiVersion: security.openshift.io/v1
kind: SecurityContextConstraints
metadata:
  name: nested-podman
  annotations:
    kubernetes.io/description: >
      Pre-4.21 nested Podman: userns required, SETUID/SETGID/SYS_CHROOT,
      container_engine_t, Localhost seccomp. Not privileged. SA-scoped only.
allowPrivilegedContainer: false
allowHostDirVolumePlugin: false
allowHostNetwork: false
allowHostPorts: false
allowHostPID: false
allowHostIPC: false
readOnlyRootFilesystem: false
allowPrivilegeEscalation: true
allowedCapabilities:
  - SETUID
  - SETGID
  - SYS_CHROOT
requiredDropCapabilities:
  - ALL
defaultAddCapabilities: null
runAsUser:
  type: MustRunAsRange
  uidRangeMin: 0
  uidRangeMax: 65534
fsGroup:
  type: MustRunAs
  ranges:
    - min: 0
      max: 65534
supplementalGroups:
  type: MustRunAs
  ranges:
    - min: 0
      max: 65534
seLinuxContext:
  type: MustRunAs
  seLinuxOptions:
    type: container_engine_t
seccompProfiles:
  - localhost/*
  - runtime/default
userNamespaceLevel: RequirePodLevel
volumes:
  - configMap
  - csi
  - downwardAPI
  - emptyDir
  - ephemeral
  - persistentVolumeClaim
  - projected
  - secret
users: []
groups: []
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: system:openshift:scc:nested-podman
rules:
  - apiGroups: [security.openshift.io]
    resources: [securitycontextconstraints]
    resourceNames: [nested-podman]
    verbs: [use]
```

If `userNamespaceLevel` is rejected on your z-stream, remove that field and still set `hostUsers: false` on the pod.

Optional stricter UID (instead of 0–65534):

```yaml
runAsUser:
  type: MustRunAs
  uid: 1000
```

### 5.5 Pilot namespace, SA, RoleBinding, Deployment

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: nested-podman
  labels:
    pod-security.kubernetes.io/enforce: privileged
    pod-security.kubernetes.io/audit: privileged
    pod-security.kubernetes.io/warn: privileged
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: nested-podman
  namespace: nested-podman
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: nested-podman-scc
  namespace: nested-podman
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: system:openshift:scc:nested-podman
subjects:
  - kind: ServiceAccount
    name: nested-podman
    namespace: nested-podman
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: nested-podman
  namespace: nested-podman
  annotations:
    openshift.io/required-scc: nested-podman
spec:
  replicas: 1
  selector:
    matchLabels:
      app: nested-podman
  template:
    metadata:
      labels:
        app: nested-podman
      annotations:
        openshift.io/required-scc: nested-podman
        io.kubernetes.cri-o.Devices: /dev/fuse,/dev/net/tun
    spec:
      serviceAccountName: nested-podman
      hostUsers: false
      securityContext:
        seccompProfile:
          type: Localhost
          localhostProfile: operator/podman-default.json
      containers:
        - name: podman
          image: quay.io/podman/stable:latest
          command: ["sleep", "infinity"]
          workingDir: /home/podman
          env:
            - name: STORAGE_DRIVER
              value: overlay
          securityContext:
            allowPrivilegeEscalation: true
            procMount: Unmasked
            runAsNonRoot: true
            runAsUser: 1000
            runAsGroup: 1000
            capabilities:
              drop: ["ALL"]
              add: ["SETUID", "SETGID", "SYS_CHROOT"]
            seccompProfile:
              type: Localhost
              localhostProfile: operator/podman-default.json
          resources:
            requests:
              cpu: 250m
              memory: 256Mi
            limits:
              cpu: "1"
              memory: 1Gi
```

Omit `/dev/net/tun` from the devices annotation if pasta is out of scope.

---

## 6. Optional — force seccomp with SPO ProfileBinding

Not required if every pod already sets Localhost. Useful for Security: enforce attachment in a **dedicated** namespace.

**Matching rules**

- `spec.image` is **exact** string match to the container image (tag included), **or**
- `image: "*"` = all containers in **that namespace only**
- Globs like `registry/repo/*` are **not** supported
- Namespace must be labeled `spo.x-k8s.io/enable-binding=true`
- SeccompProfile (or Localhost file) must exist; binding mutates **new** pods (restart after apply)

**Recommended for Coder pilot ns:**

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: nested-podman
  labels:
    spo.x-k8s.io/enable-binding: "true"
    pod-security.kubernetes.io/enforce: privileged
    pod-security.kubernetes.io/audit: privileged
    pod-security.kubernetes.io/warn: privileged
---
apiVersion: security-profiles-operator.x-k8s.io/v1alpha1
kind: ProfileBinding
metadata:
  name: nested-podman-podman-default
  namespace: nested-podman
spec:
  profileRef:
    kind: SeccompProfile
    name: podman-default
  image: "*"
```

If not using `*`, create one ProfileBinding per exact image string (including tag). Keep other namespaces without the enable-binding label.

(SPO `SeccompProfile` CR: convert/import the same Podman JSON into a `SeccompProfile` named `podman-default` in `nested-podman`, or keep DaemonSet install and rely on explicit Localhost in the pod without ProfileBinding.)

---

## 7. Pilot test plan

### Apply order

```bash
# 1) Extract seccomp JSON (§4)
# 2) openshift-seccomp ns + ConfigMap + DaemonSet (§5.1–5.3)
oc -n openshift-seccomp rollout status ds/install-podman-seccomp

# 3) SCC + ClusterRole (§5.4)
# 4) Workload (§5.5)
oc -n nested-podman rollout status deploy/nested-podman
```

### Acceptance

```bash
NS=nested-podman
DEPLOY=nested-podman

oc -n "$NS" get pod -l app=nested-podman \
  -o jsonpath='{.items[0].metadata.annotations.openshift\.io/scc}{"\n"}'
# expect: nested-podman   (or nested-container on 4.21+)

oc -n "$NS" exec deploy/"$DEPLOY" -- cat /proc/self/uid_map
# expect remapped, e.g. 0 <hostBase> 65536

oc -n "$NS" exec deploy/"$DEPLOY" -- grep -E 'Seccomp|CapBnd|NoNewPrivs' /proc/self/status
# Seccomp: 2 ; NoNewPrivs: 0 ; CapBnd has setuid,setgid,sys_chroot

oc -n "$NS" exec deploy/"$DEPLOY" -- id
# expect uid=1000

oc -n "$NS" exec deploy/"$DEPLOY" -- ls -l /dev/fuse
oc -n "$NS" exec deploy/"$DEPLOY" -- podman unshare id
oc -n "$NS" exec deploy/"$DEPLOY" -- podman run --rm alpine:3.20 echo OK

# If tun injected:
oc -n "$NS" exec deploy/"$DEPLOY" -- podman run --rm --network=pasta alpine:3.20 echo OK_PASTA
```

### Pass criteria

| Check | Pass |
| --- | --- |
| SCC annotation | nested-podman (or nested-container) |
| uid_map | Remapped userns |
| Seccomp | `2` with Localhost profile |
| Caps | setuid, setgid, sys_chroot |
| Nested run | `echo OK` succeeds |
| Pasta (if in scope) | `OK_PASTA` |

---

## 8. Sunset (after 4.21+)

1. Bind workspace SA to **`nested-container`**.  
2. Remove custom `nested-podman` SCC when equivalent.  
3. Revisit seccomp with Security (`*` vs keep Podman Localhost).  
4. Caps may reduce to SETUID+SETGID only on RH’s open-seccomp path.

---

## 9. Restricted-v2 / v3 context (defaults)

- **restricted-v2** is still what authenticated users get by default (project high UIDs, drop ALL, RuntimeDefault, no priv-esc). It **cannot** run nested Podman.
- **restricted-v3** is like v2 plus mandatory userns and in-userns UID floor **1000–65534** — still no SETUID/SETGID/SYS_CHROOT for a container engine.
- Nested engines need **`nested-container`** (4.21+) or this **custom SCC** (&lt;4.21).
