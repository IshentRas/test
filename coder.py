import json
import logging
import os
from datetime import datetime

import urllib3
from airflow.sdk import dag, task, Variable
from airflow.sdk.execution_time.secrets_masker import mask_secret
from kubernetes.client import models as k8s

log = logging.getLogger("airflow.task")

# Coder API configuration
CODER_BASE_URL = "http://coder.coder.svc"


@dag(
    dag_id="coder-workspace-manager",
    description="Authenticates with Coder API, lists workspaces, and conditionally deletes them.",
    start_date=datetime(2024, 1, 1),
    schedule=None,
    catchup=False,
    tags=["local", "test", "coder"],
)
def local_test_dag():
    """Simple test DAG using TaskFlow API decorators."""

    @task(
        show_return_value_in_logs=False,
        executor_config={
            "pod_override": k8s.V1Pod(
                spec=k8s.V1PodSpec(
                    containers=[
                        k8s.V1Container(
                            name="base",
                            env=[
                                k8s.V1EnvVar(
                                    name="PASS",
                                    value_from=k8s.V1EnvVarSource(
                                        secret_key_ref=k8s.V1SecretKeySelector(
                                            name="superpass",
                                            key="password"
                                        )
                                    )
                                )
                            ]
                        )
                    ]
                )
            )
        }
    )
    def authenticate_coder():
        """Authenticate with Coder API and return session token."""

        # Get password from environment variable
        password = os.getenv("PASS", "NOT_SET")
        if password == "NOT_SET":
            raise ValueError("ERROR: PASS environment variable not set!")
        
        # Coder API endpoint
        coder_url = f"{CODER_BASE_URL}/api/v2/users/login"
        
        # Login payload
        login_data = {
            "email": "admin@local.com",
            "password": password
        }
        
        try:
            # Make POST request to Coder API using urllib3
            log.info(f"Connecting to Coder at {coder_url}...")
            
            # Create urllib3 PoolManager
            http = urllib3.PoolManager()
            
            # Encode JSON data
            json_data = json.dumps(login_data).encode('utf-8')
            
            # Make POST request
            response = http.request(
                'POST',
                coder_url,
                body=json_data,
                headers={
                    "Content-Type": "application/json",
                    "Accept": "application/json"
                },
                timeout=urllib3.Timeout(connect=10, read=30)
            )
            
            # Check response status
            if response.status >= 200 and response.status < 300:
                # Parse JSON response
                result = json.loads(response.data.decode('utf-8'))
                session_token = result.get("session_token", "NOT_FOUND")
                
                if session_token == "NOT_FOUND":
                    raise ValueError("ERROR: Session token not found in response!")
                
                # Store token in Variable and return key reference
                secret_key = "coder_session_token"
                Variable.set(key=secret_key, value=session_token)
                
                # Mask locally for safety
                mask_secret(session_token)
                
                log.info(f"Successfully connected to Coder! Token stored under key: {secret_key}")
                
                # Return the key reference instead of the actual token
                return secret_key
            else:
                raise Exception(f"Coder API returned status {response.status}: {response.data.decode('utf-8')}")
            
        except urllib3.exceptions.HTTPError as e:
            raise Exception(f"ERROR connecting to Coder (HTTPError): {e}")
        except urllib3.exceptions.RequestError as e:
            raise Exception(f"ERROR connecting to Coder (RequestError): {e}")
        except json.JSONDecodeError as e:
            raise Exception(f"ERROR parsing JSON response: {e}")
        except Exception as e:
            raise Exception(f"ERROR connecting to Coder: {e}")

    @task
    def list_workspaces(secret_ref_key: str):
        """List workspaces using the session token."""
        if not secret_ref_key:
            raise ValueError("ERROR: Secret reference key is required!")
        
        try:
            log.info(f"Received reference key: {secret_ref_key}")
            
            # Retrieve the actual session token from Variable
            session_token = Variable.get(key=secret_ref_key)
            
            # Re-mask immediately
            mask_secret(session_token)
            
            # Create urllib3 PoolManager
            http = urllib3.PoolManager()
            
            # List workspaces using the session token
            log.info("Listing workspaces...")
            workspaces_url = f"{CODER_BASE_URL}/api/v2/workspaces"
            
            workspaces_response = http.request(
                'GET',
                workspaces_url,
                headers={
                    "Accept": "application/json",
                    "Coder-Session-Token": session_token
                },
                timeout=urllib3.Timeout(connect=10, read=30)
            )
            
            if workspaces_response.status >= 200 and workspaces_response.status < 300:
                workspaces_data = json.loads(workspaces_response.data.decode('utf-8'))
                workspaces = workspaces_data.get("workspaces", [])
                
                log.info(f"Found {len(workspaces)} workspace(s):")
                workspace_ids = []
                for idx, workspace in enumerate(workspaces, 1):
                    workspace_id = workspace.get("id", "N/A")
                    workspace_name = workspace.get("name", "N/A")
                    workspace_owner = workspace.get("owner_name", "N/A")
                    workspace_status = workspace.get("latest_build", {}).get("status", "N/A") if workspace.get("latest_build") else "N/A"
                    log.info(f"  {idx}. Name: {workspace_name}, ID: {workspace_id}, Owner: {workspace_owner}, Status: {workspace_status}")
                    if workspace_id != "N/A":
                        workspace_ids.append(workspace_id)
                
                # Return workspace IDs for conditional deletion
                return {
                    "workspace_ids": workspace_ids,
                    "count": len(workspace_ids)
                }
            else:
                raise Exception(f"Failed to list workspaces. Status: {workspaces_response.status}, Response: {workspaces_response.data.decode('utf-8')}")
            
        except urllib3.exceptions.HTTPError as e:
            raise Exception(f"ERROR listing workspaces (HTTPError): {e}")
        except urllib3.exceptions.RequestError as e:
            raise Exception(f"ERROR listing workspaces (RequestError): {e}")
        except json.JSONDecodeError as e:
            raise Exception(f"ERROR parsing JSON response: {e}")
        except Exception as e:
            raise Exception(f"ERROR listing workspaces: {e}")

    @task
    def delete_workspaces(secret_ref_key: str, workspaces_info: dict):
        """Delete workspaces using the session token."""
        if not secret_ref_key:
            raise ValueError("ERROR: Secret reference key is required!")
        
        workspace_ids = workspaces_info.get("workspace_ids", [])
        if not workspace_ids:
            raise ValueError("ERROR: Workspace IDs are required!")
        
        try:
            log.info(f"Received {len(workspace_ids)} workspace ID(s) to delete")
            
            # Retrieve the actual session token from Variable
            session_token = Variable.get(key=secret_ref_key)
            
            # Re-mask immediately
            mask_secret(session_token)
            
            # Create urllib3 PoolManager
            http = urllib3.PoolManager()
            
            # Delete workspaces by creating a build with transition="delete"
            log.info(f"Deleting {len(workspace_ids)} workspace(s)...")
            for workspace_id in workspace_ids:
                delete_build_url = f"{CODER_BASE_URL}/api/v2/workspaces/{workspace_id}/builds"
                delete_payload = {
                    "transition": "delete"
                }
                
                delete_json_data = json.dumps(delete_payload).encode('utf-8')
                
                delete_response = http.request(
                    'POST',
                    delete_build_url,
                    body=delete_json_data,
                    headers={
                        "Content-Type": "application/json",
                        "Accept": "application/json",
                        "Coder-Session-Token": session_token
                    },
                    timeout=urllib3.Timeout(connect=10, read=30)
                )
                
                if delete_response.status >= 200 and delete_response.status < 300:
                    delete_result = json.loads(delete_response.data.decode('utf-8'))
                    build_id = delete_result.get("id", "N/A")
                    build_status = delete_result.get("job", {}).get("status", "N/A") if delete_result.get("job") else "N/A"
                    log.info(f"  Successfully initiated deletion for workspace {workspace_id}")
                    log.info(f"    Build ID: {build_id}, Status: {build_status}")
                else:
                    log.error(f"  ERROR: Failed to delete workspace {workspace_id}. Status: {delete_response.status}")
                    log.error(f"    Response body: {delete_response.data.decode('utf-8')}")
            
        except urllib3.exceptions.HTTPError as e:
            raise Exception(f"ERROR deleting workspaces (HTTPError): {e}")
        except urllib3.exceptions.RequestError as e:
            raise Exception(f"ERROR deleting workspaces (RequestError): {e}")
        except json.JSONDecodeError as e:
            raise Exception(f"ERROR parsing JSON response: {e}")
        except Exception as e:
            raise Exception(f"ERROR deleting workspaces: {e}")

    @task.branch
    def check_workspaces(workspaces_info: dict):
        """Conditionally branch to delete task if workspaces were found."""
        count = workspaces_info.get("count", 0)
        
        if count > 0:
            log.info(f"Found {count} workspace(s), proceeding with deletion...")
            # Return the task ID to execute next
            return "delete_workspaces"
        else:
            log.info("No workspaces found, skipping deletion.")
            # Return empty list to skip all downstream tasks
            return []

    # Task flow: authenticate first, then list workspaces, then conditionally delete
    secret_key_ref = authenticate_coder()
    workspaces_info = list_workspaces(secret_key_ref)
    
    # Branch based on whether workspaces were found
    branch_result = check_workspaces(workspaces_info)
    
    # Define delete task (will only execute if branch returns its task_id)
    delete_task = delete_workspaces(secret_key_ref, workspaces_info)
    
    # Set up the branching - delete_task will only run if branch_result returns its task_id
    branch_result >> delete_task


# Instantiate the DAG
local_test_dag()

