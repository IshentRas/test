import logging
import os
from datetime import datetime

import hvac
from airflow.sdk import Variable, dag, task
from airflow.sdk.execution_time.secrets_masker import mask_secret

log = logging.getLogger("airflow.task")

# Vault configuration
VAULT_URL = os.getenv("VAULT_ADDR", "http://vault.vault.svc:8200")
VAULT_NAMESPACE = os.getenv("VAULT_NAMESPACE", None)  # None = root namespace
VAULT_IAM_ROLE = os.getenv("VAULT_IAM_ROLE", "default")
VAULT_SECRET_PATH = os.getenv("VAULT_SECRET_PATH", "secret/data/myapp/password")
VAULT_SECRET_KEY = os.getenv("VAULT_SECRET_KEY", "password")


class VaultClient:
    """Client for interacting with HashiCorp Vault using IAM authentication.
    
    Supports Vault Enterprise namespaces for multi-tenancy.
    """
    
    def __init__(self, vault_url: str, iam_role: str = None, namespace: str = None):
        """Initialize Vault client.
        
        Args:
            vault_url: Base URL for Vault API
            iam_role: Optional IAM role name for authentication
            namespace: Optional Vault namespace (for Vault Enterprise).
                      If None, uses root namespace. Namespace format: "ns1" or "ns1/ns2"
        """
        self.vault_url = vault_url.rstrip('/')
        self.iam_role = iam_role or "default"
        self.namespace = namespace
        self.client = hvac.Client(url=self.vault_url)
        
        # Set namespace if provided (for Vault Enterprise)
        if self.namespace:
            self.client.adapter.namespace = self.namespace
            log.info(f"Using Vault namespace: {self.namespace}")
        else:
            log.info("Using root namespace (no namespace specified)")
        
        self._authenticated = False
    
    def authenticate_with_iam(self) -> None:
        """Authenticate with Vault using AWS IAM role.
        
        This method uses the IAM role attached to the pod/service account
        to authenticate with Vault's AWS auth method.
        
        Raises:
            Exception: If authentication fails
        """
        try:
            namespace_info = f" in namespace '{self.namespace}'" if self.namespace else ""
            log.info(
                f"Authenticating with Vault at {self.vault_url} "
                f"using IAM role: {self.iam_role}{namespace_info}"
            )
            
            # Authenticate using AWS IAM method
            # This uses the IAM role credentials from the pod's service account
            # (when running on EKS) or EC2 instance profile
            self.client.auth.aws.iam_login(
                role=self.iam_role
            )
            
            if self.client.is_authenticated():
                self._authenticated = True
                log.info("Successfully authenticated with Vault using IAM!")
            else:
                raise Exception("Authentication failed - client not authenticated")
        
        except Exception as e:
            raise Exception(
                f"ERROR authenticating with Vault using IAM: {e}"
            )
    
    def get_secret(self, secret_path: str, secret_key: str = None) -> str:
        """Retrieve a secret from Vault.
        
        Args:
            secret_path: Vault secret path (e.g., "secret/data/myapp/password")
            secret_key: Optional key name within the secret data. 
                       If None, returns the entire data dictionary as string.
            
        Returns:
            Secret value as string
            
        Raises:
            ValueError: If not authenticated
            Exception: If secret retrieval fails
        """
        if not self._authenticated and not self.client.is_authenticated():
            raise ValueError(
                "ERROR: Not authenticated. Call authenticate_with_iam() first."
            )
        
        try:
            log.info(f"Retrieving secret from Vault path: {secret_path}")
            
            # Read secret from Vault
            # For KV v2 secrets engine, the path format is: secret/data/path
            # The response structure is: {'data': {'data': {...}, 'metadata': {...}}}
            response = self.client.secrets.kv.v2.read_secret_version(
                path=secret_path
            )
            
            # Extract the data
            secret_data = response.get('data', {}).get('data', {})
            
            if not secret_data:
                raise Exception(f"No data found at path: {secret_path}")
            
            # If secret_key is specified, return that specific key
            if secret_key:
                if secret_key not in secret_data:
                    raise ValueError(
                        f"Secret key '{secret_key}' not found in secret data. "
                        f"Available keys: {list(secret_data.keys())}"
                    )
                secret_value = secret_data[secret_key]
                log.info(f"Successfully retrieved secret key: {secret_key}")
                return str(secret_value)
            else:
                # Return the entire data dictionary as JSON string
                import json
                return json.dumps(secret_data)
        
        except hvac.exceptions.InvalidPath:
            raise Exception(f"Secret path not found: {secret_path}")
        except Exception as e:
            raise Exception(f"ERROR retrieving secret from Vault: {e}")


