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
    description="Authenticates with Coder API, identifies suspended users and their workspaces, then deletes those workspaces.",
    start_date=datetime(2024, 1, 1),
    schedule="*/10 * * * *",  # Run every 10 minutes
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
        """List all workspaces using the session token."""
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
            log.info("Listing all workspaces...")
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
                for idx, workspace in enumerate(workspaces, 1):
                    workspace_id = workspace.get("id", "N/A")
                    workspace_name = workspace.get("name", "N/A")
                    workspace_owner = workspace.get("owner_name", "N/A")
                    workspace_owner_id = workspace.get("owner_id", "N/A")
                    workspace_status = workspace.get("latest_build", {}).get("status", "N/A") if workspace.get("latest_build") else "N/A"
                    log.info(f"  {idx}. Name: {workspace_name}, ID: {workspace_id}, Owner: {workspace_owner}, Owner ID: {workspace_owner_id}, Status: {workspace_status}")
                
                # Return full workspace data for filtering
                return {
                    "workspaces": workspaces,
                    "count": len(workspaces)
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
    def list_suspended_users(secret_ref_key: str):
        """List all suspended users using the session token."""
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
            
            # List users using the session token
            log.info("Listing all users...")
            users_url = f"{CODER_BASE_URL}/api/v2/users"
            
            users_response = http.request(
                'GET',
                users_url,
                headers={
                    "Accept": "application/json",
                    "Coder-Session-Token": session_token
                },
                timeout=urllib3.Timeout(connect=10, read=30)
            )
            
            if users_response.status >= 200 and users_response.status < 300:
                users_data = json.loads(users_response.data.decode('utf-8'))
                all_users = users_data.get("users", [])
                
                # Filter for suspended users
                suspended_users = [
                    user for user in all_users 
                    if user.get("status", "").lower() == "suspended"
                ]
                
                log.info(f"Found {len(all_users)} total user(s), {len(suspended_users)} suspended user(s):")
                for idx, user in enumerate(suspended_users, 1):
                    user_id = user.get("id", "N/A")
                    user_name = user.get("name", "N/A")
                    username = user.get("username", "N/A")
                    user_email = user.get("email", "N/A")
                    log.info(f"  {idx}. Name: {user_name}, Username: {username}, ID: {user_id}, Email: {user_email}")
                
                # Return suspended user data for filtering
                return {
                    "suspended_users": suspended_users,
                    "count": len(suspended_users)
                }
            else:
                raise Exception(f"Failed to list users. Status: {users_response.status}, Response: {users_response.data.decode('utf-8')}")
            
        except urllib3.exceptions.HTTPError as e:
            raise Exception(f"ERROR listing users (HTTPError): {e}")
        except urllib3.exceptions.RequestError as e:
            raise Exception(f"ERROR listing users (RequestError): {e}")
        except json.JSONDecodeError as e:
            raise Exception(f"ERROR parsing JSON response: {e}")
        except Exception as e:
            raise Exception(f"ERROR listing users: {e}")

    @task
    def filter_workspaces_by_suspended_users(workspaces_info: dict, suspended_users_info: dict):
        """Filter workspaces to only those owned by suspended users."""
        workspaces = workspaces_info.get("workspaces", [])
        suspended_users = suspended_users_info.get("suspended_users", [])
        
        # Create sets of identifiers for suspended users (match by ID, username, or name)
        suspended_user_ids = {user.get("id") for user in suspended_users if user.get("id")}
        suspended_usernames = {user.get("username") for user in suspended_users if user.get("username")}
        suspended_names = {user.get("name") for user in suspended_users if user.get("name")}
        
        log.info(f"Filtering {len(workspaces)} workspace(s) for {len(suspended_users)} suspended user(s)...")
        
        # Filter workspaces owned by suspended users
        filtered_workspaces = []
        for workspace in workspaces:
            workspace_id = workspace.get("id")
            workspace_owner_id = workspace.get("owner_id")
            workspace_owner_name = workspace.get("owner_name")
            
            # Match by owner_id, owner_name (username), or owner_name (name)
            is_owned_by_suspended = (
                (workspace_owner_id and workspace_owner_id in suspended_user_ids) or
                (workspace_owner_name and workspace_owner_name in suspended_usernames) or
                (workspace_owner_name and workspace_owner_name in suspended_names)
            )
            
            if is_owned_by_suspended:
                filtered_workspaces.append(workspace)
                log.info(f"  Matched workspace: {workspace.get('name', 'N/A')} (ID: {workspace_id}, Owner: {workspace_owner_name})")
        
        workspace_ids = [ws.get("id") for ws in filtered_workspaces if ws.get("id")]
        
        log.info(f"Found {len(workspace_ids)} workspace(s) owned by suspended users out of {len(workspaces)} total workspace(s)")
        
        return {
            "workspace_ids": workspace_ids,
            "workspaces": filtered_workspaces,
            "count": len(workspace_ids)
        }

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
    def check_filtered_workspaces(filtered_workspaces_info: dict):
        """Conditionally branch to delete task if workspaces owned by suspended users were found."""
        count = filtered_workspaces_info.get("count", 0)
        
        if count > 0:
            log.info(f"Found {count} workspace(s) owned by suspended users, proceeding with deletion...")
            # Return the task ID to execute next
            return "delete_workspaces"
        else:
            log.info("No workspaces owned by suspended users found, skipping deletion.")
            # Return empty list to skip all downstream tasks
            return []

    # Task flow:
    # 1. Authenticate first
    secret_key_ref = authenticate_coder()
    
    # 2. Run list_workspaces and list_suspended_users in parallel
    workspaces_info = list_workspaces(secret_key_ref)
    suspended_users_info = list_suspended_users(secret_key_ref)
    
    # 3. Filter workspaces to only those owned by suspended users
    filtered_workspaces_info = filter_workspaces_by_suspended_users(workspaces_info, suspended_users_info)
    
    # 4. Branch based on whether filtered workspaces were found
    branch_result = check_filtered_workspaces(filtered_workspaces_info)
    
    # 5. Define delete task (will only execute if branch returns its task_id)
    delete_task = delete_workspaces(secret_key_ref, filtered_workspaces_info)
    
    # Set up the branching - delete_task will only run if branch_result returns its task_id
    branch_result >> delete_task


# Instantiate the DAG
local_test_dag()