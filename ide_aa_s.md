ADR 004: IDE Backend Provisioning via Distroless Init-Containers

Status

Proposed

Context

Our Coder deployment on air-gapped EKS and OpenShift clusters currently faces significant efficiency and usability challenges regarding IDE backend delivery. These challenges differ by IDE type:

VS Code Server: While the binary size is relatively small (~60MB), the primary issue is version synchronization and "evergreening." Currently, the server is baked into a 6GB "Golden Image," making it difficult to update the IDE version without rebuilding the entire workspace image. Additionally, in an air-gapped environment, the local desktop client often fails to download the matching server version, leading to 10+ minute timeouts before falling back to a slow SCP transfer.

IntelliJ (JetBrains Gateway): At approximately 1.7GB, IntelliJ presents both a versioning and a storage/scalability challenge. Baking it into the base image is not feasible due to extreme image bloat. The current solution relies on pulling binaries from S3 (AWS) or an on-premise object store (Dell S3-compatible).

The existing object-store approach introduces significant engineering overhead to replicate and maintain the logic across different targets. Specifically, the on-premise implementation requires custom handling for the Dell object store to ensure parity with AWS, increasing the technical debt and maintenance surface for every infrastructure expansion.

Decision

We will transition to a Distroless Init-Container strategy to manage and deliver IDE binaries (VS Code and IntelliJ) specifically for Kubernetes-based workspaces.

Note: For non-containerized targets (such as standalone VMs), we will continue to utilize existing Configuration Management (CM) tools like Chef or Ansible to provision IDE backends.

Container-as-Artifact: IDE binaries will be stored as layers within a scratch-based container image. This image will also contain a statically compiled Go "Mover" binary.

Infrastructure Agnostic: We will leverage the existing Kubernetes imagePull mechanism. Since the cluster is already configured to pull from an internal registry, we eliminate the need for infrastructure-specific logic (IAM, VPC Endpoints, or Object Store authentication) within the workspace.

Persistence Layer: The initContainer will mount the user's Home PVC. Because the init-container and main container reside in the same Pod, they can share a ReadWriteOnce (RWO) EBS volume.

Evergreen Versioning (N-1 Policy): To solve the version-matching problem for both IDEs, the Go mover will enforce an N-1 version policy:

It will check a VERSION file in the persistent directory (e.g., /home/coder/.intellij-dist/VERSION).

If the version on disk is the current (N) or previous (N-1), the mover exits immediately (~0ms delay).

If the version is older or missing, the mover performs a clean extraction of the binaries to match the current platform standard.

User Choice: VS Code provisioning is mandatory/default (solving the version sync issue). IntelliJ is an opt-in parameter via the Coder UI.

Automatic Resource Scaling (Mitigation): To support the IntelliJ backend, the Coder template will dynamically adjust the PVC request. If the user opts-in for IntelliJ, the template will enforce a minimum PVC size of 10GB to accommodate the binary, the IDE runtime overhead, and the user's project data.

Future Extensibility: The IDE Sidecar Pattern

This strategy is designed as a reusable pattern for the platform. By standardizing the "Mover" logic, we can rapidly extend support to other JetBrains IDEs (PyCharm, WebStorm, CLion) or specialized data science tools without modifying the base workspace image.

The pattern for new tools follows a three-step integration:

Artifact Generation: Package tool binaries in a versioned scratch image with the Go mover.

Template Integration: Add a bool parameter to the Coder template for user opt-in.

Dynamic Orchestration: Use the same initContainer logic to hydrate the tool into a dedicated persistent directory on the user's PVC.

Implementation Details

1. High-Level Go "Mover" Logic

The Go binary handles the "Smart Sync" logic without requiring a shell environment (Distroless):

```go
// High-level Logic Flow
func main() {
    // 1. Identify tool and environment choice (e.g., PROVISION_INTELLIJ=true)
    // 2. Read 'VERSION' file from /home/coder/.intellij-dist/VERSION
    // 3. Compare with internal 'allowedVersions' list (N, N-1)
    
    if versionMatches(allowedVersions) {
        os.Exit(0) // Successful bypass
    }

    // 4. If mismatch: Wipe old directory and Extract embedded tarball
    os.RemoveAll("/home/coder/.intellij-dist")
    extractTarball("/intellij-backend.tar.gz", "/home/coder/.intellij-dist")
    
    // 5. Write new VERSION file
    os.WriteFile("/home/coder/.intellij-dist/VERSION", currentVersion)
}
```

2. High-Level Coder Template (Terraform)

The Coder template orchestrates the Pod lifecycle:

```hcl
# Coder Parameter for Opt-in
data "coder_parameter" "enable_intellij" {
  type    = "bool"
  name    = "enable_intellij"
  default = "false"
}

# Dynamic PVC Size Logic
locals {
  # Enforce 10GB minimum if IntelliJ is enabled
  pvc_size = data.coder_parameter.enable_intellij.value ? "10Gi" : "5Gi"
}

# Kubernetes Pod Spec
resource "kubernetes_pod" "workspace" {
  spec {
    init_container {
      name  = "ide-provisioner"
      image = "internal-registry/ide-mover:v2024.1"
      env {
        name  = "PROVISION_INTELLIJ"
        value = data.coder_parameter.enable_intellij.value
      }
      volume_mount {
        name       = "home"
        mount_path = "/home/coder"
      }
    }

    container {
      name  = "dev-container"
      image = "internal-registry/base-dev-image:latest"
      volume_mount {
        name       = "home"
        mount_path = "/home/coder"
      }
    }
  }
}
```

Consequences

Positive

Performance: Drastic reduction in startup time. Restarts are instantaneous once the binary is cached on the user's PVC.

Engineering Efficiency: Decouples IDE provisioning from storage protocols (S3/Dell). We leverage the standard container registry for artifact distribution, which is already replicated across environments.

Version Guarantee: Ensures that the VS Code Server version always matches the expected environment standard, eliminating desktop-to-server version mismatches.

Security: The scratch image has zero attack surface (no shell, no utilities).

Reliability: Eliminates network timeouts associated with air-gapped SCP/Proxy fallbacks.

Negative

Disk Usage (Mitigated): Each user PVC must accommodate the ~1.7GB IntelliJ backend. This is mitigated by the Coder template automatically scaling the PVC request to a minimum of 10GB upon opt-in.

First-Run Latency (Contextualized): While there is a delay during the initial extraction of the 1.7GB IntelliJ backend to the EBS volume, this latency is significantly lower than traditional methods.

Native extraction from a container layer happens at local node IO speed (EBS/SSD).

This is incomparable to the high latency of "over-the-wire" network transfers (S3 pulls or SCP fallbacks) which are prone to congestion and proxy-induced timeouts in air-gapped environments.

Alternatives Considered

S3 Sideloading (AWS & On-Prem): Rejected due to high engineering overhead required to maintain sync logic across multiple object-store providers (AWS S3 vs. Dell). It introduces unnecessary technical debt by requiring separate authentication and networking paths for binaries.

Full Baking: Rejected due to extreme image sizes (8GB+) and slow CI/CD cycles.

Chef/Ansible in Containers: Rejected in favor of native Kubernetes patterns (InitContainers) for containerized workloads to avoid installing CM agents inside ephemeral images.
