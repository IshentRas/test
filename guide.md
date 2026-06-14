```hcl
terraform {
  required_providers {
    coder      = { source = "coder/coder" }
    kubernetes = { source = "hashicorp/kubernetes" }
  }
}

# 1. Get Coder User Identity
data "coder_workspace_owner" "me" {}

# 2. Execute Python Decryption Logic
data "external" "decrypted_vault" {
  program = ["python3", "${path.module}/decrypt_secrets.py"]

  query = {
    email       = data.coder_workspace_owner.me.email
    public_key  = data.coder_workspace_owner.me.ssh_public_key
    private_key = data.coder_workspace_owner.me.ssh_private_key
  }
}

# 3. Create Kubernetes Secret
resource "kubernetes_secret" "vault" {
  metadata {
    name      = "user-vault-decrypted"
    namespace = "default"
    labels = {
      "app.kubernetes.io/managed-by" = "terraform"
      "coder.owner"                  = data.coder_workspace_owner.me.name
    }
  }

  # data.external.vault.result is a map like {"JIRA_TOKEN": "...", "GITLAB_TOKEN": "..."}
  data = data.external.decrypted_vault.result

  type = "Opaque"
}

# 4. Output the Secret Name for Deployment usage
output "secret_name" {
  description = "Name of the secret to be used in env_from"
  value       = kubernetes_secret.vault.metadata[0].name
}
```

Hybrid Terraform Module: Secure Secret Retrieval & Decryption

This document outlines a solution for retrieving encrypted secrets from AWS Secrets Manager (ASM), decrypting them locally using a user's Coder-managed SSH keys, and provision them as a Kubernetes Secret.

1. Architecture Overview

We use a Hybrid Approach:

Terraform (coder provider): Retrieves the user's identity, email, and SSH keys (Ed25519).

Python Helper (external data source): Performs complex cryptography that Terraform cannot do natively. It converts the Ed25519 keys to X25519 for an Elliptic Curve Diffie-Hellman (ECDH) key exchange and decrypts the AES-GCM payload.

Kubernetes Provider: Creates a single Opaque secret containing all decrypted tokens (JIRA, GitLab, etc.).

Cryptographic Flow

Identity: The user's email and ssh_public_key create a deterministic hash.

Naming: Secrets are stored in ASM as {feature}-{hash}.

Key Exchange: The Python script performs a birational map to convert the Coder Ed25519 key to a curve25519 (X25519) key.

Decryption: It uses the ephemeral public key stored in the ASM blob to derive a shared secret (HKDF) and decrypt the payload via AES-256-GCM.

2. The Python Decryption Script (decrypt_secrets.py)

This script acts as the engine for the Terraform external data source.

import sys
import json
import hashlib
import base64
import boto3
import os
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import x25519
from cryptography.hazmat.primitives.kdf.hkdf import HKDF
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes

def ed25519_to_x25519_priv(ed_priv_key):
    """Converts Ed25519 private key to X25519 for decryption."""
    priv_bytes = ed_priv_key.private_bytes_raw()
    h = hashlib.sha512(priv_bytes).digest()
    x_bytes = bytearray(h[:32])
    x_bytes[0] &= 248
    x_bytes[31] &= 127
    x_bytes[31] |= 64
    return x25519.X25519PrivateKey.from_private_bytes(bytes(x_bytes))

def decrypt_payload(encoded_blob, x25519_priv):
    """Decrypts the AES-GCM payload using the derived shared secret."""
    data = base64.b64decode(encoded_blob)
    # Layout: IV(12) | EphemeralPub(32) | Tag(16) | Ciphertext(...)
    iv, eph_pub_bytes, tag, ciphertext = data[:12], data[12:44], data[44:60], data[60:]
    
    eph_pub = x25519.X25519PublicKey.from_public_bytes(eph_pub_bytes)
    shared_key = x25519_priv.exchange(eph_pub)
    
    derived_key = HKDF(
        algorithm=hashes.SHA256(),
        length=32,
        salt=None,
        info=b'wallet-encryption'
    ).derive(shared_key)
    
    decryptor = Cipher(algorithms.AES(derived_key), modes.GCM(iv, tag)).decryptor()
    return (decryptor.update(ciphertext) + decryptor.finalize()).decode('utf-8')

