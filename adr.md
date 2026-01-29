ADR-001: Governed AI Architecture for Engineering

Status

Proposed (SPIKE Validated)

Context

As we integrate AI agents (Claude Code) into the wider engineering team, we must balance developer velocity with three critical risks:

Intellectual Property (IP) Exfiltration: Agents must not access unauthorized repositories or publicize internal logic.

Cognitive Erosion ("Vibe Coding"): Engineers committing AI-generated code without architectural understanding.

Financial Volatility: Uncapped token spend on agentic loops.

Decision

We will implement a "Guided Architect" pattern by enforcing a multi-layered governance stack within our Coder workspaces. This stack moves security from the "User Prompt" layer to the "Infrastructure" layer.

The Governance Layers:

Managed Physics (JSON): Immutable system settings in /etc/claude-code/managed-settings.json (root-owned). These physically disable "Write," "Bash," and "WebFetch" tools and enforce the Explanatory output style.

Dynamic Git-Origin Sentinel (Python): A PreToolUse hook (/usr/local/bin/validate_origin.py) that validates the git remote origin against a dynamically updated whitelist.

Automated Whitelist Management: A Kubernetes ConfigMap (ai-governance-config) containing authorized GitLab paths. This is synced via an Airflow DAG with our GitLab Group hierarchy.

Contextual Persona (Markdown): A K8s-mounted /home/coder/CLAUDE.md (via subPath mount) that enforces a "Read-Only Tutor" persona.

Financial Hard-Stop (LiteLLM): Per-user virtual keys with a $10/week limit.

Architecture Diagram

```mermaid
graph TD
    User((Engineer)) -->|Runs 'claude'| Workspace[Coder Workspace]
    
    subgraph "Control Plane (Automation)"
        Airflow[Airflow DAG] -->|Sync Group Metadata| K8sCM[K8s ConfigMap: ai-governance-config]
    end

    subgraph "Workspace Security Boundary (Root Owned)"
        Workspace --> ManagedSettings{Managed JSON Policy}
        ManagedSettings -->|Deny Tools| Tools[Blocked: Bash, Write, Fetch]
        
        subgraph "Git Sentinel (Python Hook)"
            PreToolUse[Read/LS/Glob Request] --> Hook[validate_origin.py]
            K8sCM -->|Mounted as Volume| CMFile[/etc/ai-governance/config.json]
            Hook -->|Validate Against| CMFile
            Hook -->|Path Resolution| Resolve[Anchor to CWD/Normalize Paths]
            Resolve -->|git remote -v| Validation{Is Authorized?}
            Validation -->|Yes| Success[Allow Tool]
            Validation -->|No| Block[Deny + Redact Tokens + Persona Guard]
        end
    end
    
    subgraph "API & Financial Boundary"
        Workspace --> LiteLLM[LiteLLM Proxy]
        LiteLLM -->|Identify User| VirtualKey[Virtual Key Module]
        VirtualKey -->|Check Quota| Quota{<$10/wk?}
        Quota -->|Yes| Bedrock[AWS Bedrock / Claude 3.7]
        Quota -->|No| Reject[429 Over Quota]
    end

    Success --> LiteLLM
```

Technical Logic & Refinements

1. Smart Path Anchoring

The sentinel logic extracts the target path from tool inputs (file_path, path, or pattern). To support the @ shorthand:

Glob Anchoring: If a Glob pattern starts with **/, it is automatically anchored to the current working directory (cwd).

Path Normalization: Relative paths are resolved to absolute paths before validation to prevent directory traversal attempts.

2. Origin Discovery

The script performs a "walk-up" from the target file to the nearest .git root using git rev-parse --show-toplevel. This ensures that even if Claude is launched from a parent directory, sub-repositories are correctly validated against the whitelist.

3. Credential Scrubbing (Security)

To prevent accidental leakage of Personal Access Tokens (PATs) embedded in Git remotes:

Redaction: All glpat- patterns are redacted from logs and LLM error messages using regex.

URL Scrubbing: The scrub_url function removes user:token@ segments from URLs before they are surfaced in the UI.

4. Fast Fail Mechanism

The sentinel implements a "Filesystem Jail" check. Any attempt to access paths outside of /home/coder is rejected immediately before Git validation occurs, providing a dual-layer defense.

Consequences

Positive

IP Safety: AI cannot process code from unauthorized sources.

Automated Scalability: The whitelist evolves automatically via Airflow.

Data Privacy: Internal credentials/tokens are never exposed to the model context.

Persona Integrity: Claude remains in its "Tutor" role even during security rejections.

Negative / Risks

Protocol Latency: Minute delays due to K8s ConfigMap sync or Git sub-process execution.

"No Git" Friction: Files not tracked in an authorized Git repository cannot be analyzed (intentional security policy).

Verification Log (SPIKE Results)

Jail Test: Confirmed /etc and /tmp access is blocked.

Relative Search Test: Confirmed claude "find adr/**" correctly resolves the local repo origin.

Credential Test: Confirmed glpat-... tokens are scrubbed from all outputs.