@dag(
    dag_id="vault-password-manager",
    description=(
        "Authenticates with Vault using IAM role, retrieves a password, "
        "stores it as an Airflow Variable, and uses it in another task."
    ),
    start_date=datetime(2024, 1, 1),
    schedule="0 */6 * * *",  # Run every 6 hours
    catchup=False,
    tags=["vault", "iam", "secrets"],
)
def vault_password_dag():
    """DAG that retrieves password from Vault using IAM authentication."""

    @task(show_return_value_in_logs=False)
    def retrieve_password_from_vault():
        """Retrieve password from Vault using IAM authentication."""
        vault_url = os.getenv("VAULT_ADDR", VAULT_URL)
        vault_namespace = os.getenv("VAULT_NAMESPACE", VAULT_NAMESPACE)
        iam_role = os.getenv("VAULT_IAM_ROLE", VAULT_IAM_ROLE)
        secret_path = os.getenv("VAULT_SECRET_PATH", VAULT_SECRET_PATH)
        secret_key = os.getenv("VAULT_SECRET_KEY", VAULT_SECRET_KEY)
        
        log.info(f"Connecting to Vault at: {vault_url}")
        if vault_namespace:
            log.info(f"Using Vault namespace: {vault_namespace}")
        log.info(f"Using IAM role: {iam_role}")
        log.info(f"Secret path: {secret_path}")
        log.info(f"Secret key: {secret_key}")
        
        # Create Vault client and authenticate
        vault_client = VaultClient(vault_url, iam_role, vault_namespace)
        vault_client.authenticate_with_iam()
        
        # Retrieve the password
        password = vault_client.get_secret(secret_path, secret_key)
        
        # Mask the secret in logs
        mask_secret(password)
        
        # Store password in Airflow Variable
        variable_key = "vault_retrieved_password"
        Variable.set(key=variable_key, value=password)
        log.info(f"Password stored in Airflow Variable: {variable_key}")
        
        return variable_key

    @task
    def use_password_task(variable_key: str):
        """Example task that uses the password retrieved from Vault.
        
        This demonstrates how to retrieve and use the password stored
        in the Airflow Variable in downstream tasks.
        """
        if not variable_key:
            raise ValueError("ERROR: Variable key is required!")
        
        # Retrieve the password from Airflow Variable
        password = Variable.get(key=variable_key)
        mask_secret(password)
        
        log.info("Retrieved password from Airflow Variable")
        log.info(f"Password length: {len(password)} characters")
        
        # Example: Use the password for something
        # (e.g., connect to a database, authenticate to an API, etc.)
        # Here we'll just log that we have it and could use it
        
        log.info("Password retrieved successfully and ready to use")
        
        # Example usage simulation
        # In a real scenario, you might:
        # - Connect to a database using this password
        # - Authenticate to an API
        # - Use it as a credential for another service
        
        return {
            "status": "success",
            "password_retrieved": True,
            "variable_key": variable_key
        }

    @task
    def validate_password_strength(variable_key: str):
        """Example task that validates the password strength."""
        if not variable_key:
            raise ValueError("ERROR: Variable key is required!")
        
        password = Variable.get(key=variable_key)
        mask_secret(password)
        
        log.info("Validating password strength...")
        
        # Simple password strength checks
        strength_checks = {
            "length_check": len(password) >= 8,
            "has_uppercase": any(c.isupper() for c in password),
            "has_lowercase": any(c.islower() for c in password),
            "has_digit": any(c.isdigit() for c in password),
            "has_special": any(c in "!@#$%^&*()_+-=[]{}|;:,.<>?" for c in password),
        }
        
        passed_checks = sum(strength_checks.values())
        total_checks = len(strength_checks)
        
        log.info(f"Password strength validation: {passed_checks}/{total_checks} checks passed")
        for check_name, passed in strength_checks.items():
            status = "✓" if passed else "✗"
            log.info(f"  {status} {check_name}: {passed}")
        
        return {
            "strength_score": passed_checks,
            "max_score": total_checks,
            "checks": strength_checks,
            "is_strong": passed_checks >= 4
        }

    # Task flow:
    # 1. Retrieve password from Vault using IAM authentication
    password_var_key = retrieve_password_from_vault()
    
    # 2. Use the password in parallel tasks
    use_result = use_password_task(password_var_key)
    validation_result = validate_password_strength(password_var_key)
    
    # Both tasks depend on the password being retrieved
    password_var_key >> use_result
    password_var_key >> validation_result


# Instantiate the DAG
vault_password_dag()