def main():
    # 1. Read input from Terraform Stdin
    try:
        input_data = json.load(sys.stdin)
        email = input_data['email']
        pub_ssh = input_data['public_key'].strip()
        priv_openssh = input_data['private_key'].strip()
    except Exception as e:
        sys.stderr.write(f"Input Error: {str(e)}")
        sys.exit(1)

    # 2. Setup Identity and Key
    wallet_id = hashlib.sha256(f"{email}-{pub_ssh}".encode()).hexdigest()[:32]
    private_key_obj = serialization.load_ssh_private_key(priv_openssh.encode(), password=None)
    x25519_priv = ed25519_to_x25519_priv(private_key_obj)

    # 3. Fetch and Decrypt defined features
    client = boto3.client('secretsmanager')
    feature_map = {
        "jira": "JIRA_TOKEN",
        "gitlab": "GITLAB_TOKEN",
        "confluence": "CONFLUENCE_TOKEN",
        "litellm": "LITELLM_KEY"
    }
    
    results = {}
    for feature, env_name in feature_map.items():
        secret_name = f"secure-coder-{feature}-{wallet_id}"
        try:
            resp = client.get_secret_value(SecretId=secret_name)
            results[env_name] = decrypt_payload(resp['SecretString'], x25519_priv)
        except client.exceptions.ResourceNotFoundException:
            continue # Secret not created yet for this user
        except Exception as e:
            sys.stderr.write(f"Error decrypting {secret_name}: {str(e)}\n")

    # 4. Return JSON to Terraform Stdout
    print(json.dumps(results))

if __name__ == "__main__":
    main()


3. The Terraform Module (main.tf)

This module wraps the Python script and creates the Kubernetes resource.

terraform {
  required_providers {
    coder      = { source = "coder/coder" }
    kubernetes = { source = "hashicorp/kubernetes" }
  }
}

# 1. Get Coder User Identity
data "coder_workspace_owner" "me" {}

# 2. Execute Python Decryption Logic
data "external" "decrypted_vault" {
  program = ["python3", "${path.module}/decrypt_secrets.py"]

  query = {
    email       = data.coder_workspace_owner.me.email
    public_key  = data.coder_workspace_owner.me.ssh_public_key
    private_key = data.coder_workspace_owner.me.ssh_private_key
  }
}

# 3. Create Kubernetes Secret
resource "kubernetes_secret" "vault" {
  metadata {
    name      = "user-vault-decrypted"
    namespace = "default"
    labels = {
      "app.kubernetes.io/managed-by" = "terraform"
      "coder.owner"                  = data.coder_workspace_owner.me.name
    }
  }

  # data.external.vault.result is a map like {"JIRA_TOKEN": "...", "GITLAB_TOKEN": "..."}
  data = data.external.decrypted_vault.result

  type = "Opaque"
}

# 4. Output the Secret Name for Deployment usage
output "secret_name" {
  description = "Name of the secret to be used in env_from"
  value       = kubernetes_secret.vault.metadata[0].name
}


4. Usage in Workloads

Once the module is applied, you can inject all decrypted tokens into any Kubernetes deployment with a single env_from block.

module "my_secrets" {
  source = "./modules/user-vault"
}

resource "kubernetes_deployment" "app" {
  # ... metadata ...
  spec {
    template {
      spec {
        container {
          name  = "workspace-app"
          image = "my-image:latest"

          # All keys in the secret become ENV VARS
          env_from {
            secret_ref {
              name = module.my_secrets.secret_name
            }
          }
        }
      }
    }
  }
}


5. Security & Maintenance

Dependencies: The execution environment (Coder Provisioner) must have pip install cryptography boto3.

State File: Be aware that data.external query parameters (including the private key) are stored in the Terraform state. Use a secure backend with encryption.

Troubleshooting: If secrets are missing, check the Terraform logs. Python errors will be redirected to stderr and displayed by Terraform during plan/apply.
