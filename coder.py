import json
import logging
import os
from datetime import datetime

import urllib3
from airflow.models import DagRun
from airflow.sdk import Variable, dag, task
from airflow.sdk.execution_time.secrets_masker import mask_secret
from airflow.utils.session import provide_session
from kubernetes.client import models as k8s
from sqlalchemy.orm import Session

log = logging.getLogger("airflow.task")

# Coder API configuration
CODER_BASE_URL = "http://coder.coder.svc"

# System users to exclude from suspension checks (emails)
SYSTEM_USERS = ["admin@local.com"]

# HTTP status code constants
HTTP_OK_MIN = 200
HTTP_OK_MAX = 300

# DAG run retention configuration
KEEP_LAST_N_SUCCESSFUL_RUNS = 5  # Keep only last 5 successful runs


class CoderClient:
    """Client for interacting with Coder API."""
    
    def __init__(self, base_url: str, session_token: str = None):
        """Initialize Coder client.
        
        Args:
            base_url: Base URL for Coder API
            session_token: Optional session token. If not provided, must call authenticate() first.
        """
        self.base_url = base_url.rstrip('/')
        self.session_token = session_token
        self.http = urllib3.PoolManager()
        self.timeout = urllib3.Timeout(connect=10, read=30)
    
    def authenticate(self, email: str, password: str) -> str:
        """Authenticate with Coder API and store session token.
        
        Args:
            email: User email for authentication
            password: User password for authentication
            
        Returns:
            Session token string
            
        Raises:
            Exception: If authentication fails
        """
        login_url = f"{self.base_url}/api/v2/users/login"
        login_data = {
            "email": email,
            "password": password
        }
        
        try:
            log.info(f"Connecting to Coder at {login_url}...")
            json_data = json.dumps(login_data).encode('utf-8')
            
            response = self.http.request(
                'POST',
                login_url,
                body=json_data,
                headers={
                    "Content-Type": "application/json",
                    "Accept": "application/json"
                },
                timeout=self.timeout
            )
            
            if (response.status >= HTTP_OK_MIN
                    and response.status < HTTP_OK_MAX):
                result = json.loads(response.data.decode('utf-8'))
                session_token = result.get("session_token", "NOT_FOUND")
                
                if session_token == "NOT_FOUND":
                    raise ValueError(
                        "ERROR: Session token not found in response!"
                    )
                
                self.session_token = session_token
                mask_secret(session_token)
                log.info("Successfully authenticated with Coder!")
                return session_token
            else:
                response_body = response.data.decode('utf-8')
                raise Exception(
                    f"Coder API returned status {response.status}: "
                    f"{response_body}"
                )
        
        except urllib3.exceptions.HTTPError as e:
            raise Exception(f"ERROR connecting to Coder (HTTPError): {e}")
        except urllib3.exceptions.RequestError as e:
            raise Exception(f"ERROR connecting to Coder (RequestError): {e}")
        except json.JSONDecodeError as e:
            raise Exception(f"ERROR parsing JSON response: {e}")
        except Exception as e:
            raise Exception(f"ERROR connecting to Coder: {e}")
    
    def _make_request(
        self,
        method: str,
        endpoint: str,
        body: dict = None,
        headers: dict = None
    ) -> dict:
        """Make an authenticated API request.
        
        Args:
            method: HTTP method (GET, POST, PUT, DELETE)
            endpoint: API endpoint (relative to base_url)
            body: Optional request body dictionary
            headers: Optional additional headers
            
        Returns:
            Parsed JSON response as dictionary
            
        Raises:
            ValueError: If not authenticated
            Exception: If request fails
        """
        if not self.session_token:
            raise ValueError(
                "ERROR: Not authenticated. Call authenticate() first."
            )
        
        url = f"{self.base_url}{endpoint}" if endpoint.startswith('/') else f"{self.base_url}/{endpoint}"
        request_headers = {
            "Accept": "application/json",
            "Coder-Session-Token": self.session_token
        }
        
        if headers:
            request_headers.update(headers)
        
        if body:
            request_headers["Content-Type"] = "application/json"
        
        try:
            json_body = json.dumps(body).encode('utf-8') if body else None
            
            response = self.http.request(
                method,
                url,
                body=json_body,
                headers=request_headers,
                timeout=self.timeout
            )
            
            if (response.status >= HTTP_OK_MIN
                    and response.status < HTTP_OK_MAX):
                return json.loads(response.data.decode('utf-8'))
            else:
                response_body = response.data.decode('utf-8')
                raise Exception(
                    f"API request failed. Status: {response.status}, "
                    f"Response: {response_body}"
                )
        
        except urllib3.exceptions.HTTPError as e:
            raise Exception(f"ERROR making API request (HTTPError): {e}")
        except urllib3.exceptions.RequestError as e:
            raise Exception(f"ERROR making API request (RequestError): {e}")
        except json.JSONDecodeError as e:
            raise Exception(f"ERROR parsing JSON response: {e}")
        except Exception as e:
            raise Exception(f"ERROR making API request: {e}")
    
    def list_workspaces(self) -> list:
        """List all workspaces.
        
        Returns:
            List of workspace dictionaries
        """
        log.info("Listing all workspaces...")
        response = self._make_request('GET', '/api/v2/workspaces')
        workspaces = response.get("workspaces", [])
        log.info(f"Found {len(workspaces)} workspace(s)")
        return workspaces
    
    def list_users(self) -> list:
        """List all users.
        
        Returns:
            List of user dictionaries
        """
        log.info("Listing all users...")
        response = self._make_request('GET', '/api/v2/users')
        users = response.get("users", [])
        log.info(f"Found {len(users)} total user(s)")
        return users
    
    def suspend_user(self, user_identifier: str) -> dict:
        """Suspend a user by ID or email.
        
        Args:
            user_identifier: User ID or email
            
        Returns:
            Updated user dictionary
        """
        endpoint = f"/api/v2/users/{user_identifier}/status/suspend"
        return self._make_request('PUT', endpoint)
    
    def delete_workspace(self, workspace_id: str) -> dict:
        """Delete a workspace by creating a build with transition=delete.
        
        Args:
            workspace_id: Workspace ID
            
        Returns:
            Build result dictionary
        """
        endpoint = f"/api/v2/workspaces/{workspace_id}/builds"
        payload = {"transition": "delete"}
        return self._make_request('POST', endpoint, body=payload)


