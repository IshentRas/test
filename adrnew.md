ODS (Octopus Deployment System) - Managed Octopus Deploy Service

This repository documents the architecture and engineering standards for ODS, a centralized managed deployment service powered by Octopus Deploy.

1. Introduction

ODS is a multi-tenant Platform-as-a-Service (PaaS) designed to provide standardized, secure, and automated deployment capabilities across the enterprise. It bridges the gap between GitLab CI and various infrastructure targets (OpenShift, Cloud, and On-Prem VMs) while maintaining a Zero-Trust security posture.

2. Architectural Decision Records (ADR)

ADR 001: Consolidated Worker Strategy

Status: Accepted

Context: We need to support both cloud-native (OpenShift) and legacy on-prem infrastructure without compromising security or creating firewall complexity.

Decision: We will use a consolidated Polling Tentacle (Active) model for all workers.

Dynamic Fleet: Ephemeral workers running on OpenShift clusters.

On-Prem/Legacy: Dedicated workers on client-owned infrastructure.

Consequence: All workers initiate outbound-only connections to the ODS Server, eliminating the need for inbound firewall rules at the deployment target.

ADR 002: Attestation-Based Bootstrap Workflow

Status: Accepted

Context: Manual generation of API keys for machine registration is a security risk and an operational bottleneck.

Decision: Implement an ODS Intermediary Service to broker worker registration.

Identity Provider: A local VM service generates a short-lived JWT (30s window) containing VM identity claims (UUID, Hostname) verified against ServiceNow/CMDB.

Verification: The ODS Intermediary Service validates the JWT signature using a JKS Public Key and reconciles claims against CMDB records.

Bootstrap: Upon verification, the service generates a temporary, short-lived Octopus API key and returns it to the agent wrapper for one-time registration.

Consequence: Zero-human-touch registration with cryptographic proof of identity.

ADR 003: Zero-Trust Secret Management

Status: Accepted

Context: Secrets must not be stored statically in Octopus or on deployment targets.

Decision: Integrate HashiCorp Vault using OIDC/JWT authentication.

Flow: The ODS Worker (Tentacle) authenticates to Vault via OIDC using a JWT issued by the ODS Server.

Guardrails: Vault policies are mapped to JWT claims (ProjectID, SpaceID).

Secret Engine: GitLab runners retrieve Octopus API keys from a custom Vault secret engine using GitLab's JWT identity.

Consequence: Secrets are fetched at runtime and never persisted in the deployment tool.

ADR 004: Pragmatic Config-as-Code (CaC)

Status: Accepted

Context: Enable both UI speed for Day 1 and Git-driven governance for Day 2.

Decision: Implement a Hybrid Configuration model using OCL (Octopus Configuration Language).

Shared Logic: Common steps (e.g., Vault retrieval) are managed as Git-sourced Step Templates owned by the Platform Team.

Project Logic: Clients manage their .ocl files in GitLab. Octopus performs bidirectional sync, committing UI changes back to Git automatically.

Consequence: Full version control of deployment logic with the option for manual UI intervention when necessary.

3. The Registration Handshake

sequenceDiagram
    participant VM as Client VM (Wrapper)
    participant LS as Local Identity Service
    participant ODS as ODS Intermediary Service
    participant SN as ServiceNow (CMDB)
    participant OCT as Octopus Server

    VM->>LS: Request Identity Token
    LS->>SN: Verify Metadata
    SN-->>LS: Return Verified Identity
    LS-->>VM: Return Signed JWT (30s expiry)
    VM->>ODS: POST /register (JWT + Claims)
    ODS->>ODS: Verify JWT Signature (JKS)
    ODS->>OCT: Request Temporary API Key
    OCT-->>ODS: Return Token (10m expiry)
    ODS-->>VM: Return Token
    VM->>OCT: tentacle register-with --apiKey
    OCT-->>VM: X.509 Thumbprint Exchange
    Note over VM,OCT: Mutual Trust Established (Token Discarded)


4. Technology Stack

Component

Technology

Deployment Engine

Octopus Deploy

Configuration

OCL (HCL-based) / Terraform Provider

Secret Store

HashiCorp Vault

Identity/CMDB

ServiceNow / Custom JWT Service

Artifact Source

GitLab Container Registry / Nexus

Infrastructure

OpenShift, Windows, Linux

Last Updated: March 2, 2026
