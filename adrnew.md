Master Specification: Secure LLM Gateway & Wallet Architecture

2. Architecture Decision Record (ADR-004) 📜

Status

Approved (v3.0 - Production Ready)

Context

Users can currently exfiltrate API keys to unmanaged environments (Local CLI, Docker Desktop). We need a server-side enforcement layer that anchors trust in the Network and Cryptographic layers.

Decision: Bifurcated Security Model

Path A: Internal Controlled Access (LiteLLM)

Lifecycle: Coder generates fresh Virtual Keys on every workspace start/restart.

Identity Binding: The key is tagged in LiteLLM metadata with a SHA-256 hash of the user's session_token.

Enforcement: LiteLLM validates IP provenance and matches the session signature against the stored metadata.

Path B: External Proxy Access (Go Helper)

Lifecycle: 3rd-party keys are long-lived and stored in AWS Secrets Manager (ASM).

Envelope Encryption: Keys are stored as ENC(...) blobs. Developers only see these blobs in their environment.

Enforcement: A stateless Go Proxy validates IP provenance and the session signature before "unwrapping" the key JIT (Just-In-Time) for the vendor.

Unified Security Controls

Network Fencing: Both paths strictly enforce traffic from authorized VPC CIDR ranges.

Signature Proof: Both paths require the x-coder-signature header containing the Coder session token, encrypted with RSA-4096.

3. Implementation: Internal Path (Python/LiteLLM) 🐍

This custom authentication hook is deployed to the LiteLLM proxy to handle identity binding and network fencing.

```python
import ipaddress, hashlib, os
from fastapi import Request, HTTPException

# CIDR ranges for EKS Pods / Node IPs
ALLOWED_CIDR = [ipaddress.ip_network(os.getenv("ALLOWED_CIDR", "10.0.0.0/8"))]

async def user_api_key_auth(request: Request, api_key: str):
    """
    Custom authentication for LiteLLM.
    Handles First-Use (Registry check) vs Cache-Miss (Redis check) correctly.
    """
    # 1. GATE 1: Network Lock (IP Provenance)
    # Cheapest check: verify the request comes from the VPC.
    client_ip = ipaddress.ip_address(request.client.host)
    if not any(client_ip in net for net in ALLOWED_CIDR):
        raise HTTPException(status_code=403, detail="VPC Access Required.")

    # 2. GATE 2: Redis Cache Check (Ultra-Fast Path)
    # We check if this specific key has already been verified in the last 12 hours.
    cached_result = await litellm.proxy.proxy_server.user_api_key_cache.get_cache(api_key)
    
    if cached_result and cached_result.get("metadata", {}).get("is_verified"):
        return cached_result

    # 3. GATE 3: Registry Check (The "First Use" or "Cache Miss" Path)
    # If not in Redis or not verified, we check the persistent LiteLLM database.
    # This ensures we distinguish between a 'Fresh Key' and a 'Deleted Key'.
    db_result = await litellm.proxy.proxy_server.db.get_api_key(api_key=api_key)
    
    if not db_result:
        # Key has been deleted by Coder 'destroy' or is invalid.
        raise HTTPException(status_code=401, detail="Invalid API Key: Not found in registry.")

    # 4. GATE 4: Identity Lock (Slow Path: RSA Handshake)
    # The key exists in the registry, now we verify the person using it matches the session.
    sig_header = request.headers.get("x-coder-signature")
    if not sig_header: 
        raise HTTPException(status_code=401, detail="Missing x-coder-signature header.")

    # Decrypt RSA Payload: [session_token]:[user_id]:[timestamp]:[nonce]
    try:
        # Note: decrypt_rsa is a helper utilizing the EKS/Vault-mounted Private Key
        sig_token, _, _, _ = decrypt_rsa(sig_header) 
    except Exception:
        raise HTTPException(status_code=403, detail="Failed to decrypt session signature.")
    
    # Cross-reference Signature Token Hash vs Registry Metadata
    # We retrieve the hash from the DB record's metadata
    expected_hash = db_result.get("metadata", {}).get("session_token_hash")
    current_hash = hashlib.sha256(sig_token.encode()).hexdigest()

    if current_hash != expected_hash:
        print(f"SECURITY ALERT: Identity Mismatch for key {api_key[:8]}...")
        raise HTTPException(status_code=403, detail="Identity Mismatch: Access denied.")

    # 5. PROMOTION (Update Redis)
    # We've verified identity. Promote to Redis to avoid DB/RSA hits for 12 hours.
    # We merge the DB result with the verification flag.
    db_result["metadata"]["is_verified"] = True
    await litellm.proxy.proxy_server.user_api_key_cache.set_cache(api_key, db_result, ttl=43200)
    
    return db_result
```

4. Implementation: External Path (Go Security Proxy) 🐹