@dag(
    dag_id="coder-workspace-manager",
    description=(
        "Authenticates with Coder API, suspends unauthorized users, "
        "identifies suspended users and their workspaces, "
        "then deletes those workspaces."
    ),
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
        password = os.getenv("PASS", "NOT_SET")
        if password == "NOT_SET":
            raise ValueError("ERROR: PASS environment variable not set!")
        
        client = CoderClient(CODER_BASE_URL)
        session_token = client.authenticate("admin@local.com", password)
        
        # Store token in Variable and return key reference
        secret_key = "coder_session_token"
        Variable.set(key=secret_key, value=session_token)
        log.info(f"Token stored under key: {secret_key}")
        
        return secret_key

    @task
    def list_workspaces(secret_ref_key: str):
        """List all workspaces using the session token."""
        if not secret_ref_key:
            raise ValueError("ERROR: Secret reference key is required!")
        
        session_token = Variable.get(key=secret_ref_key)
        mask_secret(session_token)
        
        client = CoderClient(CODER_BASE_URL, session_token)
        workspaces = client.list_workspaces()
        
        log.info(f"Found {len(workspaces)} workspace(s):")
        for idx, workspace in enumerate(workspaces, 1):
            workspace_id = workspace.get("id", "N/A")
            workspace_name = workspace.get("name", "N/A")
            workspace_owner = workspace.get("owner_name", "N/A")
            workspace_owner_id = workspace.get("owner_id", "N/A")
            latest_build = workspace.get("latest_build")
            workspace_status = (
                latest_build.get("status", "N/A") if latest_build else "N/A"
            )
            log.info(
                f"  {idx}. Name: {workspace_name}, ID: {workspace_id}, "
                f"Owner: {workspace_owner}, Owner ID: {workspace_owner_id}, "
                f"Status: {workspace_status}"
            )
        
        return {
            "workspaces": workspaces,
            "count": len(workspaces)
        }

    @task
    def list_suspended_users(secret_ref_key: str):
        """List all suspended users using the session token."""
        if not secret_ref_key:
            raise ValueError("ERROR: Secret reference key is required!")
        
        session_token = Variable.get(key=secret_ref_key)
        mask_secret(session_token)
        
        client = CoderClient(CODER_BASE_URL, session_token)
        all_users = client.list_users()
        
        # Filter for suspended users
        suspended_users = [
            user for user in all_users 
            if user.get("status", "").lower() == "suspended"
        ]
        
        log.info(
            f"Found {len(all_users)} total user(s), "
            f"{len(suspended_users)} suspended user(s):"
        )
        for idx, user in enumerate(suspended_users, 1):
            user_id = user.get("id", "N/A")
            user_name = user.get("name", "N/A")
            username = user.get("username", "N/A")
            user_email = user.get("email", "N/A")
            log.info(
                f"  {idx}. Name: {user_name}, Username: {username}, "
                f"ID: {user_id}, Email: {user_email}"
            )
        
        return {
            "suspended_users": suspended_users,
            "count": len(suspended_users)
        }

    def _read_authorized_users() -> set:
        """Read authorized users from ConfigMap file."""
        authorized_users_file = "/tmp/rbac/users.json"
        if not os.path.exists(authorized_users_file):
            raise FileNotFoundError(
                f"ERROR: Authorized users file not found at "
                f"{authorized_users_file}"
            )
        
        log.info(f"Reading authorized users from {authorized_users_file}...")
        with open(authorized_users_file, 'r') as f:
            authorized_data = json.load(f)
        
        # Extract authorized emails (supports {"emails": [...]} or list)
        if isinstance(authorized_data, dict):
            authorized_emails_raw = authorized_data.get("emails", [])
        elif isinstance(authorized_data, list):
            authorized_emails_raw = authorized_data
        else:
            raise ValueError(
                f"ERROR: Unexpected JSON structure in authorized users "
                f"file: {authorized_data}"
            )
        
        # Convert to lowercase set for case-insensitive comparison
        authorized_emails = {
            email.lower() if isinstance(email, str) else str(email).lower()
            for email in authorized_emails_raw
        }
        
        log.info(f"Found {len(authorized_emails)} authorized user(s) in ConfigMap")
        return authorized_emails

    def _find_unauthorized_users(all_users: list, authorized_emails: set) -> list:
        """Find users that are not authorized."""
        # Filter out system users and already suspended users
        system_users_lower = {email.lower() for email in SYSTEM_USERS}
        users_to_check = [
            user for user in all_users
            if (user.get("email", "").lower() not in system_users_lower
                and user.get("status", "").lower() != "suspended")
        ]
        
        log.info(
            f"Checking {len(users_to_check)} user(s) "
            f"(excluding {len(SYSTEM_USERS)} system user(s) and "
            f"already suspended users)..."
        )
        
        # Find unauthorized users (not in authorized list)
        unauthorized_users = []
        for user in users_to_check:
            user_email = user.get("email", "").lower()
            if user_email and user_email not in authorized_emails:
                unauthorized_users.append(user)
                user_name = user.get('name', 'N/A')
                user_status = user.get('status', 'N/A')
                log.info(
                    f"  Unauthorized user found: {user_email} "
                    f"(Name: {user_name}, Status: {user_status})"
                )
        
        return unauthorized_users

    def _suspend_user(user: dict, client: CoderClient) -> bool:
        """Suspend a single user and return True if successful."""
        user_id = user.get("id")
        user_email = user.get("email", "N/A")
        user_identifier = user_id or user_email
        
        if not user_id:
            log.warning(f"  Skipping user {user_email}: No user ID found")
            return False
        
        try:
            client.suspend_user(user_identifier)
            log.info(
                f"  Successfully suspended user: {user_email} (ID: {user_id})"
            )
            return True
        except Exception as e:
            log.error(f"  ERROR suspending user {user_email}: {e}")
            return False

    @task(
        executor_config={
            "pod_override": k8s.V1Pod(
                spec=k8s.V1PodSpec(
                    containers=[
                        k8s.V1Container(
                            name="base",
                            volume_mounts=[
                                k8s.V1VolumeMount(
                                    name="authorized-users",
                                    mount_path="/tmp/rbac",
                                    read_only=True
                                )
                            ]
                        )
                    ],
                    volumes=[
                        k8s.V1Volume(
                            name="authorized-users",
                            config_map=k8s.V1ConfigMapVolumeSource(
                                name="authorized-users"
                            )
                        )
                    ]
                )
            )
        }
    )
    def suspend_unauthorized_users(secret_ref_key: str):
        """Suspend users that are not in the authorized users list."""
        if not secret_ref_key:
            raise ValueError("ERROR: Secret reference key is required!")
        
        try:
            # Read authorized users from ConfigMap
            authorized_emails = _read_authorized_users()
            
            # Retrieve the actual session token from Variable
            session_token = Variable.get(key=secret_ref_key)
            mask_secret(session_token)
            
            # Create client and list all users
            client = CoderClient(CODER_BASE_URL, session_token)
            all_users = client.list_users()
            
            # Find unauthorized users
            unauthorized_users = _find_unauthorized_users(
                all_users, authorized_emails
            )
            
            if not unauthorized_users:
                log.info(
                    "No unauthorized users found. All users are "
                    "authorized or already suspended."
                )
                users_to_check_count = len(all_users) - len([
                    u for u in all_users
                    if u.get("email", "").lower() in {
                        email.lower() for email in SYSTEM_USERS
                    } or u.get("status", "").lower() == "suspended"
                ])
                return {
                    "suspended_count": 0,
                    "skipped_count": users_to_check_count
                }
            
            # Suspend unauthorized users
            log.info(
                f"Suspending {len(unauthorized_users)} unauthorized "
                f"user(s)..."
            )
            suspended_count = 0
            failed_count = 0
            
            for user in unauthorized_users:
                if _suspend_user(user, client):
                    suspended_count += 1
                else:
                    failed_count += 1
            
            log.info(
                f"Suspension complete: {suspended_count} suspended, "
                f"{failed_count} failed"
            )
            
            return {
                "suspended_count": suspended_count,
                "failed_count": failed_count,
                "skipped_count": len(all_users) - len(unauthorized_users)
            }
        
        except FileNotFoundError as e:
            raise Exception(f"ERROR reading authorized users file: {e}")
        except json.JSONDecodeError as e:
            raise Exception(
                f"ERROR parsing authorized users JSON file: {e}"
            )
        except Exception as e:
            raise Exception(f"ERROR suspending unauthorized users: {e}")

    @task
    def filter_workspaces_by_suspended_users(workspaces_info: dict, suspended_users_info: dict):
        """Filter workspaces to only those owned by suspended users."""
        workspaces = workspaces_info.get("workspaces", [])
        suspended_users = suspended_users_info.get("suspended_users", [])
        
        # Create sets of identifiers for suspended users
        # (match by ID, username, or name)
        suspended_user_ids = {
            user.get("id") for user in suspended_users if user.get("id")
        }
        suspended_usernames = {
            user.get("username") for user in suspended_users
            if user.get("username")
        }
        suspended_names = {
            user.get("name") for user in suspended_users if user.get("name")
        }
        
        log.info(
            f"Filtering {len(workspaces)} workspace(s) for "
            f"{len(suspended_users)} suspended user(s)..."
        )
        
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
                workspace_name = workspace.get('name', 'N/A')
                log.info(
                    f"  Matched workspace: {workspace_name} "
                    f"(ID: {workspace_id}, Owner: {workspace_owner_name})"
                )
        
        workspace_ids = [
            ws.get("id") for ws in filtered_workspaces if ws.get("id")
        ]
        
        log.info(
            f"Found {len(workspace_ids)} workspace(s) owned by suspended "
            f"users out of {len(workspaces)} total workspace(s)"
        )
        
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
        
        session_token = Variable.get(key=secret_ref_key)
        mask_secret(session_token)
        
        client = CoderClient(CODER_BASE_URL, session_token)
        
        log.info(f"Deleting {len(workspace_ids)} workspace(s)...")
        for workspace_id in workspace_ids:
            try:
                delete_result = client.delete_workspace(workspace_id)
                build_id = delete_result.get("id", "N/A")
                delete_job = delete_result.get("job")
                build_status = (
                    delete_job.get("status", "N/A") if delete_job else "N/A"
                )
                log.info(
                    f"  Successfully initiated deletion for workspace "
                    f"{workspace_id}"
                )
                log.info(f"    Build ID: {build_id}, Status: {build_status}")
            except Exception as e:
                log.error(
                    f"  ERROR: Failed to delete workspace {workspace_id}: {e}"
                )

    @task.branch
    def check_filtered_workspaces(filtered_workspaces_info: dict):
        """Branch to delete task if workspaces owned by suspended users found."""
        count = filtered_workspaces_info.get("count", 0)
        
        if count > 0:
            log.info(
                f"Found {count} workspace(s) owned by suspended users, "
                f"proceeding with deletion..."
            )
            # Return the task ID to execute next
            return "delete_workspaces"
        else:
            log.info(
                "No workspaces owned by suspended users found, "
                "skipping deletion."
            )
            # Return empty list to skip all downstream tasks
            return []

    @task(trigger_rule="all_done")
    @provide_session
    def cleanup_old_dag_runs(session: Session = None):
        """Clean up old successful DAG runs, keeping only the last N."""
        dag_id = "coder-workspace-manager"
        
        try:
            # Query all successful runs for this DAG, ordered by execution date descending
            successful_runs = (
                session.query(DagRun)
                .filter(
                    DagRun.dag_id == dag_id,
                    DagRun.state == "success"
                )
                .order_by(DagRun.execution_date.desc())
                .all()
            )
            
            total_successful = len(successful_runs)
            log.info(
                f"Found {total_successful} successful DAG run(s) for {dag_id}"
            )
            
            if total_successful <= KEEP_LAST_N_SUCCESSFUL_RUNS:
                log.info(
                    f"Only {total_successful} successful run(s) found, "
                    f"which is <= {KEEP_LAST_N_SUCCESSFUL_RUNS}. "
                    f"No cleanup needed."
                )
                return {
                    "deleted_count": 0,
                    "kept_count": total_successful
                }
            
            # Keep the last N runs, delete the rest
            runs_to_keep = successful_runs[:KEEP_LAST_N_SUCCESSFUL_RUNS]
            runs_to_delete = successful_runs[KEEP_LAST_N_SUCCESSFUL_RUNS:]
            
            log.info(
                f"Keeping last {len(runs_to_keep)} successful run(s), "
                f"deleting {len(runs_to_delete)} older successful run(s)..."
            )
            
            deleted_count = 0
            for dag_run in runs_to_delete:
                try:
                    run_id = dag_run.run_id
                    execution_date = dag_run.execution_date
                    log.info(
                        f"  Deleting successful run: {run_id} "
                        f"(execution_date: {execution_date})"
                    )
                    session.delete(dag_run)
                    deleted_count += 1
                except Exception as e:
                    log.error(f"  ERROR deleting run {dag_run.run_id}: {e}")
            
            session.commit()
            log.info(
                f"Cleanup complete: Deleted {deleted_count} old successful "
                f"run(s), kept {len(runs_to_keep)} recent successful run(s)"
            )
            
            return {
                "deleted_count": deleted_count,
                "kept_count": len(runs_to_keep)
            }
        
        except Exception as e:
            session.rollback()
            log.error(f"ERROR during DAG run cleanup: {e}")
            raise Exception(f"ERROR cleaning up old DAG runs: {e}")

    # Task flow:
    # 1. Authenticate first
    secret_key_ref = authenticate_coder()
    
    # 2. Suspend unauthorized users first
    suspend_result = suspend_unauthorized_users(secret_key_ref)
    
    # 3. Run list_workspaces and list_suspended_users in parallel
    #    (after suspend completes)
    #    This ensures newly suspended users are included in the list
    workspaces_info = list_workspaces(secret_key_ref)
    suspended_users_info = list_suspended_users(secret_key_ref)
    
    # Set explicit dependency: suspend must complete before listing
    suspend_result >> workspaces_info
    suspend_result >> suspended_users_info
    
    # 4. Filter workspaces to only those owned by suspended users
    filtered_workspaces_info = filter_workspaces_by_suspended_users(
        workspaces_info, suspended_users_info
    )
    
    # 5. Branch based on whether filtered workspaces were found
    branch_result = check_filtered_workspaces(filtered_workspaces_info)
    
    # 6. Define delete task (will only execute if branch returns task_id)
    delete_task = delete_workspaces(secret_key_ref, filtered_workspaces_info)
    
    # Set up the branching - delete_task only runs if branch returns task_id
    branch_result >> delete_task
    
    # 7. Cleanup old successful DAG runs (always runs at the end)
    cleanup_task = cleanup_old_dag_runs()
    # Cleanup runs after delete_task completes (or if skipped due to trigger_rule="all_done")
    delete_task >> cleanup_task


# Instantiate the DAG
local_test_dag()