import os
import base64
import hashlib
import boto3
import traceback
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ed25519, x25519
from cryptography.hazmat.primitives.kdf.hkdf import HKDF
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes

# --- CONFIGURATION & MOCK DATA ---
USER_EMAIL = "henry.boyle@example.com"
MOCK_PAT = "glpat-TESTING1234567890"
REGION = "us-east-1" # Update to your region

# SIMULATED CODER API RESPONSE (As provided by user)
CODER_GIT_SSH_KEY = {
  "public_key": "XXXXXX",
  "private_key": "XXXXXXX"
}

def ed25519_to_x25519(ed_priv_key, ed_pub_key):
    """
    Converts Ed25519 keys to X25519 keys for key exchange.
    Returns (x25519_private_key, x25519_public_key) objects.
    """
    # Convert private key: SHA-512 hash + clamp
    priv_bytes = ed_priv_key.private_bytes_raw()
    h = hashlib.sha512(priv_bytes).digest()
    x25519_priv_bytes = bytearray(h[:32])
    x25519_priv_bytes[0] &= 248
    x25519_priv_bytes[31] &= 127
    x25519_priv_bytes[31] |= 64
    
    # Convert public key: birational map
    pub_bytes = ed_pub_key.public_bytes_raw()
    p = 2**255 - 19
    y = int.from_bytes(pub_bytes, 'little') & ((1 << 255) - 1)
    u = ((1 + y) * pow(1 - y, -1, p)) % p
    x25519_pub_bytes = u.to_bytes(32, 'little')
    
    return (
        x25519.X25519PrivateKey.from_private_bytes(bytes(x25519_priv_bytes)),
        x25519.X25519PublicKey.from_public_bytes(x25519_pub_bytes)
    )

def derive_key(shared_key):
    """Derive AES key from shared secret using HKDF."""
    return HKDF(
        algorithm=hashes.SHA256(), length=32, salt=None, info=b'wallet-encryption'
    ).derive(shared_key)

def encrypt_payload(plaintext, peer_x25519_pub):
    """Encrypt plaintext using X25519 key exchange. Returns base64-encoded payload."""
    ephemeral_key = x25519.X25519PrivateKey.generate()
    shared_key = ephemeral_key.exchange(peer_x25519_pub)
    derived_key = derive_key(shared_key)
    
    iv = os.urandom(12)
    encryptor = Cipher(algorithms.AES(derived_key), modes.GCM(iv)).encryptor()
    ciphertext = encryptor.update(plaintext.encode()) + encryptor.finalize()
    
    # Payload: IV(12) + EphemeralPubKey(32) + Tag(16) + Ciphertext
    payload = iv + ephemeral_key.public_key().public_bytes_raw() + encryptor.tag + ciphertext
    return base64.b64encode(payload).decode('utf-8')

def decrypt_payload(encoded_blob, x25519_priv):
    """Decrypt base64-encoded payload using X25519 private key. Returns plaintext."""
    data = base64.b64decode(encoded_blob)
    iv, ephemeral_pub_bytes, tag, ciphertext = data[:12], data[12:44], data[44:60], data[60:]
    
    ephemeral_pub = x25519.X25519PublicKey.from_public_bytes(ephemeral_pub_bytes)
    shared_key = x25519_priv.exchange(ephemeral_pub)
    derived_key = derive_key(shared_key)
    
    decryptor = Cipher(algorithms.AES(derived_key), modes.GCM(iv, tag)).decryptor()
    return (decryptor.update(ciphertext) + decryptor.finalize()).decode('utf-8')

def store_secret(client, secret_name, value):
    """Store secret in AWS Secrets Manager, creating if needed."""
    try:
        client.create_secret(Name=secret_name, SecretString=value)
    except client.exceptions.ResourceExistsException:
        client.put_secret_value(SecretId=secret_name, SecretString=value)

def run_test():
    print("--- Starting Digital Wallet Logic Verification (Ed25519/OpenSSH) ---")
    
    # 1. SETUP: Load Coder keys from provided API response
    print("[1/4] Loading Ed25519 keys from Coder API response...")
    pub_ssh = CODER_GIT_SSH_KEY["public_key"].strip()
    priv_openssh = CODER_GIT_SSH_KEY["private_key"].strip()

    # Load into objects to simulate internal processing
    # Note: load_ssh_private_key is used for "BEGIN OPENSSH PRIVATE KEY"
    private_key_obj = serialization.load_ssh_private_key(priv_openssh.encode(), password=None)
    public_key_obj = private_key_obj.public_key()

    # Convert Ed25519 to X25519 once (reused for both encryption and decryption)
    x25519_priv, x25519_pub = ed25519_to_x25519(private_key_obj, public_key_obj)

    print(f"      Identity Loaded: {pub_ssh[:30]}...")

    # 2. ENCRYPTION: Simulate 'Wallet App'
    print("[2/4] Simulating Wallet App (Encryption via Key Exchange)...")
    
    wallet_id = hashlib.sha256(f"{USER_EMAIL}-{pub_ssh}".encode()).hexdigest()[:32]
    secret_name = f"whatever-{wallet_id}"
    
    # Use pre-converted X25519 public key for encryption
    encoded_blob = encrypt_payload(MOCK_PAT, x25519_pub)
    
    client = boto3.client('secretsmanager', region_name=REGION)
    store_secret(client, secret_name, encoded_blob)
    print(f"      Secret '{secret_name}' pushed to ASM.")

    # 3. RETRIEVAL & DECRYPTION: Simulate 'Workspace Module'
    print("[3/4] Simulating Workspace Module (Decryption)...")
    
    try:
        # Step A: Demonstration of Encrypted Value
        resp = client.get_secret_value(SecretId=secret_name)
        raw_retrieved_value = resp['SecretString']
        
        print(f"      --- SECURITY DEMO ---")
        print(f"      Raw value retrieved from ASM: {raw_retrieved_value[:60]}...")
        if raw_retrieved_value != MOCK_PAT:
            print("      [VERIFIED] The retrieved value is encrypted and does NOT match the plaintext PAT.")
        else:
            print("      [ALERT] The retrieved value matches the plaintext PAT!")
        print(f"      --- END DEMO ---")

        # Step B: Decryption process using pre-converted X25519 private key
        decrypted_pat = decrypt_payload(raw_retrieved_value, x25519_priv)
        
        if decrypted_pat == MOCK_PAT:
            print("      SUCCESS: Identity Bridge Verified using real Coder Ed25519 data!")
            
    except Exception as e:
        print(f"      FAILURE: {type(e).__name__}: {str(e)}")
        traceback.print_exc()

    # 4. CLEANUP
    print("[4/4] Cleaning up...")
    client.delete_secret(SecretId=secret_name, ForceDeleteWithoutRecovery=True)
    print("      Test complete.")

if __name__ == "__main__":
    run_test()