A stateless, high-performance proxy that handles 3rd-party vendor traffic and credential "unwrapping."

```go
package main

import (
	"crypto/rsa"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"log"
)

func main() {
	target, _ := url.Parse(os.Getenv("UPSTREAM_URL"))
	_, internalNet, _ := net.ParseCIDR(os.Getenv("ALLOWED_CIDR"))
	
	// Support Dual-Key Fallback for Rotation Windows
	privKey, _ := loadKey("/etc/secrets/pki/private_key_current.pem")
	privKeyOld, _ := loadKey("/etc/secrets/pki/private_key_old.pem")

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Director = func(req *http.Request) {
		req.Host = target.Host
		
		// 1. Verify Session Signature (Proof of active Coder login)
		sig := req.Header.Get("x-coder-signature")
		if _, err := decryptWithFallback(sig, privKey, privKeyOld); err != nil {
			log.Printf("Invalid Signature from %s", req.RemoteAddr)
			return 
		}

		// 2. JIT Unwrap Wallet Token (Proof of Credential Possession)
		auth := req.Header.Get("Authorization")
		if strings.HasPrefix(auth, "Bearer ENC(") {
			blob := strings.TrimSuffix(strings.TrimPrefix(auth, "Bearer ENC("), ")")
			raw, _ := decryptWithFallback(blob, privKey, privKeyOld)
			req.Header.Set("Authorization", "Bearer "+raw)
		}
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mandatory IP Provenance Check
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !internalNet.Contains(net.ParseIP(host)) {
			http.Error(w, "Forbidden: VPC Access Only", http.StatusForbidden)
			return
		}
		proxy.ServeHTTP(w, r)
	})

	log.Printf("Security Proxy active on :8080. Tunneling to %s", os.Getenv("UPSTREAM_URL"))
	log.Fatal(http.ListenAndServe(":8080", handler))
}
```

5. Lifecycle: Secret Migration & Rotation (Airflow) ⚙️

This script runs in your weekly Airflow DAG to re-wrap secrets in AWS Secrets Manager (ASM) when the PKI keys rotate.

```python
import boto3, base64, os
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import padding

def rotate_external_secrets(v1_priv_pem, v2_pub_pem, rotation_id):
    """Re-wraps all 3rd-party secrets in ASM for the new PKI version."""
    asm = boto3.client('secretsmanager')
    v1_priv = serialization.load_pem_private_key(v1_priv_pem, None)
    v2_pub = serialization.load_pem_public_key(v2_pub_pem)

    # Filter secrets by tag: service=litellm-third-party
    paginator = asm.get_paginator('list_secrets')
    filters = [{'Key': 'tag:service', 'Values': ['litellm-third-party']}]

    for page in paginator.paginate(Filters=filters):
        for s in page['SecretList']:
            # Use 'rotation_id' marker to ensure idempotency
            tags = {t['Key']: t['Value'] for t in s.get('Tags', [])}
            if tags.get('litellm:pki_version') == rotation_id: continue

            # Re-wrap: Decrypt with Old V1 -> Encrypt with New V2
            old_enc = asm.get_secret_value(SecretId=s['ARN'])['SecretString']
            raw = v1_priv.decrypt(
                base64.b64decode(old_enc.replace("ENC(","").replace(")","")),
                padding.OAEP(padding.MGF1(hashes.SHA256()), hashes.SHA256(), None)
            )
            new_enc = v2_pub.encrypt(raw, padding.OAEP(padding.MGF1(hashes.SHA256()), hashes.SHA256(), None))
            
            # Commit update and tag with the new rotation version
            asm.put_secret_value(SecretId=s['ARN'], SecretString=f"ENC({base64.b64encode(new_enc).decode()})")
            asm.tag_resource(SecretId=s['ARN'], Tags=[{'Key':'litellm:pki_version','Value':rotation_id}])
```

6. Provisioning: Signature Generation (Terraform/Python) 🔑

This script is used as a Terraform external data source to generate the x-coder-signature when a workspace starts.

```python
import time, secrets, base64, json, sys
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import padding

def generate_signature(token, user_id, priv_key_pem):
    """Generates the RSA-encrypted session proof."""
    priv_key = serialization.load_pem_private_key(priv_key_pem.encode(), None)
    
    # Bound to: Session Token, User ID, and Current Timestamp
    payload = f"{token}:{user_id}:{int(time.time())}:{secrets.token_hex(4)}"
    
    cipher = priv_key.encrypt(
        payload.encode(),
        padding.OAEP(padding.MGF1(hashes.SHA256()), hashes.SHA256(), None)
    )
    return base64.b64encode(cipher).decode()

if __name__ == "__main__":
    # Receive query from Terraform stdin
    query = json.load(sys.stdin)
    sig = generate_signature(query['token'], query['user_id'], query['priv_key'])
    print(json.dumps({"signature": sig}))
```
